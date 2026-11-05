//go:build linux

package picture_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// Constructed pictures, each its kind's signature followed by filler.
var (
	tiffLE  = "II*\x00\x08\x00\x00\x00filler"
	tiffBE  = "MM\x00*\x00\x00\x00\x08filler"
	wmfKey  = "\xd7\xcd\xc6\x9a\x00\x00filler"
	svgData = `<?xml version="1.0"?>` + "\n" + `<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4"/>`
	pngData = pngSignature + "\x00\x00\x00\rIHDR"
)

// emfData returns an EMF's first bytes: an EMR_HEADER record, type 1, with " EMF" at offset 40.
func emfData() string {
	b := make([]byte, 88)
	binary.LittleEndian.PutUint32(b, 1)
	binary.LittleEndian.PutUint32(b[4:], 108)
	copy(b[40:], " EMF")
	return string(b)
}

// wmfStandard returns a standard WMF META_HEADER's first bytes: type 1, 9 words, version 0x0300.
func wmfStandard() string {
	b := make([]byte, 18)
	binary.LittleEndian.PutUint16(b, 1)
	binary.LittleEndian.PutUint16(b[2:], 9)
	binary.LittleEndian.PutUint16(b[4:], 0x0300)
	return string(b)
}

// sha returns the lowercase hex SHA-256 of content.
func sha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// writePicture writes content into a directory of its own, outside any working directory, as
// name, and returns its path; a name of "" is the content's SHA-256 and ext, as mail names it.
func writePicture(t *testing.T, content, name, ext string) string {
	t.Helper()
	if name == "" {
		name = sha(content) + ext
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// unchanged fails the test unless the file at path still holds content.
func unchanged(t *testing.T, path, content string) {
	t.Helper()
	if b, err := os.ReadFile(path); err != nil || string(b) != content {
		t.Errorf("the original holds %q, %v; want it kept as %q", b, err, content)
	}
}

// programs returns the programs of runs, in order.
func programs(runs []standinRun) []string {
	var out []string
	for _, r := range runs {
		out = append(out, r.Program)
	}
	return out
}

// matchArgs reports whether args match want, one regular expression per argument, each matched
// whole.
func matchArgs(args, want []string) bool {
	if len(args) != len(want) {
		return false
	}
	for i, p := range want {
		if !regexp.MustCompile(`^` + p + `$`).MatchString(args[i]) {
			return false
		}
	}
	return true
}

// drawFilter returns soffice's --convert-to for a page of width by height pixels.
func drawFilter(width, height int) string {
	return fmt.Sprintf(`png:draw_png_Export:{"PixelWidth":{"type":"long","value":"%d"},"PixelHeight":{"type":"long","value":"%d"}}`, width, height)
}

// own matches a picture's directory of its own in the working directory dir.
func own(dir string) string { return regexp.QuoteMeta(dir) + `/picture-[^/]+/[0-9]+/` }

// TestConvertTIFF: a TIFF, told by either byte order's signature whatever its name, is copied
// under its SHA-256 and .tiff into a directory of its own in the working directory and converted
// through one magick run in the working directory, its first image read as TIFF and written as
// PNG without the run's time. The PNG is the working directory's file named by the picture's
// SHA-256 and .png, computed when the name isn't it; the original is kept, and the copy's
// directory removed.
func TestConvertTIFF(t *testing.T) {
	for _, tc := range []struct {
		desc, content, name, ext string
	}{
		{"named by its SHA-256, .tif", tiffLE, "", ".tif"},
		{"named by its SHA-256, no extension", tiffBE, "", ""},
		{"named otherwise", tiffLE, "scan.dat", ""},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := standinConverter(t, false, true)
			path := writePicture(t, tc.content, tc.name, tc.ext)
			dir := t.TempDir()
			png, err := c.Convert(t.Context(), picture.Request{Dir: dir, Path: path})
			if err != nil {
				t.Fatal(err)
			}
			stem := sha(tc.content)
			if want := filepath.Join(dir, stem+".png"); png != want {
				t.Errorf("PNG: got %s, want %s", png, want)
			}
			if b, err := os.ReadFile(png); err != nil || string(b) != pngSignature+convertedPrefix+tc.content {
				t.Errorf("PNG holds %q, %v", b, err)
			}
			unchanged(t, path, tc.content)
			if got := entries(t, dir); !slices.Equal(got, []string{stem + ".png"}) {
				t.Errorf("working directory holds %q, want the PNG alone", got)
			}
			all := runs()
			if got := programs(all); !slices.Equal(got, []string{"magick"}) {
				t.Fatalf("runs: got %q, want magick alone", got)
			}
			want := []string{`tiff:` + own(dir) + stem + `\.tiff\[0\]`, `-define`, `png:exclude-chunks=date,time`, `png:` + own(dir) + stem + `\.png`}
			if !matchArgs(all[0].Args, want) {
				t.Errorf("magick's arguments: got %q, want %q", all[0].Args, want)
			}
			if all[0].Dir != dir || all[0].Input != tc.content {
				t.Errorf("magick ran in %s on %q; want in %s on the picture", all[0].Dir, all[0].Input, dir)
			}
		})
	}
}

