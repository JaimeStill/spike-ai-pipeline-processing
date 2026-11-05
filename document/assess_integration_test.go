//go:build integration && linux

package document_test

import (
	"bytes"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// all is every correction, in the fixed order.
var all = []picture.Correction{
	picture.Deskew, picture.Despeckle, picture.ContrastStretch, picture.Unsharp, picture.Grayscale,
}

// hostCorrector builds the picture package's corrections stage over the host's magick through
// the exec runner, which refuses one older than 7.1.
func hostCorrector(t *testing.T) (*picture.Corrector, *tool.Tool) {
	t.Helper()
	magick, err := tool.NewExec(t.Context(), picture.MagickSpec())
	if err != nil {
		t.Fatal(err)
	}
	c, err := picture.NewCorrector(magick)
	if err != nil {
		t.Fatal(err)
	}
	return c, magick
}

// TestAssessHost assesses every page of the three fixtures on the host's gs and magick, rendered
// at 150 dpi, records each page's measures, and checks the directions that hold whatever a page's
// content: degraded.pdf's skewed pages report their rotation, about -2.5 and -7 degrees, and the
// clean pages none; its faded pages less contrast than their clean pages, its speckled pages more
// noise, and its blurred pages less sharpness; the clean and degraded pages are gray, and every
// page is 1275x1650.
func TestAssessHost(t *testing.T) {
	g, _ := hostGhostscript(t)
	c, _ := hostCorrector(t)
	assessed := map[string][]picture.Assessment{}
	for set, n := range map[string]int{"clean": 6, "degraded": 9, "scanned": 7} {
		for page := 1; page <= n; page++ {
			var png bytes.Buffer
			if err := g.Render(t.Context(), filepath.Join("testdata", set+".pdf"), page, 150, &png); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			a, err := c.Assess(t.Context(), &png)
			if err != nil {
				t.Fatalf("Assess(%s.pdf page %d): %v", set, page, err)
			}
			assessed[set] = append(assessed[set], a)
			t.Logf("recorded: Assess %s.pdf page %d: %dx%d, skew %.3f, contrast %.4f, noise %.5f, sharpness %.4f, colored %.5f, grayscale %v, %v",
				set, page, a.Width, a.Height, a.Skew, a.Contrast, a.Noise, a.Sharpness, a.Colored, a.Grayscale,
				time.Since(start).Round(time.Millisecond))
			if a.Width != 1275 || a.Height != 1650 {
				t.Errorf("%s.pdf page %d is %dx%d, want 1275x1650", set, page, a.Width, a.Height)
			}
			if set != "scanned" && !a.Grayscale {
				t.Errorf("%s.pdf page %d isn't grayscale, colored %.5f, want gray", set, page, a.Colored)
			}
		}
	}
	clean, degraded := assessed["clean"], assessed["degraded"]
	for i, a := range clean {
		if math.Abs(a.Skew) > 0.5 {
			t.Errorf("clean.pdf page %d's skew is %.3f, want level", i+1, a.Skew)
		}
	}
	// degraded.pdf's pages, by its README's table: each its fault, and the clean page it came from.
	for _, tc := range []struct {
		page, clean int
		fault       string
		holds       func(d, c picture.Assessment) bool
	}{
		{1, 1, "skew about -2.5", func(d, _ picture.Assessment) bool { return math.Abs(d.Skew+2.5) < 0.5 }},
		{2, 3, "skew about -7", func(d, _ picture.Assessment) bool { return math.Abs(d.Skew+7) < 0.5 }},
		{3, 3, "less contrast", func(d, c picture.Assessment) bool { return d.Contrast < c.Contrast }},
		{4, 5, "less contrast", func(d, c picture.Assessment) bool { return d.Contrast < c.Contrast }},
		{5, 5, "more noise", func(d, c picture.Assessment) bool { return d.Noise > c.Noise }},
		{6, 6, "more noise", func(d, c picture.Assessment) bool { return d.Noise > c.Noise }},
		{7, 2, "less sharpness", func(d, c picture.Assessment) bool { return d.Sharpness < c.Sharpness }},
		{8, 1, "less sharpness", func(d, c picture.Assessment) bool { return d.Sharpness < c.Sharpness }},
	} {
		d, c := degraded[tc.page-1], clean[tc.clean-1]
		if !tc.holds(d, c) {
			t.Errorf("degraded.pdf page %d, %+v, against clean.pdf page %d, %+v: want %s", tc.page, d, tc.clean, c, tc.fault)
		}
	}
}
