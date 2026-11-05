package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
)

// total returns how many parts the container prepared, by any name.
func (c *container) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, calls := range c.calls {
		n += calls
	}
	return n
}

// pooled writes n inputs, in-0.txt and on, the content of each made by content, and returns
// them.
func pooled(t *testing.T, n int, content func(i int) string) []dispatch.Input {
	t.Helper()
	inputs := make([]dispatch.Input, n)
	for i := range inputs {
		inputs[i] = dispatch.Input{Path: input(t, fmt.Sprintf("in-%d.txt", i), content(i))}
	}
	return inputs
}

// leftIn returns the entries left in dir.
func leftIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestPool: 4 workers over 40 inputs, each finding its own part and one every input shares, in
// a different place. The shared content is prepared exactly once, every other dispatch's part
// seen, carrying the Ref the caller set on the prepared part's output event, which the index
// records only once the part's own event's body has returned. Each input's events come in its
// own dispatch's order, each with its input, and every input is prepared; the working directory
// is empty once the pool returns.
func TestPool(t *testing.T) {
	const n, workers = 40, 4
	world := map[string]string{"shared.txt": "leaf shared"}
	for i := range n {
		world[fmt.Sprintf("own-%d.txt", i)] = fmt.Sprintf("leaf own %d", i)
	}
	f := newFixture(t, world, nil, dispatch.Limits{})
	inputs := pooled(t, n, func(i int) string {
		if i%2 == 0 {
			return fmt.Sprintf("file shared.txt\nfile own-%d.txt", i)
		}
		return fmt.Sprintf("file own-%d.txt\nfile shared.txt", i)
	})

	byInput := map[string][]step{}
	gatherers := map[string]*gatherer{}
	var shared []dispatched
	for pe, err := range f.d.Pool(t.Context(), slices.Values(inputs), workers) {
		if err != nil {
			t.Fatal(err)
		}
		ev := pe.Event
		if ev.Part.Depth == 0 && ev.Part.Path != pe.Input.Path {
			t.Errorf("input %s's event is %s's", pe.Input.Path, ev.Part.Path)
		}
		if ev.Part.Name == "shared.txt" && ev.Outcome != dispatch.Seen {
			// The prepared one, recorded only once its own event's body returns.
			key := dispatch.Key{SHA256: ev.Part.SHA256, Stage: ev.Stage}
			if _, ok, err := f.index.Lookup(t.Context(), key); err != nil || ok {
				t.Errorf("shared.txt recorded before its event's body returned: %t, %v", ok, err)
			}
			if ev.Output != nil {
				ev.Output.Ref = "ref-shared"
			}
		}
		g := gatherers[pe.Input.Path]
		if g == nil {
			g = &gatherer{}
			gatherers[pe.Input.Path] = g
		}
		d, ok := g.add(t, ev)
		if !ok {
			continue
		}
		byInput[pe.Input.Path] = append(byInput[pe.Input.Path], step{ev.Part.Name, ev.Part.Depth, ev.Outcome})
		if ev.Part.Name == "shared.txt" {
			shared = append(shared, d)
		}
	}

	if got := f.text.called("shared.txt"); got != 1 {
		t.Errorf("shared.txt prepared %d times, want once", got)
	}
	outcomes := map[dispatch.Outcome]int{}
	for _, ev := range shared {
		outcomes[ev.Outcome]++
		if ev.Outcome == dispatch.Seen && (len(ev.Outputs) != 1 || ev.Outputs[0].Ref != "ref-shared") {
			t.Errorf("a seen shared.txt's outputs %+v, want the caller's Ref", ev.Outputs)
		}
	}
	if outcomes[dispatch.Prepared] != 1 || outcomes[dispatch.Seen] != n-1 {
		t.Errorf("shared.txt's outcomes %v, want 1 prepared and %d seen", outcomes, n-1)
	}
	for i, in := range inputs {
		first, second := "shared.txt", fmt.Sprintf("own-%d.txt", i)
		if i%2 == 1 {
			first, second = second, first
		}
		got := byInput[in.Path]
		if len(got) != 3 || got[0].name != first || got[1].name != second ||
			got[2] != (step{filepath.Base(in.Path), 0, dispatch.Prepared}) {
			t.Errorf("%s's events %v, want %s, %s, then the input prepared", in.Path, got, first, second)
		}
	}
	if left := leftIn(t, f.dir); len(left) != 0 {
		t.Errorf("the working directory holds %q", left)
	}
}

// TestPoolErrors: an input that can't be read stops its own dispatch alone: its error is yielded
// with it, and every other input is prepared. Workers under 1 is an error, yielded alone.
func TestPoolErrors(t *testing.T) {
	f := newFixture(t, map[string]string{}, nil, dispatch.Limits{})
	inputs := pooled(t, 10, func(i int) string { return fmt.Sprintf("leaf %d", i) })
	missing := dispatch.Input{Path: filepath.Join(t.TempDir(), "missing.txt")}
	inputs = slices.Insert(inputs, 4, missing)
	var failed []dispatch.Input
	prepared := map[string]bool{}
	for pe, err := range f.d.Pool(t.Context(), slices.Values(inputs), 3) {
		if err != nil {
			if pe.Event.Part.Path != "" {
				t.Errorf("an error with an event %+v", pe.Event.Part)
			}
			failed = append(failed, pe.Input)
			continue
		}
		if pe.Event.Part.Depth == 0 && pe.Event.Outcome == dispatch.Prepared {
			prepared[pe.Input.Path] = true
		}
	}
	if !slices.Equal(failed, []dispatch.Input{missing}) {
		t.Errorf("failed inputs %v, want %v", failed, missing)
	}
	if len(prepared) != len(inputs)-1 {
		t.Errorf("%d inputs prepared, want %d", len(prepared), len(inputs)-1)
	}

	var errs []error
	for pe, err := range f.d.Pool(t.Context(), slices.Values(inputs), 0) {
		if err == nil {
			t.Errorf("an event with no workers: %+v", pe)
		}
		errs = append(errs, err)
	}
	if len(errs) != 1 {
		t.Errorf("errors %v, want one", errs)
	}
}

