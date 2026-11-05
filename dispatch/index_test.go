package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// indexes returns a constructor for each shipped index, each taking the clock the index tells a
// claim's age by, time.Now when nil; the file index polls every millisecond.
func indexes() map[string]func(*testing.T, func() time.Time) dispatch.Index {
	return map[string]func(*testing.T, func() time.Time) dispatch.Index{
		"memory": func(*testing.T, func() time.Time) dispatch.Index { return dispatch.NewMemoryIndex() },
		"file": func(t *testing.T, now func() time.Time) dispatch.Index {
			f, err := dispatch.NewFileIndex(t.TempDir(), dispatch.FileOptions{Poll: time.Millisecond, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			return f
		},
	}
}

var (
	stageA = dispatch.Identity{Name: "a", Version: "1.0", Config: "c1"}
	stageB = dispatch.Identity{Name: "a", Version: "1.0", Config: "c2"}
)

// entry returns an entry with every field set, as recorded with outputs() and kids() appended.
func entry() dispatch.Entry {
	return dispatch.Entry{Outcome: dispatch.Prepared, Reason: "why", Outputs: len(outputs()), Children: len(kids())}
}

// kids returns two children with every field set between them.
func kids() []dispatch.Child {
	return []dispatch.Child{
		{SHA256: sum("child"), Name: "c.png", Where: "attachment 1", Size: 5, Route: route.Vision, Outcome: dispatch.Seen},
		{SHA256: sum("other"), Size: 0, Outcome: dispatch.Failed},
	}
}

// outputs returns two outputs with every field set between them.
func outputs() []dispatch.Output {
	return []dispatch.Output{
		{
			Kind: dispatch.RecordOutput, Name: "page 1", MediaType: "application/json", Path: "/kept/out.json",
			Value: []byte(`{"a":1}`), Meta: map[string]string{"page": "1"}, Ref: "ref-1",
		},
		{Kind: dispatch.MarkdownOutput, Name: "page 1", Value: []byte("# One")},
	}
}

// record appends outputs() and kids(), interleaved, to key's claim token holds and records
// entry(), its counts set wrong, which the index ignores; it returns the outputs it appended,
// which the test may change to show the index shares no memory with them.
func record(t *testing.T, idx dispatch.Index, key dispatch.Key, token string) []dispatch.Output {
	t.Helper()
	in := outputs()
	for i, o := range in {
		if err := idx.Append(t.Context(), key, token, o); err != nil {
			t.Fatal(err)
		}
		if err := idx.AppendChild(t.Context(), key, token, kids()[i]); err != nil {
			t.Fatal(err)
		}
	}
	e := entry()
	e.Outputs, e.Children = 99, 99
	if err := idx.Record(t.Context(), key, token, e); err != nil {
		t.Fatal(err)
	}
	return in
}

// recorded returns key's recorded outputs, read back from idx; an error fails the test.
func recorded(t *testing.T, idx dispatch.Index, key dispatch.Key) []dispatch.Output {
	t.Helper()
	var out []dispatch.Output
	for o, err := range idx.Outputs(t.Context(), key) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	return out
}

// children returns key's recorded children, read back from idx; an error fails the test.
func children(t *testing.T, idx dispatch.Index, key dispatch.Key) []dispatch.Child {
	t.Helper()
	var out []dispatch.Child
	for c, err := range idx.Children(t.Context(), key) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// TestIndexContract: both shipped indexes keep the Index contract: lookups by key, the stage's
// identity part of it; one claimant at a time, a claim ended by its own token only, and none
// while the key is recorded; outputs and children appended under a claim, interleaved, each
// recorded in their order and counted by the record, read back by Outputs and Children, and
// dropped by a release; waits that return at once on an
// unclaimed or recorded key, wake on a record with its entry and on a release with none, and end
// with their context; entries, outputs, and flags that share no memory with the caller's.
func TestIndexContract(t *testing.T) {
	for name, newIndex := range indexes() {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			key := dispatch.Key{SHA256: sum("content"), Stage: stageA}
			other := dispatch.Key{SHA256: key.SHA256, Stage: stageB}

			t.Run("miss", func(t *testing.T) {
				idx := newIndex(t, nil)
				if _, ok, err := idx.Lookup(ctx, key); err != nil || ok {
					t.Fatalf("lookup: %v, %v", ok, err)
				}
				if _, ok, err := idx.Wait(ctx, key); err != nil || ok {
					t.Fatalf("wait on an unclaimed key: %v, %v", ok, err)
				}
				if _, ok, err := idx.Flags(ctx, key.SHA256); err != nil || ok {
					t.Fatalf("flags: %v, %v", ok, err)
				}
				if got := recorded(t, idx, key); len(got) != 0 {
					t.Fatalf("outputs of an unrecorded key: %+v", got)
				}
				if got := children(t, idx, key); len(got) != 0 {
					t.Fatalf("children of an unrecorded key: %+v", got)
				}
			})

			t.Run("claim", func(t *testing.T) {
				idx := newIndex(t, nil)
				tok, ok, err := idx.Claim(ctx, key)
				if err != nil || !ok || tok == "" {
					t.Fatalf("first claim: %q, %v, %v", tok, ok, err)
				}
				if _, ok, _ := idx.Claim(ctx, key); ok {
					t.Fatal("second claim succeeded")
				}
				if _, ok, _ := idx.Claim(ctx, other); !ok {
					t.Fatal("another stage's key is claimed")
				}
				if err := idx.Release(ctx, key, tok+"x"); err != nil {
					t.Fatal(err)
				}
				if _, ok, _ := idx.Claim(ctx, key); ok {
					t.Fatal("another token released the claim")
				}
				if err := idx.Release(ctx, key, tok); err != nil {
					t.Fatal(err)
				}
				if _, ok, _ := idx.Claim(ctx, key); !ok {
					t.Fatal("claim after release failed")
				}
			})

			t.Run("record", func(t *testing.T) {
				idx := newIndex(t, nil)
				tok, _, _ := idx.Claim(ctx, key)
				in := record(t, idx, key, tok)
				in[0].Value[0] = 'X'
				in[0].Meta["page"] = "changed"
				got, ok, err := idx.Lookup(ctx, key)
				if err != nil || !ok || !reflect.DeepEqual(got, entry()) {
					t.Fatalf("lookup: %+v, %v, %v", got, ok, err)
				}
				outs := recorded(t, idx, key)
				if !reflect.DeepEqual(outs, outputs()) {
					t.Fatalf("outputs:\n got %+v\nwant %+v", outs, outputs())
				}
				if got := children(t, idx, key); !reflect.DeepEqual(got, kids()) {
					t.Fatalf("children:\n got %+v\nwant %+v", got, kids())
				}
				outs[0].Value[0] = 'Y'
				outs[0].Meta["page"] = "changed"
				if again := recorded(t, idx, key); !reflect.DeepEqual(again, outputs()) {
					t.Fatalf("the index shares memory with its outputs: %+v", again)
				}
				// Outputs and Children stop when their loops do.
				for _, err := range idx.Outputs(ctx, key) {
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				for _, err := range idx.Children(ctx, key) {
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				if _, ok, _ := idx.Lookup(ctx, other); ok {
					t.Fatal("another stage's key is recorded")
				}
				if _, ok, _ := idx.Claim(ctx, key); ok {
					t.Fatal("a recorded key was claimed")
				}
				if got, ok, err := idx.Wait(ctx, key); err != nil || !ok || !reflect.DeepEqual(got, entry()) {
					t.Fatalf("wait on a recorded key: %+v, %v, %v", got, ok, err)
				}
			})

			t.Run("release drops the pending outputs and children", func(t *testing.T) {
				idx := newIndex(t, nil)
				tok, _, _ := idx.Claim(ctx, key)
				for i, o := range outputs() {
					if err := idx.Append(ctx, key, tok, o); err != nil {
						t.Fatal(err)
					}
					if err := idx.AppendChild(ctx, key, tok, kids()[i]); err != nil {
						t.Fatal(err)
					}
				}
				if err := idx.Release(ctx, key, tok); err != nil {
					t.Fatal(err)
				}
				if _, ok, _ := idx.Lookup(ctx, key); ok {
					t.Fatal("a released claim's outputs were recorded")
				}
				tok, ok, err := idx.Claim(ctx, key)
				if err != nil || !ok {
					t.Fatalf("claim after release: %v, %v", ok, err)
				}
				if err := idx.Record(ctx, key, tok, dispatch.Entry{Outcome: dispatch.NoProcessor}); err != nil {
					t.Fatal(err)
				}
				if e, _, _ := idx.Lookup(ctx, key); e.Outputs != 0 || e.Children != 0 || len(recorded(t, idx, key)) != 0 || len(children(t, idx, key)) != 0 {
					t.Fatalf("a record after a release holds the released outputs or children: %+v", e)
				}
			})

			t.Run("wait wakes on record", func(t *testing.T) {
				idx := newIndex(t, nil)
				tok, _, _ := idx.Claim(ctx, key)
				done := make(chan dispatch.Entry)
				go func() {
					e, ok, err := idx.Wait(ctx, key)
					if err != nil || !ok {
						t.Errorf("wait: %v, %v", ok, err)
					}
					done <- e
				}()
				time.Sleep(5 * time.Millisecond)
				record(t, idx, key, tok)
				if e := <-done; !reflect.DeepEqual(e, entry()) {
					t.Fatalf("wait's entry: %+v", e)
				}
			})

			t.Run("wait wakes on release", func(t *testing.T) {
				idx := newIndex(t, nil)
				tok, _, _ := idx.Claim(ctx, key)
				done := make(chan bool)
				go func() {
					_, ok, err := idx.Wait(ctx, key)
					if err != nil {
						t.Error(err)
					}
					done <- ok
				}()
				time.Sleep(5 * time.Millisecond)
				if err := idx.Release(ctx, key, tok); err != nil {
					t.Fatal(err)
				}
				if <-done {
					t.Fatal("wait reported a record after a release")
				}
			})

			t.Run("wait ends with its context", func(t *testing.T) {
				idx := newIndex(t, nil)
				if _, ok, err := idx.Claim(ctx, key); err != nil || !ok {
					t.Fatalf("claim: %v, %v", ok, err)
				}
				wctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
				if _, _, err := idx.Wait(wctx, key); err != context.DeadlineExceeded {
					t.Fatalf("got %v, want the deadline", err)
				}
			})

			t.Run("one claimant", func(t *testing.T) {
				idx := newIndex(t, nil)
				var mu sync.Mutex
				won := 0
				var wg sync.WaitGroup
				for range 16 {
					wg.Go(func() {
						if _, ok, err := idx.Claim(ctx, key); err != nil {
							t.Error(err)
						} else if ok {
							mu.Lock()
							won++
							mu.Unlock()
						}
					})
				}
				wg.Wait()
				if won != 1 {
					t.Fatalf("%d claimants won", won)
				}
			})

			t.Run("flags", func(t *testing.T) {
				idx := newIndex(t, nil)
				flags := []inspect.Flag{{Check: inspect.PDFJavaScript, Class: inspect.Active, Evidence: "/JavaScript at offset 1"}}
				if err := idx.RecordFlags(ctx, key.SHA256, flags); err != nil {
					t.Fatal(err)
				}
				flags[0].Evidence = "changed"
				got, ok, err := idx.Flags(ctx, key.SHA256)
				if err != nil || !ok || len(got) != 1 || got[0].Evidence != "/JavaScript at offset 1" {
					t.Fatalf("flags: %+v, %v, %v", got, ok, err)
				}
				if err := idx.RecordFlags(ctx, sum("clean"), nil); err != nil {
					t.Fatal(err)
				}
				if got, ok, err := idx.Flags(ctx, sum("clean")); err != nil || !ok || len(got) != 0 {
					t.Fatalf("no flags: %+v, %v, %v", got, ok, err)
				}
			})
		})
	}
}

// waitSignal is an index that tells waiting when a Wait begins.
type waitSignal struct {
	dispatch.Index
	waiting chan struct{}
}

func (w *waitSignal) Wait(ctx context.Context, key dispatch.Key) (dispatch.Entry, bool, error) {
	select {
	case w.waiting <- struct{}{}:
	default:
	}
	return w.Index.Wait(ctx, key)
}

// TestConcurrentDispatch: two dispatches of the same content over one index, run together: one
// claims and prepares it, the other waits on the claim, then reports it seen, with the outputs
// the first recorded; the stage runs once.
func TestConcurrentDispatch(t *testing.T) {
	for name, newIndex := range indexes() {
		t.Run(name, func(t *testing.T) {
			idx := &waitSignal{Index: newIndex(t, nil), waiting: make(chan struct{}, 1)}
			f := newFixture(t, map[string]string{}, idx, dispatch.Limits{})
			f.text.entered = make(chan string, 1)
			f.text.release = make(chan struct{})
			content := "block"
			results := make(chan []dispatched, 2)
			run := func(path string) {
				var g gatherer
				var events []dispatched
				for ev, err := range f.d.Dispatch(t.Context(), dispatch.Input{Path: path}) {
					if err != nil {
						t.Error(err)
						break
					}
					if d, ok := g.add(t, ev); ok {
						events = append(events, d)
					}
				}
				results <- events
			}
			go run(input(t, "first.txt", content))
			<-f.text.entered
			go run(input(t, "second.txt", content))
			// A second dispatch that enters the stage, rather than waiting on the claim, is a
			// duplicate prepare.
			select {
			case <-idx.waiting:
			case name := <-f.text.entered:
				close(f.text.release)
				t.Fatalf("%s entered the stage while another dispatch held the claim", name)
			}
			close(f.text.release)
			outcomes := map[dispatch.Outcome]int{}
			for range 2 {
				events := <-results
				if len(events) != 1 {
					t.Fatalf("events: %v", steps(events))
				}
				outcomes[events[0].Outcome]++
				if events[0].Outcome == dispatch.Seen && len(events[0].Outputs) != 1 {
					t.Errorf("seen without the recorded outputs: %+v", events[0])
				}
			}
			if outcomes[dispatch.Prepared] != 1 || outcomes[dispatch.Seen] != 1 {
				t.Fatalf("outcomes: %v", outcomes)
			}
			if n := f.text.called("first.txt") + f.text.called("second.txt"); n != 1 {
				t.Fatalf("stage ran %d times", n)
			}
		})
	}
}

// clock is a settable clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// pendingIn returns the pending files in a file index's claims directory.
func pendingIn(t *testing.T, dir string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "claims", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// TestFileIndexStale: a file index's claim older than its stale timeout is taken over by the
// next claimant, its pending outputs removed, and a waiter on it returns; the old claimant's
// record then records nothing and its release leaves the new claim, which records. Two file
// indexes on one directory share their claims and entries, as two processes would.
func TestFileIndexStale(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	opts := dispatch.FileOptions{Stale: time.Minute, Poll: time.Millisecond, Now: c.now}
	a, err := dispatch.NewFileIndex(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := dispatch.NewFileIndex(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	key := dispatch.Key{SHA256: sum("content"), Stage: stageA}
	old, ok, err := a.Claim(ctx, key)
	if err != nil || !ok {
		t.Fatalf("claim: %v, %v", ok, err)
	}
	if err := a.Append(ctx, key, old, outputs()[1]); err != nil {
		t.Fatal(err)
	}
	if got := pendingIn(t, dir); len(got) != 1 {
		t.Fatalf("pending files %q, want the claim's", got)
	}
	c.advance(time.Minute)
	if _, ok, _ := b.Claim(ctx, key); ok {
		t.Fatal("a claim a minute old, not past the timeout, was taken over")
	}
	waited := make(chan bool)
	go func() {
		_, ok, err := b.Wait(ctx, key)
		if err != nil {
			t.Error(err)
		}
		waited <- ok
	}()
	time.Sleep(5 * time.Millisecond)
	c.advance(time.Second)
	if <-waited {
		t.Fatal("a wait on a stale claim reported a record")
	}
	tok, ok, err := b.Claim(ctx, key)
	if err != nil || !ok || tok == old {
		t.Fatalf("reclaim: %q, %v, %v", tok, ok, err)
	}
	if got := pendingIn(t, dir); len(got) != 0 {
		t.Fatalf("pending files %q after the takeover, want the stale claim's removed", got)
	}
	if err := a.Record(ctx, key, old, entry()); err != nil {
		t.Fatalf("the stale claimant's record: %v", err)
	}
	if _, ok, _ := a.Lookup(ctx, key); ok {
		t.Fatal("the stale claimant's record, its pending outputs removed, was recorded")
	}
	if _, ok, _ := a.Claim(ctx, key); ok {
		t.Fatal("the stale claimant's release ended the new claim")
	}
	record(t, b, key, tok)
	if got, ok, err := a.Lookup(ctx, key); err != nil || !ok || !reflect.DeepEqual(got, entry()) {
		t.Fatalf("the other index's lookup: %+v, %v, %v", got, ok, err)
	}
	if got := recorded(t, a, key); !reflect.DeepEqual(got, outputs()) {
		t.Fatalf("the other index's outputs: %+v", got)
	}
	if got := pendingIn(t, dir); len(got) != 0 {
		t.Fatalf("pending files %q after the record", got)
	}
}

// TestFileIndexRecordFails: a record the file index can't write, its entries directory read
// only, fails and records nothing; the release the dispatcher then makes removes the claim's
// pending outputs and children, and the key may be claimed and recorded again.
func TestFileIndexRecordFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	ctx := t.Context()
	dir := t.TempDir()
	idx, err := dispatch.NewFileIndex(dir, dispatch.FileOptions{Poll: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	key := dispatch.Key{SHA256: sum("content"), Stage: stageA}
	tok, ok, err := idx.Claim(ctx, key)
	if err != nil || !ok {
		t.Fatalf("claim: %v, %v", ok, err)
	}
	if err := idx.Append(ctx, key, tok, outputs()[0]); err != nil {
		t.Fatal(err)
	}
	if err := idx.AppendChild(ctx, key, tok, kids()[0]); err != nil {
		t.Fatal(err)
	}
	entries := filepath.Join(dir, "entries")
	if err := os.Chmod(entries, 0o500); err != nil {
		t.Fatal(err)
	}
	err = idx.Record(ctx, key, tok, entry())
	if cerr := os.Chmod(entries, 0o700); cerr != nil {
		t.Fatal(cerr)
	}
	if err == nil {
		t.Fatal("a record into a read-only entries directory: no error")
	}
	if err := idx.Release(ctx, key, tok); err != nil {
		t.Fatal(err)
	}
	if got := pendingIn(t, dir); len(got) != 0 {
		t.Fatalf("pending files %q after the failed record's release", got)
	}
	if _, ok, _ := idx.Lookup(ctx, key); ok {
		t.Fatal("the failed record was recorded")
	}
	tok, ok, err = idx.Claim(ctx, key)
	if err != nil || !ok {
		t.Fatalf("claim after the release: %v, %v", ok, err)
	}
	record(t, idx, key, tok)
	if got := recorded(t, idx, key); !reflect.DeepEqual(got, outputs()) {
		t.Fatalf("outputs recorded after the failure: %+v", got)
	}
}

// TestNewFileIndex: no directory, or a negative duration, is refused.
func TestNewFileIndex(t *testing.T) {
	if _, err := dispatch.NewFileIndex("", dispatch.FileOptions{}); err == nil {
		t.Error("no directory: no error")
	}
	if _, err := dispatch.NewFileIndex(t.TempDir(), dispatch.FileOptions{Stale: -1}); err == nil {
		t.Error("negative stale: no error")
	}
}
