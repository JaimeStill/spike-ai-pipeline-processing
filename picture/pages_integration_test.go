//go:build integration && linux

package picture_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
)

// convertPagesHost converts the picture at path through c's ConvertPages into a working
// directory of its own, twice, and returns its PNGs decoded, in order, failing unless both runs
// gave the same bytes, page for page, the working directory holds the PNGs alone, and the
// original is kept.
func convertPagesHost(t *testing.T, c *picture.Converter, path string) []image.Image {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var runs [][][]byte
	for range 2 {
		dir := t.TempDir()
		pngs, err := c.ConvertPages(t.Context(), picture.Request{Dir: dir, Path: path})
		if err != nil {
			t.Fatal(err)
		}
		if got := entries(t, dir); len(got) != len(pngs) {
			t.Errorf("working directory holds %q, want the %d PNGs alone", got, len(pngs))
		}
		var run [][]byte
		for _, p := range pngs {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			run = append(run, b)
		}
		runs = append(runs, run)
	}
	if len(runs[0]) != len(runs[1]) {
		t.Fatalf("converted twice, %d and %d PNGs", len(runs[0]), len(runs[1]))
	}
	var imgs []image.Image
	for i, b := range runs[0] {
		if !bytes.Equal(b, runs[1][i]) {
			t.Errorf("PNG %d: converted twice, its bytes differ; the conversion isn't reproducible", i+1)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("PNG %d doesn't decode: %v", i+1, err)
		}
		imgs = append(imgs, img)
	}
	unchanged(t, path, string(data))
	return imgs
}

// sizes returns each of imgs' width and height, in order.
func sizes(imgs []image.Image) []image.Point {
	var out []image.Point
	for _, img := range imgs {
		out = append(out, img.Bounds().Size())
	}
	return out
}

// wantSizes fails the test unless imgs are of sizes want, in order.
func wantSizes(t *testing.T, imgs []image.Image, want ...image.Point) {
	t.Helper()
	got := sizes(imgs)
	if len(got) != len(want) {
		t.Fatalf("%d PNGs of sizes %v, want %d of %v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("PNG %d is %v, want %v", i+1, got[i], want[i])
		}
	}
}

// near reports whether img's pixel at x, y is within 8 of want in each channel, opaque.
func near(img image.Image, x, y int, want color.NRGBA) bool {
	c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	d := func(a, b uint8) bool { return max(a, b)-min(a, b) <= 8 }
	return c.A == 0xff && d(c.R, want.R) && d(c.G, want.G) && d(c.B, want.B)
}

var (
	white = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	red   = color.NRGBA{0xff, 0, 0, 0xff}
)

// docsImage returns the path of ~/docs' image/formats/rel, skipping the test when this machine
// hasn't it.
func docsImage(t *testing.T, rel string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	path := filepath.Join(home, "docs", "image", "formats", rel)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no ~/docs/image/formats/%s on this machine", rel)
	}
	return path
}

