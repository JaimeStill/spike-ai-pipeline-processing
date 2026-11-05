package dispatch_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// sum returns content's lowercase hex SHA-256.
func sum(content string) string {
	s := sha256.Sum256([]byte(content))
	return hex.EncodeToString(s[:])
}

// TestDepthFirst: every part a stage finds is dispatched, with every part in it, before the
// stage's next; a part's event follows its descendants', the input's last. Each part names its
// size, SHA-256, kind, route and reason, parent, depth, and where it was found, and its event its
// stage's identity, its outputs, and its children.
func TestDepthFirst(t *testing.T) {
	world := map[string]string{
		"b.txt": "file d.txt",
		"c.txt": "leaf c",
		"d.txt": "leaf d",
	}
	f := newFixture(t, world, nil, dispatch.Limits{})
	top := "file b.txt\nfile c.txt"
	events := dispatchAll(t, f.d, input(t, "a.txt", top), nil)
	wantSteps(t, events, []step{
		{"d.txt", 2, dispatch.Prepared},
		{"b.txt", 1, dispatch.Prepared},
		{"c.txt", 1, dispatch.Prepared},
		{"a.txt", 0, dispatch.Prepared},
	})
	d, b, a := byName(t, events, "d.txt"), byName(t, events, "b.txt"), byName(t, events, "a.txt")
	if d.Part.Parent != sum(world["b.txt"]) || b.Part.Parent != sum(top) || a.Part.Parent != "" {
		t.Errorf("parents: d %s, b %s, a %q", d.Part.Parent, b.Part.Parent, a.Part.Parent)
	}
	if d.Part.SHA256 != sum("leaf d") || d.Part.Size != int64(len("leaf d")) || d.Part.Where != "line 1" {
		t.Errorf("d: %+v", d.Part)
	}
	if a.Part.Route != route.Text || a.Part.Reason != "text by its .txt extension" || a.Part.Kind != route.Unknown {
		t.Errorf("a's route: %s, %q, kind %q", a.Part.Route, a.Part.Reason, a.Part.Kind)
	}
	if a.Stage != f.text.Identity() || len(a.Outputs) != 1 || a.Outputs[0].Kind != dispatch.TextOutput {
		t.Errorf("a's stage and outputs: %v, %+v", a.Stage, a.Outputs)
	}
	want := []dispatch.Child{
		{SHA256: sum(world["b.txt"]), Name: "b.txt", Where: "line 1", Size: int64(len(world["b.txt"])), Route: route.Text, Outcome: dispatch.Prepared},
		{SHA256: sum(world["c.txt"]), Name: "c.txt", Where: "line 2", Size: int64(len(world["c.txt"])), Route: route.Text, Outcome: dispatch.Prepared},
	}
	if !slices.Equal(a.Children, want) {
		t.Errorf("a's children:\n got %+v\nwant %+v", a.Children, want)
	}
}

