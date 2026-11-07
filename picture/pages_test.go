//go:build linux

package picture_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
	"github.com/JaimeStill/spike-ai-pipeline-processing/route"
)

// Constructed rasters, each its kind's signature followed by filler.
var (
	heicData = "\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic" + "filler"
	webpData = "RIFF\x10\x00\x00\x00WEBPVP8 " + "filler"
	avifData = "\x00\x00\x00\x18ftypavif\x00\x00\x00\x00mif1avif" + "filler"
)

// pageNames returns the names of a TIFF's PNGs, its content's SHA-256, "-page", each page's
// number from 1 to n, and .png.
func pageNames(content string, n int) []string {
	var names []string
	for i := range n {
		names = append(names, fmt.Sprintf("%s-page%d.png", sha(content), i+1))
	}
	return names
}

// TestConvertPagesTIFF: a TIFF, told by its signature whatever its name, is copied under its
// SHA-256 and .tiff into a directory of its own and converted through one magick run in that
// directory: every image read as TIFF and each written as a PNG of its own, without the run's
// time, to a pattern relative to it. Each page's PNG is the working directory's file named by the
// TIFF's SHA-256, "-page", and its number from 1, returned in page order; a TIFF of one page
// gives one. The original is kept, and the copy's directory removed.
func TestConvertPagesTIFF(t *testing.T) {
	for _, tc := range []struct {
		desc, content, name string
		pages               int
	}{
		{"three pages", tiffLE, "", 3},
		{"one page, named otherwise", tiffBE, "scan.dat", 1},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := standinConverter(t, false, true)
			t.Setenv(pagesEnv, fmt.Sprint(tc.pages))
			path := writePicture(t, tc.content, tc.name, "")
			dir := t.TempDir()
			pngs, err := c.ConvertPages(t.Context(), picture.Request{Dir: dir, Path: path})
			if err != nil {
				t.Fatal(err)
			}
			names := pageNames(tc.content, tc.pages)
			var want []string
			for _, n := range names {
				want = append(want, filepath.Join(dir, n))
			}
			if !slices.Equal(pngs, want) {
				t.Fatalf("PNGs: got %q, want %q", pngs, want)
			}
			for i, png := range pngs {
				page := fmt.Sprintf("%s%s%s page %d", pngSignature, convertedPrefix, tc.content, i+1)
				if b, err := os.ReadFile(png); err != nil || string(b) != page {
					t.Errorf("page %d holds %q, %v; want %q", i+1, b, err, page)
				}
			}
			unchanged(t, path, tc.content)
			if got := entries(t, dir); !slices.Equal(got, names) {
				t.Errorf("working directory holds %q, want the pages alone", got)
			}
			all := runs()
			if got := programs(all); !slices.Equal(got, []string{"magick"}) {
				t.Fatalf("runs: got %q, want magick alone", got)
			}
			stem := sha(tc.content)
			args := []string{`tiff:` + own(dir) + stem + `\.tiff`, `\+adjoin`, `-define`, `png:exclude-chunks=date,time`, `png:` + stem + `-%d\.png`}
			if !matchArgs(all[0].Args, args) {
				t.Errorf("magick's arguments: got %q, want %q", all[0].Args, args)
			}
			if !regexp.MustCompile(`^`+own(dir)+`?$`).MatchString(all[0].Dir) || all[0].Input != tc.content {
				t.Errorf("magick ran in %s on %q; want in the copy's directory on the TIFF", all[0].Dir, all[0].Input)
			}
		})
	}
}

// TestConvertPagesOne: a picture of any other kind gives the one PNG Convert gives, through the
// same run.
func TestConvertPagesOne(t *testing.T) {
	c, runs := standinConverter(t, false, true)
	path := writePicture(t, svgData, "", "")
	dir := t.TempDir()
	pngs, err := c.ConvertPages(t.Context(), picture.Request{Dir: dir, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, sha(svgData)+".png")}; !slices.Equal(pngs, want) {
		t.Errorf("PNGs: got %q, want %q", pngs, want)
	}
	if all := runs(); len(all) != 1 || !slices.Contains(all[0].Args, "-density") {
		t.Errorf("runs: got %v, want the SVG's one", all)
	}
}

