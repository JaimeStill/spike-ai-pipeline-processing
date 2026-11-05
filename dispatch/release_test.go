package dispatch_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// store is a stage shaped like mail reading a store, for route.Text: for each of its records, in
// order, it writes the record's content into the part's attachments directory once, named by its
// SHA-256, just before the first record naming it, as mail does, then finds it, as the email
// stage's stash does: a hard link in a directory of its own, carrying the content's SHA-256 and a
// release that removes the original; or, once released, a part with no file. Then it yields the
// record's output. A record whose content is neither there nor released fails the stage: it is
// lost.
type store struct {
	// refs names each record's content, by its key in contents.
	refs     []string
	contents map[string][]byte
	// lie, when set, gives each found part a SHA-256 that isn't its content's.
	lie bool

	mu       sync.Mutex
	releases int
}

func (s *store) Identity() dispatch.Identity {
	return dispatch.Identity{Name: "store", Version: "1", Config: fmt.Sprintf("%d/%v", len(s.refs), s.lie)}
}

func (s *store) released() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releases
}

func (s *store) Prepare(_ context.Context, _ dispatch.Part, dir string) iter.Seq2[dispatch.Item, error] {
	return func(yield func(dispatch.Item, error) bool) {
		att := filepath.Join(dir, "attachments")
		if err := os.Mkdir(att, 0o700); err != nil {
			yield(dispatch.Item{}, err)
			return
		}
		written := map[string]bool{}
		released := map[string]bool{}
		for i, ref := range s.refs {
			content := s.contents[ref]
			sum := sha(content)
			path := filepath.Join(att, sum+".bin")
			if !written[sum] {
				if err := os.WriteFile(path, content, 0o600); err != nil {
					yield(dispatch.Item{}, err)
					return
				}
				written[sum] = true
			}
			where := fmt.Sprintf("record %d", i)
			f := dispatch.Found{Name: ref + ".bin", Where: where, SHA256: sum}
			link := ""
			switch _, err := os.Stat(path); {
			case err == nil:
				link = filepath.Join(dir, fmt.Sprintf("found-%d", i), sum+".bin")
				if err := os.Mkdir(filepath.Dir(link), 0o700); err != nil {
					yield(dispatch.Item{}, err)
					return
				}
				if err := os.Link(path, link); err != nil {
					yield(dispatch.Item{}, err)
					return
				}
				f.Path = link
				if s.lie {
					f.SHA256 = sha([]byte("not " + ref))
				}
				f.Release = func() {
					s.mu.Lock()
					s.releases++
					s.mu.Unlock()
					if err := os.Remove(path); err == nil {
						released[sum] = true
					}
				}
			case released[sum]:
			default:
				yield(dispatch.Item{}, fmt.Errorf("store: %s's content is lost", where))
				return
			}
			if !yield(dispatch.PartItem(f), nil) {
				return
			}
			if link != "" {
				_ = os.Remove(filepath.Dir(link)) // empty once the dispatcher removed the link
			}
			if !yield(dispatch.OutputItem(dispatch.Output{Kind: dispatch.RecordOutput, Name: where}), nil) {
				return
			}
		}
	}
}

// sha returns b's lowercase hex SHA-256.
func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// picky is a stage that yields one output for each part and fails the parts found at one of
// fail's places, counting its calls.
type picky struct {
	fail map[string]bool

	mu    sync.Mutex
	calls int
}

func (p *picky) Identity() dispatch.Identity {
	return dispatch.Identity{Name: "picky", Version: "1", Config: "test"}
}

func (p *picky) Prepare(_ context.Context, part dispatch.Part, _ string) iter.Seq2[dispatch.Item, error] {
	return func(yield func(dispatch.Item, error) bool) {
		p.mu.Lock()
		p.calls++
		p.mu.Unlock()
		if p.fail[part.Where] {
			yield(dispatch.Item{}, errors.New("picky: told to fail"))
			return
		}
		yield(dispatch.OutputItem(dispatch.Output{Kind: dispatch.TextOutput, Name: "content", Value: []byte(part.SHA256)}), nil)
	}
}

// onDisk returns how many regular files are under dir, and their bytes summed, a hard link
// counted with each of its names.
func onDisk(t *testing.T, dir string) (int, int64) {
	t.Helper()
	n, size := 0, int64(0)
	err := filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			// A directory removed while it is walked is gone, not an error.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.Type().IsRegular() {
			info, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			n++
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n, size
}

