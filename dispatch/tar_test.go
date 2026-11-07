package dispatch_test

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// inspectFixture returns the path of one of inspect's synthesized fixtures.
func inspectFixture(name string) string { return filepath.Join(inspectFixtures, name) }

// TestTarStage dispatches inspect's clean.tar, a directory, a text, and a picture: the directory
// is no part; each file is named by its stored name, found "tar entry <name>", written under its
// SHA-256 inside the working directory, and routed by its content; and nothing is left once the
// dispatch is done.
func TestTarStage(t *testing.T) {
	d, zs, work := archiveDispatcher(t, inspect.Options{})
	events := dispatchAll(t, d, inspectFixture("clean.tar"), func(ev *dispatch.Event) {
		if ev.Part.Depth == 0 {
			return
		}
		if filepath.Base(ev.Part.Path) != ev.Part.SHA256 || !inside(work, ev.Part.Path) {
			t.Errorf("%s at %s: want its SHA-256 inside the working directory", ev.Part.Name, ev.Part.Path)
		}
	})
	wantSteps(t, events, []step{
		{"docs/notes.txt", 1, dispatch.NoProcessor},
		{"docs/pixel.png", 1, dispatch.Prepared},
		{"clean.tar", 0, dispatch.Prepared},
	})
	if got, want := wheres(events), []string{"tar entry docs/notes.txt", "tar entry docs/pixel.png"}; !slices.Equal(got, want) {
		t.Errorf("wheres:\n got %q\nwant %q", got, want)
	}
	if ev := events[2]; ev.Part.Kind != route.Tar || ev.Stage.Name != "archive" || ev.Children != 2 || len(ev.Outputs) != 0 {
		t.Errorf("clean.tar: %s by %v, %d children, %d outputs", ev.Part.Kind, ev.Stage, ev.Children, len(ev.Outputs))
	}
	if ev := events[0]; ev.Part.Route != route.Text {
		t.Errorf("docs/notes.txt routed %s, want text", ev.Part.Route)
	}
	if n := zs.calls.Load(); n != 1 {
		t.Errorf("the archive stage prepared %d parts, want 1", n)
	}
	wantEmpty(t, work)
}

// TestGzipStage dispatches inspect's clean.tar.gz and unnamed.tgz, each a gzip stream holding
// clean.tar: the stream yields one part, its decompressed content, found "gzip content <name>"
// and routed again, a tar, whose entries are dispatched below it. clean.tar.gz's content is named
// by its header, "clean.tar"; unnamed.tgz's header names nothing, so its content is named by the
// stream's stem, "unnamed.tar".
func TestGzipStage(t *testing.T) {
	for _, c := range []struct{ fixture, content string }{
		{"clean.tar.gz", "clean.tar"},
		{"unnamed.tgz", "unnamed.tar"},
	} {
		d, zs, work := archiveDispatcher(t, inspect.Options{})
		events := dispatchAll(t, d, inspectFixture(c.fixture), nil)
		wantSteps(t, events, []step{
			{"docs/notes.txt", 2, dispatch.NoProcessor},
			{"docs/pixel.png", 2, dispatch.Prepared},
			{c.content, 1, dispatch.Prepared},
			{c.fixture, 0, dispatch.Prepared},
		})
		want := []string{"tar entry docs/notes.txt", "tar entry docs/pixel.png", "gzip content " + c.content}
		if got := wheres(events); !slices.Equal(got, want) {
			t.Errorf("%s: wheres:\n got %q\nwant %q", c.fixture, got, want)
		}
		if tar, gz := events[2], events[3]; tar.Part.Kind != route.Tar || gz.Part.Kind != route.Gzip || gz.Children != 1 || len(gz.Outputs) != 0 {
			t.Errorf("%s: %s holding %s, %d children, %d outputs", c.fixture, gz.Part.Kind, tar.Part.Kind, gz.Children, len(gz.Outputs))
		}
		if n := zs.calls.Load(); n != 2 {
			t.Errorf("%s: the archive stage prepared %d parts, want 2", c.fixture, n)
		}
		wantEmpty(t, work)
	}
}

