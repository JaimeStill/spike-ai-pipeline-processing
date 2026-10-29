//go:build linux

package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
	"strings"
	"sync"

	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
)

// defaultDPI is the DPI a Request of zero DPI renders its pages at.
const defaultDPI = 150

// Vision sends a PDF's pages to a model's vision: each page rendered, corrected when a request
// names corrections, and sent as an image in a request of its own. The composition root builds
// one from its Generator, its Renderer, and, optionally, its corrections stage, and its methods
// are safe for concurrent use as those are.
type Vision struct {
	gen     Generator
	render  Renderer
	correct *Corrector
}

// NewVision builds a Vision over gen and render, and over correct when it isn't nil. It refuses
// a nil Generator and a nil Renderer. A Vision with no corrections stage refuses a Request that
// names corrections.
func NewVision(gen Generator, render Renderer, correct *Corrector) (*Vision, error) {
	if gen == nil {
		return nil, errors.New("document: vision: no generator")
	}
	if render == nil {
		return nil, errors.New("document: vision: no renderer")
	}
	return &Vision{gen: gen, render: render, correct: correct}, nil
}

// Request is one PDF's run through a Vision: Pages reads all but Final, and Document all of it.
// One type serves both, so a caller building a Document's request builds the pages' with it, and
// Pages ignores Final.
type Request struct {
	// Dir is the working directory the PDF is spooled into, which the caller owns: Pages and
	// Document remove the spooled file when they end, and leave the directory. It is required;
	// an empty Dir is refused rather than taken as the system's temporary directory.
	Dir string
	// Template is the request for one page: its model, instructions, and other fields. Each
	// page's request is a copy of it with one user message appended to its Input, the page's
	// image as a PNG data URL. Template is never changed. It is required.
	Template *protocol.Request
	// DPI is the DPI each page is rendered at. Zero means 150; a negative DPI is refused.
	DPI int
	// Corrections are the corrections each page's image goes through before it is sent. A
	// Vision with no corrections stage refuses a request that names any.
	Corrections []Correction
	// Concurrency is how many pages are in flight at once, from rendering to the model's
	// response. Zero means 1; a negative concurrency is refused.
	Concurrency int
	// PageRetries is how many more times a page is sent when its response isn't completed, such
	// as one incomplete at its output cap, a model looping on a page's text running it there on
	// one run and not the next. The page is re-sent with the image already rendered and
	// corrected, holding its concurrency slot, until a response is completed or the retries run
	// out, and the page then comes back with its last response, as it would with no retries. A page
	// whose Generate fails, such as a refused request or a transport error, is never re-sent: its
	// error is the page's, and retrying a transport is the Generator's, as the client's opt-in
	// Config.Retry policy does. Zero sends each page once; a negative count is refused.
	PageRetries int
	// Final is Document's final request: its model, instructions, text format, and output
	// limit. Document appends one user message to a copy of it, a text part per page, and
	// requires it. Pages ignores it.
	Final *protocol.Request
}

// Page is one page's outcome: its number, 1-based, the PDF's page count, the model's response to
// the page, and how many times the page was sent. Response is nil for a page that failed.
type Page struct {
	Number   int
	Count    int
	Response *protocol.Response
	// Attempts is how many times the page was sent to the Generator: 1 for a page sent once, up
	// to 1 plus the request's PageRetries for a page re-sent because its response wasn't
	// completed, and 0 for a page that failed before it was sent, in rendering or correcting.
	// A caller records a page's retries from it.
	Attempts int
}

