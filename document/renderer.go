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
	// one process for the page. It refuses a DPI of zero or less and a page below 1 before any
	// tool runs, as CheckRender does. It doesn't count the pages: a page past the last gives the
	// rendering tool's own error, and a caller that walks a PDF's pages refuses one past the last
	// against its own count.
	Render(ctx context.Context, pdf string, page, dpi int, w io.Writer) error
}

var (
	// ErrPageRange is the cause of a refused page: below 1, or past the PDF's last for a caller
	// that holds the count.
	ErrPageRange = errors.New("page out of range")
	// ErrDPI is the cause of a refused DPI: zero or less.
	ErrDPI = errors.New("DPI must be positive")
)

// CheckRender makes a Renderer's refusals, and a Render calls it before any tool runs. It refuses
// a DPI of zero or less, wrapping ErrDPI, and a page below 1, wrapping ErrPageRange. It runs no
// tool, so it has no upper bound for the page: that needs the count, which a caller walking the
// pages holds already, and a direct Render past the last fails in its rendering tool.
func CheckRender(page, dpi int) error {
	if dpi <= 0 {
		return fmt.Errorf("document: %d dpi: %w", dpi, ErrDPI)
	}
	if page < 1 {
		return fmt.Errorf("document: page %d: %w", page, ErrPageRange)
	}
	return nil
}
