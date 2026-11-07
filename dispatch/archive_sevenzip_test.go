//go:build linux

package dispatch_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/dispatch"
	"github.com/JaimeStill/spike-ai-pipeline-processing/inspect"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
	"github.com/JaimeStill/spike-ai-pipeline-processing/sevenzip"
)

// sevenZipFixtures are the sevenzip package's fixtures.
var sevenZipFixtures = filepath.Join("..", "sevenzip", "testdata")

// sevenZipFixture returns the path of one of the sevenzip package's fixtures.
func sevenZipFixture(name string) string { return filepath.Join(sevenZipFixtures, name) }

// sevenZipConfig returns an archive stage's configuration under opts over the 7zz mise pins.
func sevenZipConfig(t *testing.T, opts inspect.Options) dispatch.ArchiveConfig {
	t.Helper()
	tl := pinned(t, sevenzip.SevenZipSpec())
	r, err := sevenzip.NewReader(sevenzip.Config{SevenZip: tl})
	if err != nil {
		t.Fatal(err)
	}
	return dispatch.ArchiveConfig{Inspect: opts, SevenZip: r, Version: dispatch.Versions(tl)}
}

// sevenZipDispatcher returns a dispatcher as archiveDispatcher does, its archive stage reading
// through the 7zz mise pins.
func sevenZipDispatcher(t *testing.T, opts inspect.Options) (*dispatch.Dispatcher, *counting, string) {
	t.Helper()
	return archiveDispatcherWith(t, sevenZipConfig(t, opts))
}

// TestSevenZipStage dispatches clean.7z, a directory, a text, and a picture: the directory is no
// part; each file is extracted through 7zz, named by its stored name, found "7z entry <name>",
// written under its SHA-256 inside the working directory, and routed by its content; and nothing
// is left once the dispatch is done.
func TestSevenZipStage(t *testing.T) {
	d, zs, work := sevenZipDispatcher(t, inspect.Options{})
	events := dispatchAll(t, d, sevenZipFixture("generated/clean.7z"), func(ev *dispatch.Event) {
		if ev.Part.Depth > 0 && (filepath.Base(ev.Part.Path) != ev.Part.SHA256 || !inside(work, ev.Part.Path)) {
			t.Errorf("%s at %s: want its SHA-256 inside the working directory", ev.Part.Name, ev.Part.Path)
		}
	})
	wantSteps(t, events, []step{
		{"docs/notes.txt", 1, dispatch.NoProcessor},
		{"docs/pixel.png", 1, dispatch.Prepared},
		{"clean.7z", 0, dispatch.Prepared},
	})
	if got, want := wheres(events), []string{"7z entry docs/notes.txt", "7z entry docs/pixel.png"}; !slices.Equal(got, want) {
		t.Errorf("wheres:\n got %q\nwant %q", got, want)
	}
	if ev := events[2]; ev.Part.Kind != route.SevenZip || ev.Stage.Name != "archive" || ev.Children != 2 || len(ev.Outputs) != 0 {
		t.Errorf("clean.7z: %s by %v, %d children, %d outputs", ev.Part.Kind, ev.Stage, ev.Children, len(ev.Outputs))
	}
	if events[0].Part.Route != route.Text || events[0].Part.Size != 22 {
		t.Errorf("docs/notes.txt: %s, %d bytes", events[0].Part.Route, events[0].Part.Size)
	}
	if n := zs.calls.Load(); n != 1 {
		t.Errorf("the archive stage prepared %d parts, want 1", n)
	}
	wantEmpty(t, work)
}

