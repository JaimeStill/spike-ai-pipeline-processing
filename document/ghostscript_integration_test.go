//go:build integration && linux

package document_test

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"path/filepath"
	"strings"
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

// TestGhostscriptTextHost checks the host's gs reads clean.pdf's text layer, every page of it
// having one, and finds none on scanned.pdf's pages, their text an image, and records each clean
// page's character and word error rates against its ground truth. A page past the last is
// ErrPageRange.
func TestGhostscriptTextHost(t *testing.T) {
	g, _ := hostGhostscript(t)
	pdf := filepath.Join("testdata", "clean.pdf")
	var edits, chars, wedits, words int
	for i, truth := range truthOf(t, "clean") {
		text, err := g.Text(t.Context(), pdf, i+1)
		if err != nil {
			t.Fatalf("Text(clean.pdf page %d): %v", i+1, err)
		}
		if !document.HasText(text) {
			t.Errorf("clean.pdf page %d has no text layer, want one", i+1)
			continue
		}
		e, n := charErrors(truth, text)
		we, wn := wordErrors(truth, text)
		found := wordsFound(truth, text)
		edits, chars, wedits, words = edits+e, chars+n, wedits+we, words+wn
		// txtwrite keeps the page's layout, so two columns read across both, line by line, and a
		// form's labels and values sit apart: the error rates, which count order, are recorded,
		// and the words are asserted, whatever their order.
		if found < 0.95 {
			t.Errorf("clean.pdf page %d's text layer holds %.4f of its truth's words, want nearly all: %q", i+1, found, text)
		}
		t.Logf("recorded: text layer clean.pdf page %d: CER %.4f, WER %.4f, %.4f of the truth's words found, %d bytes",
			i+1, rate(e, n), rate(we, wn), found, len(text))
	}
	t.Logf("recorded: text layer clean.pdf, 6 pages: CER %.4f, WER %.4f", rate(edits, chars), rate(wedits, words))
	scanned := filepath.Join("testdata", "scanned.pdf")
	for page := 1; page <= 7; page++ {
		text, err := g.Text(t.Context(), scanned, page)
		if err != nil {
			t.Fatalf("Text(scanned.pdf page %d): %v", page, err)
		}
		if document.HasText(text) {
			t.Errorf("scanned.pdf page %d has text layer %q, want none", page, text)
		}
	}
	if _, err := g.Text(t.Context(), pdf, 7); !errors.Is(err, document.ErrPageRange) {
		t.Errorf("Text(clean.pdf page 7) of 6 pages returned %v, want document.ErrPageRange", err)
	}
}

// TestGhostscriptRenderRegionHost checks RenderRegion on the host's gs: a footer band of
// scanned.pdf's page 7 at 300 dpi decodes as a PNG of the band's size, letter's width by a tenth
// of its height, 2550x330, whose pixels are the whole page's, rendered at 300 dpi, in that band.
// The whole page as a region is the page Render renders, and a page past the last is an error
// that writes nothing.
func TestGhostscriptRenderRegionHost(t *testing.T) {
	g, _ := hostGhostscript(t)
	pdf := filepath.Join("testdata", "scanned.pdf")
	render := func(region *document.Region) image.Image {
		t.Helper()
		var out bytes.Buffer
		var err error
		if region == nil {
			err = g.Render(t.Context(), pdf, 7, 300, &out)
		} else {
			err = g.RenderRegion(t.Context(), pdf, 7, 300, *region, &out)
		}
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("region %+v isn't a PNG: %v", region, err)
		}
		return img
	}
	page := render(nil)
	footer := render(&document.Region{Top: 0.9, Bottom: 1, Left: 0, Right: 1})
	if b := footer.Bounds(); b.Dx() != 2550 || b.Dy() != 330 {
		t.Fatalf("the footer band is %dx%d, want 2550x330", b.Dx(), b.Dy())
	}
	if !samePixels(footer, page, image.Pt(0, 3300-330)) {
		t.Error("the footer band's pixels aren't the page's bottom 330 rows")
	}
	whole := render(&document.Region{Top: 0, Bottom: 1, Left: 0, Right: 1})
	if !samePixels(whole, page, image.Point{}) || whole.Bounds() != page.Bounds() {
		t.Error("the whole page as a region isn't the page Render renders")
	}
	corner := render(&document.Region{Top: 0, Bottom: 0.25, Left: 0.5, Right: 1})
	if b := corner.Bounds(); b.Dx() != 1275 || b.Dy() != 825 || !samePixels(corner, page, image.Pt(1275, 0)) {
		t.Errorf("the top right quarter band is %v, want 1275x825 of the page's own pixels", corner.Bounds())
	}
	t.Logf("recorded: RenderRegion scanned.pdf page 7 at 300 dpi: footer band %v, top right %v, the page's own pixels",
		footer.Bounds(), corner.Bounds())
	var out bytes.Buffer
	err := g.RenderRegion(t.Context(), filepath.Join("testdata", "clean.pdf"), 7, 150, document.Region{Top: 0, Bottom: 1, Left: 0, Right: 1}, &out)
	if err == nil || out.Len() != 0 {
		t.Errorf("RenderRegion(clean.pdf page 7) of 6 pages returned %v and wrote %d bytes, want an error and nothing", err, out.Len())
	}
	t.Logf("region past the last: %v", err)
}

// samePixels reports whether every pixel of part equals whole's at part's position offset by at.
func samePixels(part, whole image.Image, at image.Point) bool {
	b := part.Bounds()
	if !b.Add(at).In(whole.Bounds()) {
		return false
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if part.At(x, y) != whole.At(x+at.X, y+at.Y) {
				return false
			}
		}
	}
	return true
}

// wordsFound returns the fraction of truth's words that got holds, whatever their order, each
// word of got counted once: 1 when got holds every word of truth, as often as truth does.
func wordsFound(truth, got string) float64 {
	have := map[string]int{}
	for _, w := range strings.Fields(got) {
		have[w]++
	}
	want := strings.Fields(truth)
	found := 0
	for _, w := range want {
		if have[w] > 0 {
			have[w]--
			found++
		}
	}
	return rate(found, len(want))
}
