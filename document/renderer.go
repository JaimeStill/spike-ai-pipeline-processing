//go:build linux

package document

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Renderer renders a PDF's pages, one page per process. A renderer counts pages with a tool from
// its own package, as the poppler renderers use pdfinfo, so its count and its rendering read the
// PDF the same way.
type Renderer interface {
	// Count returns the number of pages in the PDF at pdf.
	Count(ctx context.Context, pdf string) (int, error)
	// Render writes page, 1-based, of the PDF at pdf to w as PNG at dpi dots per inch, running
	// one process for the page. It refuses a page out of range and a DPI of zero or less before
	// its rendering tool runs, as CheckRender does.
	Render(ctx context.Context, pdf string, page, dpi int, w io.Writer) error
}

var (
	// ErrPageRange is the cause of a refused page: below 1, or past the PDF's last.
	ErrPageRange = errors.New("page out of range")
	// ErrDPI is the cause of a refused DPI: zero or less.
	ErrDPI = errors.New("DPI must be positive")
)

// CheckRender makes a Renderer's refusals, and a Render calls it before its rendering tool runs.
// It refuses a DPI of zero or less, wrapping ErrDPI, and a page below 1, wrapping ErrPageRange,
// before any tool runs. Then it counts pdf's pages through r and refuses a page past the last,
// wrapping ErrPageRange; an error from the count is returned as it is. An upper bound needs the
// count, so a page past the last costs r's counting tool one run, and never a rendering.
func CheckRender(ctx context.Context, r Renderer, pdf string, page, dpi int) error {
	if dpi <= 0 {
		return fmt.Errorf("document: %d dpi: %w", dpi, ErrDPI)
	}
	if page < 1 {
		return fmt.Errorf("document: page %d: %w", page, ErrPageRange)
	}
	count, err := r.Count(ctx, pdf)
	if err != nil {
		return err
	}
	if page > count {
		return fmt.Errorf("document: page %d of %d: %w", page, count, ErrPageRange)
	}
	return nil
}