// TestOutcomes: one input reaches every outcome but limited: a route with no stage and a stage
// with no processor yet are no processor yet; a stage's ErrEncrypted, and a failing stage on
// content flagged encrypted, are encrypted; a stage's failure is failed; a ZIP with a ".." entry
// is quarantined, no stage run and its file kept; content equal to an ancestor's is a cycle;
// and content seen earlier in the input is seen, carrying its first event's outputs and flags.
func TestOutcomes(t *testing.T) {
	top := "file x.bin\nfile n.txt\nfile e.txt\nfile f.txt\ncopy traversal.zip\ncopy encrypted.zip\nfile loop.txt\ncopy javascript.pdf\nfile s1.txt\nfile s2.txt"
	world := map[string]string{
		"x.bin":    "\x00\x01no signature",
		"n.txt":    "noproc",
		"e.txt":    "encrypted",
		"f.txt":    "leaf\nfail",
		"loop.txt": "file again.txt",
		"again.txt": "file x.bin\nfile n.txt\nfile e.txt\nfile f.txt\ncopy traversal.zip\ncopy encrypted.zip\nfile loop.txt\n" +
			"copy javascript.pdf\nfile s1.txt\nfile s2.txt",
		"s1.txt": "same",
		"s2.txt": "same",
	}
	if world["again.txt"] != top {
		t.Fatal("again.txt must be the input's content")
	}
	f := newFixture(t, world, nil, dispatch.Limits{})
	var quarantinedPath string
	events := dispatchAll(t, f.d, input(t, "top.txt", top), func(ev *dispatch.Event) {
		if ev.Outcome == dispatch.Quarantined {
			quarantinedPath = ev.Part.Path
		}
	})
	wantSteps(t, events, []step{
		{"x.bin", 1, dispatch.NoProcessor},
		{"n.txt", 1, dispatch.NoProcessor},
		{"e.txt", 1, dispatch.Encrypted},
		{"f.txt", 1, dispatch.Failed},
		{"traversal.zip", 1, dispatch.Quarantined},
		{"encrypted.zip", 1, dispatch.Encrypted},
		{"again.txt", 2, dispatch.Cycle},
		{"loop.txt", 1, dispatch.Prepared},
		{"javascript.pdf", 1, dispatch.Prepared},
		{"s1.txt", 1, dispatch.Prepared},
		{"s2.txt", 1, dispatch.Seen},
		{"top.txt", 0, dispatch.Prepared},
	})
	if ev := byName(t, events, "x.bin"); ev.Reason != "no stage for route unrouted" || ev.Stage != (dispatch.Identity{}) {
		t.Errorf("x.bin: %q, %v", ev.Reason, ev.Stage)
	}
	if ev := byName(t, events, "n.txt"); ev.Reason != "container: no processor yet for this" {
		t.Errorf("n.txt: %q", ev.Reason)
	}
	if ev := byName(t, events, "e.txt"); !errors.Is(ev.Err, dispatch.ErrEncrypted) {
		t.Errorf("e.txt: %v", ev.Err)
	}
	if ev := byName(t, events, "f.txt"); ev.Err == nil || len(ev.Outputs) != 1 {
		t.Errorf("f.txt: %v, its outputs before the failure %+v", ev.Err, ev.Outputs)
	}
	q := byName(t, events, "traversal.zip")
	if !(inspect.Report{Flags: q.Part.Flags}).Has(inspect.ZIPTraversal) || q.Reason == "" {
		t.Errorf("traversal.zip: %+v, %q", q.Part.Flags, q.Reason)
	}
	if _, err := os.Stat(quarantinedPath); err != nil {
		t.Errorf("the quarantined file isn't kept: %v", err)
	}
	if enc := byName(t, events, "encrypted.zip"); !(inspect.Report{Flags: enc.Part.Flags}).Has(inspect.ZIPEncrypted) || enc.Err == nil {
		t.Errorf("encrypted.zip: %+v, %v", enc.Part.Flags, enc.Err)
	}
	if f.archive.called() != 1 {
		t.Errorf("archive stage ran %d times, want once: never for the quarantined ZIP", f.archive.called())
	}
	if ev := byName(t, events, "again.txt"); ev.Reason != "same content as its ancestor at depth 0" {
		t.Errorf("again.txt: %q", ev.Reason)
	}
	s1, s2 := byName(t, events, "s1.txt"), byName(t, events, "s2.txt")
	if s2.Recorded != dispatch.Prepared || s2.Stage != f.text.Identity() || len(s2.Outputs) != 1 || s2.Outputs[0].Path != "" {
		t.Errorf("s2.txt: recorded %s, stage %v, outputs %+v", s2.Recorded, s2.Stage, s2.Outputs)
	}
	if s1.Part.SHA256 != s2.Part.SHA256 || f.text.called("s2.txt") != 0 {
		t.Errorf("s2.txt prepared %d times", f.text.called("s2.txt"))
	}
}

// TestRetry: a part with a failed descendant is not recorded, so a second dispatch over the same
// index prepares it again, and the failure again, while its complete parts are seen.
func TestRetry(t *testing.T) {
	world := map[string]string{"ok.txt": "leaf", "f.txt": "fail"}
	f := newFixture(t, world, nil, dispatch.Limits{})
	path := input(t, "top.txt", "file ok.txt\nfile f.txt")
	dispatchAll(t, f.d, path, nil)
	events := dispatchAll(t, f.d, path, nil)
	wantSteps(t, events, []step{
		{"ok.txt", 1, dispatch.Seen},
		{"f.txt", 1, dispatch.Failed},
		{"top.txt", 0, dispatch.Prepared},
	})
	if n := f.text.called("f.txt"); n != 2 {
		t.Errorf("f.txt prepared %d times, want 2", n)
	}
}

