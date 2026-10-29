//go:build linux

package document_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// countingRenderer is a stand-in Renderer over a PDF of pages pages. It records how often each of
// its tools ran, and its Render refuses through CheckRender, as a renderer does.
type countingRenderer struct {
	pages    int
	countErr error
	counts   int
	renders  int
}

func (r *countingRenderer) Count(context.Context, string) (int, error) {
	r.counts++
	return r.pages, r.countErr
}

func (r *countingRenderer) Render(ctx context.Context, pdf string, page, dpi int, w io.Writer) error {
	if err := document.CheckRender(ctx, r, pdf, page, dpi); err != nil {
		return err
	}
	r.renders++
	_, err := fmt.Fprintf(w, "page %d at %d dpi", page, dpi)
	return err
}

func TestRenderRefusals(t *testing.T) {
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
			var out writeCounter
			err := r.Render(t.Context(), "doc.pdf", tc.page, tc.dpi, &out)
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("Render(page %d, %d dpi) returned %v, want %v", tc.page, tc.dpi, err, tc.want)
			}
			if r.counts != tc.counts {
				t.Errorf("the counting tool ran %d times, want %d", r.counts, tc.counts)
			}
			if tc.want != nil && (r.renders != 0 || out != 0) {
				t.Errorf("a refused page rendered %d times and wrote %d bytes, want none", r.renders, out)
			}
			if tc.want == nil && r.renders != 1 {
				t.Errorf("rendered %d times, want 1", r.renders)
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

// writeCounter counts the bytes written to it.
type writeCounter int

func (w *writeCounter) Write(p []byte) (int, error) {
	*w += writeCounter(len(p))
	return len(p), nil
}
