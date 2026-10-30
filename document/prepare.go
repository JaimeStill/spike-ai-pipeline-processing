//go:build linux

package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"sync"
)

// defaultDPI is the DPI a request of zero DPI renders its pages at.
const defaultDPI = 150

// Preparer prepares a PDF's pages for a model's vision: each page rendered, and corrected when a
// request names corrections. The composition root builds one from its Renderer and, optionally,
// its corrections stage, and its methods are safe for concurrent use as those are. It runs no
// inference: a layer above sends the pages it yields to a model.
type Preparer struct {
	render  Renderer
	correct *Corrector
}

// NewPreparer builds a Preparer over render, and over correct when it isn't nil. It refuses a nil
// Renderer. A Preparer with no corrections stage refuses a request that names corrections.
func NewPreparer(render Renderer, correct *Corrector) (*Preparer, error) {
	if render == nil {
		return nil, errors.New("document: preparer: no renderer")
	}
	return &Preparer{render: render, correct: correct}, nil
}

// ImagesRequest is one PDF's run through a Preparer's Images.
type ImagesRequest struct {
	// Dir is the working directory the PDF is spooled into, which the caller owns: Images removes
	// the spooled file when it ends, and leaves the directory. It is required; an empty Dir is
	// refused rather than taken as the system's temporary directory.
	Dir string
	// DPI is the DPI each page is rendered at. Zero means 150; a negative DPI is refused.
	DPI int
	// Corrections are the corrections each page's image goes through. A Preparer with no
	// corrections stage refuses a request that names any.
	Corrections []Correction
	// Concurrency is how many pages are prepared at once, from rendering to the end of their
	// corrections. Zero means 1; a negative concurrency is refused.
	Concurrency int
}

// PageImage is one prepared page: its number, 1-based, the PDF's page count, and the page's image
// as PNG, corrected when the request names corrections. PNG is nil for a page that failed.
type PageImage struct {
	Number int
	Count  int
	PNG    []byte
}

// Images returns the stream of a PDF's pages, read from pdf, prepared as req says. The stream is
// lazy: nothing runs, not even the request's validation, until it is ranged over.
//
// Ranging over it validates req, spools pdf into req.Dir, counts the pages with the Renderer, and
// then, for every page from 1 to the count, renders the page at req.DPI and corrects it when req
// names corrections. At most req.Concurrency pages are being prepared at once. Pages are yielded
// in page order, a page that finishes early held until the pages before it are yielded.
//
// A page that fails, in rendering or correcting, yields the PageImage's number and count with the
// error, which names the page and wraps its cause, and the stream carries on. A refused request,
// a failed spool, or a failed count yields one error with a zero PageImage, and the stream ends.
// When ctx ends, the stream yields ctx's error once, with a zero PageImage, and ends.
//
// Breaking out of the range cancels the pages in flight, and the loop returns once their work has
// ended. The spooled file is removed before the stream ends, however it ends.
func (p *Preparer) Images(ctx context.Context, pdf io.Reader, req ImagesRequest) iter.Seq2[PageImage, error] {
	return func(yield func(PageImage, error) bool) {
		if err := p.check(&req); err != nil {
			yield(PageImage{}, err)
			return
		}
		if err := ctx.Err(); err != nil {
			yield(PageImage{}, err)
			return
		}
		path, remove, err := spool(req.Dir, pdf)
		if err != nil {
			yield(PageImage{}, err)
			return
		}
		more := p.pages(ctx, path, req, yield)
		if err := remove(); err != nil && more {
			yield(PageImage{}, fmt.Errorf("document: removing the spooled PDF: %w", err))
		}
	}
}

// outcome is one page's image or error, as its worker hands it to the stream.
type outcome struct {
	png []byte
	err error
}

// pages counts the pages of the PDF at path and prepares them, yielding each in page order, req
// already checked and defaulted. It returns once every page's work has ended, reporting whether
// the consumer still wants values.
//
// A dispatcher starts a worker per page, in page order, each holding one of req.Concurrency slots
// from its rendering to the end of its corrections; each worker hands its outcome to a channel of
// its page's own, buffered so it never waits on the consumer. The consumer reads the channels in
// page order, so a later page that finishes first waits in its channel, its slot already freed.
func (p *Preparer) pages(ctx context.Context, path string, req ImagesRequest, yield func(PageImage, error) bool) bool {
	n, err := p.render.Count(ctx, path)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			err = cerr
		}
		return yield(PageImage{}, err)
	}
	work, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	done := make([]chan outcome, n)
	for i := range done {
		done[i] = make(chan outcome, 1)
	}
	slots := make(chan struct{}, req.Concurrency)
	wg.Go(func() {
		for i := range n {
			select {
			case slots <- struct{}{}:
			case <-work.Done():
				return
			}
			wg.Go(func() {
				defer func() { <-slots }()
				png, err := p.page(work, path, i+1, n, req)
				done[i] <- outcome{png, err}
			})
		}
	})
	for i := range n {
		var o outcome
		select {
		case o = <-done[i]:
		case <-ctx.Done():
		}
		// A page that failed because ctx ended is ctx's error, not the page's.
		if err := ctx.Err(); err != nil {
			return yield(PageImage{}, err)
		}
		if !yield(PageImage{Number: i + 1, Count: n, PNG: o.png}, o.err) {
			return false
		}
	}
	return true
}

// page renders page of the n pages of the PDF at path and corrects it when req names
// corrections, returning its PNG. Its error names the page.
func (p *Preparer) page(ctx context.Context, path string, page, n int, req ImagesRequest) ([]byte, error) {
	var png bytes.Buffer
	if err := p.render.Render(ctx, path, page, req.DPI, &png); err != nil {
		return nil, fmt.Errorf("document: page %d of %d: render: %w", page, n, err)
	}
	if len(req.Corrections) == 0 {
		return png.Bytes(), nil
	}
	var corrected bytes.Buffer
	if err := p.correct.Correct(ctx, req.Corrections, &png, &corrected); err != nil {
		return nil, fmt.Errorf("document: page %d of %d: correct: %w", page, n, err)
	}
	return corrected.Bytes(), nil
}

// check refuses a request Images can't run, before anything runs, and sets its defaults: a DPI of
// 150 for zero, and a concurrency of 1 for zero.
func (p *Preparer) check(req *ImagesRequest) error {
	switch {
	case req.Dir == "":
		return errors.New("document: request has no working directory")
	case req.DPI < 0:
		return fmt.Errorf("document: request's %d dpi: %w", req.DPI, ErrDPI)
	case req.Concurrency < 0:
		return fmt.Errorf("document: request's concurrency %d is negative", req.Concurrency)
	case len(req.Corrections) > 0 && p.correct == nil:
		return errors.New("document: request names corrections, and the preparer has no corrections stage")
	}
	for _, corr := range req.Corrections {
		if _, ok := known[corr]; !ok {
			return fmt.Errorf("document: request names unknown correction %v", corr)
		}
	}
	if req.DPI == 0 {
		req.DPI = defaultDPI
	}
	if req.Concurrency == 0 {
		req.Concurrency = 1
	}
	return nil
}
