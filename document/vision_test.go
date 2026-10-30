//go:build linux

package document_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	client "github.com/JaimeStill/spike-ai-pipeline-client"
	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// This file is the vision harness: the layer above document, in test code, that sends the pages
// the released Images stream prepares to a model, as ruling M of task documents places inference
// above processing's libraries. The integration tier's vision tests, and the corrections matrix,
// the instruction placement comparison, and the staged flow in the bake-off tier, run on it, so
// they prove evidence 4 end to end through the released preparation. It is test code, never released: the module's requirement of
// spike-ai-pipeline-client is a test requirement. The tests below check its inference: the page
// requests, retries, the ordered stream over the generator's concurrency, and the final request.

// generator sends one request to a model and returns its response: the one method the harness
// needs of spike-ai-pipeline-client, which *client.Client satisfies; a test passes a stand-in.
type generator interface {
	Generate(ctx context.Context, req *protocol.Request) (*protocol.Response, error)
}

// The integration tier passes its *client.Client as the generator; this fails to compile if the
// Client stops satisfying it.
var _ generator = (*client.Client)(nil)

// transcribeInstructions asks a vision model for a page's text: every word on the page, in
// reading order, as plain text, and nothing else. It asks for no layout or formatting, so the
// answer compares with a page's ground truth as text.
//
// The renderer bake-off sent it to Gemma 4 26B-A4B with reasoning off, a request's
// Reasoning.Effort of none, and a request for a transcription should too: with its reasoning on,
// the model reasoned about a hard scanned page until the 8192-token output cap and returned no
// text.
const transcribeInstructions = "Transcribe every word on this page, in reading order, as plain text. " +
	"Reply with the page's text and nothing else: no commentary, no description of the page, " +
	"and no Markdown or other formatting."

// vision sends the pages a Preparer prepares to a model's vision, each in a request of its own,
// and, for document, one final request over their text.
type vision struct {
	gen     generator
	prepare *document.Preparer
}

// newVision builds a vision over gen and a Preparer of render and correct, correct optional.
func newVision(gen generator, render document.Renderer, correct *document.Corrector) (*vision, error) {
	if gen == nil {
		return nil, errors.New("vision: no generator")
	}
	p, err := document.NewPreparer(render, correct)
	if err != nil {
		return nil, err
	}
	return &vision{gen: gen, prepare: p}, nil
}

// visionRequest is one PDF's run through a vision: the preparation's fields, which Images reads
// and validates, and inference's own. pages reads all but Final, and document all of it.
type visionRequest struct {
	Dir         string
	DPI         int
	Corrections []document.Correction
	// Concurrency bounds both the pages Images prepares at once and the pages awaiting the model.
	Concurrency int
	// Template is the request for one page; each page's request is a copy of it with one user
	// message appended, the page's image as a PNG data URL.
	Template *protocol.Request
	// Prompt, when not empty, is a text part placed before the image in each page's user
	// message: the instructions beside the image, as the renderer bake-off sent them, where the
	// template's Instructions put them in the request's own. Empty sends the image alone.
	Prompt string
	// Region, when not nil, renders only that band of each page, as Images's request does.
	Region *document.Region
	// PageRetries is how many more times a page is sent when its response isn't completed, with
	// the image already prepared, holding its slot. A page whose Generate fails is never re-sent.
	PageRetries int
	// Final is document's final request, to which it appends a text part per page.
	Final *protocol.Request
}

// images is the request's preparation, for Images.
func (r visionRequest) images() document.ImagesRequest {
	return document.ImagesRequest{
		Dir: r.Dir, DPI: r.DPI, Corrections: r.Corrections, Concurrency: r.Concurrency, Region: r.Region,
	}
}

// visionPage is one page's outcome: its number, the page count, the model's response, nil for a
// page that failed, and how many times it was sent, 0 for a page that failed in preparation.
type visionPage struct {
	Number   int
	Count    int
	Response *protocol.Response
	Attempts int
}

// sent is one page's response or error, and its attempts, as its worker hands it to the stream.
type sent struct {
	resp     *protocol.Response
	attempts int
	err      error
}

// queued is a page the producer has taken from Images, in page order, and where its outcome
// arrives. number is 0 for the Images stream's own error.
type queued struct {
	number, count int
	done          chan sent
}