// Pages returns the stream of a PDF's pages through the model, read from pdf, as req says. The
// stream is lazy: nothing runs, not even the request's validation, until it is ranged over.
//
// Ranging over it validates req, spools pdf into req.Dir, counts the pages with the Renderer,
// and then, for every page from 1 to the count, renders the page at req.DPI, corrects it when
// req names corrections, and sends it to the Generator in a copy of req.Template with one user
// message appended: the page's PNG as an InputImage data URL, its detail left to the endpoint.
// A page whose response isn't completed is sent again, up to req.PageRetries times, as Request
// says, and is yielded with its last response, completed or not: Pages never fails a page for
// its status, which the caller reads. At most req.Concurrency pages are in flight at once,
// rendering and retries included. Pages are yielded in page order, a page that finishes early
// held until the pages before it are yielded.
//
// A page that fails, in rendering, correcting, or sending, yields the Page's number and count
// with the error, which names the page and wraps its cause, such as the Generator's
// *client.APIError for a request the server refused, and the stream carries on. A refused
// request, a failed spool, or a failed count yields one error with a zero Page, and the stream
// ends. When ctx ends, the stream yields ctx's error once, with a zero Page, and ends.
//
// The package sets no size limit on a page's image: a page over the server's limit is sent, and
// the server's refusal is that page's error.
//
// Breaking out of the range cancels the pages in flight, and the loop returns once their work
// has ended. The spooled file is removed before the stream ends, however it ends.
func (v *Vision) Pages(ctx context.Context, pdf io.Reader, req Request) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) {
		if err := v.check(&req); err != nil {
			yield(Page{}, err)
			return
		}
		if err := ctx.Err(); err != nil {
			yield(Page{}, err)
			return
		}
		path, remove, err := spool(req.Dir, pdf)
		if err != nil {
			yield(Page{}, err)
			return
		}
		more := v.pages(ctx, path, req, yield)
		if err := remove(); err != nil && more {
			yield(Page{}, fmt.Errorf("document: removing the spooled PDF: %w", err))
		}
	}
}

// outcome is one page's response or error, and its attempts, as its worker hands it to the
// stream.
type outcome struct {
	resp     *protocol.Response
	attempts int
	err      error
}

// pages counts the pages of the PDF at path and runs them, yielding each in page order, req
// already checked and defaulted. It returns once every page's work has ended, reporting whether
// the consumer still wants values.
//
// A dispatcher starts a worker per page, in page order, each holding one of req.Concurrency
// slots from its rendering to its response; each worker hands its outcome to a channel of its
// page's own, buffered so it never waits on the consumer. The consumer reads the channels in
// page order, so a later page that finishes first waits in its channel, its slot already freed.
func (v *Vision) pages(ctx context.Context, path string, req Request, yield func(Page, error) bool) bool {
	n, err := v.render.Count(ctx, path)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			err = cerr
		}
		return yield(Page{}, err)
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
				resp, attempts, err := v.page(work, path, i+1, n, req)
				done[i] <- outcome{resp, attempts, err}
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
			return yield(Page{}, err)
		}
		if !yield(Page{Number: i + 1, Count: n, Response: o.resp, Attempts: o.attempts}, o.err) {
			return false
		}
	}
	return true
}

// page renders page of the n pages of the PDF at path, corrects it when req names corrections,
// and sends it to the Generator in a copy of req.Template, again with the same image while its
// response isn't completed and req.PageRetries allows, returning the last response and the
// times it was sent. A Generate error ends the page without a retry, and so does ctx ending,
// which leaves the last response. Its error names the page.
func (v *Vision) page(ctx context.Context, path string, page, n int, req Request) (*protocol.Response, int, error) {
	var png bytes.Buffer
	if err := v.render.Render(ctx, path, page, req.DPI, &png); err != nil {
		return nil, 0, fmt.Errorf("document: page %d of %d: render: %w", page, n, err)
	}
	if len(req.Corrections) > 0 {
		var corrected bytes.Buffer
		if err := v.correct.Correct(ctx, req.Corrections, &png, &corrected); err != nil {
			return nil, 0, fmt.Errorf("document: page %d of %d: correct: %w", page, n, err)
		}
		png = corrected
	}
	var resp *protocol.Response
	attempts := 0
	for {
		// Each attempt is a request of its own, so no two share an input array.
		var err error
		resp, err = v.gen.Generate(ctx, withImage(req.Template, png.Bytes()))
		attempts++
		if err != nil {
			return nil, attempts, fmt.Errorf("document: page %d of %d: generate: %w", page, n, err)
		}
		if (resp != nil && resp.Status == protocol.StatusCompleted) || attempts > req.PageRetries || ctx.Err() != nil {
			return resp, attempts, nil
		}
	}
}

