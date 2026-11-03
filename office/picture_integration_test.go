//go:build integration && linux

package office_test

import (
	"archive/zip"
	"bytes"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
)

// exportPictures are the pictures TestConvertPictureReal extracts from the committed fixtures,
// one of each converted kind: the TIFF fixture's TIFF, an OLE object's EMF preview, and a sheet's
// WMF.
var exportPictures = []struct {
	fixture, entry string
}{
	{"pictures/tiff.docx", "word/media/image1.tiff"},
	{"ooxml/word-with-attachments.docx", "word/media/image4.emf"},
	{"ooxml/with-drawing.xlsx", "xl/media/image5.wmf"},
}

// TestConvertPictureReal extracts a TIFF, an EMF, and a WMF from the committed fixtures, writes
// each as mail names an attachment, its SHA-256 and stored extension, outside the working
// directory, and converts it through ConvertPicture with the real soffice in the default image
// and the host's magick, twice, the second run in a later second than the first ended in. Each
// gives the working directory's file named by its SHA-256 and .png, which image/png decodes, the
// same bytes both times and the same bytes as Convert hands the picture on as within its
// document, since both run one code path; the original is kept and nothing else is left.
func TestConvertPictureReal(t *testing.T) {
	c := fullConverter(t)
	for _, p := range exportPictures {
		t.Run(filepath.Base(p.entry), func(t *testing.T) {
			data := extract(t, filepath.Join("testdata", p.fixture), p.entry)
			name := sha(string(data)) + filepath.Ext(p.entry)
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
				out, err := c.ConvertPicture(t.Context(), office.PictureRequest{Dir: dir, Path: path})
				if err != nil {
					t.Fatal(err)
				}
				stem := sha(string(data))
				if want := filepath.Join(dir, stem+".png"); out != want {
					t.Errorf("PNG: got %s, want %s", out, want)
				}
				if got := entries(t, dir); !slices.Equal(got, []string{stem + ".png"}) {
					t.Errorf("working directory holds %q, want the PNG alone", got)
				}
				b, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				img, err := png.Decode(bytes.NewReader(b))
				if err != nil {
					t.Fatalf("its PNG doesn't decode: %v", err)
				}
				if i == 0 {
					t.Logf("picture: %s %7d B -> png %4dx%-4d %7d B", name[:12]+"…"+filepath.Ext(name),
						len(data), img.Bounds().Dx(), img.Bounds().Dy(), len(b))
				}
				pngs = append(pngs, b)
			}
			if !bytes.Equal(pngs[0], pngs[1]) {
				t.Errorf("converted twice, its PNGs differ (%d and %d B); the conversion isn't reproducible", len(pngs[0]), len(pngs[1]))
			}
			if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, data) {
				t.Errorf("the original wasn't kept: %v", err)
			}

			f, err := os.Open(filepath.Join("testdata", p.fixture))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			res, err := c.Convert(t.Context(), f, office.Request{Dir: t.TempDir(), Name: p.fixture})
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(res.Media, func(m office.MediaFile) bool { return m.Name == name })
			if i < 0 || !res.Media[i].Converted {
				t.Fatalf("Convert didn't convert %s", name)
			}
			within, err := os.ReadFile(res.Media[i].File)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(within, pngs[0]) {
				t.Errorf("ConvertPicture's PNG (%d B) isn't Convert's (%d B)", len(pngs[0]), len(within))
			}
		})
	}
}

// extract returns the bytes of the entry named entry in the ZIP package at path.
func extract(t *testing.T, path, entry string) []byte {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	f, err := r.Open(entry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