// TestConvertSVG: an SVG, told by its root element whatever its name, is copied under its
// SHA-256 and .svg into a directory of its own, holding nothing else, and rendered through one
// magick run, at 150 dpi through the rsvg coder, flattened onto white, and written as PNG
// without the run's time. The PNG is named by the SVG's SHA-256 and .png.
func TestConvertSVG(t *testing.T) {
	for _, tc := range []struct{ desc, name string }{
		{"named by its SHA-256, .svg", sha(svgData) + ".svg"},
		{"named .xml", "diagram.xml"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := standinConverter(t, false, true)
			path := writePicture(t, svgData, tc.name, "")
			// A file beside the original, which an SVG's relative reference would name, isn't
			// copied with it.
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), "beside.png"), []byte(pngData), 0o600); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			png, err := c.Convert(t.Context(), picture.Request{Dir: dir, Path: path})
			if err != nil {
				t.Fatal(err)
			}
			stem := sha(svgData)
			if want := filepath.Join(dir, stem+".png"); png != want {
				t.Errorf("PNG: got %s, want %s", png, want)
			}
			if b, err := os.ReadFile(png); err != nil || string(b) != pngSignature+convertedPrefix+svgData {
				t.Errorf("PNG holds %q, %v", b, err)
			}
			unchanged(t, path, svgData)
			all := runs()
			if got := programs(all); !slices.Equal(got, []string{"magick"}) {
				t.Fatalf("runs: got %q, want magick alone", got)
			}
			want := []string{`-density`, `150`, `-background`, `white`, `rsvg:` + own(dir) + stem + `\.svg`,
				`-alpha`, `remove`, `-alpha`, `off`, `-define`, `png:exclude-chunks=date,time`, `png:` + own(dir) + stem + `\.png`}
			if !matchArgs(all[0].Args, want) {
				t.Errorf("magick's arguments: got %q, want %q", all[0].Args, want)
			}
			if all[0].Dir != dir || all[0].Input != svgData {
				t.Errorf("magick ran in %s on %q; want in %s on the SVG", all[0].Dir, all[0].Input, dir)
			}
		})
	}
}