// pages returns the stream of a PDF's pages through the model, built on Images: each page Images
// yields is sent in a copy of req.Template, retried as req.PageRetries says, at most
// req.Concurrency awaiting the model at once, and yielded in page order. A page Images failed is
// yielded with its error, never sent; the Images stream's own error, a refusal, a failed spool or
// count, is yielded with a zero visionPage. A refused template or retry count is the stream's one
// error. When ctx ends, the stream yields ctx's error once. Breaking out of the range cancels the
// work in flight, Images's included, and returns once it has ended.
func (v *vision) pages(ctx context.Context, pdf io.Reader, req visionRequest) iter.Seq2[visionPage, error] {
	return func(yield func(visionPage, error) bool) {
		if err := check(req); err != nil {
			yield(visionPage{}, err)
			return
		}
		work, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		defer func() {
			cancel()
			wg.Wait()
		}()
		slots := make(chan struct{}, max(req.Concurrency, 1))
		queue := make(chan queued)
		wg.Go(func() {
			defer close(queue)
			for img, err := range v.prepare.Images(work, pdf, req.images()) {
				done := make(chan sent, 1)
				if err != nil {
					done <- sent{err: err}
				} else {
					select {
					case slots <- struct{}{}:
					case <-work.Done():
						return
					}
					wg.Go(func() {
						defer func() { <-slots }()
						resp, attempts, err := v.send(work, img, req)
						done <- sent{resp, attempts, err}
					})
				}
				select {
				case queue <- queued{img.Number, img.Count, done}:
				case <-work.Done():
					return
				}
			}
		})
		for q := range queue {
			var s sent
			select {
			case s = <-q.done:
			case <-ctx.Done():
			}
			// A page that failed because ctx ended is ctx's error, not the page's.
			if err := ctx.Err(); err != nil {
				yield(visionPage{}, err)
				return
			}
			if !yield(visionPage{Number: q.number, Count: q.count, Response: s.resp, Attempts: s.attempts}, s.err) {
				return
			}
		}
	}
}

// send sends img in a copy of req.Template, beside req.Prompt when it is set, again with the same image while its response isn't
// completed and req.PageRetries allows, returning the last response and the times it was sent. A
// Generate error ends the page without a retry, and so does ctx ending.
func (v *vision) send(ctx context.Context, img document.PageImage, req visionRequest) (*protocol.Response, int, error) {
	attempts := 0
	for {
		// Each attempt is a request of its own, so no two share an input array.
		resp, err := v.gen.Generate(ctx, withImage(req.Template, req.Prompt, img.PNG))
		attempts++
		if err != nil {
			return nil, attempts, fmt.Errorf("vision: page %d of %d: generate: %w", img.Number, img.Count, err)
		}
		if (resp != nil && resp.Status == protocol.StatusCompleted) || attempts > req.PageRetries || ctx.Err() != nil {
			return resp, attempts, nil
		}
	}
}

// check refuses the inference fields of a request before anything runs, validating the template
// by the request it makes for a page; Images refuses the preparation's.
func check(req visionRequest) error {
	switch {
	case req.Template == nil:
		return errors.New("vision: request has no template")
	case req.PageRetries < 0:
		return fmt.Errorf("vision: request's page retries %d is negative", req.PageRetries)
	}
	if err := withImage(req.Template, req.Prompt, nil).Validate(); err != nil {
		return fmt.Errorf("vision: request's template: %w", err)
	}
	return nil
}

// withImage returns a copy of tmpl with one user message appended to its Input: prompt as an
// InputText when it isn't empty, then png as an InputImage's PNG data URL, its detail unset.
func withImage(tmpl *protocol.Request, prompt string, png []byte) *protocol.Request {
	var parts []protocol.ContentPart
	if prompt != "" {
		parts = append(parts, protocol.InputText{Text: prompt})
	}
	parts = append(parts, protocol.InputImage{ImageURL: protocol.DataURL("image/png", png)})
	return withMessage(tmpl, protocol.Message{Role: protocol.RoleUser, Content: parts})
}

// withMessage returns a copy of tmpl with msg appended to its Input. The copy's slices are its
// own, Input, Tools, and Native, so no two requests built from one template share a backing array
// and tmpl is never changed.
func withMessage(tmpl *protocol.Request, msg protocol.Message) *protocol.Request {
	r := *tmpl
	r.Input = slices.Concat(tmpl.Input, []protocol.Item{msg})
	r.Tools = slices.Clone(tmpl.Tools)
	r.Native = slices.Clone(tmpl.Native)
	return &r
}

// docResult is a document's outcome: every page's, in page order, and the final response, nil
// when document failed before the final request or the request itself failed.
type docResult struct {
	Pages []pageResult
	Final *protocol.Response
}

// pageResult is one page of a document with its error, nil for a page that succeeded.
type pageResult struct {
	visionPage
	Err error
}

// Text returns the document's digitized content: every page's output text, in page order, each
// under its "Page N of M:" heading, the heading document's final request gives each page's part,
// the pages joined by a blank line. A page with no response is its heading alone. A nil result
// returns "".
func (r *docResult) Text() string {
	if r == nil {
		return ""
	}
	parts := make([]string, 0, len(r.Pages))
	for _, p := range r.Pages {
		parts = append(parts, pageText(p.Number, p.Count, text(p.Response)))
	}
	return strings.Join(parts, "\n\n")
}

