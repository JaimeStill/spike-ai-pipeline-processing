//go:build integration && linux

package document_test

import (
	"bytes"
	"image/png"
	"path/filepath"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// hostGhostscript builds the released renderer over the host's gs through the exec runner,
// which refuses one older than 10.0.
func hostGhostscript(t *testing.T) (*document.Ghostscript, *tool.Tool) {
	t.Helper()
	gs, err := tool.NewExec(t.Context(), document.GhostscriptSpec())
	if err != nil {
		t.Fatal(err)
	}
	g, err := document.NewGhostscript(gs)
	if err != nil {
		t.Fatal(err)
	}
	return g, gs
}

func TestGhostscriptVersion(t *testing.T) {
	_, gs := hostGhostscript(t)
	t.Logf("%s %s", gs.Name(), gs.Version())
}

// TestGhostscriptCountHost checks the host's gs counts the fixtures' pages.
func TestGhostscriptCountHost(t *testing.T) {
	g, _ := hostGhostscript(t)
	for set, want := range map[string]int{"clean": 6, "scanned": 7} {
		n, err := g.Count(t.Context(), filepath.Join("testdata", set+".pdf"))
		if err != nil || n != want {
			t.Errorf("Count(%s.pdf) returned %d, %v, want %d", set, n, err, want)
		}
	}
}

// TestGhostscriptRenderHost checks a page of each fixture renders as a PNG of letter size at
// 150 dpi, 1275x1650, and that a page past the last is an error that writes nothing.
func TestGhostscriptRenderHost(t *testing.T) {
	g, _ := hostGhostscript(t)
	for set, page := range map[string]int{"clean": 1, "scanned": 7} {
		var out bytes.Buffer
		if err := g.Render(t.Context(), filepath.Join("testdata", set+".pdf"), page, 150, &out); err != nil {
			t.Fatalf("Render(%s.pdf page %d): %v", set, page, err)
		}
		img, err := png.Decode(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("%s.pdf page %d isn't a PNG: %v", set, page, err)
		}
		if b := img.Bounds(); b.Dx() != 1275 || b.Dy() != 1650 {
			t.Errorf("%s.pdf page %d is %dx%d, want letter at 150 dpi, 1275x1650", set, page, b.Dx(), b.Dy())
		}
		t.Logf("%s.pdf page %d: %d PNG bytes, %v", set, page, out.Len(), img.Bounds())
	}
	var out bytes.Buffer
	err := g.Render(t.Context(), filepath.Join("testdata", "clean.pdf"), 7, 150, &out)
	if err == nil {
		t.Error("Render(clean.pdf page 7) of 6 pages returned no error")
	}
	if out.Len() != 0 {
		t.Errorf("Render(clean.pdf page 7) of 6 pages wrote %d bytes, want none", out.Len())
	}
	t.Logf("page past the last: %v", err)
}
