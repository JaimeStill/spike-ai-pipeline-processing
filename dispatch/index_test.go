package dispatch_test

import (
	"context"
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

// entry returns an entry with every field set.
func entry() dispatch.Entry {
	return dispatch.Entry{
		Outcome: dispatch.Prepared,
		Reason:  "why",
		Outputs: []dispatch.Output{{
			Kind: dispatch.RecordOutput, Name: "page 1", MediaType: "application/json", Path: "/kept/out.json",
			Value: []byte(`{"a":1}`), Meta: map[string]string{"page": "1"}, Ref: "ref-1",
		}},
		Children: []dispatch.Child{{SHA256: sum("child"), Name: "c.png", Where: "attachment 1", Size: 5, Route: route.Vision, Outcome: dispatch.Seen}},
	}
}

// TestIndexContract: both shipped indexes keep the Index contract: lookups by key, the stage's
// identity part of it; one claimant at a time, a claim ended by its own token only, and none
// while the key is recorded; waits that return at once on an unclaimed or recorded key, wake on
// a record with its entry and on a release with none, and end with their context; entries and
// flags that share no memory with the caller's.
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
				in := entry()
				if err := idx.Record(ctx, key, tok, in); err != nil {
					t.Fatal(err)
				}
				in.Outputs[0].Value[0] = 'X'
				in.Outputs[0].Meta["page"] = "changed"
				got, ok, err := idx.Lookup(ctx, key)
				if err != nil || !ok || !reflect.DeepEqual(got, entry()) {
					t.Fatalf("lookup: %+v, %v, %v", got, ok, err)
				}
				got.Outputs[0].Value[0] = 'Y'
				got.Children[0].Name = "changed"
				if again, _, _ := idx.Lookup(ctx, key); !reflect.DeepEqual(again, entry()) {
					t.Fatalf("the index shares memory with a lookup: %+v", again)
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
				if err := idx.Record(ctx, key, tok, entry()); err != nil {
					t.Fatal(err)
				}
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
			results := make(chan []dispatch.Event, 2)
			run := func(path string) {
				var events []dispatch.Event
				for ev, err := range f.d.Dispatch(t.Context(), dispatch.Input{Path: path}) {
					if err != nil {
						t.Error(err)
						break
					}
					events = append(events, ev)
				}
				results <- events
			}
			go run(input(t, "first.txt", content))
			<-f.text.entered
			go run(input(t, "second.txt", content))
			<-idx.waiting
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

// TestFileIndexStale: a file index's claim older than its stale timeout is taken over by the
// next claimant, and a waiter on it returns; the old claimant's release leaves the new claim.
// Two file indexes on one directory share their claims and entries, as two processes would.
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
	if err := a.Release(ctx, key, old); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.Claim(ctx, key); ok {
		t.Fatal("the stale claimant's release ended the new claim")
	}
	if err := b.Record(ctx, key, tok, entry()); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := a.Lookup(ctx, key); err != nil || !ok || !reflect.DeepEqual(got, entry()) {
		t.Fatalf("the other index's lookup: %+v, %v, %v", got, ok, err)
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
