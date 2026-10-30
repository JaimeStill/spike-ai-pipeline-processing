//go:build integration && linux

package document_test

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// hostCorrector builds the corrections stage over the host's magick through the exec runner,
// which refuses one older than 7.1.
func hostCorrector(t *testing.T) (*document.Corrector, *tool.Tool) {
	t.Helper()
	magick, err := tool.NewExec(t.Context(), document.MagickSpec())
	if err != nil {
		t.Fatal(err)
	}
	c, err := document.NewCorrector(magick)
	if err != nil {
		t.Fatal(err)
	}
	return c, magick
}

func TestMagickVersion(t *testing.T) {
	_, magick := hostCorrector(t)
	t.Logf("%s %s", magick.Name(), magick.Version())
}

// scannedPage draws a page-like PNG with the host's magick: dark bars for lines of print on a
// tinted, slightly rotated page, with speckle, so every correction has something to act on. It is
// 8-bit and opaque, as a renderer's page is.
func scannedPage(t *testing.T, magick *tool.Tool) []byte {
	t.Helper()
	args := []string{"-size", "850x1100", "xc:#f4efe6", "-fill", "#303030"}
	for y := 100; y < 1000; y += 40 {
		args = append(args, "-draw", fmt.Sprintf("rectangle 100,%d 750,%d", y, y+14))
	}
	args = append(args, "-background", "white", "-rotate", "2", "-blur", "0x1",
		"-seed", "7", "-attenuate", "0.2", "+noise", "Impulse",
		"-alpha", "off", "+repage", "-depth", "8", "png:-")
	var out bytes.Buffer
	if err := magick.Run(t.Context(), tool.Invocation{Args: args, Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestCorrectHost checks each correction alone and all five together on the host's magick: each
// run reads the page's PNG and writes a valid PNG, and Grayscale's is gray.
func TestCorrectHost(t *testing.T) {
	c, magick := hostCorrector(t)
	page := scannedPage(t, magick)
	cases := map[string][]document.Correction{"all": all}
	for _, corr := range all {
		cases[corr.String()] = []document.Correction{corr}
	}
	for name, corrections := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			start := time.Now()
			if err := c.Correct(t.Context(), corrections, bytes.NewReader(page), &out); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			img, err := png.Decode(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("magick's output isn't a PNG: %v", err)
			}
			if b := img.Bounds(); b.Dx() < 800 || b.Dy() < 1000 {
				t.Errorf("corrected page is %v, want at least the page's 850x1100 less a deskew's crop", b)
			}
			if name == "all" || name == document.Grayscale.String() {
				if !gray(img) {
					t.Error("Grayscale's page has a pixel whose channels differ, want gray")
				}
			}
			t.Logf("%s: %d bytes in %v, %v", name, out.Len(), elapsed, img.Bounds())
		})
	}
}

// gray reports whether every pixel of img has equal red, green, and blue.
func gray(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r != g || g != bl {
				return false
			}
		}
	}
	return true
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
	assessed := map[string][]document.Assessment{}
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
		holds       func(d, c document.Assessment) bool
	}{
		{1, 1, "skew about -2.5", func(d, _ document.Assessment) bool { return math.Abs(d.Skew+2.5) < 0.5 }},
		{2, 3, "skew about -7", func(d, _ document.Assessment) bool { return math.Abs(d.Skew+7) < 0.5 }},
		{3, 3, "less contrast", func(d, c document.Assessment) bool { return d.Contrast < c.Contrast }},
		{4, 5, "less contrast", func(d, c document.Assessment) bool { return d.Contrast < c.Contrast }},
		{5, 5, "more noise", func(d, c document.Assessment) bool { return d.Noise > c.Noise }},
		{6, 6, "more noise", func(d, c document.Assessment) bool { return d.Noise > c.Noise }},
		{7, 2, "less sharpness", func(d, c document.Assessment) bool { return d.Sharpness < c.Sharpness }},
		{8, 1, "less sharpness", func(d, c document.Assessment) bool { return d.Sharpness < c.Sharpness }},
	} {
		d, c := degraded[tc.page-1], clean[tc.clean-1]
		if !tc.holds(d, c) {
			t.Errorf("degraded.pdf page %d, %+v, against clean.pdf page %d, %+v: want %s", tc.page, d, tc.clean, c, tc.fault)
		}
	}
}
