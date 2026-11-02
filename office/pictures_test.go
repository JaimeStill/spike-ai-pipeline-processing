//go:build linux

package office_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// pictureConverter builds a Converter over the stand-in office at 0.1.0 and, when asked, the
// stand-in soffice and the stand-in magick, each built from its Spec through the exec runner, and
// returns it with the stand-ins' run log.
func pictureConverter(t *testing.T, libreoffice, magick bool) (*office.Converter, func() []standinRun) {
	t.Helper()
	runs := installStandin(t, "0.1.0")
	var cfg office.Config
	var err error
	if cfg.Office, err = tool.NewExec(t.Context(), office.OfficeSpec()); err != nil {
		t.Fatal(err)
	}
	if libreoffice {
		if cfg.LibreOffice, err = tool.NewExec(t.Context(), office.LibreOfficeSpec()); err != nil {
			t.Fatal(err)
		}
	}
	if magick {
		if cfg.Magick, err = tool.NewExec(t.Context(), office.MagickSpec()); err != nil {
			t.Fatal(err)
		}
	}
	c, err := office.NewConverter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c, runs
}

// programs returns the programs of runs, in order.
func programs(runs []standinRun) []string {
	var out []string
	for _, r := range runs {
		out = append(out, r.Program)
	}
	return out
}