// TestConvertPagesFails: a pages run that fails, writes no PNG, or leaves a gap in its pages is
// an error naming the TIFF's copy, and leaves nothing in the working directory; a TIFF without
// magick is refused with ErrNoMagick before anything runs.
func TestConvertPagesFails(t *testing.T) {
	copied := sha(tiffLE) + ".tiff"
	for _, tc := range []struct {
		desc string
		env  map[string]string
		want string
	}{
		{"magick exits 1", map[string]string{failEnv: "magick: improper image header\n"},
			"picture: magick: " + copied + ": tool: magick: exit code 1: magick: improper image header"},
		{"magick writes no PNG", map[string]string{rawEnv: "1"},
			"picture: magick: " + copied + ": " + sha(tiffLE) + "-0.png isn't a PNG"},
		{"a page left out", map[string]string{pagesEnv: "3", gapEnv: "1"},
			"picture: magick: " + copied + ": wrote 2 pages without page 2"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, _ := standinConverter(t, false, true)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			path := writePicture(t, tiffLE, "", "")
			dir := t.TempDir()
			pngs, err := c.ConvertPages(t.Context(), picture.Request{Dir: dir, Path: path})
			if pngs != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, %v; want an error saying %q", pngs, err, tc.want)
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Errorf("working directory holds %q, want nothing", got)
			}
			unchanged(t, path, tiffLE)
		})
	}
	t.Run("no magick", func(t *testing.T) {
		c, runs := standinConverter(t, false, false)
		dir := t.TempDir()
		pngs, err := c.ConvertPages(t.Context(), picture.Request{Dir: dir, Path: writePicture(t, tiffLE, "", "")})
		if pngs != nil || !errors.Is(err, picture.ErrNoMagick) {
			t.Fatalf("got %q, %v; want ErrNoMagick", pngs, err)
		}
		if got := runs(); len(got) != 0 || len(entries(t, dir)) != 0 {
			t.Errorf("runs %q, working directory %q; want neither", programs(got), entries(t, dir))
		}
	})
}

// TestConvertRaster: a HEIC, WebP, or AVIF, told by its signature, is copied under its SHA-256
// and its kind's extension into a directory of its own and converted through one magick run in
// the working directory: its first image, read through its kind's coder, turned upright by its
// stored orientation, and written as PNG without the run's time, named by its SHA-256 and .png.
// ConvertPages gives the same one PNG.
func TestConvertRaster(t *testing.T) {
	for _, tc := range []struct {
		content, coder, ext string
	}{
		{heicData, "heic", ".heic"},
		{webpData, "webp", ".webp"},
		{avifData, "avif", ".avif"},
	} {
		t.Run(tc.coder, func(t *testing.T) {
			c, runs := standinConverter(t, false, true)
			path := writePicture(t, tc.content, "photo.bin", "")
			for _, pages := range []bool{false, true} {
				dir := t.TempDir()
				var pngs []string
				if pages {
					var err error
					if pngs, err = c.ConvertPages(t.Context(), picture.Request{Dir: dir, Path: path}); err != nil {
						t.Fatal(err)
					}
				} else {
					png, err := c.Convert(t.Context(), picture.Request{Dir: dir, Path: path})
					if err != nil {
						t.Fatal(err)
					}
					pngs = []string{png}
				}
				stem := sha(tc.content)
				if want := []string{filepath.Join(dir, stem+".png")}; !slices.Equal(pngs, want) {
					t.Fatalf("PNGs: got %q, want %q", pngs, want)
				}
				if b, err := os.ReadFile(pngs[0]); err != nil || string(b) != pngSignature+convertedPrefix+tc.content {
					t.Errorf("PNG holds %q, %v", b, err)
				}
				if got := entries(t, dir); !slices.Equal(got, []string{stem + ".png"}) {
					t.Errorf("working directory holds %q, want the PNG alone", got)
				}
				all := runs()
				last := all[len(all)-1]
				want := []string{tc.coder + `:` + own(dir) + stem + regexp.QuoteMeta(tc.ext) + `\[0\]`, `-auto-orient`, `-define`, `png:exclude-chunks=date,time`, `png:` + own(dir) + stem + `\.png`}
				if !matchArgs(last.Args, want) {
					t.Errorf("magick's arguments: got %q, want %q", last.Args, want)
				}
				if last.Dir != dir || last.Input != tc.content {
					t.Errorf("magick ran in %s on %q; want in %s on the picture", last.Dir, last.Input, dir)
				}
			}
			if got := programs(runs()); !slices.Equal(got, []string{"magick", "magick"}) {
				t.Errorf("runs: got %q, want one magick run each", got)
			}
			unchanged(t, path, tc.content)
		})
	}
}