// TestGzipStem: a gzip stream whose header names nothing names its content by its own name
// without .gz, with a .tgz's .tar, or, with neither, as itself.
func TestGzipStem(t *testing.T) {
	gz := func(body string) string {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	d, _, work := archiveDispatcher(t, inspect.Options{})
	for name, want := range map[string]string{
		"notes.txt.gz": "notes.txt", "NOTES.TXT.GZ": "NOTES.TXT", "bundle.TGZ": "bundle.tar", "notes": "notes",
	} {
		events := dispatchAll(t, d, input(t, name, gz("hello "+name+"\n")), nil)
		if len(events) != 2 || events[0].Part.Name != want || events[0].Part.Where != "gzip content "+want {
			t.Errorf("%s: %v, want its content named %q", name, steps(events), want)
		}
	}
	wantEmpty(t, work)
}

// TestTarUnsafe: inspect quarantines a tar with a traversing, an absolute, or a drive letter name,
// a symbolic or hard link, or a device or a FIFO, and the archive stage never sees it. Under a
// policy that lets them proceed, each regular entry is written by its SHA-256 inside the working
// directory, its stored name only its name, no file lands outside, and each link, device, and FIFO
// is a record naming its check, no link or node made.
func TestTarUnsafe(t *testing.T) {
	permissive := inspect.Options{Policy: map[inspect.Check]inspect.Verdict{
		inspect.TarTraversal: inspect.Proceed, inspect.TarAbsolutePath: inspect.Proceed, inspect.TarSymlink: inspect.Proceed,
		inspect.TarHardlink: inspect.Proceed, inspect.TarDevice: inspect.Proceed,
	}}
	for _, c := range []struct {
		fixture string
		parts   []string
		records map[string]inspect.Check
	}{
		{"traversal.tar", []string{"notes.txt", "docs/../../evil.txt"}, nil},
		{"absolute.tar", []string{"/tmp/evil.txt", `C:\evil.txt`}, nil},
		{"symlink.tar", []string{"notes.txt"}, map[string]inspect.Check{"passwd": inspect.TarSymlink}},
		{"hardlink.tar", []string{"notes.txt"}, map[string]inspect.Check{"shadow": inspect.TarHardlink}},
		{"device.tar", []string{"notes.txt"}, map[string]inspect.Check{
			"null": inspect.TarDevice, "sda": inspect.TarDevice, "pipe": inspect.TarDevice,
		}},
	} {
		d, zs, _ := archiveDispatcher(t, inspect.Options{})
		if ev := dispatchAll(t, d, inspectFixture(c.fixture), nil)[0]; ev.Outcome != dispatch.Quarantined || zs.calls.Load() != 0 {
			t.Errorf("%s: %s, the stage called %d times; want quarantined before the stage", c.fixture, ev.Outcome, zs.calls.Load())
		}

		root := t.TempDir()
		work := filepath.Join(root, "a", "b", "work")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatal(err)
		}
		s, err := dispatch.NewArchiveStage(dispatch.ArchiveConfig{Inspect: permissive})
		if err != nil {
			t.Fatal(err)
		}
		d, err = dispatch.New(dispatch.Config{
			Stages: dispatch.Stages{route.Archive: s}, Index: dispatch.NewMemoryIndex(), Dir: work, Inspect: permissive,
		})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		events := dispatchAll(t, d, inspectFixture(c.fixture), func(ev *dispatch.Event) {
			if ev.Part.Depth == 0 {
				return
			}
			names = append(names, ev.Part.Name)
			if filepath.Base(ev.Part.Path) != ev.Part.SHA256 || !inside(work, ev.Part.Path) {
				t.Errorf("%s: %q at %s, want its SHA-256 inside the working directory", c.fixture, ev.Part.Name, ev.Part.Path)
			}
			if info, err := os.Lstat(ev.Part.Path); err != nil || !info.Mode().IsRegular() {
				t.Errorf("%s: %q isn't a regular file: %v", c.fixture, ev.Part.Name, err)
			}
		})
		last := events[len(events)-1]
		if last.Outcome != dispatch.Prepared || !slices.Equal(names, c.parts) {
			t.Errorf("%s let proceed: %s, parts %q, want %q", c.fixture, last.Outcome, names, c.parts)
		}
		if len(last.Outputs) != len(c.records) {
			t.Errorf("%s: %d records, want %d", c.fixture, len(last.Outputs), len(c.records))
		}
		for name, check := range c.records {
			m := outputNamed(t, last, dispatch.RecordOutput, "tar entry "+name).Meta
			if m["check"] != string(check) || m["class"] != string(inspect.Unsafe) || m["name"] != name ||
				!strings.HasSuffix(m["reason"], ": no part") || m["outcome"] != "" {
				t.Errorf("%s: %s's record meta %v", c.fixture, name, m)
			}
		}
		if left := files(t, root); len(left) > 0 {
			t.Errorf("%s: files left: %q", c.fixture, left)
		}
	}
}