// TestSevenZipStreams: clean.tar.zst's content, named by the stream's stem, "clean.tar", is found
// "zstd content clean.tar" and dispatched as a tar below it; notebook.cab's files are found
// "cabinet entry <name>", each a OneNote file by its signature; and commons-compress's
// bla.deflate64.7z, Deflate64 in a 7z, gives its two XML files.
func TestSevenZipStreams(t *testing.T) {
	d, _, work := sevenZipDispatcher(t, inspect.Options{})
	events := dispatchAll(t, d, sevenZipFixture("generated/clean.tar.zst"), nil)
	wantSteps(t, events, []step{
		{"docs/notes.txt", 2, dispatch.NoProcessor},
		{"docs/pixel.png", 2, dispatch.Prepared},
		{"clean.tar", 1, dispatch.Prepared},
		{"clean.tar.zst", 0, dispatch.Prepared},
	})
	if got, want := wheres(events), []string{"tar entry docs/notes.txt", "tar entry docs/pixel.png", "zstd content clean.tar"}; !slices.Equal(got, want) {
		t.Errorf("clean.tar.zst: wheres:\n got %q\nwant %q", got, want)
	}

	events = dispatchAll(t, d, sevenZipFixture("generated/notebook.cab"), nil)
	wantSteps(t, events, []step{
		{"Open Notebook.onetoc2", 1, dispatch.NoProcessor},
		{"New Section 1.one", 1, dispatch.NoProcessor},
		{"notebook.cab", 0, dispatch.Prepared},
	})
	for _, ev := range events[:2] {
		if ev.Part.Route != route.OneNote || ev.Part.Where != "cabinet entry "+ev.Part.Name {
			t.Errorf("%s: %s at %q", ev.Part.Name, ev.Part.Route, ev.Part.Where)
		}
	}
	section, err := os.ReadFile(filepath.Join("..", "onenote", "testdata", "onenote", "fixtures", "onenote-rs", "New Section Group", "New Section 1.one"))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(section); events[1].Part.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("New Section 1.one: SHA-256 %s, want the section's", events[1].Part.SHA256)
	}

	events = dispatchAll(t, d, sevenZipFixture("commons-compress/bla.deflate64.7z"), nil)
	wantSteps(t, events, []step{
		{"test1.xml", 1, dispatch.NoProcessor},
		{"test2.xml", 1, dispatch.NoProcessor},
		{"bla.deflate64.7z", 0, dispatch.Prepared},
	})
	if events[0].Part.Size != 610 || events[1].Part.Size != 82 {
		t.Errorf("bla.deflate64.7z: %d and %d bytes, want 610 and 82", events[0].Part.Size, events[1].Part.Size)
	}
	wantEmpty(t, work)
}

// zipMethods are the ZIP fixtures whose one entry archive/zip doesn't read, but bzip2-zip.zip's,
// which it reads through compress/bzip2: each fixture, its entry, the entry's size, and its
// method.
var zipMethods = []struct {
	fixture, entry string
	size           int64
	method         uint16
}{
	{"commons-compress/lorem-ipsum-deflate64.zip", "lorem-ipsum.txt", 144060, 9},
	{"commons-compress/lorem-ipsum-implode.zip", "LOREM.TXT", 144060, 6},
	{"commons-compress/test-method-xz.zip", "LICENSE.txt", 11357, 95},
	{"commons-compress/bzip2-zip.zip", "lots-of-as", 42, 12},
	{"libarchive/test_read_format_zip_lzma.zipx", "vimrc", 912, 14},
	{"libarchive/test_read_format_zip_ppmd8.zipx", "vimrc", 912, 98},
	{"libarchive/test_read_format_zip_zstd.zipx", "vimrc", 912, 93},
}