// document digitizes a PDF and then digests it: it runs the pages as pages does, and then one
// final request, a copy of req.Final with one user message of a "Page N of M:" text part per
// page. A page that fails, or whose response isn't completed after its retries, fails it before
// the final request, with an error naming each such page and wrapping each page's error; a final
// response that isn't completed is an error, kept in the result. The stream's own error is
// document's, the result holding the pages yielded before it.
func (v *vision) document(ctx context.Context, pdf io.Reader, req visionRequest) (*docResult, error) {
	if req.Final == nil {
		return nil, errors.New("vision: request has no final template")
	}
	probe := withMessage(req.Final, protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{
		protocol.InputText{Text: pageText(1, 1, "")},
	}})
	if err := probe.Validate(); err != nil {
		return nil, fmt.Errorf("vision: request's final template: %w", err)
	}
	res := &docResult{}
	var failed []int
	var errs []error
	for p, err := range v.pages(ctx, pdf, req) {
		if p.Number == 0 {
			return res, err
		}
		switch {
		case err != nil:
		case p.Response == nil:
			err = fmt.Errorf("vision: page %d of %d: no response", p.Number, p.Count)
		case p.Response.Status != protocol.StatusCompleted:
			err = fmt.Errorf("vision: page %d of %d: response is %s, attempts %d", p.Number, p.Count, status(p.Response), p.Attempts)
		}
		res.Pages = append(res.Pages, pageResult{visionPage: p, Err: err})
		if err != nil {
			failed = append(failed, p.Number)
			errs = append(errs, err)
		}
	}
	if len(failed) > 0 {
		return res, fmt.Errorf("vision: %d of %d pages failed, %v: %w", len(failed), len(res.Pages), failed, errors.Join(errs...))
	}
	if len(res.Pages) == 0 {
		return res, errors.New("vision: the PDF has no pages")
	}
	parts := make([]protocol.ContentPart, 0, len(res.Pages))
	for _, p := range res.Pages {
		parts = append(parts, protocol.InputText{Text: pageText(p.Number, p.Count, text(p.Response))})
	}
	final, err := v.gen.Generate(ctx, withMessage(req.Final, protocol.Message{Role: protocol.RoleUser, Content: parts}))
	if err != nil {
		return res, fmt.Errorf("vision: final request: %w", err)
	}
	if final == nil {
		return res, errors.New("vision: final request: no response")
	}
	res.Final = final
	if final.Status != protocol.StatusCompleted {
		return res, fmt.Errorf("vision: final response is %s", status(final))
	}
	return res, nil
}

// pageText is a page's part of the final request and of docResult.Text: "Page N of M:", a
// newline, and its text.
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

// text returns a response's output text, the text of every OutputText part of its messages, in
// order, joined, or "" for nil.
func text(r *protocol.Response) string {
	if r == nil {
		return ""
	}
	var s strings.Builder
	for _, it := range r.Output {
		if m, ok := it.(protocol.Message); ok {
			for _, p := range m.Content {
				if o, ok := p.(protocol.OutputText); ok {
					s.WriteString(o.Text)
				}
			}
		}
	}
	return s.String()
}

// standinGenerator is the stand-in generator. It records every request, then answers with
// respond, which gets the page the request's image is, 0 for a request with no image, such as
// document's final request. It counts the requests awaiting respond.
type standinGenerator struct {
	respond func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error)

	mu     sync.Mutex
	reqs   []*protocol.Request
	active int
}

func (g *standinGenerator) Generate(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	g.mu.Lock()
	g.reqs = append(g.reqs, req)
	g.active++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.active--
		g.mu.Unlock()
	}()
	return g.respond(ctx, imagePage(req), req)
}

// requests returns the requests the generator was sent, in the order sent.
func (g *standinGenerator) requests() []*protocol.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reqs)
}

// awaiting returns how many requests are awaiting respond.
func (g *standinGenerator) awaiting() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

// imagePage returns the page whose image the request's last message holds, decoded from the data
// URL the stand-in renderer's page makes, or 0 when it holds none.
func imagePage(req *protocol.Request) int {
	if len(req.Input) == 0 {
		return 0
	}
	m, ok := req.Input[len(req.Input)-1].(protocol.Message)
	if !ok {
		return 0
	}
	i := slices.IndexFunc(m.Content, func(p protocol.ContentPart) bool {
		_, ok := p.(protocol.InputImage)
		return ok
	})
	if i < 0 {
		return 0
	}
	img := m.Content[i].(protocol.InputImage)
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(img.ImageURL, "data:image/png;base64,"))
	if err != nil {
		return 0
	}
	var page int
	if _, err := fmt.Sscanf(string(bytes.TrimPrefix(b, []byte("\x89PNG\r\n\x1a\n"))), "page %d", &page); err != nil {
		return 0
	}
	return page
}

