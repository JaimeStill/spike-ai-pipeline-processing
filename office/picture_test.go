//go:build linux

package office_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
)

// Constructed pictures, each its kind's signature followed by filler.
var (
	tiffLE  = "II*\x00\x08\x00\x00\x00filler"
	tiffBE  = "MM\x00*\x00\x00\x00\x08filler"
	wmfKey  = "\xd7\xcd\xc6\x9a\x00\x00filler"
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

// TestConvertPictureTIFF: a TIFF, told by either byte order's signature whatever its name, is
// copied under its SHA-256 and .tiff into a directory of its own in the working directory and
// converted through one magick run in the working directory, its first image read as TIFF and
// written as PNG without the run's time, as Convert converts a document's TIFF. The PNG is the
// working directory's file named by the picture's SHA-256 and .png, computed when the name isn't
// it; the original is kept, the copy's directory removed, and office never runs.
func TestConvertPictureTIFF(t *testing.T) {
	for _, tc := range []struct {
		desc, content, name, ext string
	}{
		{"named by its SHA-256, .tif", tiffLE, "", ".tif"},
		{"named by its SHA-256, no extension", tiffBE, "", ""},
		{"named otherwise", tiffLE, "scan.dat", ""},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := pictureConverter(t, false, true)
			path := writePicture(t, tc.content, tc.name, tc.ext)
			dir := t.TempDir()
			png, err := c.ConvertPicture(t.Context(), office.PictureRequest{Dir: dir, Path: path})
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
			work := regexp.QuoteMeta(dir) + `/picture-[^/]+/`
			want := []string{`tiff:` + work + stem + `\.tiff\[0\]`, `-define`, `png:exclude-chunks=date,time`, `png:` + work + stem + `\.png`}
			if !matchArgs(all[0].Args, want) {
				t.Errorf("magick's arguments: got %q, want %q", all[0].Args, want)
			}
			if all[0].Dir != dir || all[0].Input != tc.content {
				t.Errorf("magick ran in %s on %q; want in %s on the picture", all[0].Dir, all[0].Input, dir)
			}
		})
	}
}

// TestConvertPictureMetafile: an EMF or WMF, told by its signature, a WMF's placeable key or
// standard header, whatever its name, is rendered through one soffice run in the working
// directory, read under its SHA-256 and its kind's extension, with the profile's British English
// locale and the Draw PNG export at the request's scale, and its page trimmed through magick, as
// Convert renders a document's metafile. The PNG is the working directory's file named by the
// picture's SHA-256 and .png, and the original is kept.
func TestConvertPictureMetafile(t *testing.T) {
	for _, tc := range []struct {
		desc, content, name, ext string
		scale, width, height     int
	}{
		{"EMF at the default scale", emfData(), "", ".emf", 0, 2382, 3369},
		{"placeable WMF at scale 1", wmfKey, "", ".wmf", 1, 794, 1123},
		{"standard WMF, named otherwise", wmfStandard(), "logo", "", 5, 3970, 5615},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := pictureConverter(t, true, true)
			path := writePicture(t, tc.content, tc.name, tc.ext)
			dir := t.TempDir()
			png, err := c.ConvertPicture(t.Context(), office.PictureRequest{Dir: dir, Path: path, MetafileScale: tc.scale})
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
			kind := ".emf"
			if strings.HasPrefix(tc.desc, "placeable") || strings.HasPrefix(tc.desc, "standard") {
				kind = ".wmf"
			}
			lo := regexp.QuoteMeta(dir) + `/libreoffice-[^/]+`
			wantArgs := []string{
				`-env:UserInstallation=file://` + lo + `/profile`,
				`--headless`, `--norestore`,
				`--convert-to`, regexp.QuoteMeta(drawFilter(tc.width, tc.height)),
				`--outdir`, lo + `/out`,
				regexp.QuoteMeta(dir) + `/picture-[^/]+/` + stem + regexp.QuoteMeta(kind),
			}
			if !matchArgs(all[0].Args, wantArgs) {
				t.Errorf("soffice's arguments: got %q, want %q", all[0].Args, wantArgs)
			}
			if all[0].Dir != dir || !strings.Contains(all[0].Registry, "<value>en-GB</value>") {
				t.Errorf("soffice ran in %s with profile %q; want in %s with the en-GB locale", all[0].Dir, all[0].Registry, dir)
			}
			trim := []string{`png:` + lo + `/out/` + stem + `\.png`, `-trim`, `\+repage`, `-define`, `png:exclude-chunks=date,time`,
				`png:` + regexp.QuoteMeta(dir) + `/picture-[^/]+/` + stem + `\.png`}
			if !matchArgs(all[1].Args, trim) {
				t.Errorf("magick's trim arguments: got %q, want %q", all[1].Args, trim)
			}
		})
	}
}