// TestZipMethods: each ZIP entry of a method archive/zip doesn't read is extracted through 7zz,
// held to its central directory's size and CRC-32, and found "zip entry <name>"; the two
// lorem-ipsum ZIPs, Deflate64 and implode, give the same bytes. With no 7-Zip, each such entry is
// a record, its reason naming its method, and the ZIP is prepared, but bzip2's entry, which
// compress/bzip2 reads, is found as before.
func TestZipMethods(t *testing.T) {
	d, _, work := sevenZipDispatcher(t, inspect.Options{})
	sums := map[string]string{}
	for _, c := range zipMethods {
		events := dispatchAll(t, d, sevenZipFixture(c.fixture), nil)
		if len(events) != 2 || events[0].Part.Name != c.entry || events[0].Part.Size != c.size ||
			events[0].Part.Where != "zip entry "+c.entry || events[1].Outcome != dispatch.Prepared || len(events[1].Outputs) != 0 {
			t.Errorf("%s: %v, %d bytes, %d records", c.fixture, steps(events), events[0].Part.Size, len(events[len(events)-1].Outputs))
			continue
		}
		sums[c.fixture] = events[0].Part.SHA256
	}
	if a, b := sums["commons-compress/lorem-ipsum-deflate64.zip"], sums["commons-compress/lorem-ipsum-implode.zip"]; a == "" || a != b {
		t.Errorf("lorem-ipsum: Deflate64 %s, implode %s; want the same bytes", a, b)
	}
	wantEmpty(t, work)

	d, _, work = archiveDispatcher(t, inspect.Options{})
	for _, c := range zipMethods {
		events := dispatchAll(t, d, sevenZipFixture(c.fixture), nil)
		last := events[len(events)-1]
		if c.method == 12 {
			if len(events) != 2 || events[0].Part.Size != c.size {
				t.Errorf("%s without 7-Zip: %v", c.fixture, steps(events))
			}
			continue
		}
		if len(events) != 1 || last.Outcome != dispatch.Prepared {
			t.Errorf("%s without 7-Zip: %v", c.fixture, steps(events))
			continue
		}
		m := outputNamed(t, last, dispatch.RecordOutput, "zip entry "+c.entry).Meta
		if want := "compression method " + strconv.Itoa(int(c.method)) + " has no processor yet without 7-Zip"; m["reason"] != want {
			t.Errorf("%s without 7-Zip: reason %q, want %q", c.fixture, m["reason"], want)
		}
	}
	wantEmpty(t, work)
}

