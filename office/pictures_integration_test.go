//go:build integration && linux

package office_test

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/tool"
)

// pictureFixtures are the globs, under testdata, of every fixture that may hold pictures: the
// OOXML fixtures, the TIFF fixture, and the legacy fixtures, generated and real.
var pictureFixtures = []string{
	"ooxml/*.docx", "ooxml/*.xlsx", "ooxml/*.pptx", "pictures/*.docx",
	"legacy/generated/*.doc", "legacy/generated/*.xls", "legacy/generated/*.ppt",
	"legacy/real/*.doc", "legacy/real/*.xls", "legacy/real/*.ppt",
}

// TestPictures converts every fixture as Markdown through the real office, soffice in the
// default image, and the host's magick, and asserts that each EMF, WMF, and TIFF picture is
// converted to a PNG beside it, named by its stem, that image/png decodes, and that the Markdown
// links the PNG in its place; an EMF or WMF, rendered at the default scale, is trimmed to its
// picture, smaller than the A4 page it was rendered on. Between them the fixtures hold all three
// kinds.
//
// It logs a line per picture: its fixture, kind, stored size, and its PNG's dimensions, an EMF's
// or WMF's as trimmed, and size, with its document's picture-conversion wall time and the
// pictures that shared it, since a document's EMF and WMF convert in one soffice run and are
// trimmed one magick run each. For an OOXML fixture that time is the full
// conversion's less office's alone, each the faster of two runs; a legacy fixture's pictures
// can't be timed apart from its legacy conversion, so its line gives the whole conversion's.
func TestPictures(t *testing.T) {
	o, err := tool.NewExec(t.Context(), office.OfficeSpec())
	if err != nil {
		t.Fatalf("office, which mise pins, run the tier through mise run integration: %v", err)
	}
	lo, err := tool.NewContainer(t.Context(), "", tool.Image{Ref: office.LibreOfficeImage}, office.LibreOfficeSpec())
	if err != nil {
		t.Fatal(err)
	}
	mg, err := tool.NewExec(t.Context(), office.MagickSpec())
	if err != nil {
		t.Fatalf("magick, a host prerequisite for TIFF: %v", err)
	}
	t.Logf("soffice %s in %s, magick %s", lo.Version(), office.LibreOfficeImage, mg.Version())
	full, err := office.NewConverter(office.Config{Office: o, LibreOffice: lo, Magick: mg})
	if err != nil {
		t.Fatal(err)
	}
	alone, err := office.NewConverter(office.Config{Office: o})
	if err != nil {
		t.Fatal(err)
	}

	var paths []string
	for _, pattern := range pictureFixtures {
		matches, err := filepath.Glob(filepath.Join("testdata", pattern))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, matches...)
	}
	kinds := map[string]int{}
	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			legacy := legacyTargets[filepath.Ext(path)] != ""
			res, elapsed := convertTimed(t, full, path)
			timing := "conversion"
			if !legacy {
				_, base := convertTimed(t, alone, path)
				elapsed -= base
				timing = "pictures"
			}
			var converted []office.MediaFile
			for _, m := range res.Media {
				kind := strings.TrimPrefix(strings.ToLower(filepath.Ext(m.Name)), ".")
				if kind == "tif" {
					kind = "tiff"
				}
				if kind != "emf" && kind != "wmf" && kind != "tiff" {
					continue
				}
				kinds[kind]++
				png := filepath.Join(res.MediaDir, strings.TrimSuffix(m.Name, filepath.Ext(m.Name))+".png")
				if !m.Converted || m.File != png {
					t.Errorf("%s: handed on as %q, converted %t; want %s", m.Name, m.File, m.Converted, png)
					continue
				}
				if strings.Contains(res.Markdown, m.Name) {
					t.Errorf("%s: the Markdown still names the stored picture", m.Name)
				}
				if !strings.Contains(res.Markdown, filepath.Base(png)) {
					t.Errorf("%s: the Markdown doesn't link its PNG", m.Name)
				}
				converted = append(converted, m)
			}
			for _, m := range converted {
				logPicture(t, name, res.MediaDir, m, fmt.Sprintf("%s %5d ms for %d", timing, elapsed.Milliseconds(), len(converted)))
			}
		})
	}
	t.Logf("pictures: %d emf, %d wmf, %d tiff", kinds["emf"], kinds["wmf"], kinds["tiff"])
	for _, kind := range []string{"emf", "wmf", "tiff"} {
		if kinds[kind] == 0 {
			t.Errorf("no fixture holds a %s picture", kind)
		}
	}
}

// convertTimed converts the fixture at path as Markdown, in a working directory of its own,
// twice, and returns the first result and the faster run's wall time.
func convertTimed(t *testing.T, c *office.Converter, path string) (*office.Result, time.Duration) {
	t.Helper()
	var first *office.Result
	var fastest time.Duration
	for range 2 {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		res, err := c.Convert(t.Context(), f, office.Request{Dir: t.TempDir(), Name: filepath.Base(path), Format: office.Markdown})
		elapsed := time.Since(start)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first, fastest = res, elapsed
		}
		fastest = min(fastest, elapsed)
	}
	return first, fastest
}

// logPicture fails the test unless m's PNG decodes with image/png and, for an EMF or WMF, is
// smaller than the page at the default scale in both dimensions, and logs the picture's line:
// fixture, the start of its name, kind, stored size, its PNG's dimensions and size, and timing.
func logPicture(t *testing.T, fixture, media string, m office.MediaFile, timing string) {
	t.Helper()
	stored, err := os.Stat(filepath.Join(media, m.Name))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.File)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Errorf("%s: its PNG doesn't decode: %v", m.Name, err)
		return
	}
	b := img.Bounds()
	if kind := strings.ToLower(filepath.Ext(m.Name)); (kind == ".emf" || kind == ".wmf") &&
		(b.Dx() >= office.DefaultMetafileScale*794 || b.Dy() >= office.DefaultMetafileScale*1123) {
		t.Errorf("%s: its PNG is %dx%d, the page it was rendered on, not trimmed to the picture", m.Name, b.Dx(), b.Dy())
	}
	t.Logf("picture: %-34s %s… %-4s %7d B -> png %4dx%-4d %7d B  %s",
		fixture, m.Name[:12], strings.TrimPrefix(filepath.Ext(m.Name), "."), stored.Size(),
		b.Dx(), b.Dy(), len(data), timing)
}