// check refuses a request Pages can't run, before anything runs, and sets its defaults: a DPI
// of 150 for zero, and a concurrency of 1 for zero. It validates the template by validating the
// request it makes for a page, so a template the Generator would refuse is refused once, here,
// rather than once per page.
func (v *Vision) check(req *Request) error {
	switch {
	case req.Dir == "":
		return errors.New("document: request has no working directory")
	case req.Template == nil:
		return errors.New("document: request has no template")
	case req.DPI < 0:
		return fmt.Errorf("document: request's %d dpi: %w", req.DPI, ErrDPI)
	case req.Concurrency < 0:
		return fmt.Errorf("document: request's concurrency %d is negative", req.Concurrency)
	case req.PageRetries < 0:
		return fmt.Errorf("document: request's page retries %d is negative", req.PageRetries)
	case len(req.Corrections) > 0 && v.correct == nil:
		return errors.New("document: request names corrections, and the vision has no corrections stage")
	}
	for _, corr := range req.Corrections {
		if _, ok := known[corr]; !ok {
			return fmt.Errorf("document: request names unknown correction %v", corr)
		}
	}
	if err := withImage(req.Template, nil).Validate(); err != nil {
		return fmt.Errorf("document: request's template: %w", err)
	}
	if req.DPI == 0 {
		req.DPI = defaultDPI
	}
	if req.Concurrency == 0 {
		req.Concurrency = 1
	}
	return nil
}

// withImage returns a copy of tmpl with one user message appended to its Input: png as an
// InputImage's PNG data URL, its detail unset.
func withImage(tmpl *protocol.Request, png []byte) *protocol.Request {
	return withMessage(tmpl, protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{
		protocol.InputImage{ImageURL: protocol.DataURL("image/png", png)},
	}})
}

// withMessage returns a copy of tmpl with msg appended to its Input. The copy's slices are its
// own, Input, Tools, and Native, so no two requests built from one template share a backing
// array and tmpl is never changed. The items and tools themselves are values, shared as values.
func withMessage(tmpl *protocol.Request, msg protocol.Message) *protocol.Request {
	r := *tmpl
	r.Input = slices.Concat(tmpl.Input, []protocol.Item{msg})
	r.Tools = slices.Clone(tmpl.Tools)
	r.Native = slices.Clone(tmpl.Native)
	return &r
}

// Result is a Document's outcome: every page's, in page order, and the final response. The
// pages' transcriptions are the document's digitized content, which Text joins; the final
// response is derived from them and never replaces them.
type Result struct {
	// Pages holds every page the stream yielded, in page order, each with its response or its
	// error.
	Pages []PageResult
	// Final is the final request's response. It is nil when Document failed before the final
	// request, or the request itself failed, and kept when its status isn't completed.
	Final *protocol.Response
}

// Text returns the document's digitized content: every page's output text, in page order, each
// preceded by its "Page N of M:" heading and a newline, the heading Document's final request
// gives each page's part, and the pages joined by a blank line. A page's text is its response's
// as the model wrote it, not trimmed.
//
// A Result Document returns without an error holds only completed pages. A Result returned with
// an error may hold pages that failed: each still appears, a page with no response as its heading
// alone, and a page whose response isn't completed with whatever text that response holds, so
// the numbering shows where text is missing. A nil Result, or one with no pages, returns "".
func (r *Result) Text() string {
	if r == nil {
		return ""
	}
	parts := make([]string, 0, len(r.Pages))
	for _, p := range r.Pages {
		var text string
		if p.Response != nil {
			text = outputText(p.Response)
		}
		parts = append(parts, pageText(p.Number, p.Count, text))
	}
	return strings.Join(parts, "\n\n")
}

// PageResult is one page of a Document: the Page and its error, nil for a page that succeeded.
type PageResult struct {
	Page
	Err error
}

