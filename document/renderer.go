//go:build linux

package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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

// RegionRenderer is a Renderer that also renders a band of a page: a Preparer needs one for a
// request that names a Region. The released Ghostscript is one.
type RegionRenderer interface {
	Renderer
	// RenderRegion writes the region of page, 1-based, of the PDF at pdf to w as PNG at dpi, as
	// Render writes the whole page, running one process. It refuses what Render refuses, and a
	// region CheckRegion refuses, before any tool runs.
	RenderRegion(ctx context.Context, pdf string, page, dpi int, region Region, w io.Writer) error
}

// TextExtractor reads a PDF page's text layer: a Preparer needs one for a request that asks for
// pages' text. The released Ghostscript is one.
type TextExtractor interface {
	// Text returns the text of page, 1-based, of the PDF at pdf, as its text layer holds it,
	// running one process. A page with no text layer returns text that HasText reports as none.
	Text(ctx context.Context, pdf string, page int) (string, error)
}

// HasText reports whether text, a page's text layer as a TextExtractor reads it, holds any: the
// rule for a page having a text layer is that its text isn't empty once whitespace is trimmed. A
// scanned page, its text an image, has none.
func HasText(text string) bool {
	return strings.TrimSpace(text) != ""
}

// Region is a band of a page in fractions of the page as it is rendered, rotation applied: Top
// and Bottom measured down from its top edge, Left and Right across from its left edge, each 0
// to 1. The whole page is {Top: 0, Bottom: 1, Left: 0, Right: 1}; a footer band of a tenth of
// the page is {Top: 0.9, Bottom: 1, Left: 0, Right: 1}. Its zero value is no region and is
// refused.
type Region struct {
	Top, Bottom, Left, Right float64
}

// ErrRegion is the cause of a refused Region.
var ErrRegion = errors.New("region out of range")

// CheckRegion refuses a Region a RegionRenderer can't render, wrapping ErrRegion: a fraction
// outside 0 to 1, or that isn't a number, a Top not above its Bottom, or a Left not left of its
// Right.
func CheckRegion(r Region) error {
	for _, f := range []float64{r.Top, r.Bottom, r.Left, r.Right} {
		if !(f >= 0 && f <= 1) {
			return fmt.Errorf("document: region %+v: fraction %v not in 0 to 1: %w", r, f, ErrRegion)
		}
	}
	if r.Top >= r.Bottom {
		return fmt.Errorf("document: region %+v: top isn't above bottom: %w", r, ErrRegion)
	}
	if r.Left >= r.Right {
		return fmt.Errorf("document: region %+v: left isn't left of right: %w", r, ErrRegion)
	}
	return nil
}