// TestConvertPictureRefused: content of no converted kind, a picture whose converter the
// Converter wasn't given, a scale out of range, no working directory, and a missing file are
// each an error before anything runs, leaving the working directory empty; a missing converter's
// matches ErrNoLibreOffice or ErrNoMagick.
func TestConvertPictureRefused(t *testing.T) {
	for _, tc := range []struct {
		desc                string
		libreoffice, magick bool
		content             string
		req                 func(dir, path string) office.PictureRequest
		is                  error
		want                string
	}{
		{"a PNG", true, true, pngData, nil, nil, "isn't a TIFF, EMF, or WMF picture"},
		{"empty content", true, true, "", nil, nil, "isn't a TIFF, EMF, or WMF picture"},
		{"a TIFF without magick", true, false, tiffLE, nil, office.ErrNoMagick, "is a TIFF picture: no magick converter"},
		{"an EMF without libreoffice", false, true, emfData(), nil, office.ErrNoLibreOffice, "is an EMF picture: no LibreOffice converter"},
		{"a WMF without magick", true, false, wmfKey, nil, office.ErrNoMagick, "is a WMF picture: no magick converter"},
		{"a scale above 8", true, true, emfData(),
			func(dir, path string) office.PictureRequest {
				return office.PictureRequest{Dir: dir, Path: path, MetafileScale: 9}
			}, nil, "office: metafile scale 9 isn't from 1 to 8"},
		{"no working directory", true, true, tiffLE,
			func(_, path string) office.PictureRequest { return office.PictureRequest{Path: path} },
			nil, "office: no working directory"},
		{"a missing file", true, true, tiffLE,
			func(dir, path string) office.PictureRequest {
				return office.PictureRequest{Dir: dir, Path: path + ".missing"}
			}, os.ErrNotExist, "office: picture: open "},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, runs := pictureConverter(t, tc.libreoffice, tc.magick)
			path := writePicture(t, tc.content, "", "")
			dir := t.TempDir()
			req := office.PictureRequest{Dir: dir, Path: path}
			if tc.req != nil {
				req = tc.req(dir, path)
			}
			png, err := c.ConvertPicture(t.Context(), req)
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

// TestConvertPictureFails: soffice exiting nonzero or writing no PNG, magick exiting nonzero, and
// magick writing something that isn't a PNG each fail the conversion with the converter's
// message or the picture's copied name, its SHA-256 and kind, and leave nothing in the working
// directory and the original as it was.
func TestConvertPictureFails(t *testing.T) {
	emf := emfData()
	for _, tc := range []struct {
		desc    string
		content string
		env     map[string]string
		want    string
	}{
		{"soffice exits 1", emf,
			map[string]string{sofficeExitEnv: "1", sofficeOutputEnv: "Error: source file could not be loaded\n"},
			"office: libreoffice: tool: soffice: exit code 1: Error: source file could not be loaded"},
		{"soffice writes nothing", emf,
			map[string]string{sofficeNothingEnv: "1"},
			"office: libreoffice: " + sha(emf) + ".emf: no PNG written"},
		{"the trim exits 1", emf,
			map[string]string{magickExitEnv: "1", magickOutputEnv: "magick: insufficient image data\n"},
			"office: magick: " + sha(emf) + ".emf: tool: magick: exit code 1: magick: insufficient image data"},
		{"magick exits 1 on a TIFF", tiffLE,
			map[string]string{magickExitEnv: "1", magickOutputEnv: "magick: improper image header\n"},
			"office: magick: " + sha(tiffLE) + ".tiff: tool: magick: exit code 1: magick: improper image header"},
		{"magick writes no PNG", tiffLE,
			map[string]string{magickRawEnv: "1"},
			"office: magick: " + sha(tiffLE) + ".tiff: " + sha(tiffLE) + ".png isn't a PNG"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			c, _ := pictureConverter(t, true, true)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			path := writePicture(t, tc.content, "", "")
			dir := t.TempDir()
			png, err := c.ConvertPicture(t.Context(), office.PictureRequest{Dir: dir, Path: path})
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
