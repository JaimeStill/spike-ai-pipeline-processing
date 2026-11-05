//go:build integration && linux

package document_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
	"github.com/JaimeStill/spike-ai-pipeline-processing/internal/goldentest"
	"github.com/JaimeStill/spike-ai-pipeline-processing/picture"
)

// goldenCorrections is the golden file of the corrected pages' sums and their assessments',
// recorded before the corrections stage moved into the picture package.
const goldenCorrections = "testdata/golden/corrections.sha256"

// TestGoldenCorrections prepares every page of the three fixture sets through Images on the
// host's gs and magick at 150 dpi with all five corrections, assesses each page as rendered,
// and corrects each set's first page with each correction alone, and asserts that each
// corrected page, and each Assessment, is what it was when the golden file was recorded, at the
// commit before the picture package took the corrections stage over. magick writes a corrected
// page with the time of the run, so a page is compared without the chunks that carry it,
// goldentest.CanonicalPNG's; every other byte of it is compared.
func TestGoldenCorrections(t *testing.T) {
	g, _ := hostGhostscript(t)
	c, _ := hostCorrector(t)
	p, err := document.NewPreparer(g, c)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, set := range []string{"clean", "degraded", "scanned"} {
		req := document.ImagesRequest{Dir: t.TempDir(), Corrections: all, Concurrency: 4}
		for img, err := range p.Images(t.Context(), openSet(t, set), req) {
			if err != nil {
				t.Fatalf("%s page %d: %v", set, img.Number, err)
			}
			got[fmt.Sprintf("images/%s/page-%d/all", set, img.Number)] = goldentest.Sum(goldentest.CanonicalPNG(img.PNG))
		}
		pdf := filepath.Join("testdata", set+".pdf")
		n, err := g.Count(t.Context(), pdf)
		if err != nil {
			t.Fatal(err)
		}
		for page := 1; page <= n; page++ {
			var rendered bytes.Buffer
			if err := g.Render(t.Context(), pdf, page, 150, &rendered); err != nil {
				t.Fatal(err)
			}
			a, err := c.Assess(t.Context(), bytes.NewReader(rendered.Bytes()))
			if err != nil {
				t.Fatalf("%s page %d: %v", set, page, err)
			}
			got[fmt.Sprintf("assess/%s/page-%d", set, page)] = goldentest.Sum(fmt.Appendf(nil, "%+v", a))
			if page != 1 {
				continue
			}
			for _, corr := range all {
				var out bytes.Buffer
				if err := c.Correct(t.Context(), []picture.Correction{corr}, bytes.NewReader(rendered.Bytes()), &out); err != nil {
					t.Fatal(err)
				}
				got[fmt.Sprintf("correct/%s/page-1/%s", set, corr)] = goldentest.Sum(goldentest.CanonicalPNG(out.Bytes()))
			}
		}
	}
	goldentest.Check(t, goldenCorrections, got)
}
