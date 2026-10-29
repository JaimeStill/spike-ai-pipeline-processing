//go:build integration && linux

package document_test

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
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