// Document digitizes a PDF and then digests it. It runs the PDF's pages through the model as
// Pages does, their transcriptions being the document's digitized content, which the result's
// Text joins, and then sends one final request: a copy of req.Final with one user message
// appended, holding a text part per page, in page order, each "Page N of M:" followed by a
// newline and that page's output text. The final response derives from the transcriptions and
// never replaces them; what it holds, such as a digest for search under a JSON schema, is the
// final template's to ask, and the package imposes no schema. It sets no cap on the page count.
//
// A missing req.Final is refused before anything runs, as Pages refuses a request. A page that
// fails, or whose response's status isn't completed after req.PageRetries, fails Document before
// the final request, with an error naming each such page and wrapping each page's error, so
// errors.As finds a page's *client.APIError; the result holds every page's entry then, each with
// its attempts. A final response whose status isn't completed is an error, and the result keeps
// the response; the final request is sent once, never retried. A stream that ends with
// an error of its own, a refused request, a failed spool or count, or an ended ctx, is
// Document's error, the result holding the pages yielded before it.
func (v *Vision) Document(ctx context.Context, pdf io.Reader, req Request) (*Result, error) {
	if req.Final == nil {
		return nil, errors.New("document: request has no final template")
	}
	// The final template is validated with a part for a page, as it is sent.
	probe := withMessage(req.Final, protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{
		protocol.InputText{Text: pageText(1, 1, "")},
	}})
	if err := probe.Validate(); err != nil {
		return nil, fmt.Errorf("document: request's final template: %w", err)
	}
	res := &Result{}
	var failed []int
	var errs []error
	for p, err := range v.Pages(ctx, pdf, req) {
		if p.Number == 0 {
			return res, err
		}
		switch {
		case err != nil:
		case p.Response == nil:
			err = fmt.Errorf("document: page %d of %d: no response", p.Number, p.Count)
		case p.Response.Status != protocol.StatusCompleted:
			err = fmt.Errorf("document: page %d of %d: response is %s, attempts %d", p.Number, p.Count, status(p.Response), p.Attempts)
		}
		res.Pages = append(res.Pages, PageResult{Page: p, Err: err})
		if err != nil {
			failed = append(failed, p.Number)
			errs = append(errs, err)
		}
	}
	if len(failed) > 0 {
		return res, fmt.Errorf("document: %d of %d pages failed, %v: %w", len(failed), len(res.Pages), failed, errors.Join(errs...))
	}
	if len(res.Pages) == 0 {
		return res, errors.New("document: the PDF has no pages")
	}
	parts := make([]protocol.ContentPart, 0, len(res.Pages))
	for _, p := range res.Pages {
		parts = append(parts, protocol.InputText{Text: pageText(p.Number, p.Count, outputText(p.Response))})
	}
	final, err := v.gen.Generate(ctx, withMessage(req.Final, protocol.Message{Role: protocol.RoleUser, Content: parts}))
	if err != nil {
		return res, fmt.Errorf("document: final request: %w", err)
	}
	if final == nil {
		return res, errors.New("document: final request: no response")
	}
	res.Final = final
	if final.Status != protocol.StatusCompleted {
		return res, fmt.Errorf("document: final response is %s", status(final))
	}
	return res, nil
}

// pageText is a page's part of the final request and of Result.Text: "Page N of M:", a newline,
// and its text. The two share it so their headings can't drift.
func pageText(page, n int, text string) string {
	return fmt.Sprintf("Page %d of %d:\n%s", page, n, text)
}

// status describes a response's status, with why it is incomplete or failed when it says.
func status(r *protocol.Response) string {
	switch {
	case r.IncompleteDetails != nil:
		return fmt.Sprintf("%s (%s)", r.Status, r.IncompleteDetails.Reason)
	case r.Error != nil:
		return fmt.Sprintf("%s (%s: %s)", r.Status, r.Error.Code, r.Error.Message)
	}
	return string(r.Status)
}

// outputText returns a response's output text: the text of every OutputText part of its
// messages, in order, joined. The client has no helper for it.
func outputText(r *protocol.Response) string {
	var text strings.Builder
	for _, it := range r.Output {
		if m, ok := it.(protocol.Message); ok {
			for _, p := range m.Content {
				if o, ok := p.(protocol.OutputText); ok {
					text.WriteString(o.Text)
				}
			}
		}
	}
	return text.String()
}
