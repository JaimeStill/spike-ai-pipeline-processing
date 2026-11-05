package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// container is the test stage, for route.Text: a container whose content is lines, each a
// directive. "file NAME" finds a part named NAME holding world[NAME]; "copy NAME" finds inspect's
// fixture NAME; "fail", "noproc", and "encrypted" end the stage with a failure, ErrNoProcessor,
// or ErrEncrypted; "block" tells entered the part's name and waits for release to close. Before
// its directives it yields a text output in a file, its content's line count.
type container struct {
	world   map[string]string
	entered chan string
	release chan struct{}

	mu    sync.Mutex
	calls map[string]int
}

func newContainer(world map[string]string) *container {
	return &container{world: world, calls: map[string]int{}}
}

func (c *container) Identity() dispatch.Identity {
	return dispatch.Identity{Name: "container", Version: "1", Config: "test"}
}

// called returns how many times the stage prepared a part named name.
func (c *container) called(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func (c *container) Prepare(ctx context.Context, p dispatch.Part, dir string) iter.Seq2[dispatch.Item, error] {
	return func(yield func(dispatch.Item, error) bool) {
		c.mu.Lock()
		c.calls[p.Name]++
		c.mu.Unlock()
		b, err := os.ReadFile(p.Path)
		if err != nil {
			yield(dispatch.Item{}, err)
			return
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		out := filepath.Join(dir, "lines.txt")
		if err := os.WriteFile(out, fmt.Appendf(nil, "%d", len(lines)), 0o644); err != nil {
			yield(dispatch.Item{}, err)
			return
		}
		o := dispatch.Output{Kind: dispatch.TextOutput, Name: "lines", MediaType: "text/plain", Path: out}
		if !yield(dispatch.OutputItem(o), nil) {
			return
		}
		for i, line := range lines {
			verb, arg, _ := strings.Cut(line, " ")
			where := fmt.Sprintf("line %d", i+1)
			switch verb {
			case "file", "copy":
				var content []byte
				if verb == "file" {
					content = []byte(c.world[arg])
				} else if content, err = os.ReadFile(filepath.Join("..", "inspect", "testdata", arg)); err != nil {
					yield(dispatch.Item{}, err)
					return
				}
				path := filepath.Join(dir, arg)
				if err := os.WriteFile(path, content, 0o644); err != nil {
					yield(dispatch.Item{}, err)
					return
				}
				if !yield(dispatch.PartItem(dispatch.Found{Path: path, Name: arg, Where: where}), nil) {
					return
				}
			case "fail":
				yield(dispatch.Item{}, errors.New("container: told to fail"))
				return
			case "noproc":
				yield(dispatch.Item{}, fmt.Errorf("container: %w for this", dispatch.ErrNoProcessor))
				return
			case "encrypted":
				yield(dispatch.Item{}, fmt.Errorf("container: %w content", dispatch.ErrEncrypted))
				return
			case "block":
				c.entered <- p.Name
				select {
				case <-c.release:
				case <-ctx.Done():
					yield(dispatch.Item{}, ctx.Err())
					return
				}
			}
		}
	}
}

// leaf is a stage that yields one inline record output for each part and counts its calls; with
// err set, it fails with err instead.
type leaf struct {
	name string
	err  error

	mu    sync.Mutex
	calls int
}

func (l *leaf) Identity() dispatch.Identity {
	return dispatch.Identity{Name: l.name, Version: "1", Config: "test"}
}

func (l *leaf) called() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

func (l *leaf) Prepare(_ context.Context, p dispatch.Part, _ string) iter.Seq2[dispatch.Item, error] {
	return func(yield func(dispatch.Item, error) bool) {
		l.mu.Lock()
		l.calls++
		l.mu.Unlock()
		if l.err != nil {
			yield(dispatch.Item{}, l.err)
			return
		}
		yield(dispatch.OutputItem(dispatch.Output{
			Kind: dispatch.RecordOutput, Value: fmt.Appendf(nil, `{"name":%q}`, p.Name),
			Meta: map[string]string{"stage": l.name},
		}), nil)
	}
}

// fixture is a test's dispatcher, its stages, and its working directory.
type fixture struct {
	d       *dispatch.Dispatcher
	index   dispatch.Index
	text    *container
	pdf     *leaf
	archive *leaf
	dir     string
}

// newFixture returns a dispatcher over index, a new MemoryIndex when nil, with the container on
// route.Text, a leaf on route.Document, and a leaf that fails on route.Archive, under limits.
func newFixture(t *testing.T, world map[string]string, index dispatch.Index, limits dispatch.Limits) *fixture {
	t.Helper()
	if index == nil {
		index = dispatch.NewMemoryIndex()
	}
	f := &fixture{
		index:   index,
		text:    newContainer(world),
		pdf:     &leaf{name: "pdf"},
		archive: &leaf{name: "archive", err: errors.New("archive: can't read entry")},
		dir:     t.TempDir(),
	}
	d, err := dispatch.New(dispatch.Config{
		Stages: dispatch.Stages{route.Text: f.text, route.Document: f.pdf, route.Archive: f.archive},
		Index:  index, Dir: f.dir, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.d = d
	return f
}

// input writes content to a file named name in a directory of its own, returning its path.
func input(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// step is one event, as the tests compare them: the part's name, its depth, and its outcome.
type step struct {
	name    string
	depth   int
	outcome dispatch.Outcome
}

func (s step) String() string { return fmt.Sprintf("%s@%d:%s", s.name, s.depth, s.outcome) }

// dispatchAll dispatches path, calling each, when not nil, in the loop's body, and returns the
// events; an error fails the test.
func dispatchAll(t *testing.T, d *dispatch.Dispatcher, path string, each func(*dispatch.Event)) []dispatch.Event {
	t.Helper()
	var events []dispatch.Event
	for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: path}) {
		if err != nil {
			t.Fatal(err)
		}
		if each != nil {
			each(&ev)
		}
		events = append(events, ev)
	}
	return events
}

// steps returns events' steps.
func steps(events []dispatch.Event) []step {
	out := make([]step, 0, len(events))
	for _, ev := range events {
		out = append(out, step{ev.Part.Name, ev.Part.Depth, ev.Outcome})
	}
	return out
}

// wantSteps fails the test unless events' steps are want.
func wantSteps(t *testing.T, events []dispatch.Event, want []step) {
	t.Helper()
	if got := steps(events); !slices.Equal(got, want) {
		t.Fatalf("events:\n got %v\nwant %v", got, want)
	}
}

// byName returns the first event of the part named name.
func byName(t *testing.T, events []dispatch.Event, name string) dispatch.Event {
	t.Helper()
	for _, ev := range events {
		if ev.Part.Name == name {
			return ev
		}
	}
	t.Fatalf("no event for %s", name)
	return dispatch.Event{}
}