// TestLimits: no cap applies by default; each cap the caller sets limits the parts past it,
// neither inspected nor prepared, and their siblings go on.
func TestLimits(t *testing.T) {
	world := map[string]string{
		"b.txt":   "file d.txt",
		"d.txt":   "leaf d",
		"c.txt":   "leaf c",
		"big.txt": "a line longer than the bytes cap leaves room for",
	}
	top := "file b.txt\nfile big.txt\nfile c.txt"
	for _, tc := range []struct {
		name   string
		limits dispatch.Limits
		want   []step
	}{
		{"none", dispatch.Limits{}, []step{
			{"d.txt", 2, dispatch.Prepared}, {"b.txt", 1, dispatch.Prepared},
			{"big.txt", 1, dispatch.Prepared}, {"c.txt", 1, dispatch.Prepared}, {"top.txt", 0, dispatch.Prepared},
		}},
		{"depth", dispatch.Limits{Depth: 1}, []step{
			{"d.txt", 2, dispatch.Limited}, {"b.txt", 1, dispatch.Prepared},
			{"big.txt", 1, dispatch.Prepared}, {"c.txt", 1, dispatch.Prepared}, {"top.txt", 0, dispatch.Prepared},
		}},
		{"parts", dispatch.Limits{Parts: 3}, []step{
			{"d.txt", 2, dispatch.Prepared}, {"b.txt", 1, dispatch.Prepared},
			{"big.txt", 1, dispatch.Limited}, {"c.txt", 1, dispatch.Limited}, {"top.txt", 0, dispatch.Prepared},
		}},
		{"bytes", dispatch.Limits{Bytes: int64(len(top) + len("file d.txt") + len("leaf d") + len("leaf c"))}, []step{
			{"d.txt", 2, dispatch.Prepared}, {"b.txt", 1, dispatch.Prepared},
			{"big.txt", 1, dispatch.Limited}, {"c.txt", 1, dispatch.Prepared}, {"top.txt", 0, dispatch.Prepared},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, world, nil, tc.limits)
			events := dispatchAll(t, f.d, input(t, "top.txt", top), nil)
			wantSteps(t, events, tc.want)
			for _, ev := range events {
				if ev.Outcome == dispatch.Limited && (ev.Reason == "" || ev.Part.Flags != nil || f.text.called(ev.Part.Name) != 0) {
					t.Errorf("%s: limited, yet %q, flags %v, prepared %d times", ev.Part.Name, ev.Reason, ev.Part.Flags, f.text.called(ev.Part.Name))
				}
			}
		})
	}
	t.Run("limited is not recorded", func(t *testing.T) {
		index := dispatch.NewMemoryIndex()
		path := input(t, "top.txt", top)
		dispatchAll(t, newFixture(t, world, index, dispatch.Limits{Depth: 1}).d, path, nil)
		events := dispatchAll(t, newFixture(t, world, index, dispatch.Limits{}).d, path, nil)
		wantSteps(t, events, []step{
			{"d.txt", 2, dispatch.Prepared}, {"b.txt", 1, dispatch.Prepared},
			{"big.txt", 1, dispatch.Seen}, {"c.txt", 1, dispatch.Seen}, {"top.txt", 0, dispatch.Prepared},
		})
	})
}

