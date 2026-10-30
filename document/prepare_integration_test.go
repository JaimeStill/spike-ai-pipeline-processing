//go:build integration && linux

package document_test

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// TestImagesHost checks the released preparation on the host's tools: Images over the host's gs
// and magick prepares every page of scanned.pdf, in page order, as a PNG of letter size at 150
// dpi, with no corrections and with all five, and records each run's wall time.
func TestImagesHost(t *testing.T) {
	g, _ := hostGhostscript(t)
	c, _ := hostCorrector(t)
	p, err := document.NewPreparer(g, c)
	if err != nil {
		t.Fatal(err)
	}
	for name, corrections := range map[string][]document.Correction{"none": nil, "all": all} {
		t.Run(name, func(t *testing.T) {
			req := document.ImagesRequest{Dir: t.TempDir(), Corrections: corrections, Concurrency: 4}
			start := time.Now()
			next := 1
			for img, err := range p.Images(t.Context(), openSet(t, "scanned"), req) {
				if err != nil {
					t.Fatalf("page %d: %v", img.Number, err)
				}
				if img.Number != next || img.Count != 7 {
					t.Errorf("yield %d is page %d of %d, want page %d of 7", next, img.Number, img.Count, next)
				}
				cfg, err := png.DecodeConfig(bytes.NewReader(img.PNG))
				if err != nil {
					t.Fatalf("page %d isn't a PNG: %v", img.Number, err)
				}
				// Deskew rotates the page, so a corrected page's size may differ by a few pixels.
				if cfg.Width < 1225 || cfg.Width > 1325 || cfg.Height < 1600 || cfg.Height > 1700 {
					t.Errorf("page %d is %dx%d, want about letter at 150 dpi, 1275x1650", img.Number, cfg.Width, cfg.Height)
				}
				next++
			}
			if next-1 != 7 {
				t.Errorf("the stream yielded %d pages, want 7", next-1)
			}
			assertEmpty(t, req.Dir)
			t.Logf("Images scanned.pdf, corrections %s, 7 pages at concurrency 4: %v", name, time.Since(start).Round(time.Millisecond))
		})
	}
}

// TestImagesTextLayerHost checks the flow of text layer first, vision only for the pages without
// one, on the host's gs: Images with SkipText yields every page of clean.pdf with its text and no
// image, and every page of scanned.pdf with no text and its image. With a Region, the image is
// that band of the page.
func TestImagesTextLayerHost(t *testing.T) {
	g, _ := hostGhostscript(t)
	p, err := document.NewPreparer(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	footer := document.Region{Top: 0.9, Bottom: 1, Left: 0, Right: 1}
	for _, tc := range []struct {
		set    string
		pages  int
		text   bool
		region *document.Region
		w, h   int
	}{
		{"clean", 6, true, nil, 0, 0},
		{"scanned", 7, false, nil, 1275, 1650},
		{"scanned", 7, false, &footer, 1275, 165},
	} {
		req := document.ImagesRequest{Dir: t.TempDir(), SkipText: true, Region: tc.region, Concurrency: 4}
		start := time.Now()
		yielded := 0
		for img, err := range p.Images(t.Context(), openSet(t, tc.set), req) {
			if err != nil {
				t.Fatalf("%s.pdf page %d: %v", tc.set, img.Number, err)
			}
			yielded++
			if got := document.HasText(img.Text); got != tc.text {
				t.Errorf("%s.pdf page %d has a text layer %v, want %v", tc.set, img.Number, got, tc.text)
			}
			if tc.text {
				if img.PNG != nil {
					t.Errorf("%s.pdf page %d has a text layer and an image, want no image", tc.set, img.Number)
				}
				continue
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(img.PNG))
			if err != nil || cfg.Width != tc.w || cfg.Height != tc.h {
				t.Errorf("%s.pdf page %d is %dx%d, %v, want a PNG of %dx%d", tc.set, img.Number, cfg.Width, cfg.Height, err, tc.w, tc.h)
			}
		}
		if yielded != tc.pages {
			t.Errorf("%s.pdf: the stream yielded %d pages, want %d", tc.set, yielded, tc.pages)
		}
		assertEmpty(t, req.Dir)
		t.Logf("recorded: Images %s.pdf with SkipText, region %v, %d pages at concurrency 4: %v",
			tc.set, tc.region, tc.pages, time.Since(start).Round(time.Millisecond))
	}
}