// answer is a completed response whose output text is text.
func answer(text string) *protocol.Response {
	return &protocol.Response{
		Status: protocol.StatusCompleted,
		Output: []protocol.Item{protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.ContentPart{
			protocol.OutputText{Text: text},
		}}},
	}
}

// pageAnswer answers a page with "text of page N", and a final request with "summary".
func pageAnswer(_ context.Context, page int, _ *protocol.Request) (*protocol.Response, error) {
	if page == 0 {
		return answer("summary"), nil
	}
	return answer(fmt.Sprintf("text of page %d", page)), nil
}

// visionFixture is a vision over the stand-in renderer and generator, the PDF it reads, and a
// request for it.
type visionFixture struct {
	render *renderer
	gen    *standinGenerator
	vision *vision
	pdf    []byte
	req    visionRequest
}

// newVisionFixture builds a vision over a stand-in renderer of pages pages and a stand-in
// generator answering with pageAnswer, and a request for the PDF with its own working directory,
// a template with a developer message, and a final template.
func newVisionFixture(t *testing.T, pages int) *visionFixture {
	t.Helper()
	f := &visionFixture{
		render: &renderer{pages: pages, flight: &flightLog{}},
		gen:    &standinGenerator{respond: pageAnswer},
		pdf:    []byte("%PDF-1.7 stand-in"),
		req: visionRequest{
			Dir: t.TempDir(),
			Template: &protocol.Request{
				Model:        "vision",
				Instructions: transcribeInstructions,
				Input: []protocol.Item{protocol.Message{Role: protocol.RoleDeveloper, Content: []protocol.ContentPart{
					protocol.InputText{Text: "be exact"},
				}}},
				Native: jsontext.Value(`{"temperature":0}`),
			},
			Final: &protocol.Request{Model: "final", Instructions: "Summarize the document."},
		},
	}
	f.build(t, nil)
	return f
}

// build rebuilds f's vision with correct as its corrections stage.
func (f *visionFixture) build(t *testing.T, correct *document.Corrector) {
	t.Helper()
	var err error
	f.vision, err = newVision(f.gen, f.render, correct)
	if err != nil {
		t.Fatal(err)
	}
}

// pages ranges over f's stream to its end and returns what it yielded.
func (f *visionFixture) pages(ctx context.Context) ([]visionPage, []error) {
	var pages []visionPage
	var errs []error
	for p, err := range f.vision.pages(ctx, bytes.NewReader(f.pdf), f.req) {
		pages = append(pages, p)
		errs = append(errs, err)
	}
	return pages, errs
}

// TestVisionPages checks the harness sends each page Images yields, in page order when later pages
// render first, and yields its response, a page Images failed with its error and never sent.
func TestVisionPages(t *testing.T) {
	f := newVisionFixture(t, 5)
	f.req.Concurrency = 5
	renderErr := errors.New("gs: page 3 is damaged")
	f.render.fail = map[int]error{3: renderErr}
	f.render.wait = delays(func(page int) time.Duration { return time.Duration(6-page) * 10 * time.Millisecond })
	pages, errs := f.pages(t.Context())
	if len(pages) != 5 {
		t.Fatalf("the stream yielded %d pages, want 5", len(pages))
	}
	for i, p := range pages {
		if p.Number != i+1 || p.Count != 5 {
			t.Errorf("yield %d is page %d of %d, want page %d of 5", i+1, p.Number, p.Count, i+1)
		}
		if p.Number == 3 {
			if !errors.Is(errs[i], renderErr) || p.Response != nil || p.Attempts != 0 {
				t.Errorf("page 3: %v, %v, %d attempts, want the render's error, unsent", errs[i], p.Response, p.Attempts)
			}
			continue
		}
		if want := fmt.Sprintf("text of page %d", p.Number); errs[i] != nil || text(p.Response) != want || p.Attempts != 1 {
			t.Errorf("page %d: %v, %q, %d attempts, want %q after 1", p.Number, errs[i], text(p.Response), p.Attempts, want)
		}
	}
	for _, r := range f.gen.requests() {
		if imagePage(r) == 3 {
			t.Error("page 3 failed in rendering and was sent")
		}
	}
}

// incomplete is a response cut off at its output cap, holding the text it reached.
func incomplete(text string) *protocol.Response {
	r := answer(text)
	r.Status = protocol.StatusIncomplete
	r.IncompleteDetails = &protocol.IncompleteDetails{Reason: protocol.ReasonMaxOutputTokens}
	return r
}