// heavyStore returns a store of n records, every every'th naming one shared content and each
// other a content of its own, each content size bytes.
func heavyStore(n, every, size int) *store {
	s := &store{contents: map[string][]byte{}}
	pad := func(key string) []byte {
		return append([]byte(key), bytes.Repeat([]byte{0}, size-len(key))...)
	}
	s.contents["shared"] = pad("shared")
	for i := range n {
		ref := "shared"
		if i%every != 0 {
			ref = fmt.Sprintf("own-%d", i)
			s.contents[ref] = pad(ref)
		}
		s.refs = append(s.refs, ref)
	}
	return s
}

// probe is a dispatch of a store consumed with its working directory probed at every event: the
// peak number and bytes of the files in it, and each found part's outcome by its place.
type probe struct {
	peakFiles int
	peakBytes int64
	outcomes  map[string]dispatch.Outcome
	reasons   map[string]string
	last      dispatch.Event
}

// probeStore dispatches a store input through d, whose working directory is dir, as probe says;
// each, when not nil, runs in the loop's body for every event.
func probeStore(t *testing.T, d *dispatch.Dispatcher, dir string, each func(*dispatch.Event)) probe {
	t.Helper()
	pr := probe{outcomes: map[string]dispatch.Outcome{}, reasons: map[string]string{}}
	for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: input(t, "store.txt", "store")}) {
		if err != nil {
			t.Fatal(err)
		}
		n, size := onDisk(t, dir)
		pr.peakFiles, pr.peakBytes = max(pr.peakFiles, n), max(pr.peakBytes, size)
		if each != nil {
			each(&ev)
		}
		switch {
		case ev.Output != nil:
		case ev.Part.Depth == 1:
			pr.outcomes[ev.Part.Where] = ev.Outcome
			pr.reasons[ev.Part.Where] = ev.Reason
		default:
			pr.last = ev
		}
	}
	return pr
}