// blockingPool returns a fixture whose container tells entered of each input it prepares and
// blocks it until release closes, and n inputs, each its own content that blocks.
func blockingPool(t *testing.T, n, workers int) (*fixture, []dispatch.Input) {
	t.Helper()
	f := newFixture(t, map[string]string{}, nil, dispatch.Limits{})
	f.text.entered = make(chan string, n)
	f.text.release = make(chan struct{})
	return f, pooled(t, n, func(i int) string { return fmt.Sprintf("block\nnumber %d", i) })
}

// TestPoolCancel: ctx ending while every worker's stage blocks stops all of them: the pool takes
// no more inputs, yields ctx's error alone, last, and returns with its working directory empty
// and every claim released, so another dispatch prepares the inputs.
func TestPoolCancel(t *testing.T) {
	const n, workers = 20, 4
	f, inputs := blockingPool(t, n, workers)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for range workers {
			<-f.text.entered
		}
		cancel()
	}()
	var errs []error
	for pe, err := range f.d.Pool(ctx, slices.Values(inputs), workers) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// Each input's first output comes before its stage blocks.
		if pe.Event.Output == nil {
			t.Errorf("an event of %s after the stages blocked", pe.Event.Part.Name)
		}
	}
	if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
		t.Errorf("errors %v, want context.Canceled alone", errs)
	}
	if got := f.text.total(); got != workers {
		t.Errorf("%d inputs prepared, want %d: a cancelled pool takes no more", got, workers)
	}
	if left := leftIn(t, f.dir); len(left) != 0 {
		t.Errorf("the working directory holds %q", left)
	}
	close(f.text.release)
	for _, in := range inputs[:1] {
		events := dispatchAll(t, f.d, in.Path, nil)
		if last := events[len(events)-1]; last.Outcome != dispatch.Prepared {
			t.Errorf("%s after the pool: %s, want prepared, its claim released", in.Path, last.Outcome)
		}
	}
}

// TestPoolBackPressure: a loop that holds its first event, an input's first output, holds every
// worker at its own: once each worker's input has yielded its first output, no more is prepared,
// and the working directory holds no more than a part directory per worker, the held event's
// files among them. The event's part file is there while the body holds it. Once the loop goes
// on, every input is prepared, each yielding four events, its output, its part's output, its
// part's, and its own, and the working directory is empty when the pool returns. Stopping early
// then stops every worker.
func TestPoolBackPressure(t *testing.T) {
	const n, workers = 30, 3
	world := map[string]string{}
	for i := range n {
		world[fmt.Sprintf("part-%d.txt", i)] = fmt.Sprintf("leaf part %d", i)
	}
	f := newFixture(t, world, nil, dispatch.Limits{})
	inputs := pooled(t, n, func(i int) string { return fmt.Sprintf("file part-%d.txt", i) })
	first := true
	events := 0
	for pe, err := range f.d.Pool(t.Context(), slices.Values(inputs), workers) {
		if err != nil {
			t.Fatal(err)
		}
		events++
		if !first {
			continue
		}
		first = false
		if _, err := os.Stat(pe.Event.Part.Path); err != nil {
			t.Errorf("the held event's file: %v", err)
		}
		// Every worker comes to an event of its own, then waits on this loop.
		deadline := time.Now().Add(10 * time.Second)
		for f.text.total() < workers && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		if got := f.text.total(); got != workers {
			t.Errorf("%d parts prepared while the loop held its first event, want %d: an input per worker", got, workers)
		}
		if left := leftIn(t, f.dir); len(left) > workers {
			t.Errorf("the working directory holds %d entries while the loop held its first event, want at most %d", len(left), workers)
		}
	}
	if events != 4*n || f.text.total() != 2*n {
		t.Errorf("%d events and %d parts prepared, want %d and %d", events, f.text.total(), 4*n, 2*n)
	}
	if left := leftIn(t, f.dir); len(left) != 0 {
		t.Errorf("the working directory holds %q", left)
	}

	// Stopping early: the pool returns once every worker has, its files removed.
	f, inputs = blockingPool(t, n, workers)
	close(f.text.release)
	seen := 0
	for _, err := range f.d.Pool(t.Context(), slices.Values(inputs), workers) {
		if err != nil {
			t.Fatal(err)
		}
		seen++
		break
	}
	if seen != 1 || f.text.total() > workers {
		t.Errorf("%d events taken, %d inputs prepared after stopping at the first", seen, f.text.total())
	}
	if left := leftIn(t, f.dir); len(left) != 0 {
		t.Errorf("the working directory holds %q after stopping early", left)
	}
}

// TestPoolInputs: the inputs are ranged over one at a time as workers free up, and once the loop
// stops, no more are taken from them.
func TestPoolInputs(t *testing.T) {
	f := newFixture(t, map[string]string{}, nil, dispatch.Limits{})
	all := pooled(t, 50, func(i int) string { return fmt.Sprintf("leaf %d", i) })
	taken := 0
	var inputs iter.Seq[dispatch.Input] = func(yield func(dispatch.Input) bool) {
		for _, in := range all {
			taken++
			if !yield(in) {
				return
			}
		}
	}
	for _, err := range f.d.Pool(t.Context(), inputs, 2) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	// The input whose event stopped the loop, the other worker's, and one the feeder held.
	if taken > 3 {
		t.Errorf("%d inputs taken after the loop stopped at its first event, want at most 3", taken)
	}
}