// cutOff answers page cut times incomplete and then completed, with pageAnswer, and every other
// page and the final request with pageAnswer; a negative cut answers the page incomplete every
// time. It counts the page's attempts, which sent returns.
func cutOff(page, cut int) (respond func(context.Context, int, *protocol.Request) (*protocol.Response, error), sent func() int) {
	var mu sync.Mutex
	n := 0
	respond = func(ctx context.Context, p int, req *protocol.Request) (*protocol.Response, error) {
		if p != page {
			return pageAnswer(ctx, p, req)
		}
		mu.Lock()
		n++
		attempt := n
		mu.Unlock()
		if cut < 0 || attempt <= cut {
			return incomplete(fmt.Sprintf("seal seal seal %d", attempt)), nil
		}
		return pageAnswer(ctx, p, req)
	}
	sent = func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
	return respond, sent
}

// TestVisionRetries checks a page whose response isn't completed is sent again, up to the
// request's PageRetries, with the image already prepared, and yielded with its last response and
// its attempts, completed or not, with no error; the other pages are sent once.
func TestVisionRetries(t *testing.T) {
	cases := []struct {
		name          string
		retries, cut  int
		attempts      int
		wantCompleted bool
	}{
		{"completed on a retry", 3, 2, 3, true},
		{"completed on the last retry", 2, 2, 3, true},
		{"retries exhausted", 2, -1, 3, false},
		{"no retries", 0, -1, 1, false},
		{"completed first", 2, 0, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVisionFixture(t, 3)
			f.req.Concurrency = 3
			f.req.PageRetries = tc.retries
			var sent func() int
			f.gen.respond, sent = cutOff(2, tc.cut)
			pages, errs := f.pages(t.Context())
			if len(pages) != 3 || errors.Join(errs...) != nil {
				t.Fatalf("the stream yielded %d pages, %v, want 3 and no error", len(pages), errors.Join(errs...))
			}
			for _, p := range pages {
				want := 1
				if p.Number == 2 {
					want = tc.attempts
				}
				if p.Attempts != want {
					t.Errorf("page %d reports %d attempts, want %d", p.Number, p.Attempts, want)
				}
			}
			if got := sent(); got != tc.attempts {
				t.Errorf("page 2 was sent %d times, want %d", got, tc.attempts)
			}
			p := pages[1]
			if completed := p.Response.Status == protocol.StatusCompleted; completed != tc.wantCompleted {
				t.Errorf("page 2's response is %s, want completed %v", p.Response.Status, tc.wantCompleted)
			}
			if !tc.wantCompleted && text(p.Response) != fmt.Sprintf("seal seal seal %d", tc.attempts) {
				t.Errorf("page 2's response holds %q, want the last attempt's", text(p.Response))
			}
			// The page is rendered once, and every attempt sends its image.
			if r, _ := f.render.rendered(); len(r) != 3 {
				t.Errorf("rendered pages %v, want each page once", r)
			}
			for _, r := range f.gen.requests() {
				if imagePage(r) == 0 {
					t.Errorf("request %+v holds no page image", r)
				}
			}
		})
	}
}

// TestVisionRetryCorrectsOnce checks a retried page's image goes through its corrections once, the
// corrected image re-sent.
func TestVisionRetryCorrectsOnce(t *testing.T) {
	f := newVisionFixture(t, 1)
	corrector, runs := standinCorrector(t)
	f.build(t, corrector)
	f.req.Corrections = []document.Correction{document.Deskew}
	f.req.PageRetries = 2
	f.gen.respond, _ = cutOff(1, 2)
	for p, err := range f.vision.pages(t.Context(), bytes.NewReader(f.pdf), f.req) {
		if err != nil || p.Attempts != 3 {
			t.Fatalf("page %d: %v, %d attempts, want no error and 3 attempts", p.Number, err, p.Attempts)
		}
	}
	if got := runs(); len(got) != 1 {
		t.Errorf("magick ran %d times for a page sent 3 times, want once", len(got))
	}
	reqs := f.gen.requests()
	if len(reqs) != 3 || !reflect.DeepEqual(reqs[0].Input, reqs[2].Input) {
		t.Errorf("sent %d requests, want 3 of the same image", len(reqs))
	}
}

// TestVisionRetryNotOnError checks a page whose Generate fails is never re-sent, its attempts 1,
// its error naming the page and matchable with errors.As, and the stream carries on.
func TestVisionRetryNotOnError(t *testing.T) {
	f := newVisionFixture(t, 3)
	f.req.PageRetries = 3
	tooLarge := &client.APIError{Status: http.StatusRequestEntityTooLarge, Message: "request body too large"}
	f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
		if page == 2 {
			return nil, tooLarge
		}
		return pageAnswer(ctx, page, req)
	}
	pages, errs := f.pages(t.Context())
	if len(pages) != 3 {
		t.Fatalf("the stream yielded %d pages, want 3", len(pages))
	}
	var api *client.APIError
	if !errors.As(errs[1], &api) || api.Status != http.StatusRequestEntityTooLarge || pages[1].Attempts != 1 ||
		!strings.Contains(errs[1].Error(), "page 2 of 3") {
		t.Errorf("page 2: %v, %d attempts, want the 413 naming the page after 1 attempt", errs[1], pages[1].Attempts)
	}
	if errs[0] != nil || errs[2] != nil {
		t.Errorf("pages 1 and 3: %v, %v, want no errors", errs[0], errs[2])
	}
	sent := 0
	for _, r := range f.gen.requests() {
		if imagePage(r) == 2 {
			sent++
		}
	}
	if sent != 1 {
		t.Errorf("page 2 was sent %d times, want once", sent)
	}
}