// storeDispatcher returns a dispatcher with s on route.Text and leaf on route.Unrouted, which the
// store's .bin content routes to, over a memory index, and its working directory.
func storeDispatcher(t *testing.T, s *store, leaf dispatch.Stage) (*dispatch.Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := dispatch.New(dispatch.Config{
		Stages: dispatch.Stages{route.Text: s, route.Unrouted: leaf}, Index: dispatch.NewMemoryIndex(), Dir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, dir
}

// TestReleaseBoundsDisk: a store of 10,000 records, a thousand of them naming the same content
// and each other a content of its own, written once each as mail writes them, holds a few files
// in the dispatch's working directory at any event, however many records it has: each content's
// original is released once the dispatcher has recorded it. Every record's part is dispatched,
// none failed: each content prepared once, at its first record, and the shared content's later
// records seen with no file, from the index.
func TestReleaseBoundsDisk(t *testing.T) {
	const n, every, size = 10_000, 10, 1024
	s := heavyStore(n, every, size)
	leaf := &picky{}
	d, dir := storeDispatcher(t, s, leaf)
	pr := probeStore(t, d, dir, nil)

	if pr.last.Outcome != dispatch.Prepared || pr.last.Children != n || pr.last.Outputs != n {
		t.Fatalf("the store: %s, %d children, %d outputs", pr.last.Outcome, pr.last.Children, pr.last.Outputs)
	}
	distinct := n - n/every + 1
	counts := map[dispatch.Outcome]int{}
	for i := range n {
		where := fmt.Sprintf("record %d", i)
		got, ok := pr.outcomes[where]
		if !ok {
			t.Fatalf("%s: no event", where)
		}
		counts[got]++
		if want := dispatch.Prepared; i%every == 0 && i > 0 {
			if got != dispatch.Seen {
				t.Fatalf("%s, the shared content again: %s, %q", where, got, pr.reasons[where])
			}
		} else if got != want {
			t.Fatalf("%s: %s, %q", where, got, pr.reasons[where])
		}
	}
	if counts[dispatch.Prepared] != distinct || counts[dispatch.Seen] != n/every-1 {
		t.Errorf("outcomes %v, want %d prepared and %d seen", counts, distinct, n/every-1)
	}
	if leaf.calls != distinct || s.released() != distinct {
		t.Errorf("%d contents prepared and %d released, want each of the %d once", leaf.calls, s.released(), distinct)
	}
	// At most a record's original, its link, and the store's input's working files: a bound of
	// its own, whatever n is; holding every original would take n-n/every+1 files.
	if pr.peakFiles > 3 || pr.peakBytes > 3*size {
		t.Errorf("the working directory held up to %d files, %d bytes", pr.peakFiles, pr.peakBytes)
	}
	t.Logf("peak %d files, %d bytes, of %d contents, %d bytes", pr.peakFiles, pr.peakBytes, distinct, distinct*size)
}

// TestReleaseKeepsUnrecorded: when the first record of the shared content fails, nothing is
// recorded of it, so its original is kept: the next record naming it is dispatched from that
// file, prepared, and released, and the later ones are seen. No record's content is lost.
func TestReleaseKeepsUnrecorded(t *testing.T) {
	const n, every, size = 1000, 10, 64
	s := heavyStore(n, every, size)
	leaf := &picky{fail: map[string]bool{"record 0": true}}
	d, dir := storeDispatcher(t, s, leaf)
	pr := probeStore(t, d, dir, nil)
	if pr.last.Outcome != dispatch.Prepared {
		t.Fatalf("the store: %s, %q", pr.last.Outcome, pr.last.Reason)
	}
	for i := 0; i < n; i += every {
		where := fmt.Sprintf("record %d", i)
		want := dispatch.Seen
		switch i {
		case 0:
			want = dispatch.Failed
		case every:
			want = dispatch.Prepared
		}
		if got := pr.outcomes[where]; got != want {
			t.Fatalf("%s: %s, %q, want %s", where, got, pr.reasons[where], want)
		}
	}
	if distinct := n - n/every + 1; s.released() != distinct {
		t.Errorf("%d released, want %d", s.released(), distinct)
	}
}

// TestReleaseRefused: no original is released when the stage's SHA-256 isn't the content's, nor
// when the caller keeps each part, so each record is dispatched from its file; and a part with
// no file whose SHA-256 the dispatch released nothing of fails.
func TestReleaseRefused(t *testing.T) {
	t.Run("a lying SHA-256", func(t *testing.T) {
		s := heavyStore(30, 10, 64)
		s.lie = true
		d, dir := storeDispatcher(t, s, &picky{})
		pr := probeStore(t, d, dir, nil)
		if s.released() != 0 || pr.last.Outcome != dispatch.Prepared || pr.outcomes["record 10"] != dispatch.Seen {
			t.Fatalf("%d released; the store %s; record 10 %s", s.released(), pr.last.Outcome, pr.outcomes["record 10"])
		}
	})
	t.Run("kept", func(t *testing.T) {
		s := heavyStore(30, 10, 64)
		d, dir := storeDispatcher(t, s, &picky{})
		pr := probeStore(t, d, dir, func(ev *dispatch.Event) {
			if ev.Output == nil && ev.Part.Depth == 1 {
				ev.Keep()
			}
		})
		if s.released() != 0 || pr.last.Outcome != dispatch.Prepared || pr.outcomes["record 20"] != dispatch.Seen {
			t.Fatalf("%d released; the store %s; record 20 %s", s.released(), pr.last.Outcome, pr.outcomes["record 20"])
		}
	})
	t.Run("no file", func(t *testing.T) {
		d, err := dispatch.New(dispatch.Config{
			Stages: dispatch.Stages{route.Text: ghost{}}, Index: dispatch.NewMemoryIndex(), Dir: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		events := dispatchAll(t, d, input(t, "a.txt", "ghost"), nil)
		wantSteps(t, events, []step{{"ghost.bin", 1, dispatch.Failed}, {"a.txt", 0, dispatch.Prepared}})
		if ev := events[0]; !strings.Contains(ev.Reason, "no file") {
			t.Errorf("ghost.bin: %q", ev.Reason)
		}
	})
}

// ghost is a stage that finds a part with no file, of content no dispatch released.
type ghost struct{}

func (ghost) Identity() dispatch.Identity {
	return dispatch.Identity{Name: "ghost", Version: "1", Config: "test"}
}

func (ghost) Prepare(context.Context, dispatch.Part, string) iter.Seq2[dispatch.Item, error] {
	return func(yield func(dispatch.Item, error) bool) {
		yield(dispatch.PartItem(dispatch.Found{Name: "ghost.bin", SHA256: sum("ghost")}), nil)
	}
}
