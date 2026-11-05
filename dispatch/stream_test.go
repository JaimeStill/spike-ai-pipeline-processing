package dispatch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// flood is a stage that yields n inline outputs, each named o<i> and holding size bytes of i's
// low byte, made as it yields them; with failAt above zero, it fails once it has yielded that
// many.
type flood struct {
	n, size, failAt int
}

func (f *flood) Identity() dispatch.Identity {
	return dispatch.Identity{Name: "flood", Version: "1", Config: fmt.Sprintf("%d/%d/%d", f.n, f.size, f.failAt)}
}

func (f *flood) Prepare(_ context.Context, _ dispatch.Part, _ string) iter.Seq2[dispatch.Item, error] {
	return func(yield func(dispatch.Item, error) bool) {
		for i := range f.n {
			if f.failAt > 0 && i == f.failAt {
				yield(dispatch.Item{}, errors.New("flood: told to fail"))
				return
			}
			o := dispatch.Output{Kind: dispatch.TextOutput, Name: fmt.Sprintf("o%d", i), Value: bytes.Repeat([]byte{byte(i)}, f.size)}
			if !yield(dispatch.OutputItem(o), nil) {
				return
			}
		}
	}
}

// floodDispatcher returns a dispatcher with s on route.Text over a file index in a directory of
// its own, and the index's directory.
func floodDispatcher(t *testing.T, s dispatch.Stage) (*dispatch.Dispatcher, dispatch.Index, string) {
	t.Helper()
	dir := t.TempDir()
	idx, err := dispatch.NewFileIndex(dir, dispatch.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := dispatch.New(dispatch.Config{Stages: dispatch.Stages{route.Text: s}, Index: idx, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return d, idx, dir
}

// heap returns the bytes of the live heap, once a collection has run.
func heap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// heapBound is the most the live heap may grow while 200,000 outputs of 512 bytes stream through
// a dispatch: a tenth of what holding them would take, the outputs alone over 100 MB.
const heapBound = 24 << 20

// TestStreamedOutputs: 200,000 outputs stream through a dispatch over a file index, each an
// output event in the stage's order, the caller's Ref set on it, and the live heap stays within
// a bound far under the outputs' size while they do, so neither the dispatcher nor the index
// holds them. The part's event counts them; the index's record counts them and reads them back,
// streamed, with their Refs. A second dispatch sees the part, its outputs streamed from the index
// in order, as seen output events, within the same bound.
func TestStreamedOutputs(t *testing.T) {
	const n, size, every = 200_000, 512, 20_000
	s := &flood{n: n, size: size}
	d, idx, _ := floodDispatcher(t, s)
	path := input(t, "flood.txt", "flood")

	consume := func(wantOutcome dispatch.Outcome) dispatch.Event {
		t.Helper()
		base := heap()
		var peak uint64
		i := 0
		var last dispatch.Event
		for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: path}) {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Output == nil {
				last = ev
				continue
			}
			if o := ev.Output; o.Name != fmt.Sprintf("o%d", i) || len(o.Value) != size || o.Value[0] != byte(i) || ev.Outcome != wantOutcome {
				t.Fatalf("output %d: %s, %d bytes, outcome %q", i, o.Name, len(o.Value), ev.Outcome)
			}
			if wantOutcome == dispatch.Seen {
				if ev.Output.Ref != fmt.Sprintf("ref %d", i) {
					t.Fatalf("seen output %d's Ref %q", i, ev.Output.Ref)
				}
			} else {
				ev.Output.Ref = fmt.Sprintf("ref %d", i)
			}
			i++
			if i%every == 0 {
				peak = max(peak, heap())
			}
		}
		if i != n {
			t.Fatalf("%d output events, want %d", i, n)
		}
		if peak > base && peak-base > heapBound {
			t.Errorf("the live heap grew %d MB while %d outputs streamed, past %d MB", (peak-base)>>20, n, heapBound>>20)
		}
		return last
	}

	ev := consume("")
	if ev.Outcome != dispatch.Prepared || ev.Outputs != n {
		t.Fatalf("the part's event: %s, %d outputs", ev.Outcome, ev.Outputs)
	}
	key := dispatch.Key{SHA256: ev.Part.SHA256, Stage: s.Identity()}
	e, ok, err := idx.Lookup(t.Context(), key)
	if err != nil || !ok || e.Outputs != n {
		t.Fatalf("the record: %+v, %v, %v", e, ok, err)
	}
	i := 0
	for o, err := range idx.Outputs(t.Context(), key) {
		if err != nil {
			t.Fatal(err)
		}
		if o.Name != fmt.Sprintf("o%d", i) || o.Ref != fmt.Sprintf("ref %d", i) || len(o.Value) != size {
			t.Fatalf("recorded output %d: %s, %q, %d bytes", i, o.Name, o.Ref, len(o.Value))
		}
		i++
	}
	if i != n {
		t.Fatalf("the index read back %d outputs, want %d", i, n)
	}

	if ev := consume(dispatch.Seen); ev.Outcome != dispatch.Seen || ev.Recorded != dispatch.Prepared || ev.Outputs != n {
		t.Fatalf("the seen part's event: %s, recorded %s, %d outputs", ev.Outcome, ev.Recorded, ev.Outputs)
	}
}

// TestStreamReleased: a stage that fails after yielding outputs, and a loop that stops among
// them, each leave no record and no pending outputs, under either index: the claim is released,
// so the key may be claimed again, and the file index's claims directory is empty.
func TestStreamReleased(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage *flood
		stop  int
	}{
		{"failure", &flood{n: 1000, size: 16, failAt: 600}, 0},
		{"stop", &flood{n: 1000, size: 16}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, idx, dir := floodDispatcher(t, tc.stage)
			path := input(t, "flood.txt", "flood")
			outputs := 0
			var last dispatch.Event
			for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: path}) {
				if err != nil {
					t.Fatal(err)
				}
				if ev.Output == nil {
					last = ev
					continue
				}
				outputs++
				if outputs == tc.stop {
					break
				}
			}
			if tc.stop == 0 && (last.Outcome != dispatch.Failed || last.Outputs != tc.stage.failAt || outputs != tc.stage.failAt) {
				t.Fatalf("the failing part: %s, %d outputs counted, %d output events", last.Outcome, last.Outputs, outputs)
			}
			key := dispatch.Key{SHA256: sum("flood"), Stage: tc.stage.Identity()}
			if _, ok, err := idx.Lookup(t.Context(), key); err != nil || ok {
				t.Fatalf("recorded: %v, %v", ok, err)
			}
			if left, err := os.ReadDir(filepath.Join(dir, "claims")); err != nil || len(left) != 0 {
				t.Fatalf("the claims directory holds %v, %v", left, err)
			}
			tok, ok, err := idx.Claim(t.Context(), key)
			if err != nil || !ok {
				t.Fatalf("claim after the release: %v, %v", ok, err)
			}
			_ = idx.Release(t.Context(), key, tok)
		})
	}
	t.Run("memory", func(t *testing.T) {
		s := &flood{n: 1000, size: 16, failAt: 600}
		idx := dispatch.NewMemoryIndex()
		d, err := dispatch.New(dispatch.Config{Stages: dispatch.Stages{route.Text: s}, Index: idx, Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		collect(t, d.Dispatch(t.Context(), dispatch.Input{Path: input(t, "flood.txt", "flood")}), nil)
		key := dispatch.Key{SHA256: sum("flood"), Stage: s.Identity()}
		if _, ok, _ := idx.Lookup(t.Context(), key); ok {
			t.Fatal("a failed part was recorded")
		}
		if _, ok, err := idx.Claim(t.Context(), key); err != nil || !ok {
			t.Fatalf("claim after the release: %v, %v", ok, err)
		}
	})
}

// interleaved is a stage that, for the input, yields an output, finds a part, inner.txt, then
// yields another output; for a part below the input, it yields two outputs.
type interleaved struct{}

func (interleaved) Identity() dispatch.Identity {
	return dispatch.Identity{Name: "interleaved", Version: "1", Config: "test"}
}

func (interleaved) Prepare(_ context.Context, p dispatch.Part, dir string) iter.Seq2[dispatch.Item, error] {
	return func(yield func(dispatch.Item, error) bool) {
		if !yield(dispatch.OutputItem(dispatch.Output{Kind: dispatch.TextOutput, Name: "before"}), nil) {
			return
		}
		if p.Depth == 0 {
			path := filepath.Join(dir, "inner.txt")
			if err := os.WriteFile(path, []byte("inner"), 0o644); err != nil {
				yield(dispatch.Item{}, err)
				return
			}
			if !yield(dispatch.PartItem(dispatch.Found{Path: path, Name: "inner.txt"}), nil) {
				return
			}
		}
		yield(dispatch.OutputItem(dispatch.Output{Kind: dispatch.TextOutput, Name: "after"}), nil)
	}
}

// TestStreamOrder: a stage's outputs and the parts it finds reach the caller in the stage's
// order, each found part's subtree, its outputs, then its own event, between the outputs yielded
// before and after it; the part's own event comes last.
func TestStreamOrder(t *testing.T) {
	d, err := dispatch.New(dispatch.Config{
		Stages: dispatch.Stages{route.Text: interleaved{}}, Index: dispatch.NewMemoryIndex(), Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: input(t, "a.txt", "outer")}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Output != nil {
			got = append(got, ev.Part.Name+" "+ev.Output.Name)
		} else {
			got = append(got, ev.Part.Name+" "+string(ev.Outcome))
		}
	}
	want := []string{
		"a.txt before",
		"inner.txt before", "inner.txt after", "inner.txt prepared",
		"a.txt after", "a.txt prepared",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events:\n got %q\nwant %q", got, want)
	}
}

// brood is a stage that, for the input, finds n parts, each a file it writes as it finds it,
// all of one content, named and placed by its index at length, so a child's reference is some
// hundreds of bytes.
type brood struct{ n int }

func (b brood) Identity() dispatch.Identity {
	return dispatch.Identity{Name: "brood", Version: "1", Config: fmt.Sprint(b.n)}
}

// broodName returns the name, or with where set, the place, of brood's child i.
func broodName(i int, where bool) string {
	label := "child"
	if where {
		label = "place"
	}
	return fmt.Sprintf("%s %08d %s.txt", label, i, strings.Repeat("x", 160))
}

func (b brood) Prepare(_ context.Context, p dispatch.Part, dir string) iter.Seq2[dispatch.Item, error] {
	return func(yield func(dispatch.Item, error) bool) {
		if p.Depth > 0 {
			yield(dispatch.OutputItem(dispatch.Output{Kind: dispatch.TextOutput, Name: "leaf"}), nil)
			return
		}
		for i := range b.n {
			path := filepath.Join(dir, fmt.Sprintf("c%d.txt", i))
			if err := os.WriteFile(path, []byte("leaf"), 0o600); err != nil {
				yield(dispatch.Item{}, err)
				return
			}
			if !yield(dispatch.PartItem(dispatch.Found{Path: path, Name: broodName(i, false), Where: broodName(i, true)}), nil) {
				return
			}
		}
	}
}

// childBound is the most the live heap may grow while 100,000 children stream through a
// dispatch: their references alone, held, would take over 40 MB.
const childBound = 12 << 20

// TestStreamedChildren: 100,000 parts found in one input stream through a dispatch over a file
// index, the first prepared and the rest seen, and the live heap stays within a bound far under
// their references' size, so neither the dispatcher nor the index holds them. The input's event
// counts them; the index's record counts them and reads them back, streamed, in the order found.
// A second dispatch sees the input, its event counting the children it never reads.
func TestStreamedChildren(t *testing.T) {
	const n, every = 100_000, 10_000
	s := brood{n: n}
	d, idx, _ := floodDispatcher(t, s)
	path := input(t, "brood.txt", "brood")

	base := heap()
	var peak uint64
	i := 0
	var last dispatch.Event
	for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: path}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Output != nil {
			continue
		}
		if ev.Part.Depth == 0 {
			last = ev
			continue
		}
		if want := dispatch.Seen; ev.Part.Where != broodName(i, true) || (i > 0 && ev.Outcome != want) {
			t.Fatalf("child %d: %q, %s", i, ev.Part.Where, ev.Outcome)
		}
		i++
		if i%every == 0 {
			peak = max(peak, heap())
		}
	}
	if i != n || last.Outcome != dispatch.Prepared || last.Children != n {
		t.Fatalf("%d child events; the input %s, %d children", i, last.Outcome, last.Children)
	}
	if peak > base && peak-base > childBound {
		t.Errorf("the live heap grew %d MB while %d children streamed, past %d MB", (peak-base)>>20, n, childBound>>20)
	}

	key := dispatch.Key{SHA256: last.Part.SHA256, Stage: s.Identity()}
	e, ok, err := idx.Lookup(t.Context(), key)
	if err != nil || !ok || e.Children != n {
		t.Fatalf("the record: %+v, %v, %v", e, ok, err)
	}
	base, peak, i = heap(), 0, 0
	for c, err := range idx.Children(t.Context(), key) {
		if err != nil {
			t.Fatal(err)
		}
		wantOutcome := dispatch.Seen
		if i == 0 {
			wantOutcome = dispatch.Prepared
		}
		if c.Name != broodName(i, false) || c.Where != broodName(i, true) || c.SHA256 != sum("leaf") || c.Outcome != wantOutcome {
			t.Fatalf("recorded child %d: %+v", i, c)
		}
		i++
		if i%every == 0 {
			peak = max(peak, heap())
		}
	}
	if i != n {
		t.Fatalf("the index read back %d children, want %d", i, n)
	}
	if peak > base && peak-base > childBound {
		t.Errorf("the live heap grew %d MB while %d children were read back", (peak-base)>>20, n)
	}

	events := collect(t, d.Dispatch(t.Context(), dispatch.Input{Path: path}), nil)
	if ev := events[len(events)-1]; len(events) != 1 || ev.Outcome != dispatch.Seen || ev.Children != n {
		t.Fatalf("the seen input: %d events, %s, %d children", len(events), ev.Outcome, ev.Children)
	}
}