// TestVisionRetryHoldsSlot checks a page's retries hold its concurrency slot: at a concurrency of
// 1, every attempt of page 1 is sent before page 2 is.
func TestVisionRetryHoldsSlot(t *testing.T) {
	f := newVisionFixture(t, 2)
	f.req.PageRetries = 2
	f.gen.respond, _ = cutOff(1, 2)
	if _, errs := f.pages(t.Context()); errors.Join(errs...) != nil {
		t.Fatal(errors.Join(errs...))
	}
	var order []int
	for _, r := range f.gen.requests() {
		order = append(order, imagePage(r))
	}
	if !slices.Equal(order, []int{1, 1, 1, 2}) {
		t.Errorf("pages sent in the order %v, want page 1's three attempts and then page 2", order)
	}
}

// TestVisionRetryContextEnds checks a ctx that ends stops a page's retries: the stream yields
// ctx's error, and the page isn't sent again.
func TestVisionRetryContextEnds(t *testing.T) {
	f := newVisionFixture(t, 1)
	f.req.PageRetries = 5
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.gen.respond = func(context.Context, int, *protocol.Request) (*protocol.Response, error) {
		cancel()
		return incomplete("seal seal"), nil
	}
	pages, errs := f.pages(ctx)
	if len(pages) != 1 || !errors.Is(errs[0], context.Canceled) {
		t.Errorf("the stream yielded %v, %v, want context.Canceled once", pages, errs)
	}
	if got := len(f.gen.requests()); got != 1 {
		t.Errorf("the page was sent %d times after ctx ended, want once", got)
	}
	assertEmpty(t, f.req.Dir)
}

// TestVisionBreak checks that breaking out of the harness's range cancels the requests in flight
// and returns once they and Images's work have ended, with the spooled PDF removed.
func TestVisionBreak(t *testing.T) {
	f := newVisionFixture(t, 8)
	f.req.Concurrency = 3
	f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
		if page == 1 {
			return pageAnswer(ctx, page, req)
		}
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		return nil, ctx.Err()
	}
	for p, err := range f.vision.pages(t.Context(), bytes.NewReader(f.pdf), f.req) {
		if err != nil || p.Number != 1 {
			t.Fatalf("first yield is page %d, %v, want page 1", p.Number, err)
		}
		break
	}
	if n := f.gen.awaiting(); n != 0 {
		t.Errorf("%d requests still in flight after the range returned, want none", n)
	}
	if now, _ := f.render.flight.counts(); now != 0 {
		t.Errorf("%d renders still in flight after the range returned, want none", now)
	}
	assertEmpty(t, f.req.Dir)
}

// TestVisionRequestCopy checks each page's request is a copy of the template with one user message
// appended, the page's PNG as a data URL with its detail unset, and that the template is unchanged
// and no two requests share an input array.
func TestVisionRequestCopy(t *testing.T) {
	f := newVisionFixture(t, 3)
	f.req.Concurrency = 3
	// Spare capacity in the template's input would let an append share it.
	f.req.Template.Input = slices.Grow(f.req.Template.Input, 4)
	before := *f.req.Template
	before.Input = slices.Clone(f.req.Template.Input)
	before.Native = slices.Clone(f.req.Template.Native)
	if _, errs := f.pages(t.Context()); errors.Join(errs...) != nil {
		t.Fatal(errors.Join(errs...))
	}
	if !reflect.DeepEqual(*f.req.Template, before) {
		t.Errorf("the template changed: %+v, want %+v", *f.req.Template, before)
	}
	reqs := f.gen.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests sent, want 3", len(reqs))
	}
	for _, r := range reqs {
		page := imagePage(r)
		if r == f.req.Template || r.Model != "vision" || r.Instructions != transcribeInstructions ||
			string(r.Native) != `{"temperature":0}` {
			t.Errorf("page %d's request %+v isn't a copy of the template", page, r)
		}
		if len(r.Input) != 2 || !reflect.DeepEqual(r.Input[0], before.Input[0]) {
			t.Fatalf("page %d's input is %+v, want the template's message and one more", page, r.Input)
		}
		want := protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{
			protocol.InputImage{ImageURL: protocol.DataURL("image/png", pagePNG(page))},
		}}
		if !reflect.DeepEqual(r.Input[1], want) {
			t.Errorf("page %d's message is %+v, want %+v", page, r.Input[1], want)
		}
		if &r.Input[0] == &f.req.Template.Input[0] || &r.Native[0] == &f.req.Template.Native[0] {
			t.Errorf("page %d's request shares the template's slices", page)
		}
	}
	if &reqs[0].Input[0] == &reqs[1].Input[0] {
		t.Error("two pages' requests share an input array")
	}
}