// TestSeenAcrossInputs: two inputs sharing an index prepare their shared content once; the
// second's seen events carry the Refs the caller set on the first's outputs, not their removed
// paths, and the flags the index recorded.
func TestSeenAcrossInputs(t *testing.T) {
	world := map[string]string{"shared.txt": "file inner.txt", "inner.txt": "leaf"}
	for name, newIndex := range indexes() {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, world, newIndex(t, nil), dispatch.Limits{})
			dispatchAll(t, f.d, input(t, "one.txt", "file shared.txt\ncopy javascript.pdf"), func(ev *dispatch.Event) {
				for i := range ev.Outputs {
					ev.Outputs[i].Ref = "ref:" + ev.Part.Name
				}
			})
			events := dispatchAll(t, f.d, input(t, "two.txt", "copy javascript.pdf\nfile shared.txt"), nil)
			wantSteps(t, events, []step{
				{"javascript.pdf", 1, dispatch.Seen},
				{"shared.txt", 1, dispatch.Seen},
				{"two.txt", 0, dispatch.Prepared},
			})
			shared := byName(t, events, "shared.txt")
			if len(shared.Outputs) != 1 || shared.Outputs[0].Ref != "ref:shared.txt" || shared.Outputs[0].Path != "" {
				t.Errorf("shared.txt's outputs: %+v", shared.Outputs)
			}
			if len(shared.Children) != 1 || shared.Children[0].Name != "inner.txt" || shared.Children[0].SHA256 != sum("leaf") {
				t.Errorf("shared.txt's children: %+v", shared.Children)
			}
			pdf := byName(t, events, "javascript.pdf")
			if !(inspect.Report{Flags: pdf.Part.Flags}).Has(inspect.PDFJavaScript) || string(pdf.Outputs[0].Value) != `{"name":"javascript.pdf"}` {
				t.Errorf("javascript.pdf: flags %+v, outputs %+v", pdf.Part.Flags, pdf.Outputs)
			}
			if f.text.called("shared.txt") != 1 || f.text.called("inner.txt") != 1 || f.pdf.called() != 1 {
				t.Errorf("prepared shared %d, inner %d, pdf %d times, want each once",
					f.text.called("shared.txt"), f.text.called("inner.txt"), f.pdf.called())
			}
		})
	}
}