// TestSevenZipUnsafe: the stage quarantines a 7z or cabinet whose 7-Zip listing names a
// traversing, absolute, or drive letter entry, a symbolic link, or a FIFO, before anything is
// extracted, the listing's flags its evidence, and the archive's file kept. Under a policy that
// lets them proceed, each file is written by its SHA-256 inside the working directory, its
// stored name only its name, no file lands outside, and each link or FIFO is a record naming its
// check, no link or node made.
func TestSevenZipUnsafe(t *testing.T) {
	permissive := inspect.Options{Policy: map[inspect.Check]inspect.Verdict{
		inspect.SevenZipTraversal: inspect.Proceed, inspect.SevenZipAbsolutePath: inspect.Proceed,
		inspect.SevenZipSymlink: inspect.Proceed, inspect.SevenZipDevice: inspect.Proceed,
		inspect.CabTraversal: inspect.Proceed, inspect.CabAbsolutePath: inspect.Proceed,
	}}
	for _, c := range []struct {
		fixture string
		check   inspect.Check
		parts   []string
		records map[string]inspect.Check
	}{
		{"traversal.7z", inspect.SevenZipTraversal, []string{"docs/../../evil.txt", "notes.txt"}, nil},
		{"absolute.7z", inspect.SevenZipAbsolutePath, []string{"/tmp/evil.txt", `C:\evil.txt`}, nil},
		{"symlink.7z", inspect.SevenZipSymlink, []string{"notes.txt"}, map[string]inspect.Check{"passwd": inspect.SevenZipSymlink}},
		{"device.7z", inspect.SevenZipDevice, []string{"notes.txt"}, map[string]inspect.Check{"pipe": inspect.SevenZipDevice}},
		{"traversal.cab", inspect.CabTraversal, []string{"notes.txt", "docs/../../evil.txt"}, nil},
		{"absolute.cab", inspect.CabAbsolutePath, []string{"/tmp/evil.txt", "C:/evil.txt"}, nil},
	} {
		d, zs, work := sevenZipDispatcher(t, inspect.Options{})
		events := dispatchAll(t, d, sevenZipFixture("generated/"+c.fixture), nil)
		ev := events[0]
		if len(events) != 1 || ev.Outcome != dispatch.Quarantined || zs.calls.Load() != 1 ||
			!slices.ContainsFunc(ev.Part.Flags, func(f inspect.Flag) bool { return f.Check == c.check }) ||
			!strings.Contains(ev.Reason, string(c.check)+": entry ") {
			t.Errorf("%s: %v, flags %v, reason %q; want quarantined by the stage for %s", c.fixture, steps(events), ev.Part.Flags, ev.Reason, c.check)
		}
		wantEmpty(t, work)

		root := t.TempDir()
		work = filepath.Join(root, "a", "b", "work")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := sevenZipConfig(t, permissive)
		s, err := dispatch.NewArchiveStage(cfg)
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
		events = dispatchAll(t, d, sevenZipFixture("generated/"+c.fixture), func(ev *dispatch.Event) {
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
		if last.Outcome != dispatch.Prepared || !slices.Equal(names, c.parts) || len(last.Outputs) != len(c.records) {
			t.Errorf("%s let proceed: %s, parts %q, %d records; want %q, %d", c.fixture, last.Outcome, names, len(last.Outputs), c.parts, len(c.records))
		}
		where := "7z entry "
		if strings.HasSuffix(c.fixture, ".cab") {
			where = "cabinet entry "
		}
		for name, check := range c.records {
			m := outputNamed(t, last, dispatch.RecordOutput, where+name).Meta
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

// TestSevenZipQuarantineKept: a quarantined 7z found inside a ZIP keeps its file, as a part
// inspect quarantines does, and its flags, the listing's among them, are the index's.
func TestSevenZipQuarantineKept(t *testing.T) {
	traversal, err := os.ReadFile(sevenZipFixture("generated/traversal.7z"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := sevenZipConfig(t, inspect.Options{})
	s, err := dispatch.NewArchiveStage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	index := dispatch.NewMemoryIndex()
	work := t.TempDir()
	d, err := dispatch.New(dispatch.Config{Stages: dispatch.Stages{route.Archive: s}, Index: index, Dir: work})
	if err != nil {
		t.Fatal(err)
	}
	events := dispatchAll(t, d, input(t, "outer.zip", zipBytes(t, zent{name: "traversal.7z", body: string(traversal)})), nil)
	wantSteps(t, events, []step{{"traversal.7z", 1, dispatch.Quarantined}, {"outer.zip", 0, dispatch.Prepared}})
	flags, ok, err := index.Flags(t.Context(), events[0].Part.SHA256)
	if err != nil || !ok || !slices.ContainsFunc(flags, func(f inspect.Flag) bool { return f.Check == inspect.SevenZipTraversal }) {
		t.Errorf("the index's flags for traversal.7z: %v, %t, %v", flags, ok, err)
	}
	if left := files(t, work); len(left) != 1 || filepath.Base(left[0]) != events[0].Part.SHA256 {
		t.Errorf("files left: %q, want the quarantined 7z's alone", left)
	}
}

// TestSevenZipBombs: bomb.7z's and bomb.cab's listings declare 8 MiB and declared-bomb.zst's
// frame header 4 GiB; past the thresholds, lowered for the 8 MiB, each is quarantined by its
// listing, nothing extracted. Under a policy that lets the listing's bomb proceed, the stage
// stops writing the zeros 7zz gives once they pass both, removes the partial file, and records
// the entry quarantined, its check the kind's bomb; bomb.zst, whose header declares no size,
// passes its listing and is stopped as written.
func TestSevenZipBombs(t *testing.T) {
	lowered := inspect.Options{BombSize: 1 << 20, BombRatio: 100}
	for _, c := range []struct {
		fixture string
		opts    inspect.Options
		check   inspect.Check
	}{
		{"bomb.7z", lowered, inspect.SevenZipBomb},
		{"bomb.cab", lowered, inspect.CabBomb},
		{"declared-bomb.zst", inspect.Options{}, inspect.ZstdBomb},
	} {
		d, _, work := sevenZipDispatcher(t, c.opts)
		events := dispatchAll(t, d, sevenZipFixture("generated/"+c.fixture), nil)
		if len(events) != 1 || events[0].Outcome != dispatch.Quarantined || !strings.Contains(events[0].Reason, string(c.check)+": entry ") {
			t.Errorf("%s: %v, %q; want quarantined by its listing's %s", c.fixture, steps(events), events[0].Reason, c.check)
		}
		wantEmpty(t, work)
	}

	for _, c := range []struct {
		fixture, record string
		check           inspect.Check
	}{
		{"bomb.7z", "7z entry zeros.bin", inspect.SevenZipBomb},
		{"bomb.cab", "cabinet entry zeros.bin", inspect.CabBomb},
		{"bomb.zst", "zstd content bomb", inspect.ZstdBomb},
	} {
		opts := lowered
		opts.Policy = map[inspect.Check]inspect.Verdict{c.check: inspect.Proceed}
		d, _, work := sevenZipDispatcher(t, opts)
		events := dispatchAll(t, d, sevenZipFixture("generated/"+c.fixture), func(ev *dispatch.Event) {
			if left := files(t, work); len(left) > 0 {
				t.Errorf("%s: files at the archive's event: %q, want the partial file removed", c.fixture, left)
			}
		})
		wantSteps(t, events, []step{{c.fixture, 0, dispatch.Prepared}})
		m := outputNamed(t, events[0], dispatch.RecordOutput, c.record).Meta
		if m["check"] != string(c.check) || m["class"] != string(inspect.Unsafe) || m["outcome"] != string(dispatch.Quarantined) ||
			!strings.Contains(m["evidence"], "bytes written from") || m["reason"] != "past the bomb thresholds as written" {
			t.Errorf("%s: record meta %v", c.fixture, m)
		}
		wantEmpty(t, work)
	}
}

// TestSevenZipEncrypted: encrypted.7z's clear entry is found and its encrypted one recorded, its
// check 7z-encrypted and its outcome encrypted, and the archive is encrypted once its readable
// entry is dispatched; encrypted-headers.7z, which lists nothing without its password, is
// encrypted at once.
func TestSevenZipEncrypted(t *testing.T) {
	d, _, work := sevenZipDispatcher(t, inspect.Options{})
	events := dispatchAll(t, d, sevenZipFixture("generated/encrypted.7z"), nil)
	wantSteps(t, events, []step{{"notes.txt", 1, dispatch.NoProcessor}, {"encrypted.7z", 0, dispatch.Encrypted}})
	last := events[1]
	m := outputNamed(t, last, dispatch.RecordOutput, "7z entry secret.txt").Meta
	if m["check"] != string(inspect.SevenZipEncrypted) || m["class"] != string(inspect.Active) || m["outcome"] != string(dispatch.Encrypted) {
		t.Errorf("secret.txt's record meta %v", m)
	}
	if !strings.Contains(last.Reason, "1 of 2 entries") {
		t.Errorf("encrypted.7z: reason %q", last.Reason)
	}
	events = dispatchAll(t, d, sevenZipFixture("generated/encrypted-headers.7z"), nil)
	wantSteps(t, events, []step{{"encrypted-headers.7z", 0, dispatch.Encrypted}})
	if !strings.Contains(events[0].Reason, "headers are encrypted") || len(events[0].Outputs) != 0 {
		t.Errorf("encrypted-headers.7z: %q, %d outputs", events[0].Reason, len(events[0].Outputs))
	}
	wantEmpty(t, work)
}

// TestSevenZipDamaged: a 7z cut short fails, 7-Zip unable to open it, and a zstd stream cut short
// is a record, its content's partial file removed.
func TestSevenZipDamaged(t *testing.T) {
	whole, err := os.ReadFile(sevenZipFixture("generated/clean.7z"))
	if err != nil {
		t.Fatal(err)
	}
	d, _, work := sevenZipDispatcher(t, inspect.Options{})
	events := dispatchAll(t, d, input(t, "cut.7z", string(whole[:100])), nil)
	if ev := events[0]; len(events) != 1 || ev.Outcome != dispatch.Failed || !strings.Contains(ev.Reason, "can't be opened") {
		t.Errorf("cut.7z: %v, %q; want failed", steps(events), ev.Reason)
	}
	zst, err := os.ReadFile(sevenZipFixture("generated/clean.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	events = dispatchAll(t, d, input(t, "cut.tar.zst", string(zst[:1000])), nil)
	wantSteps(t, events, []step{{"cut.tar.zst", 0, dispatch.Prepared}})
	if m := outputNamed(t, events[0], dispatch.RecordOutput, "zstd content cut.tar").Meta; !strings.HasPrefix(m["reason"], "doesn't decompress") {
		t.Errorf("cut.tar.zst's record meta %v", m)
	}
	wantEmpty(t, work)
}