// TestConvertMetafile: an EMF or WMF, told by its signature, a WMF's placeable key or standard
// header, whatever its name, is rendered through one soffice run in the working directory, read
// under its SHA-256 and its kind's extension in a directory of its own, with the profile's
// British English locale and the Draw PNG export at the request's scale, and its page trimmed
// through magick. The PNG is the working directory's file named by the picture's SHA-256 and
// .png, and the original is kept.
func TestConvertMetafile(t *testing.T) {
	for _, tc := range []struct {
		desc, content, name, ext, kind string
		scale, width, height           int
	}{
		{"EMF at the default scale", emfData(), "", ".emf", ".emf", 0, 2382, 3369},
		{"placeable WMF at scale 1", wmfKey, "", ".wmf", ".wmf", 1, 794, 1123},
		{"standard WMF, named otherwise", wmfStandard(), "logo", "", ".wmf", 5, 3970, 5615},
		{"EMF at the largest scale", emfData(), "", "", ".emf", picture.MaxMetafileScale, 6352, 8984},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := standinConverter(t, true, true)
			path := writePicture(t, tc.content, tc.name, tc.ext)
			dir := t.TempDir()
			png, err := c.Convert(t.Context(), picture.Request{Dir: dir, Path: path, MetafileScale: tc.scale})
			if err != nil {
				t.Fatal(err)
			}
			stem := sha(tc.content)
			if want := filepath.Join(dir, stem+".png"); png != want {
				t.Errorf("PNG: got %s, want %s", png, want)
			}
			if b, err := os.ReadFile(png); err != nil || string(b) != pngSignature+convertedPrefix+convertedPrefix+tc.content {
				t.Errorf("PNG holds %q, %v", b, err)
			}
			unchanged(t, path, tc.content)
			if got := entries(t, dir); !slices.Equal(got, []string{stem + ".png"}) {
				t.Errorf("working directory holds %q, want the PNG alone", got)
			}
			all := runs()
			if got := programs(all); !slices.Equal(got, []string{"soffice", "magick"}) {
				t.Fatalf("runs: got %q, want soffice and magick", got)
			}
			lo := regexp.QuoteMeta(dir) + `/libreoffice-[^/]+`
			wantArgs := []string{
				`-env:UserInstallation=file://` + lo + `/profile`,
				`--headless`, `--norestore`,
				`--convert-to`, regexp.QuoteMeta(drawFilter(tc.width, tc.height)),
				`--outdir`, lo + `/out`,
				own(dir) + stem + regexp.QuoteMeta(tc.kind),
			}
			if !matchArgs(all[0].Args, wantArgs) {
				t.Errorf("soffice's arguments: got %q, want %q", all[0].Args, wantArgs)
			}
			locale := `<item oor:path="/org.openoffice.Setup/L10N"><prop oor:name="ooSetupSystemLocale" oor:op="fuse"><value>en-GB</value></prop></item>`
			if all[0].Dir != dir || !strings.Contains(all[0].Registry, locale) {
				t.Errorf("soffice ran in %s with profile %q; want in %s with the en-GB locale", all[0].Dir, all[0].Registry, dir)
			}
			trim := []string{`png:` + lo + `/out/` + stem + `\.png`, `-trim`, `\+repage`, `-define`, `png:exclude-chunks=date,time`,
				`png:` + own(dir) + stem + `\.png`}
			if !matchArgs(all[1].Args, trim) {
				t.Errorf("magick's trim arguments: got %q, want %q", all[1].Args, trim)
			}
		})
	}
}