// TestConvertPictures: with both converters, every EMF and WMF goes to soffice in one run, in name
// order, with its profile and its output in a directory of its own in the working directory, and
// each TIFF to magick, one run each, reading its first image as TIFF and writing PNG. Each
// converted picture's PNG is its stored name's stem and .png, beside the stored file, which
// stays, and is its entry's File, Converted; PNG, JPEG, GIF, and BMP, and a kind the standard
// names no converter for, are handed on as stored. Only the media directory is left.
func TestConvertPictures(t *testing.T) {
	c, runs := pictureConverter(t, true, true)
	t.Setenv(mediaEnv, "a1.emf,b2.WMF,c3.tiff,d4.png,e5.jpeg,f6.gif,g7.bmp,h8.TIF,i9.svg,j0.emf")
	dir := t.TempDir()
	res, err := c.Convert(t.Context(), strings.NewReader("doc"), office.Request{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	media := res.MediaDir
	at := func(name string) string { return filepath.Join(media, name) }
	want := []office.MediaFile{
		{Name: "a1.emf", ContentType: "image/x-emf", File: at("a1.png"), Converted: true},
		{Name: "b2.WMF", ContentType: "image/x-wmf", File: at("b2.png"), Converted: true},
		{Name: "c3.tiff", ContentType: "image/tiff", File: at("c3.png"), Converted: true},
		{Name: "d4.png", ContentType: "image/png", File: at("d4.png")},
		{Name: "e5.jpeg", ContentType: "image/jpeg", File: at("e5.jpeg")},
		{Name: "f6.gif", ContentType: "image/gif", File: at("f6.gif")},
		{Name: "g7.bmp", ContentType: "image/bmp", File: at("g7.bmp")},
		{Name: "h8.TIF", ContentType: "image/tiff", File: at("h8.png"), Converted: true},
		{Name: "i9.svg", ContentType: "image/svg+xml", File: at("i9.svg")},
		{Name: "j0.emf", ContentType: "image/x-emf", File: at("j0.png"), Converted: true},
	}
	if !slices.Equal(res.Media, want) {
		t.Errorf("media:\ngot  %+v\nwant %+v", res.Media, want)
	}
	for _, m := range res.Media {
		content := m.Name
		if m.Converted {
			content = pngSignature + convertedPrefix + m.Name
		}
		if b, err := os.ReadFile(m.File); err != nil || string(b) != content {
			t.Errorf("%s: handed on %q, %v; want %q", m.Name, b, err, content)
		}
		if b, err := os.ReadFile(at(m.Name)); err != nil || string(b) != m.Name {
			t.Errorf("%s: the stored file holds %q, %v", m.Name, b, err)
		}
	}
	if got := entries(t, dir); !slices.Equal(got, []string{filepath.Base(media)}) {
		t.Errorf("working directory holds %q, want the media directory alone", got)
	}

	all := runs()
	if got, want := programs(all), []string{"spike-ai-pipeline-office", "magick", "magick", "soffice"}; !slices.Equal(got, want) {
		t.Fatalf("runs: got %q, want %q", got, want)
	}
	for i, tiff := range []string{"c3", "h8"} {
		mg := all[1+i]
		ext := map[string]string{"c3": ".tiff", "h8": ".TIF"}[tiff]
		if want := []string{"tiff:" + at(tiff+ext) + "[0]", "png:" + at(tiff+".png")}; !slices.Equal(mg.Args, want) {
			t.Errorf("magick's arguments: got %q, want %q", mg.Args, want)
		}
		if mg.Dir != dir {
			t.Errorf("magick's working directory: got %s, want %s", mg.Dir, dir)
		}
	}
	so := all[3]
	if so.Dir != dir {
		t.Errorf("soffice's working directory: got %s, want %s", so.Dir, dir)
	}
	work := regexp.QuoteMeta(dir) + `/libreoffice-[^/]+`
	wantArgs := []string{
		`-env:UserInstallation=file://` + work + `/profile`,
		`--headless`, `--norestore`,
		`--convert-to`, regexp.QuoteMeta("png:draw_png_Export"),
		`--outdir`, work + `/out`,
		regexp.QuoteMeta(at("a1.emf")), regexp.QuoteMeta(at("b2.WMF")), regexp.QuoteMeta(at("j0.emf")),
	}
	if !matchArgs(so.Args, wantArgs) {
		t.Errorf("soffice's arguments: got %q, want %q", so.Args, wantArgs)
	}
}

// TestConvertPicturesNoConverter: a picture whose converter the Converter wasn't given is listed
// with no file handed onward, and the conversion succeeds; nothing runs for it, and its links in
// a Markdown result stay as office wrote them. A converter given converts its own kinds.
func TestConvertPicturesNoConverter(t *testing.T) {
	md := "![m](a1.emf) ![w](b2.wmf) ![t](c3.tiff) ![p](d4.png)\n"
	for _, tc := range []struct {
		desc                string
		libreoffice, magick bool
		converted           []string
		programs            []string
		markdown            string
	}{
		{"neither", false, false, nil, []string{"spike-ai-pipeline-office"}, md},
		{"libreoffice", true, false, []string{"a1.emf", "b2.wmf"},
			[]string{"spike-ai-pipeline-office", "soffice"},
			"![m](a1.png) ![w](b2.png) ![t](c3.tiff) ![p](d4.png)\n"},
		{"magick", false, true, []string{"c3.tiff"},
			[]string{"spike-ai-pipeline-office", "magick"},
			"![m](a1.emf) ![w](b2.wmf) ![t](c3.png) ![p](d4.png)\n"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := pictureConverter(t, tc.libreoffice, tc.magick)
			t.Setenv(mediaEnv, "a1.emf,b2.wmf,c3.tiff,d4.png")
			t.Setenv(stdoutEnv, md)
			res, err := c.Convert(t.Context(), strings.NewReader("doc"), office.Request{Dir: t.TempDir(), Format: office.Markdown})
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range res.Media {
				switch {
				case m.Name == "d4.png":
					if m.File != filepath.Join(res.MediaDir, m.Name) || m.Converted {
						t.Errorf("%s: handed on as %s, converted %t; want as stored", m.Name, m.File, m.Converted)
					}
				case slices.Contains(tc.converted, m.Name):
					if !m.Converted || m.File == "" {
						t.Errorf("%s: handed on as %s, converted %t; want converted", m.Name, m.File, m.Converted)
					}
				default:
					if m.File != "" || m.Converted {
						t.Errorf("%s: handed on as %s, converted %t; want no file", m.Name, m.File, m.Converted)
					}
					if _, err := os.Stat(filepath.Join(res.MediaDir, m.Name)); err != nil {
						t.Errorf("%s: the stored file: %v", m.Name, err)
					}
				}
			}
			if got := programs(runs()); !slices.Equal(got, tc.programs) {
				t.Errorf("runs: got %q, want %q", got, tc.programs)
			}
			if res.Markdown != tc.markdown {
				t.Errorf("Markdown:\ngot  %q\nwant %q", res.Markdown, tc.markdown)
			}
		})
	}
}

// TestConvertPicturesFails: soffice exiting nonzero, soffice writing no PNG for one of its
// pictures, magick exiting nonzero, and magick writing something that isn't a PNG each fail the
// conversion, with the converter's own message or the picture's name, and leave nothing in the
// working directory.
func TestConvertPicturesFails(t *testing.T) {
	noise := "(process:22): dconf-CRITICAL **: unable to create directory '/.cache/dconf'\nFontconfig error: No writable cache directories\n"
	for _, tc := range []struct {
		desc string
		env  map[string]string
		want string
	}{
		{"soffice exits 1",
			map[string]string{sofficeExitEnv: "1", sofficeOutputEnv: noise + "Error: Please verify input parameters... (SfxBaseModel::impl_store failed)\n"},
			"office: libreoffice: tool: soffice: exit code 1: Error: Please verify input parameters... (SfxBaseModel::impl_store failed)"},
		{"soffice skips a picture",
			map[string]string{sofficeSkipEnv: "b2.wmf", sofficeOutputEnv: noise},
			"office: libreoffice: b2.wmf: no PNG written: Fontconfig error: No writable cache directories"},
		{"magick exits 1",
			map[string]string{magickExitEnv: "1", magickOutputEnv: "magick: improper image header `c3.tiff' @ error/tiff.c/TIFFErrors/608.\n"},
			"office: magick: c3.tiff: tool: magick: exit code 1: magick: improper image header `c3.tiff' @ error/tiff.c/TIFFErrors/608."},
		{"magick writes no PNG",
			map[string]string{magickRawEnv: "1"},
			"office: magick: c3.tiff: c3.png isn't a PNG"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, _ := pictureConverter(t, true, true)
			t.Setenv(mediaEnv, "a1.emf,b2.wmf,c3.tiff,d4.png")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			dir := t.TempDir()
			res, err := c.Convert(t.Context(), strings.NewReader("doc"), office.Request{Dir: dir, Format: office.Markdown})
			if res != nil || err == nil {
				t.Fatalf("got %+v, %v; want an error", res, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q doesn't say %q", err, tc.want)
			}
			if got := entries(t, dir); len(got) != 0 {
				t.Errorf("working directory holds %q, want nothing", got)
			}
		})
	}
}

// TestConvertRewritesLinks: a Markdown result's every occurrence of a converted picture's stored
// name, a name repeated included, becomes its PNG's, and a picture handed on as stored keeps its
// links. A stored .tif and .tiff of one stem, the same bytes under two extensions, are converted
// once and each swapped whole, the longer name never read as the shorter and a suffix. A JSON
// result's model keeps office's names.
func TestConvertRewritesLinks(t *testing.T) {
	c, runs := pictureConverter(t, true, true)
	t.Setenv(mediaEnv, "a1.emf,c3.tif,c3.tiff,d4.png")
	md := "# Pictures\n\n![a1.emf](a1.emf)\n\nagain ![](a1.emf), and ![](a1.emf)\n\n" +
		"![t](c3.tiff) ![t](c3.tif)\n\n![p](d4.png) ![p](d4.png)\n"
	t.Setenv(stdoutEnv, md)
	res, err := c.Convert(t.Context(), strings.NewReader("doc"), office.Request{Dir: t.TempDir(), Format: office.Markdown})
	if err != nil {
		t.Fatal(err)
	}
	want := "# Pictures\n\n![a1.png](a1.png)\n\nagain ![](a1.png), and ![](a1.png)\n\n" +
		"![t](c3.png) ![t](c3.png)\n\n![p](d4.png) ![p](d4.png)\n"
	if res.Markdown != want {
		t.Errorf("Markdown:\ngot  %q\nwant %q", res.Markdown, want)
	}
	if got := programs(runs()); !slices.Equal(got, []string{"spike-ai-pipeline-office", "magick", "soffice"}) {
		t.Errorf("runs: got %q, want office, one magick, and soffice", got)
	}
	png := filepath.Join(res.MediaDir, "c3.png")
	for _, m := range res.Media {
		if strings.HasPrefix(m.Name, "c3.") && (m.File != png || !m.Converted) {
			t.Errorf("%s: handed on as %s, converted %t; want %s", m.Name, m.File, m.Converted, png)
		}
	}

	t.Setenv(stdoutEnv, `{"schema": "1", "format": "docx", "blocks": [`+
		`{"type": "media", "name": "a1.emf", "contentType": "image/x-emf"}]}`)
	res, err = c.Convert(t.Context(), strings.NewReader("doc"), office.Request{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := res.Document.Blocks[0].(*office.Media); !ok || b.Name != "a1.emf" {
		t.Errorf("JSON model's picture: got %+v, want office's name", res.Document.Blocks[0])
	}
}

// TestNewConverterMagick: MagickSpec reads magick's version line, and refuses an older magick at
// the Tool's construction; the constructor refuses one a laxer Spec built, whatever minimum the
// Tool's own Spec declared.
func TestNewConverterMagick(t *testing.T) {
	installStandin(t, "0.1.0")
	o, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	mg, err := tool.NewExec(t.Context(), office.MagickSpec())
	if err != nil {
		t.Fatal(err)
	}
	if got := mg.Version().String(); got != "7.1.2" {
		t.Errorf("version: got %s, want 7.1.2", got)
	}

	t.Setenv(magickVersionEnv, "6.9.13-17")
	if _, err := tool.NewExec(t.Context(), office.MagickSpec()); !errors.Is(err, tool.ErrTooOld) {
		t.Errorf("NewExec over 6.9.13: got %v, want ErrTooOld", err)
	}
	lax := office.MagickSpec()
	lax.Minimum = "6.0"
	old, err := tool.NewExec(t.Context(), lax)
	if err != nil {
		t.Fatal(err)
	}
	_, err = office.NewConverter(office.Config{Office: o, Magick: old})
	var verr *tool.VersionError
	if !errors.As(err, &verr) || !errors.Is(err, tool.ErrTooOld) {
		t.Fatalf("NewConverter over 6.9.13: got %v, want a VersionError wrapping ErrTooOld", err)
	}
	if verr.Program != "magick" || verr.Found != "6.9.13" || verr.Minimum != office.MagickMinimum {
		t.Errorf("VersionError: %+v", verr)
	}
}
