//go:build linux

package document_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// TestSpool checks that the spooled file lands in the caller's directory with the reader's
// content, and that its remove function removes it.
func TestSpool(t *testing.T) {
	dir := t.TempDir()
	pdf, err := os.ReadFile(filepath.Join("testdata", "clean.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	path, remove, err := document.Spool(dir, bytes.NewReader(pdf))
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(path); got != dir {
		t.Errorf("spooled into %s, want the caller's %s", got, dir)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pdf) {
		t.Errorf("spooled %d bytes, want the reader's %d, equal", len(got), len(pdf))
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, dir)
}

// TestSpoolEach checks that two spools into one directory are two files.
func TestSpoolEach(t *testing.T) {
	dir := t.TempDir()
	a, removeA, err := document.Spool(dir, bytes.NewReader([]byte("a")))
	if err != nil {
		t.Fatal(err)
	}
	b, removeB, err := document.Spool(dir, bytes.NewReader([]byte("b")))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("both spools are %s, want two files", a)
	}
	if err := errors.Join(removeA(), removeB()); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, dir)
}

// TestSpoolFailedRead checks that a reader that fails part way is the spool's error and leaves no
// file behind.
func TestSpoolFailedRead(t *testing.T) {
	dir := t.TempDir()
	readErr := errors.New("connection reset")
	pdf := io.MultiReader(bytes.NewReader([]byte("%PDF-1.7")), iotest.ErrReader(readErr))
	if _, _, err := document.Spool(dir, pdf); !errors.Is(err, readErr) {
		t.Errorf("Spool returned %v, want the reader's %v", err, readErr)
	}
	assertEmpty(t, dir)
}

// TestSpoolNoDirectory checks that a spool needs the caller's directory: an empty one is refused,
// not taken as the system's temporary directory, and a missing one fails.
func TestSpoolNoDirectory(t *testing.T) {
	if _, _, err := document.Spool("", bytes.NewReader(nil)); err == nil {
		t.Error("Spool into no directory succeeded, want an error")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, _, err := document.Spool(missing, bytes.NewReader(nil)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Spool into a missing directory returned %v, want fs.ErrNotExist", err)
	}
}

// assertEmpty fails the test when dir holds anything.
func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s left behind in the caller's directory", e.Name())
	}
}