// TestConvertRefused: content of no converted kind, a picture whose converter the Converter
// wasn't given, a scale out of range, no working directory, and a missing file are each an error
// before anything runs, leaving the working directory empty; a missing converter's matches
// ErrNoLibreOffice or ErrNoMagick, and a scale's ErrScale.
func TestConvertRefused(t *testing.T) {
	for _, tc := range []struct {
		desc                string
		libreoffice, magick bool
		content             string
		req                 func(dir, path string) picture.Request
		is                  error
		want                string
	}{
		{"a PNG", true, true, pngData, nil, nil, "isn't a TIFF, EMF, WMF, or SVG picture"},
		{"empty content", true, true, "", nil, nil, "isn't a TIFF, EMF, WMF, or SVG picture"},
		{"html with an inline svg", true, true, "<html><svg/></html>", nil, nil, "isn't a TIFF, EMF, WMF, or SVG picture"},
		{"a TIFF without magick", true, false, tiffLE, nil, picture.ErrNoMagick, "is a TIFF picture: no magick converter"},
		{"an SVG without magick", true, false, svgData, nil, picture.ErrNoMagick, "is an SVG picture: no magick converter"},
		{"an EMF without libreoffice", false, true, emfData(), nil, picture.ErrNoLibreOffice, "is an EMF picture: no LibreOffice converter"},
		{"a WMF without magick", true, false, wmfKey, nil, picture.ErrNoMagick, "is a WMF picture: no magick converter"},
		{"a scale above 8", true, true, emfData(),
			func(dir, path string) picture.Request {
				return picture.Request{Dir: dir, Path: path, MetafileScale: 9}
			}, picture.ErrScale, "picture: metafile scale 9 isn't from 1 to 8"},
		{"a scale below 0", true, true, tiffLE,
			func(dir, path string) picture.Request {
				return picture.Request{Dir: dir, Path: path, MetafileScale: -1}
			}, picture.ErrScale, "picture: metafile scale -1 isn't from 1 to 8"},
		{"no working directory", true, true, tiffLE,
			func(_, path string) picture.Request { return picture.Request{Path: path} },
			nil, "picture: no working directory"},
		{"a missing file", true, true, tiffLE,
			func(dir, path string) picture.Request {
				return picture.Request{Dir: dir, Path: path + ".missing"}
			}, os.ErrNotExist, "picture: open "},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := standinConverter(t, tc.libreoffice, tc.magick)
			path := writePicture(t, tc.content, "", "")
			dir := t.TempDir()
			req := picture.Request{Dir: dir, Path: path}
			if tc.req != nil {
				req = tc.req(dir, path)
			}
			png, err := c.Convert(t.Context(), req)
			if png != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, %v; want an error saying %q", png, err, tc.want)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("error %v doesn't match %v", err, tc.is)
			}
			if got := runs(); len(got) != 0 {
				t.Errorf("runs: got %q, want none", programs(got))
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Errorf("working directory holds %q, want nothing", got)
			}
			unchanged(t, path, tc.content)
		})
	}
}

// TestConvertFails: soffice exiting nonzero or writing no page, magick exiting nonzero, and
// magick writing something that isn't a PNG each fail the conversion with the converter's
// message or the picture's copied name, its SHA-256 and kind, and leave nothing in the working
// directory and the original as it was.
func TestConvertFails(t *testing.T) {
	emf := emfData()
	for _, tc := range []struct {
		desc    string
		content string
		env     map[string]string
		want    string
	}{
		{"soffice exits 1", emf,
			map[string]string{sofficeFailEnv: "Error: source file could not be loaded\n"},
			"picture: libreoffice: tool: soffice: exit code 1: Error: source file could not be loaded"},
		{"soffice writes nothing", emf,
			map[string]string{sofficeNothingEnv: "1"},
			"picture: libreoffice: " + sha(emf) + ".emf: no PNG written"},
		{"the trim exits 1", emf,
			map[string]string{failEnv: "magick: insufficient image data\n"},
			"picture: magick: " + sha(emf) + ".emf: tool: magick: exit code 1: magick: insufficient image data"},
		{"magick exits 1 on a TIFF", tiffLE,
			map[string]string{failEnv: "magick: improper image header\n"},
			"picture: magick: " + sha(tiffLE) + ".tiff: tool: magick: exit code 1: magick: improper image header"},
		{"magick exits 1 on an SVG", svgData,
			map[string]string{failEnv: "magick: unable to read image data\n"},
			"picture: magick: " + sha(svgData) + ".svg: tool: magick: exit code 1: magick: unable to read image data"},
		{"magick writes no PNG", tiffLE,
			map[string]string{rawEnv: "1"},
			"picture: magick: " + sha(tiffLE) + ".tiff: " + sha(tiffLE) + ".png isn't a PNG"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, _ := standinConverter(t, true, true)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			path := writePicture(t, tc.content, "", "")
			dir := t.TempDir()
			png, err := c.Convert(t.Context(), picture.Request{Dir: dir, Path: path})
			if png != "" || err == nil {
				t.Fatalf("got %q, %v; want an error", png, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q doesn't say %q", err, tc.want)
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Errorf("working directory holds %q, want nothing", got)
			}
			unchanged(t, path, tc.content)
		})
	}
}

