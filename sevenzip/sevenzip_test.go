//go:build linux

package sevenzip_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/sevenzip"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// standinEnv marks a process as the stand-in: the test binary, re-executed through a link named
// 7zz, printing the banner of the version standinVersionEnv names instead of running the tests,
// or, for a listing, its l command, the listing standinListingEnv holds.
const (
	standinEnv        = "SEVENZIP_TEST_STANDIN"
	standinVersionEnv = "SEVENZIP_TEST_STANDIN_VERSION"
	standinListingEnv = "SEVENZIP_TEST_STANDIN_LISTING"
)

func TestMain(m *testing.M) {
	if os.Getenv(standinEnv) == "1" && filepath.Base(os.Args[0]) == "7zz" {
		if len(os.Args) > 1 && os.Args[1] == "l" {
			fmt.Print(os.Getenv(standinListingEnv))
			os.Exit(0)
		}
		fmt.Printf("\n7-Zip (z) %s (x64) : Copyright (c) 1999-2026 Igor Pavlov : 2026-10-21\n", os.Getenv(standinVersionEnv))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// standin builds a 7zz Tool over the test binary, linked as 7zz, printing version's banner, its
// Spec's minimum lowered so the Tool is built whatever version it prints.
func standin(t *testing.T, version string) *tool.Tool {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "7zz")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(standinEnv, "1")
	t.Setenv(standinVersionEnv, version)
	spec := sevenzip.SevenZipSpec()
	spec.Program, spec.Minimum = link, "1"
	tl, err := tool.NewExec(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// reader returns a Reader over the 7zz mise pins.
func reader(t *testing.T) *sevenzip.Reader {
	t.Helper()
	tl, err := tool.NewExec(t.Context(), sevenzip.SevenZipSpec())
	if err != nil {
		t.Fatalf("7zz, which mise pins, run the check through mise run check: %v", err)
	}
	r, err := sevenzip.NewReader(sevenzip.Config{SevenZip: tl})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// fixture returns the path of one of the package's fixtures, under testdata.
func fixture(name string) string { return filepath.Join("testdata", name) }

// TestNewReader: a Config with no 7zz is refused with ErrNoSevenZip, and a 7zz older than 24.01,
// whatever its Spec's minimum, with a *tool.VersionError wrapping tool.ErrTooOld; 24.01 is
// accepted. The pinned 7zz's banner gives its version.
func TestNewReader(t *testing.T) {
	if _, err := sevenzip.NewReader(sevenzip.Config{}); !errors.Is(err, sevenzip.ErrNoSevenZip) {
		t.Errorf("no 7zz: %v, want ErrNoSevenZip", err)
	}
	_, err := sevenzip.NewReader(sevenzip.Config{SevenZip: standin(t, "23.01")})
	if verr, ok := errors.AsType[*tool.VersionError](err); !ok || !errors.Is(err, tool.ErrTooOld) || verr.Found != "23.1" {
		t.Errorf("23.01: %v, want a VersionError wrapping ErrTooOld", err)
	}
	if _, err := sevenzip.NewReader(sevenzip.Config{SevenZip: standin(t, "24.01")}); err != nil {
		t.Errorf("24.01: %v", err)
	}
	tl, err := tool.NewExec(t.Context(), sevenzip.SevenZipSpec())
	if err != nil {
		t.Fatal(err)
	}
	if v := tl.Version().String(); v != "26.5" {
		t.Errorf("the pinned 7zz is version %s, want 26.5, mise.toml's 26.05", v)
	}
}

// TestList lists the generated fixtures: clean.7z's directory and files with their sizes and
// CRC-32s; a symbolic link and a FIFO by their modes; traversing and absolute names as stored;
// an encrypted entry beside a clear one; a cabinet's entries, its backslashes listed as slashes;
// and declared-bomb.zst's one item sized by its frame header, bomb.zst's unsized.
func TestList(t *testing.T) {
	r := reader(t)
	list := func(name string, format sevenzip.Format) []sevenzip.Entry {
		t.Helper()
		l, err := r.List(t.Context(), sevenzip.ListRequest{Path: fixture(name), Format: format})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return l.Entries
	}
	paths := func(entries []sevenzip.Entry) []string {
		var out []string
		for _, e := range entries {
			out = append(out, e.Path)
		}
		return out
	}

	clean := list("generated/clean.7z", sevenzip.SevenZ)
	if len(clean) != 3 || !clean[0].Folder || clean[0].Path != "docs" {
		t.Fatalf("clean.7z: %+v", clean)
	}
	if e := clean[1]; e.Path != "docs/notes.txt" || e.Size != 22 || !e.HasCRC || e.CRC != crc32.ChecksumIEEE([]byte("a 7z's ordinary entry\n")) || e.Mode != 0o644 {
		t.Errorf("clean.7z's notes: %+v", e)
	}
	if e := list("generated/symlink.7z", sevenzip.SevenZ)[1]; e.Path != "passwd" || !e.IsSymlink() || e.Mode&fs.ModeSymlink == 0 {
		t.Errorf("symlink.7z's passwd: %+v", e)
	}
	if e := list("generated/device.7z", sevenzip.SevenZ)[0]; e.Path != "pipe" || !e.IsDevice() {
		t.Errorf("device.7z's pipe: %+v", e)
	}
	if got := paths(list("generated/traversal.7z", sevenzip.SevenZ)); strings.Join(got, ",") != "docs/../../evil.txt,notes.txt" {
		t.Errorf("traversal.7z: %q", got)
	}
	if got := paths(list("generated/absolute.7z", sevenzip.SevenZ)); strings.Join(got, ",") != `/tmp/evil.txt,C:\evil.txt` {
		t.Errorf("absolute.7z: %q", got)
	}
	enc := list("generated/encrypted.7z", sevenzip.SevenZ)
	if len(enc) != 2 || enc[0].Encrypted || !enc[1].Encrypted || enc[1].Path != "secret.txt" {
		t.Errorf("encrypted.7z: %+v", enc)
	}
	if got := paths(list("generated/traversal.cab", sevenzip.Cabinet)); strings.Join(got, ",") != "notes.txt,docs/../../evil.txt" {
		t.Errorf("traversal.cab: %q", got)
	}
	if got := paths(list("generated/absolute.cab", sevenzip.Cabinet)); strings.Join(got, ",") != "/tmp/evil.txt,C:/evil.txt" {
		t.Errorf("absolute.cab: %q", got)
	}
	if e := list("generated/declared-bomb.zst", sevenzip.Zstd); len(e) != 1 || !e[0].Sized || e[0].Size != 4<<30 {
		t.Errorf("declared-bomb.zst: %+v", e)
	}
	if e := list("generated/bomb.zst", sevenzip.Zstd); len(e) != 1 || e[0].Sized {
		t.Errorf("bomb.zst: %+v", e)
	}
}

// TestListRefused: a 7z whose headers are encrypted is refused with ErrEncrypted, a file that
// isn't the format named with ErrNotArchive, and a request with no path or an unknown format
// before anything runs, with ErrRequest.
func TestListRefused(t *testing.T) {
	r := reader(t)
	if _, err := r.List(t.Context(), sevenzip.ListRequest{Path: fixture("generated/encrypted-headers.7z"), Format: sevenzip.SevenZ}); !errors.Is(err, sevenzip.ErrEncrypted) {
		t.Errorf("encrypted-headers.7z: %v, want ErrEncrypted", err)
	}
	if _, err := r.List(t.Context(), sevenzip.ListRequest{Path: fixture("generated/clean.tar.zst"), Format: sevenzip.SevenZ}); !errors.Is(err, sevenzip.ErrNotArchive) {
		t.Errorf("a zstd stream as 7z: %v, want ErrNotArchive", err)
	}
	if _, err := r.List(t.Context(), sevenzip.ListRequest{Format: sevenzip.SevenZ}); !errors.Is(err, sevenzip.ErrRequest) {
		t.Errorf("no path: %v, want ErrRequest", err)
	}
	if _, err := r.List(t.Context(), sevenzip.ListRequest{Path: fixture("generated/clean.7z"), Format: "rar"}); !errors.Is(err, sevenzip.ErrRequest) {
		t.Errorf("rar: %v, want ErrRequest", err)
	}
}

// TestExtract extracts one entry at a time to a buffer: each of clean.7z's files by its name, a
// traversing name's content as any other's, a cabinet's entry, a zstd stream's content, and the
// ZIP entries of each method archive/zip doesn't read, each to its listed size and CRC-32; a name
// with a wildcard matches only itself, here nothing; an encrypted entry is refused with
// ErrEncrypted; and a request names an entry for every format but zstd, and none for zstd.
func TestExtract(t *testing.T) {
	r := reader(t)
	extract := func(path string, format sevenzip.Format, name string) ([]byte, error) {
		var b bytes.Buffer
		err := r.Extract(t.Context(), sevenzip.ExtractRequest{Path: path, Format: format, Name: name}, &b)
		return b.Bytes(), err
	}
	for _, c := range []struct {
		path   string
		format sevenzip.Format
		name   string
		want   string
	}{
		{fixture("generated/clean.7z"), sevenzip.SevenZ, "docs/notes.txt", "a 7z's ordinary entry\n"},
		{fixture("generated/traversal.7z"), sevenzip.SevenZ, "docs/../../evil.txt", "written outside the extraction directory\n"},
		{fixture("generated/traversal.cab"), sevenzip.Cabinet, "docs/../../evil.txt", "written outside the extraction directory\n"},
		{fixture("generated/encrypted.7z"), sevenzip.SevenZ, "notes.txt", "an ordinary entry\n"},
	} {
		got, err := extract(c.path, c.format, c.name)
		if err != nil || string(got) != c.want {
			t.Errorf("%s %s: %q, %v; want %q", filepath.Base(c.path), c.name, got, err, c.want)
		}
	}

	tar, err := os.ReadFile(filepath.Join("..", "inspect", "testdata", "clean.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := extract(fixture("generated/clean.tar.zst"), sevenzip.Zstd, ""); err != nil || !bytes.Equal(got, tar) {
		t.Errorf("clean.tar.zst: %d bytes, %v; want inspect's clean.tar", len(got), err)
	}

	for _, c := range []struct{ path, name string }{
		{"commons-compress/lorem-ipsum-deflate64.zip", "lorem-ipsum.txt"},
		{"commons-compress/lorem-ipsum-implode.zip", "LOREM.TXT"},
		{"commons-compress/test-method-xz.zip", "LICENSE.txt"},
		{"commons-compress/bzip2-zip.zip", "lots-of-as"},
		{"libarchive/test_read_format_zip_lzma.zipx", "vimrc"},
		{"libarchive/test_read_format_zip_ppmd8.zipx", "vimrc"},
		{"libarchive/test_read_format_zip_zstd.zipx", "vimrc"},
	} {
		l, err := r.List(t.Context(), sevenzip.ListRequest{Path: fixture(c.path), Format: sevenzip.ZIP})
		if err != nil || len(l.Entries) != 1 || l.Entries[0].Path != c.name {
			t.Fatalf("%s: %+v, %v", c.path, l.Entries, err)
		}
		got, err := extract(fixture(c.path), sevenzip.ZIP, c.name)
		if e := l.Entries[0]; err != nil || uint64(len(got)) != e.Size || crc32.ChecksumIEEE(got) != e.CRC {
			t.Errorf("%s: %d bytes, CRC-32 %08x, %v; want %d, %08x", c.path, len(got), crc32.ChecksumIEEE(got), err, e.Size, e.CRC)
		}
	}
	for _, name := range []string{"test1.xml", "test2.xml"} {
		got, err := extract(fixture("commons-compress/bla.deflate64.7z"), sevenzip.SevenZ, name)
		if err != nil || !bytes.Contains(got, []byte("<?xml")) {
			t.Errorf("bla.deflate64.7z %s: %q, %v", name, got, err)
		}
	}

	if got, err := extract(fixture("generated/clean.7z"), sevenzip.SevenZ, "docs/*.txt"); err != nil || len(got) != 0 {
		t.Errorf("a wildcard name: %q, %v; want nothing", got, err)
	}
	if _, err := extract(fixture("generated/encrypted.7z"), sevenzip.SevenZ, "secret.txt"); !errors.Is(err, sevenzip.ErrEncrypted) {
		t.Errorf("secret.txt: %v, want ErrEncrypted", err)
	}
	if _, err := extract(fixture("generated/clean.7z"), sevenzip.SevenZ, ""); !errors.Is(err, sevenzip.ErrRequest) {
		t.Errorf("a 7z with no name: %v, want ErrRequest", err)
	}
	if _, err := extract(fixture("generated/clean.tar.zst"), sevenzip.Zstd, "clean.tar"); !errors.Is(err, sevenzip.ErrRequest) {
		t.Errorf("a zstd stream with a name: %v, want ErrRequest", err)
	}
}

// failAfter is a writer that takes n bytes and then fails.
type failAfter struct{ n int }

var errFull = errors.New("full")

func (w *failAfter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		took := w.n
		w.n = 0
		return took, errFull
	}
	w.n -= len(p)
	return len(p), nil
}

// TestExtractStops: a writer that fails stops 7zz while it writes bomb.7z's 8 MiB of zeros, and
// Extract returns once 7zz has exited, with an error.
func TestExtractStops(t *testing.T) {
	r := reader(t)
	err := r.Extract(t.Context(), sevenzip.ExtractRequest{Path: fixture("generated/bomb.7z"), Format: sevenzip.SevenZ, Name: "zeros.bin"}, &failAfter{n: 64 << 10})
	if err == nil {
		t.Error("an extraction whose writer failed succeeded")
	}
}

// TestFixtureSums holds every committed fixture to the SHA-256 the testdata README gives it.
func TestFixtureSums(t *testing.T) {
	readme, err := os.ReadFile(fixture("README.md"))
	if err != nil {
		t.Fatal(err)
	}
	sums := map[string]string{}
	for line := range strings.Lines(string(readme)) {
		cols := strings.Split(strings.TrimSpace(line), "|")
		if len(cols) < 5 || !strings.HasPrefix(strings.TrimSpace(cols[1]), "`") {
			continue
		}
		name := strings.Trim(strings.TrimSpace(cols[1]), "`")
		sum := strings.Trim(strings.TrimSpace(cols[len(cols)-2]), "`")
		sums[name] = sum
	}
	var files []string
	for _, dir := range []string{"commons-compress", "libarchive", "generated"} {
		matches, err := filepath.Glob(fixture(filepath.Join(dir, "*")))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, path := range files {
		name := filepath.ToSlash(strings.TrimPrefix(path, fixture("")+string(filepath.Separator)))
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if got, want := hex.EncodeToString(sum[:]), sums[name]; got != want {
			t.Errorf("%s: SHA-256 %s, README %q", name, got, want)
		}
	}
}
