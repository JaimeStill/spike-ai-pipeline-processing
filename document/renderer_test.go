//go:build linux

package document_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// countingRenderer is a stand-in Renderer over a PDF of pages pages, whose Count records how often
// it ran. CheckRender never renders, so its Render is never called.
type countingRenderer struct {
	pages    int
	countErr error
	counts   int
}

func (r *countingRenderer) Count(context.Context, string) (int, error) {
	r.counts++
	return r.pages, r.countErr
}

func (r *countingRenderer) Render(context.Context, string, int, int, io.Writer) error {
	return errors.New("CheckRender rendered a page")
}

func TestCheckRender(t *testing.T) {
	cases := []struct {
		name      string
		page, dpi int
		want      error
		// counts is how often the counting tool runs: never for a refusal that needs no count.
		counts int
	}{
		{"dpi zero", 1, 0, document.ErrDPI, 0},
		{"dpi negative", 1, -150, document.ErrDPI, 0},
		{"page zero", 0, 150, document.ErrPageRange, 0},
		{"page negative", -1, 150, document.ErrPageRange, 0},
		{"page past the last", 4, 150, document.ErrPageRange, 1},
		{"first page", 1, 150, nil, 1},
		{"last page", 3, 72, nil, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &countingRenderer{pages: 3}
			err := document.CheckRender(t.Context(), r, "doc.pdf", tc.page, tc.dpi)
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("CheckRender(page %d, %d dpi) returned %v, want %v", tc.page, tc.dpi, err, tc.want)
			}
			if r.counts != tc.counts {
				t.Errorf("the counting tool ran %d times, want %d", r.counts, tc.counts)
			}
		})
	}
}

// TestCheckRenderCountError checks that a failed count is CheckRender's error as it is.
func TestCheckRenderCountError(t *testing.T) {
	countErr := errors.New("pdfinfo: not a PDF")
	r := &countingRenderer{countErr: countErr}
	if err := document.CheckRender(t.Context(), r, "doc.pdf", 1, 150); !errors.Is(err, countErr) {
		t.Errorf("CheckRender returned %v, want the count's error %v", err, countErr)
	}
}