// TestConvertBatch: a batch's TIFFs and SVGs go to magick in order, one run each, and its EMFs
// and WMFs after them to one soffice run, in order, then each page to magick; every picture is
// read as its Picture's kind, from a copy in a directory of its own, numbered by its place in the
// batch, under its PNG's stem; each PNG is written to its Picture's PNG, anywhere the caller
// names, and only the PNGs are left.
func TestConvertBatch(t *testing.T) {
	c, runs := standinConverter(t, true, true)
	dir := t.TempDir()
	out := t.TempDir()
	src := t.TempDir()
	pictures := []struct {
		name, content string
		kind          route.Kind
	}{
		{"a1.emf", emfData(), route.EMF},
		{"b2.tif", tiffLE, route.TIFF},
		{"c3.WMF", wmfKey, route.WMF},
		{"d4.svg", svgData, route.SVG},
		// Read as its Picture's kind, whatever its content.
		{"e5.tiff", "not a tiff", route.TIFF},
	}
	var batch []picture.Picture
	for _, p := range pictures {
		path := filepath.Join(src, p.name)
		if err := os.WriteFile(path, []byte(p.content), 0o600); err != nil {
			t.Fatal(err)
		}
		stem := strings.TrimSuffix(p.name, filepath.Ext(p.name))
		batch = append(batch, picture.Picture{Path: path, Kind: p.kind, PNG: filepath.Join(out, stem+".png")})
	}
	if err := c.ConvertBatch(t.Context(), picture.Batch{Dir: dir, MetafileScale: 1, Pictures: batch}); err != nil {
		t.Fatal(err)
	}
	for i, p := range pictures {
		want := pngSignature + convertedPrefix + p.content
		if p.kind == route.EMF || p.kind == route.WMF {
			want = pngSignature + convertedPrefix + convertedPrefix + p.content
		}
		if b, err := os.ReadFile(batch[i].PNG); err != nil || string(b) != want {
			t.Errorf("%s: PNG holds %q, %v; want %q", p.name, b, err, want)
		}
		unchanged(t, batch[i].Path, p.content)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("working directory holds %q, want nothing", got)
	}
	if got, want := entries(t, out), []string{"a1.png", "b2.png", "c3.png", "d4.png", "e5.png"}; !slices.Equal(got, want) {
		t.Errorf("PNG directory holds %q, want %q", got, want)
	}
	all := runs()
	if got, want := programs(all), []string{"magick", "magick", "magick", "soffice", "magick", "magick"}; !slices.Equal(got, want) {
		t.Fatalf("runs: got %q, want %q", got, want)
	}
	at := func(i int, name string) string {
		return regexp.QuoteMeta(dir) + `/picture-[^/]+/` + fmt.Sprint(i) + `/` + regexp.QuoteMeta(name)
	}
	if want := []string{`tiff:` + at(1, "b2.tiff") + `\[0\]`, `-define`, `png:exclude-chunks=date,time`, `png:` + at(1, "b2.png")}; !matchArgs(all[0].Args, want) {
		t.Errorf("b2's arguments: got %q, want %q", all[0].Args, want)
	}
	if !matchArgs(all[1].Args[4:5], []string{`rsvg:` + at(3, "d4.svg")}) {
		t.Errorf("d4's arguments: got %q, want its copy read through rsvg", all[1].Args)
	}
	if want := []string{`tiff:` + at(4, "e5.tiff") + `\[0\]`, `-define`, `png:exclude-chunks=date,time`, `png:` + at(4, "e5.png")}; !matchArgs(all[2].Args, want) {
		t.Errorf("e5's arguments: got %q, want %q", all[2].Args, want)
	}
	if got := all[3].Args[len(all[3].Args)-2:]; !matchArgs(got, []string{at(0, "a1.emf"), at(2, "c3.wmf")}) {
		t.Errorf("soffice's inputs: got %q, want a1's and c3's copies", got)
	}
	for i, name := range []string{"a1", "c3"} {
		lo := regexp.QuoteMeta(dir) + `/libreoffice-[^/]+/out/`
		want := []string{`png:` + lo + name + `\.png`, `-trim`, `\+repage`, `-define`, `png:exclude-chunks=date,time`, `png:` + at(2*i, name+".png")}
		if !matchArgs(all[4+i].Args, want) {
			t.Errorf("%s's trim: got %q, want %q", name, all[4+i].Args, want)
		}
	}
}

