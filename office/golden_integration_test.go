//go:build integration && linux

package office_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/goldentest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/office"
	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
)

// goldenPictures is the golden file of the converted pictures' sums, recorded before the
// conversion moved into the picture package, its metafile renders recorded again on Gotenberg
// 9.0.0's LibreOffice, with no code changed.
const goldenPictures = "testdata/golden/pictures.sha256"

// TestGoldenPictures converts every fixture that may hold pictures as Markdown through the real
// office, soffice in the default image, and the host's magick, and every picture of exportPictures
// through the picture package's Convert, office's ConvertPicture before, at the default scale and
// at 1, and asserts that each converted picture's PNG, and each Markdown, is byte for byte what it
// was when the golden file was recorded, at the commit before the picture package took the
// conversion over, or for a metafile's PNG, when the image moved to Gotenberg 9.0.0. The PNGs are
// compared whole: magick writes them without the time of the run.
func TestGoldenPictures(t *testing.T) {
	c, pc := fullConverters(t)
	got := map[string]string{}
	for _, pattern := range pictureFixtures {
		paths, err := filepath.Glob(filepath.Join("testdata", pattern))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			rel, _ := filepath.Rel("testdata", path)
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.Convert(t.Context(), f, office.Request{Dir: t.TempDir(), Name: filepath.Base(path), Format: office.Markdown})
			_ = f.Close()
			if err != nil {
				t.Fatalf("%s: %v", rel, err)
			}
			got["convert/"+rel+"/markdown"] = goldentest.Sum([]byte(res.Markdown))
			for _, m := range res.Media {
				if !m.Converted {
					continue
				}
				b, err := os.ReadFile(m.File)
				if err != nil {
					t.Fatal(err)
				}
				got["convert/"+rel+"/"+m.Name] = goldentest.Sum(b)
			}
		}
	}
	for _, p := range exportPictures {
		data := extract(t, filepath.Join("testdata", p.fixture), p.entry)
		path := filepath.Join(t.TempDir(), goldentest.Sum(data)+filepath.Ext(p.entry))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, scale := range []int{0, 1} {
			out, err := pc.Convert(t.Context(), picture.Request{Dir: t.TempDir(), Path: path, MetafileScale: scale})
			if err != nil {
				t.Fatalf("%s: %v", p.entry, err)
			}
			b, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			key := "picture/" + p.fixture + "/" + p.entry
			if scale != 0 {
				key += "@scale1"
			}
			got[key] = goldentest.Sum(b)
		}
	}
	goldentest.Check(t, goldenPictures, got)
}