// TestConvertNoReader: a HEIC, WebP, or AVIF whose format magick's listing doesn't mark readable,
// whether listed write-only or left out, is refused with ErrNoReader before anything runs, by
// Convert and ConvertBatch alike, and the readable kinds still convert. The listing is read once,
// when the Converter is built.
func TestConvertNoReader(t *testing.T) {
	t.Setenv(formatsEnv, "   Format  Module    Mode  Description\n"+
		"     HEIC  HEIC      -w+   High Efficiency Image Format (1.23.6)\n"+
		"     WEBP* WEBP      rw+   WebP Image Format (libwebp 1.6.0 [0210])\n")
	c, runs := standinConverter(t, false, true)
	// The Converter has read its listing; a listing changed since isn't read again.
	t.Setenv(formatsEnv, fullListing)
	for _, tc := range []struct{ content, want string }{
		{heicData, "is a HEIC picture: no magick reader"},
		{avifData, "is an AVIF picture: no magick reader"},
	} {
		dir := t.TempDir()
		path := writePicture(t, tc.content, "", "")
		png, err := c.Convert(t.Context(), picture.Request{Dir: dir, Path: path})
		if png != "" || !errors.Is(err, picture.ErrNoReader) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Convert: got %q, %v; want ErrNoReader saying %q", png, err, tc.want)
		}
		err = c.ConvertBatch(t.Context(), picture.Batch{Dir: dir, Pictures: []picture.Picture{
			{Path: path, Kind: route.AVIF, PNG: filepath.Join(dir, "a.png")},
		}})
		if !errors.Is(err, picture.ErrNoReader) {
			t.Errorf("ConvertBatch: got %v, want ErrNoReader", err)
		}
		if got := entries(t, dir); len(got) != 0 {
			t.Errorf("working directory holds %q, want nothing", got)
		}
	}
	if got := runs(); len(got) != 0 {
		t.Errorf("runs: got %q, want none", programs(got))
	}
	if _, err := c.Convert(t.Context(), picture.Request{Dir: t.TempDir(), Path: writePicture(t, webpData, "", "")}); err != nil {
		t.Errorf("a WebP: %v", err)
	}
}

// TestNewConverterListFails: a format listing that fails is NewConverter's error, with magick's
// message; without magick nothing is listed.
func TestNewConverterListFails(t *testing.T) {
	t.Setenv(listFailEnv, "magick: unable to open module file\n")
	mg, _ := standinMagick(t, "7.1.2-34", picture.MagickSpec())
	c, err := picture.NewConverter(t.Context(), picture.Config{Magick: mg})
	if c != nil || err == nil || !strings.Contains(err.Error(), "picture: magick's format listing: tool: magick: exit code 1: magick: unable to open module file") {
		t.Errorf("got %v, %v; want the listing's error", c, err)
	}
	if _, err := picture.NewConverter(t.Context(), picture.Config{}); err != nil {
		t.Errorf("no magick: %v", err)
	}
}