// TestTarGzipUnsafe: a gzip stream holding an unsafe tar is safe itself, so it is prepared, and
// the tar it holds is quarantined once inspected, its entries never written, its content kept.
func TestTarGzipUnsafe(t *testing.T) {
	d, zs, work := archiveDispatcher(t, inspect.Options{})
	events := dispatchAll(t, d, inspectFixture("traversal.tar.gz"), nil)
	wantSteps(t, events, []step{{"traversal.tar", 1, dispatch.Quarantined}, {"traversal.tar.gz", 0, dispatch.Prepared}})
	if ev := events[0]; !slices.ContainsFunc(ev.Part.Flags, func(f inspect.Flag) bool { return f.Check == inspect.TarTraversal }) {
		t.Errorf("traversal.tar's flags %v", ev.Part.Flags)
	}
	if n := zs.calls.Load(); n != 1 {
		t.Errorf("the archive stage prepared %d parts, want the gzip alone", n)
	}
	// A quarantined part's content is kept, and nothing else.
	if left := files(t, work); len(left) != 1 || filepath.Base(left[0]) != events[0].Part.SHA256 {
		t.Errorf("files left: %q, want the quarantined tar's alone", left)
	}
}

// TestTarBombAsWritten: sparse-bomb.tar's entry declares 4 GiB, all a hole but its last 512
// bytes, in a 2 KiB tar, and inspect quarantines it by that header. Under a policy that lets
// tar-bomb proceed, at lowered thresholds, the stage stops writing the hole's zeros once they
// pass both, removes the partial file, and records it quarantined with its evidence.
func TestTarBombAsWritten(t *testing.T) {
	lowered := inspect.Options{BombSize: 1 << 20, BombRatio: 100}
	d, zs, _ := archiveDispatcher(t, lowered)
	if ev := dispatchAll(t, d, inspectFixture("sparse-bomb.tar"), nil)[0]; ev.Outcome != dispatch.Quarantined || zs.calls.Load() != 0 {
		t.Errorf("sparse-bomb.tar: %s, the stage called %d times", ev.Outcome, zs.calls.Load())
	}

	lowered.Policy = map[inspect.Check]inspect.Verdict{inspect.TarBomb: inspect.Proceed}
	d, _, work := archiveDispatcher(t, lowered)
	events := dispatchAll(t, d, inspectFixture("sparse-bomb.tar"), func(ev *dispatch.Event) {
		if left := files(t, work); len(left) > 0 {
			t.Errorf("files at the tar's event: %q, want the partial file removed", left)
		}
	})
	wantSteps(t, events, []step{{"sparse-bomb.tar", 0, dispatch.Prepared}})
	rec := outputNamed(t, events[0], dispatch.RecordOutput, "tar entry zeros.bin")
	if rec.Meta["check"] != string(inspect.TarBomb) || rec.Meta["class"] != string(inspect.Unsafe) ||
		rec.Meta["outcome"] != string(dispatch.Quarantined) || !strings.Contains(rec.Meta["evidence"], "from a 2048-byte tar") ||
		!strings.Contains(rec.Meta["evidence"], "declared 4294967296") {
		t.Errorf("zeros.bin's record meta %v", rec.Meta)
	}
	wantEmpty(t, work)
}