// TestConvertPagesTIFFHost converts the package's pages.tiff, three pages of 40, 50, and 60 by
// 30 pixels, on the host's magick: three PNGs, in page order, white, mid-gray, and black, each
// the same bytes converted twice. Convert still gives its first page alone.
func TestConvertPagesTIFFHost(t *testing.T) {
	c := hostConverter(t)
	path := filepath.Join("testdata", "pages.tiff")
	imgs := convertPagesHost(t, c, path)
	wantSizes(t, imgs, image.Pt(40, 30), image.Pt(50, 30), image.Pt(60, 30))
	for i, want := range []uint8{0xff, 0x80, 0} {
		if g := color.GrayModel.Convert(imgs[i].At(5, 5)).(color.Gray).Y; max(g, want)-min(g, want) > 2 {
			t.Errorf("page %d is gray %d, want %d", i+1, g, want)
		}
	}
	png, err := c.Convert(t.Context(), picture.Request{Dir: t.TempDir(), Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if imgs := decodeFile(t, png); imgs.Bounds().Size() != image.Pt(40, 30) {
		t.Errorf("Convert's PNG is %v, want the first page's 40x30", imgs.Bounds().Size())
	}
}

// decodeFile decodes the PNG at path.
func decodeFile(t *testing.T, path string) image.Image {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// TestConvertPagesTIFFDocs converts ~/docs' TIFFs, read only, when this machine has them: the two
// scans of three pages give three PNGs each, every page its scan's size, and the two single
// images one each, each the same bytes converted twice.
func TestConvertPagesTIFFDocs(t *testing.T) {
	c := hostConverter(t)
	for _, tc := range []struct {
		rel   string
		size  image.Point
		pages int
	}{
		{"tiff/scanned-3page-g4.tif", image.Pt(1700, 2200), 3},
		{"tiff/scanned-3page-lzw-gray.tif", image.Pt(1275, 1650), 3},
		{"tiff/photo-deflate.tif", image.Pt(960, 540), 1},
		{"tiff/old-style-jpeg-compression.tif", image.Pt(4160, 870), 1},
	} {
		t.Run(filepath.Base(tc.rel), func(t *testing.T) {
			imgs := convertPagesHost(t, c, docsImage(t, tc.rel))
			var want []image.Point
			for range tc.pages {
				want = append(want, tc.size)
			}
			wantSizes(t, imgs, want...)
		})
	}
}

// TestConvertRasterHost converts the package's HEIC, WebP, and AVIF fixtures on the host's
// magick, each the same bytes converted twice. two-images.heic's primary image is stored 48x32,
// its left half white, and rotated by its irot box a quarter turn clockwise, beside a second
// image of 32x32 blue: its PNG is the primary, upright, 32x48, white above red. oriented.webp
// holds the same picture under EXIF orientation 6 and converts to the same. animated.webp and
// animated.avif are three frames of 24x16, red, green, and blue: each converts to its first
// frame, red.
func TestConvertRasterHost(t *testing.T) {
	c := hostConverter(t)
	for _, name := range []string{"two-images.heic", "oriented.webp"} {
		t.Run(name, func(t *testing.T) {
			imgs := convertPagesHost(t, c, filepath.Join("testdata", name))
			wantSizes(t, imgs, image.Pt(32, 48))
			if !near(imgs[0], 16, 4, white) || !near(imgs[0], 16, 44, red) {
				t.Errorf("PNG is %v above %v, want white above red", imgs[0].At(16, 4), imgs[0].At(16, 44))
			}
		})
	}
	for _, name := range []string{"animated.webp", "animated.avif"} {
		t.Run(name, func(t *testing.T) {
			imgs := convertPagesHost(t, c, filepath.Join("testdata", name))
			wantSizes(t, imgs, image.Pt(24, 16))
			if !near(imgs[0], 12, 8, red) {
				t.Errorf("PNG is %v, want the first frame's red", imgs[0].At(12, 8))
			}
		})
	}
}

// TestConvertRasterDocs converts ~/docs' HEIC, WebP, and AVIF samples, read only, when this
// machine has them: each gives one PNG of its size, the same bytes converted twice. example.heic
// holds two top-level images of one size: its PNG is the primary, item 20004, whose top quarter
// is sky, bluer than red on the whole, as heif-dec decodes it, where the other image's top
// quarter is redder than blue.
func TestConvertRasterDocs(t *testing.T) {
	c := hostConverter(t)
	for _, tc := range []struct {
		rel  string
		size image.Point
	}{
		{"heic/example.heic", image.Pt(1280, 854)},
		{"webp/photo.webp", image.Pt(1920, 1080)},
		{"webp/text-lossless.webp", image.Pt(210, 746)},
		{"avif/example.avif", image.Pt(800, 533)},
		{"avif/bbb-alpha-inverted.avif", image.Pt(3840, 2160)},
		{"avif/hdr-cosmos.avif", image.Pt(2048, 858)},
		{"avif/monochrome.avif", image.Pt(1280, 720)},
	} {
		t.Run(filepath.Base(tc.rel), func(t *testing.T) {
			imgs := convertPagesHost(t, c, docsImage(t, tc.rel))
			wantSizes(t, imgs, tc.size)
			if tc.rel == "heic/example.heic" {
				d := blueOverRed(imgs[0])
				t.Logf("recorded: example.heic's top quarter is bluer than red by %.1f", d)
				if d < 30 {
					t.Errorf("its top quarter is bluer than red by %.0f, want at least 30, the primary's sky", d)
				}
			}
		})
	}
}

// blueOverRed returns how much bluer than red img's top quarter is: its mean blue less its mean
// red, from 0 to 255.
func blueOverRed(img image.Image) float64 {
	b := img.Bounds()
	var sum float64
	n := 0
	for y := b.Min.Y; y < b.Min.Y+b.Dy()/4; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			sum += float64(c.B) - float64(c.R)
			n++
		}
	}
	return sum / float64(n)
}