// TestVisionPrompt checks a request's Prompt goes beside each page's image: its user message is
// the prompt as a text part and then the image, the template's own input unchanged before it.
func TestVisionPrompt(t *testing.T) {
	f := newVisionFixture(t, 2)
	f.req.Template.Instructions = ""
	f.req.Prompt = transcribeInstructions
	if _, errs := f.pages(t.Context()); errors.Join(errs...) != nil {
		t.Fatal(errors.Join(errs...))
	}
	reqs := f.gen.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests sent, want 2", len(reqs))
	}
	for _, r := range reqs {
		page := imagePage(r)
		want := protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{
			protocol.InputText{Text: transcribeInstructions},
			protocol.InputImage{ImageURL: protocol.DataURL("image/png", pagePNG(page))},
		}}
		if page == 0 || r.Instructions != "" || len(r.Input) != 2 || !reflect.DeepEqual(r.Input[1], want) {
			t.Errorf("page %d's request is %+v, want no instructions and the prompt beside the image", page, r)
		}
	}
}

// TestVisionRegion checks a request's Region reaches Images, so each page sent is the region's
// render.
func TestVisionRegion(t *testing.T) {
	f := newVisionFixture(t, 2)
	footer := document.Region{Top: 0.88, Bottom: 1, Left: 0, Right: 1}
	f.req.Region = &footer
	if _, errs := f.pages(t.Context()); errors.Join(errs...) != nil {
		t.Fatal(errors.Join(errs...))
	}
	if _, regions := f.render.textRead(); !slices.Equal(regions, []document.Region{footer, footer}) {
		t.Errorf("the renderer rendered regions %v, want the footer for each page", regions)
	}
	if got := len(f.gen.requests()); got != 2 {
		t.Errorf("%d requests sent, want 2", got)
	}
}

// TestVisionRefusals checks the harness refuses a request with no template, an invalid one, or a
// negative retry count before anything runs, and passes Images's refusal on as the stream's one
// error.
func TestVisionRefusals(t *testing.T) {
	cases := map[string]func(*visionRequest){
		"no template":      func(r *visionRequest) { r.Template = nil },
		"no model":         func(r *visionRequest) { r.Template.Model = "" },
		"negative retries": func(r *visionRequest) { r.PageRetries = -1 },
		"no directory":     func(r *visionRequest) { r.Dir = "" },
		"negative workers": func(r *visionRequest) { r.Concurrency = -2 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newVisionFixture(t, 3)
			dir := f.req.Dir
			change(&f.req)
			pages, errs := f.pages(t.Context())
			if len(errs) != 1 || errs[0] == nil || pages[0] != (visionPage{}) {
				t.Errorf("the stream yielded %v, %v, want one refusal", pages, errs)
			}
			if len(f.render.spooled) != 0 || len(f.gen.requests()) != 0 {
				t.Error("the refused request counted or sent")
			}
			assertEmpty(t, dir)
		})
	}
}

// TestVisionDocument checks document's final request: a copy of the final template with one user
// message, a "Page N of M:" text part per page in page order, and its result, whose Text joins the
// same parts.
func TestVisionDocument(t *testing.T) {
	f := newVisionFixture(t, 4)
	f.req.Concurrency = 4
	f.render.wait = delays(func(page int) time.Duration { return time.Duration(5-page) * 5 * time.Millisecond })
	finalBefore := *f.req.Final
	res, err := f.vision.document(t.Context(), bytes.NewReader(f.pdf), f.req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*f.req.Final, finalBefore) {
		t.Errorf("the final template changed: %+v, want %+v", *f.req.Final, finalBefore)
	}
	if len(res.Pages) != 4 {
		t.Fatalf("the result holds %d pages, want 4", len(res.Pages))
	}
	for i, p := range res.Pages {
		if p.Number != i+1 || p.Count != 4 || p.Err != nil || text(p.Response) != fmt.Sprintf("text of page %d", i+1) {
			t.Errorf("result page %d is %+v, want page %d of 4 and its answer", i+1, p, i+1)
		}
	}
	if res.Final == nil || text(res.Final) != "summary" {
		t.Errorf("the result's final response is %+v, want the summary", res.Final)
	}
	reqs := f.gen.requests()
	final := reqs[len(reqs)-1]
	want := &protocol.Request{Model: "final", Instructions: "Summarize the document.", Input: []protocol.Item{
		protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentPart{
			protocol.InputText{Text: "Page 1 of 4:\ntext of page 1"},
			protocol.InputText{Text: "Page 2 of 4:\ntext of page 2"},
			protocol.InputText{Text: "Page 3 of 4:\ntext of page 3"},
			protocol.InputText{Text: "Page 4 of 4:\ntext of page 4"},
		}},
	}}
	if !reflect.DeepEqual(final, want) {
		t.Errorf("the final request is %+v, want %+v", final, want)
	}
	if len(reqs) != 5 {
		t.Errorf("%d requests sent, want 4 pages and the final", len(reqs))
	}
	wantText := "Page 1 of 4:\ntext of page 1\n\nPage 2 of 4:\ntext of page 2\n\n" +
		"Page 3 of 4:\ntext of page 3\n\nPage 4 of 4:\ntext of page 4"
	if got := res.Text(); got != wantText {
		t.Errorf("the result's text is %q, want %q", got, wantText)
	}
	var none *docResult
	if got := none.Text(); got != "" {
		t.Errorf("a nil result's text is %q, want empty", got)
	}
}