// TestRemoval: each part's file, and its working directory with its outputs' files, is there
// while the caller's loop body runs and gone once it returns, unless the caller kept it; a file
// the caller moved away is no error; the input is never removed.
func TestRemoval(t *testing.T) {
	world := map[string]string{"keep.txt": "leaf keep", "move.txt": "leaf move", "drop.txt": "leaf drop"}
	f := newFixture(t, world, nil, dispatch.Limits{})
	path := input(t, "top.txt", "file keep.txt\nfile move.txt\nfile drop.txt")
	moved := filepath.Join(t.TempDir(), "moved.txt")
	paths := map[string][]string{}
	dispatchAll(t, f.d, path, func(ev *dispatch.Event) {
		files := []string{ev.Part.Path}
		for _, o := range ev.Outputs {
			files = append(files, o.Path)
		}
		for _, p := range files {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: %s missing in the loop's body: %v", ev.Part.Name, p, err)
			}
		}
		paths[ev.Part.Name] = files
		switch ev.Part.Name {
		case "keep.txt":
			ev.Keep()
		case "move.txt":
			if err := os.Rename(ev.Outputs[0].Path, moved); err != nil {
				t.Fatal(err)
			}
		}
	})
	exists := func(p string) bool {
		_, err := os.Stat(p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		return err == nil
	}
	for name, files := range paths {
		for _, p := range files {
			want := name == "keep.txt" || p == path
			if got := exists(p); got != want {
				t.Errorf("%s: %s exists %v, want %v", name, p, got, want)
			}
		}
	}
	if !exists(moved) {
		t.Error("the moved output is gone")
	}
	// The working directory holds only the kept part's directory and its parent's file.
	var left []string
	if err := filepath.WalkDir(f.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			left = append(left, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{paths["keep.txt"][0], paths["keep.txt"][1]}; !sameSet(left, want) {
		t.Errorf("left in the working directory: %v, want %v", left, want)
	}
}

// sameSet reports whether a and b hold the same strings.
func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// TestKeptOutputPath: a kept event's outputs are recorded with their paths, which stay.
func TestKeptOutputPath(t *testing.T) {
	f := newFixture(t, map[string]string{}, nil, dispatch.Limits{})
	path := input(t, "top.txt", "leaf")
	dispatchAll(t, f.d, path, func(ev *dispatch.Event) { ev.Keep() })
	events := dispatchAll(t, f.d, path, nil)
	if p := events[0].Outputs[0].Path; events[0].Outcome != dispatch.Seen || p == "" {
		t.Fatalf("seen %s, output path %q", events[0].Outcome, p)
	} else if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

// TestStopEarly: a loop that stops early stops the stage, releases every claim, and leaves the
// working directory empty, so a second dispatch prepares everything.
func TestStopEarly(t *testing.T) {
	world := map[string]string{"b.txt": "file d.txt", "d.txt": "leaf", "c.txt": "leaf c"}
	f := newFixture(t, world, nil, dispatch.Limits{})
	path := input(t, "top.txt", "file b.txt\nfile c.txt")
	for ev, err := range f.d.Dispatch(t.Context(), dispatch.Input{Path: path}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Part.Name != "d.txt" {
			t.Fatalf("first event %s", ev.Part.Name)
		}
		break
	}
	if entries, err := os.ReadDir(f.dir); err != nil || len(entries) != 0 {
		t.Fatalf("working directory: %v, %v", entries, err)
	}
	events := dispatchAll(t, f.d, path, nil)
	wantSteps(t, events, []step{
		{"d.txt", 2, dispatch.Seen},
		{"b.txt", 1, dispatch.Prepared},
		{"c.txt", 1, dispatch.Prepared},
		{"top.txt", 0, dispatch.Prepared},
	})
}

// failingRecord is an index whose Record fails, once the entry's claim has been released.
type failingRecord struct{ dispatch.Index }

var errRecord = errors.New("told to fail recording")

func (f failingRecord) Record(ctx context.Context, key dispatch.Key, token string, _ dispatch.Entry) error {
	_ = f.Release(ctx, key, token)
	return errRecord
}

// TestRecordFails: an index that fails to record stops the dispatch with its error, alone and
// last, and the files of the part it failed on are removed all the same.
func TestRecordFails(t *testing.T) {
	f := newFixture(t, map[string]string{"b.txt": "leaf"}, failingRecord{dispatch.NewMemoryIndex()}, dispatch.Limits{})
	var events []dispatch.Event
	var errs []error
	for ev, err := range f.d.Dispatch(t.Context(), dispatch.Input{Path: input(t, "top.txt", "file b.txt")}) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		events = append(events, ev)
	}
	wantSteps(t, events, []step{{"b.txt", 1, dispatch.Prepared}})
	if len(errs) != 1 || !errors.Is(errs[0], errRecord) {
		t.Fatalf("errors: %v", errs)
	}
	if entries, err := os.ReadDir(f.dir); err != nil || len(entries) != 0 {
		t.Fatalf("working directory: %v, %v", entries, err)
	}
}

// TestInputErrors: an input that can't be read is an error alone, which stops the dispatch; a
// cancelled context stops it too.
func TestInputErrors(t *testing.T) {
	f := newFixture(t, map[string]string{}, nil, dispatch.Limits{})
	var errs []error
	for ev, err := range f.d.Dispatch(t.Context(), dispatch.Input{Path: filepath.Join(t.TempDir(), "missing.txt")}) {
		if err == nil {
			t.Fatalf("event %v", ev)
		}
		errs = append(errs, err)
	}
	if len(errs) != 1 || !errors.Is(errs[0], fs.ErrNotExist) {
		t.Fatalf("errors: %v", errs)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	errs = nil
	for _, err := range f.d.Dispatch(ctx, dispatch.Input{Path: input(t, "a.txt", "leaf")}) {
		errs = append(errs, err)
	}
	if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("a cancelled dispatch yielded %v, want the context's error alone", errs)
	}
}

// TestNew: a config with no index, no working directory, a missing one, a nil stage, or a
// negative limit is refused.
func TestNew(t *testing.T) {
	index := dispatch.NewMemoryIndex()
	dir := t.TempDir()
	for name, cfg := range map[string]dispatch.Config{
		"no index":     {Dir: dir},
		"no dir":       {Index: index},
		"missing dir":  {Index: index, Dir: filepath.Join(dir, "missing")},
		"nil stage":    {Index: index, Dir: dir, Stages: dispatch.Stages{route.Text: nil}},
		"negative cap": {Index: index, Dir: dir, Limits: dispatch.Limits{Depth: -1}},
	} {
		if _, err := dispatch.New(cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestHashConfig: the same configuration hashes the same, whatever its map's order, and another
// differently.
func TestHashConfig(t *testing.T) {
	a, err := dispatch.HashConfig(map[string]int{"dpi": 150, "scale": 2})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := dispatch.HashConfig(map[string]int{"scale": 2, "dpi": 150})
	c, _ := dispatch.HashConfig(map[string]int{"dpi": 300, "scale": 2})
	if a != b || a == c || len(a) != 64 {
		t.Fatalf("hashes %s, %s, %s", a, b, c)
	}
}