// TestGzipBombAsWritten: bomb.tar.gz expands about 1,000:1 to a tar of 8 MiB of zeros. At
// lowered thresholds the stage stops decompressing it once its bytes pass both, removes the
// partial file, and records it quarantined, its check gzip-bomb; at the defaults, under the
// size, its tar is written in full and dispatched, its entry no bomb, since a tar stores its
// bytes as they are.
func TestGzipBombAsWritten(t *testing.T) {
	d, _, work := archiveDispatcher(t, inspect.Options{BombSize: 1 << 20, BombRatio: 100})
	events := dispatchAll(t, d, inspectFixture("bomb.tar.gz"), func(ev *dispatch.Event) {
		if left := files(t, work); len(left) > 0 {
			t.Errorf("files at the stream's event: %q, want the partial file removed", left)
		}
	})
	wantSteps(t, events, []step{{"bomb.tar.gz", 0, dispatch.Prepared}})
	rec := outputNamed(t, events[0], dispatch.RecordOutput, "gzip content bomb.tar")
	if rec.Meta["check"] != string(inspect.GzipBomb) || rec.Meta["class"] != string(inspect.Unsafe) ||
		rec.Meta["outcome"] != string(dispatch.Quarantined) || rec.Meta["name"] != "bomb.tar" ||
		!strings.Contains(rec.Meta["evidence"], `content "bomb.tar"`) {
		t.Errorf("bomb.tar's record meta %v", rec.Meta)
	}
	wantEmpty(t, work)

	d, _, work = archiveDispatcher(t, inspect.Options{})
	events = dispatchAll(t, d, inspectFixture("bomb.tar.gz"), nil)
	wantSteps(t, events, []step{
		{"zeros.bin", 2, dispatch.NoProcessor},
		{"bomb.tar", 1, dispatch.Prepared},
		{"bomb.tar.gz", 0, dispatch.Prepared},
	})
	if events[0].Part.Size != 8<<20 {
		t.Errorf("zeros.bin: %d bytes, want %d", events[0].Part.Size, 8<<20)
	}
	wantEmpty(t, work)
}

// TestArchiveDamaged: a gzip stream cut short is a record, its content's partial file removed;
// a gzip whose header doesn't read fails; and a tar cut short inside its text's content, let
// past inspect's tar-unreadable, has the text recorded, its content not read whole, then fails
// at the header after it.
func TestArchiveDamaged(t *testing.T) {
	gz, err := os.ReadFile(inspectFixture("clean.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	d, _, work := archiveDispatcher(t, inspect.Options{})
	events := dispatchAll(t, d, input(t, "cut.tar.gz", string(gz[:len(gz)-20])), nil)
	wantSteps(t, events, []step{{"cut.tar.gz", 0, dispatch.Prepared}})
	if m := outputNamed(t, events[0], dispatch.RecordOutput, "gzip content clean.tar").Meta; !strings.HasPrefix(m["reason"], "doesn't decompress") {
		t.Errorf("cut.tar.gz's record meta %v", m)
	}
	events = dispatchAll(t, d, input(t, "bad.gz", "\x1f\x8b\x09 not deflate"), nil)
	if ev := events[0]; ev.Outcome != dispatch.Failed || !strings.Contains(ev.Reason, "gzip") {
		t.Errorf("bad.gz: %s, %q; want failed", ev.Outcome, ev.Reason)
	}
	wantEmpty(t, work)

	whole, err := os.ReadFile(inspectFixture("clean.tar"))
	if err != nil {
		t.Fatal(err)
	}
	opts := inspect.Options{Policy: map[inspect.Check]inspect.Verdict{inspect.TarUnreadable: inspect.Proceed}}
	d, _, work = archiveDispatcher(t, opts)
	// clean.tar's docs/notes.txt has its header at 512 and its 18 bytes at 1024.
	events = dispatchAll(t, d, input(t, "cut.tar", string(whole[:1030])), nil)
	wantSteps(t, events, []step{{"cut.tar", 0, dispatch.Failed}})
	if m := outputNamed(t, events[0], dispatch.RecordOutput, "tar entry docs/notes.txt").Meta; !strings.HasPrefix(m["reason"], "doesn't read") {
		t.Errorf("docs/notes.txt's record meta %v", m)
	}
	wantEmpty(t, work)
}