// TestConvertBatchRefused: a kind the package doesn't convert, two pictures that share a PNG's
// stem, and a picture whose converter is missing are each an error before anything runs; an
// empty batch runs nothing and succeeds.
func TestConvertBatchRefused(t *testing.T) {
	src := t.TempDir()
	path := filepath.Join(src, "a.tiff")
	if err := os.WriteFile(path, []byte(tiffLE), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		desc     string
		magick   bool
		pictures []picture.Picture
		is       error
		want     string
	}{
		{"a PNG", true, []picture.Picture{{Path: path, Kind: route.PNG, PNG: filepath.Join(src, "a.png")}},
			nil, `picture: a.tiff: no converter for kind "png"`},
		{"two PNGs of one stem", true, []picture.Picture{
			{Path: path, Kind: route.TIFF, PNG: filepath.Join(src, "a.png")},
			{Path: path, Kind: route.TIFF, PNG: filepath.Join(t.TempDir(), "a.png")},
		}, nil, "picture: two pictures' PNGs are named a.png"},
		{"an EMF without libreoffice", true, []picture.Picture{{Path: path, Kind: route.EMF, PNG: filepath.Join(src, "a.png")}},
			picture.ErrNoLibreOffice, "picture: a.tiff is an EMF picture: no LibreOffice converter"},
		{"a TIFF without magick", false, []picture.Picture{{Path: path, Kind: route.TIFF, PNG: filepath.Join(src, "a.png")}},
			picture.ErrNoMagick, "picture: a.tiff is a TIFF picture: no magick converter"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := standinConverter(t, false, tc.magick)
			dir := t.TempDir()
			err := c.ConvertBatch(t.Context(), picture.Batch{Dir: dir, Pictures: tc.pictures})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want an error saying %q", err, tc.want)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("error %v doesn't match %v", err, tc.is)
			}
			if got := runs(); len(got) != 0 {
				t.Errorf("runs: got %q, want none", programs(got))
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Errorf("working directory holds %q, want nothing", got)
			}
		})
	}
	t.Run("empty", func(t *testing.T) {
		c, runs := standinConverter(t, false, false)
		dir := t.TempDir()
		if err := c.ConvertBatch(t.Context(), picture.Batch{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		if got := runs(); len(got) != 0 {
			t.Errorf("runs: got %q, want none", programs(got))
		}
		if got := entries(t, dir); len(got) != 0 {
			t.Errorf("working directory holds %q, want nothing", got)
		}
	})
}

// TestConvertBatchFailsRemovesPNGs: a batch whose metafile fails after its TIFF's PNG was
// written removes that PNG too, so a failed batch leaves no PNG of its own.
func TestConvertBatchFailsRemovesPNGs(t *testing.T) {
	c, _ := standinConverter(t, true, true)
	t.Setenv(sofficeNothingEnv, "1")
	src, out := t.TempDir(), t.TempDir()
	tiff, emf := filepath.Join(src, "a.tiff"), filepath.Join(src, "b.emf")
	for path, content := range map[string]string{tiff: tiffLE, emf: emfData()} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := c.ConvertBatch(t.Context(), picture.Batch{Dir: t.TempDir(), Pictures: []picture.Picture{
		{Path: tiff, Kind: route.TIFF, PNG: filepath.Join(out, "a.png")},
		{Path: emf, Kind: route.EMF, PNG: filepath.Join(out, "b.png")},
	}})
	if err == nil || !strings.Contains(err.Error(), "b.emf: no PNG written") {
		t.Fatalf("got %v, want soffice's missing page", err)
	}
	if got := entries(t, out); len(got) != 0 {
		t.Errorf("PNG directory holds %q, want nothing", got)
	}
}

// TestCheckMetafileScale: zero is the default, 3; 1 to 8 are themselves; below zero and above 8
// are refused with ErrScale.
func TestCheckMetafileScale(t *testing.T) {
	for in, want := range map[int]int{0: picture.DefaultMetafileScale, 1: 1, 5: 5, 8: picture.MaxMetafileScale} {
		if got, err := picture.CheckMetafileScale(in); err != nil || got != want {
			t.Errorf("CheckMetafileScale(%d) = %d, %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []int{-1, 9} {
		if _, err := picture.CheckMetafileScale(in); !errors.Is(err, picture.ErrScale) {
			t.Errorf("CheckMetafileScale(%d) returned %v, want ErrScale", in, err)
		}
	}
}

// TestNewConverterVersion: NewConverter refuses a magick older than 7.1 and a soffice older than
// 24.2, whatever their Specs declared, with a *tool.VersionError wrapping ErrTooOld; with no
// Tools it builds a Converter that refuses every picture.
func TestNewConverterVersion(t *testing.T) {
	installStandin(t, "7.0.11-14")
	lenient := picture.MagickSpec()
	lenient.Minimum = "6"
	mg, err := tool.NewExec(t.Context(), lenient)
	if err != nil {
		t.Fatal(err)
	}
	var verr *tool.VersionError
	if _, err := picture.NewConverter(picture.Config{Magick: mg}); !errors.As(err, &verr) || verr.Minimum != picture.MagickMinimum || !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("NewConverter over magick 7.0.11-14 returned %v, want a VersionError of minimum %s", err, picture.MagickMinimum)
	}
	t.Setenv(sofficeVersionEnv, "7.6.4.1")
	lo := picture.LibreOfficeSpec()
	lo.Minimum = "7"
	so, err := tool.NewExec(t.Context(), lo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := picture.NewConverter(picture.Config{LibreOffice: so}); !errors.As(err, &verr) || verr.Minimum != picture.LibreOfficeMinimum || !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("NewConverter over soffice 7.6.4.1 returned %v, want a VersionError of minimum %s", err, picture.LibreOfficeMinimum)
	}
	c, err := picture.NewConverter(picture.Config{})
	if err != nil {
		t.Fatal(err)
	}
	path := writePicture(t, tiffLE, "", "")
	if _, err := c.Convert(t.Context(), picture.Request{Dir: t.TempDir(), Path: path}); !errors.Is(err, picture.ErrNoMagick) {
		t.Errorf("Convert without magick returned %v, want ErrNoMagick", err)
	}
}

// TestLibreOfficeSpec: LibreOfficeSpec reads soffice's version past the noise its desktop
// libraries write.
func TestLibreOfficeSpec(t *testing.T) {
	installStandin(t, "7.1.2-34")
	so, err := tool.NewExec(t.Context(), picture.LibreOfficeSpec())
	if err != nil || so.Version().String() != "26.8.1.1" {
		t.Errorf("NewExec(LibreOfficeSpec()) = %v, %v; want soffice 26.8.1.1", so, err)
	}
}
