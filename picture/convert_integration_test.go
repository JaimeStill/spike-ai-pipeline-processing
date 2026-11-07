//go:build integration && linux

package picture_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// hostConverter builds a Converter over the host's magick through the exec runner, with no
// LibreOffice: an SVG needs magick alone.
func hostConverter(t *testing.T) *picture.Converter {
	t.Helper()
	mg, err := tool.NewExec(t.Context(), picture.MagickSpec())
	if err != nil {
		t.Fatal(err)
	}
	c, err := picture.NewConverter(t.Context(), picture.Config{Magick: mg})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// convertHost copies the SVG data into a directory of its own as name, converts it through c
// into a working directory of its own, twice, the second run in a later second than the first
// ended in, and returns the PNG decoded, failing unless both runs gave the same bytes, the
// working directory holds the PNG alone, and the original is kept.
func convertHost(t *testing.T, c *picture.Converter, name string, data []byte) image.Image {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var pngs [][]byte
	for i := range 2 {
		if i > 0 {
			time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
		}
		dir := t.TempDir()
		out, err := c.Convert(t.Context(), picture.Request{Dir: dir, Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, sha(string(data))+".png"); out != want {
			t.Errorf("PNG: got %s, want %s", out, want)
		}
		if got := entries(t, dir); len(got) != 1 {
			t.Errorf("working directory holds %q, want the PNG alone", got)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		pngs = append(pngs, b)
	}
	if !bytes.Equal(pngs[0], pngs[1]) {
		t.Errorf("converted twice, its PNGs differ (%d and %d B); the conversion isn't reproducible", len(pngs[0]), len(pngs[1]))
	}
	unchanged(t, path, string(data))
	img, err := png.Decode(bytes.NewReader(pngs[0]))
	if err != nil {
		t.Fatalf("its PNG doesn't decode: %v", err)
	}
	t.Logf("recorded: %s %d B -> png %dx%d %d B", name, len(data), img.Bounds().Dx(), img.Bounds().Dy(), len(pngs[0]))
	return img
}

// census counts img's pixels: opaque white, dark (each channel below a quarter), and red (red
// high, green and blue low), and the pixels that aren't opaque.
func census(img image.Image) (white, dark, red, translucent int) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBA64Model.Convert(img.At(x, y)).(color.NRGBA64)
			switch {
			case c.A != 0xffff:
				translucent++
			case c.R == 0xffff && c.G == 0xffff && c.B == 0xffff:
				white++
			case c.R < 0x4000 && c.G < 0x4000 && c.B < 0x4000:
				dark++
			case c.R > 0xc000 && c.G < 0x4000 && c.B < 0x4000:
				red++
			}
		}
	}
	return white, dark, red, translucent
}

// TestConvertSVGHost converts the package's own SVG fixtures, shapes and text and an ink-like
// drawing, on the host's magick: each is rendered at 150 dpi, its size in CSS pixels or
// millimetres scaled to it, flattened onto opaque white with print on it, and the same bytes
// converted twice.
func TestConvertSVGHost(t *testing.T) {
	c := hostConverter(t)
	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{"shapes.svg", 500, 250},
		{"ink.svg", 236, 118},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.name))
			if err != nil {
				t.Fatal(err)
			}
			img := convertHost(t, c, tc.name, data)
			if b := img.Bounds(); b.Dx() != tc.width || b.Dy() != tc.height {
				t.Errorf("PNG is %dx%d, want %dx%d at 150 dpi", b.Dx(), b.Dy(), tc.width, tc.height)
			}
			white, dark, _, translucent := census(img)
			if translucent != 0 || white < img.Bounds().Dx()*img.Bounds().Dy()/2 || dark == 0 {
				t.Errorf("PNG has %d white, %d dark, and %d translucent pixels; want it flattened onto white with print on it", white, dark, translucent)
			}
		})
	}
}

// TestConvertSVGScriptHost converts ~/docs' script-bearing.svg, committed as testdata: an SVG with
// an onload handler, an inline script, and an image whose href is http://example.invalid. It
// converts, at 150 dpi, to a PNG of its text alone: librsvg, the rsvg coder the conversion names,
// has no script engine, so neither the handler nor the script runs, and its resolver loads no
// URL but a data: URL or a file beside the SVG, the copy alone in its directory here, so the
// image is never fetched and renders as nothing.
func TestConvertSVGScriptHost(t *testing.T) {
	c := hostConverter(t)
	data, err := os.ReadFile(filepath.Join("testdata", "script-bearing.svg"))
	if err != nil {
		t.Fatal(err)
	}
	img := convertHost(t, c, "script-bearing.svg", data)
	if b := img.Bounds(); b.Dx() != 625 || b.Dy() != 187 {
		t.Errorf("PNG is %dx%d, want 625x187, 400x120 at 150 dpi, rounded down", b.Dx(), b.Dy())
	}
	if _, dark, _, translucent := census(img); dark == 0 || translucent != 0 {
		t.Errorf("PNG has %d dark and %d translucent pixels; want its text, opaque", dark, translucent)
	}
}

// TestConvertSVGReferencesHost: an SVG whose images name a file beside it, a file by its
// absolute path, and a URL on a listener of this test's own converts to a PNG that draws none
// of them, and nothing connects to the listener. The pictures are red, so one drawn would show.
func TestConvertSVGReferencesHost(t *testing.T) {
	c := hostConverter(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var connections atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			_ = conn.Close()
		}
	}()
	red := redPNG(t)
	beside := t.TempDir()
	if err := os.WriteFile(filepath.Join(beside, "beside.png"), red, 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.png")
	if err := os.WriteFile(elsewhere, red, 0o600); err != nil {
		t.Fatal(err)
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="96" height="32">`+
		`<image href="beside.png" x="0" y="0" width="32" height="32"/>`+
		`<image xlink:href="file://%s" x="32" y="0" width="32" height="32"/>`+
		`<image href="http://%s/remote.png" x="64" y="0" width="32" height="32"/>`+
		`</svg>`, elsewhere, ln.Addr())
	path := filepath.Join(beside, "references.svg")
	if err := os.WriteFile(path, []byte(svg), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := c.Convert(t.Context(), picture.Request{Dir: t.TempDir(), Path: path})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, reds, _ := census(img); reds != 0 {
		t.Errorf("PNG has %d red pixels; an image the SVG names was drawn", reds)
	}
	if n := connections.Load(); n != 0 {
		t.Errorf("%d connections to the URL the SVG names; want none", n)
	}
	if strings.Contains(string(b), "remote") {
		t.Errorf("the PNG names the URL")
	}
}

// redPNG returns a 32x32 PNG of opaque red.
func redPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for i := 0; i < len(img.Pix); i += 4 {
		copy(img.Pix[i:], []byte{0xff, 0, 0, 0xff})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
