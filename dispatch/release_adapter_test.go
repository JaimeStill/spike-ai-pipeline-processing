//go:build linux

package dispatch_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// diskBytes returns the bytes the regular files under dir hold, a hard link's once.
func diskBytes(t *testing.T, dir string) int64 {
	t.Helper()
	seen := map[uint64]bool{}
	var size int64
	err := filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !e.Type().IsRegular() {
			return nil
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			if seen[st.Ino] {
				return nil
			}
			seen[st.Ino] = true
		}
		size += info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return size
}

// TestEmailStageReleases dispatches real stores through the email stage, a sink stage on every
// other route so that each attachment is recorded, probing the working directory's bytes at
// every event: no attachment fails or is lost, a repeated one is seen, and the peak stays under
// the attachments' sum, each original released once its content is recorded. The tika store
// gives mail no attachment content, and the plaso store, outside the repository, is dispatched
// only where it is present.
func TestEmailStageReleases(t *testing.T) {
	stores := []struct {
		path string
		// many is whether the store spreads its attachments over messages enough that its peak
		// must fall below their sum; a small store's attachments mail may write all at once,
		// ahead of the dispatch.
		many bool
	}{
		{filepath.Join(mailFixtures, "generated", "pst", "mailbox.pst"), false},
		{filepath.Join(mailFixtures, "pst", "tika", "testPST.pst"), false},
		{filepath.Join("..", ".state", "findings", "plaso", "nromanoff@stark-research-labs.com.pst"), true},
	}
	for _, st := range stores {
		t.Run(filepath.Base(st.path), func(t *testing.T) {
			if _, err := os.Stat(st.path); errors.Is(err, fs.ErrNotExist) {
				t.Skipf("%s isn't here", st.path)
			}
			stages := dispatch.Stages{route.Nested: emailStage(t)}
			for _, r := range []route.Route{
				route.Document, route.Office, route.Vision, route.VisionConverted, route.OneNote,
				route.Archive, route.Audio, route.Video, route.Text, route.Unrouted,
			} {
				stages[r] = &picky{}
			}
			dir := t.TempDir()
			d, err := dispatch.New(dispatch.Config{Stages: stages, Index: dispatch.NewMemoryIndex(), Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			var peak, sum int64
			distinct := map[string]bool{}
			found, seen := 0, 0
			var last dispatch.Event
			for ev, err := range d.Dispatch(t.Context(), dispatch.Input{Path: st.path}) {
				if err != nil {
					t.Fatal(err)
				}
				peak = max(peak, diskBytes(t, dir))
				switch {
				case ev.Output != nil:
				case ev.Part.Depth == 0:
					last = ev
				case ev.Part.Depth == 1:
					found++
					if ev.Outcome == dispatch.Failed {
						t.Errorf("%s: %s", ev.Part.Where, ev.Reason)
					}
					if ev.Outcome == dispatch.Seen {
						seen++
					}
					if !distinct[ev.Part.SHA256] {
						distinct[ev.Part.SHA256] = true
						sum += ev.Part.Size
					}
				}
			}
			if last.Outcome != dispatch.Prepared || last.Children != found {
				t.Fatalf("the store: %s, %q, %d children of %d found", last.Outcome, last.Reason, last.Children, found)
			}
			if found-len(distinct) != seen {
				t.Errorf("%d found, %d distinct, %d seen: each repeat should be seen", found, len(distinct), seen)
			}
			if st.many && peak >= sum {
				t.Errorf("the working directory peaked at %d bytes, the attachments' sum %d", peak, sum)
			}
			t.Logf("%d attachments, %d distinct, %d bytes; the working directory's peak %d bytes", found, len(distinct), sum, peak)
		})
	}
}