// TestVisionDocumentFailedPages checks a failed page fails document before the final request, with
// an error naming each failed page, a 413 among them matchable with errors.As, and every page's
// entry in the result. A page whose response isn't completed after its retries fails too, its
// response kept.
func TestVisionDocumentFailedPages(t *testing.T) {
	f := newVisionFixture(t, 5)
	f.req.Concurrency = 2
	f.req.PageRetries = 1
	tooLarge := &client.APIError{Status: http.StatusRequestEntityTooLarge, Message: "request body too large"}
	f.render.fail = map[int]error{2: errors.New("gs: page 2 is damaged")}
	f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
		switch page {
		case 4:
			return nil, tooLarge
		case 5:
			return incomplete("seal seal"), nil
		}
		return pageAnswer(ctx, page, req)
	}
	res, err := f.vision.document(t.Context(), bytes.NewReader(f.pdf), f.req)
	if err == nil || !strings.Contains(err.Error(), "[2 4 5]") || !strings.Contains(err.Error(), "attempts 2") {
		t.Errorf("document returned %v, want an error naming pages 2, 4, and 5, and page 5's 2 attempts", err)
	}
	var api *client.APIError
	if !errors.As(err, &api) || api != tooLarge {
		t.Errorf("document returned %v, want page 4's *client.APIError matchable with errors.As", err)
	}
	if len(res.Pages) != 5 {
		t.Fatalf("the result holds %d pages, want 5", len(res.Pages))
	}
	for i, p := range res.Pages {
		failed := p.Number == 2 || p.Number == 4 || p.Number == 5
		if p.Number != i+1 || p.Count != 5 || (p.Err != nil) != failed {
			t.Errorf("result page %d is %+v, want page %d of 5, failed %v", i+1, p, i+1, failed)
		}
	}
	if res.Pages[4].Response == nil {
		t.Error("page 5's incomplete response isn't kept")
	}
	if res.Final != nil {
		t.Errorf("the result has a final response %+v, want none", res.Final)
	}
	for _, r := range f.gen.requests() {
		if r.Model == "final" {
			t.Error("the final request was sent after a page failed")
		}
	}
}

// TestVisionDocumentFinal checks a final response that isn't completed is an error, sent once and
// never retried, the response kept in the result, and that document refuses a request with no
// final template, or an invalid one, before anything runs.
func TestVisionDocumentFinal(t *testing.T) {
	t.Run("incomplete", func(t *testing.T) {
		f := newVisionFixture(t, 2)
		f.req.PageRetries = 3
		cut := incomplete("sum")
		f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
			if page == 0 {
				return cut, nil
			}
			return pageAnswer(ctx, page, req)
		}
		res, err := f.vision.document(t.Context(), bytes.NewReader(f.pdf), f.req)
		if err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Errorf("document returned %v, want an error for the incomplete final response", err)
		}
		if res == nil || res.Final != cut || len(res.Pages) != 2 {
			t.Errorf("the result is %+v, want both pages and the incomplete final response", res)
		}
		if got := len(f.gen.requests()); got != 3 {
			t.Errorf("%d requests sent, want the two pages and the final, once each", got)
		}
	})
	for name, change := range map[string]func(*visionRequest){
		"no final":       func(r *visionRequest) { r.Final = nil },
		"no final model": func(r *visionRequest) { r.Final.Model = "" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newVisionFixture(t, 2)
			change(&f.req)
			if _, err := f.vision.document(t.Context(), bytes.NewReader(f.pdf), f.req); err == nil {
				t.Error("document succeeded, want a refusal")
			}
			if len(f.render.spooled) != 0 || len(f.gen.requests()) != 0 {
				t.Error("the refused request counted or sent")
			}
		})
	}
}
