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
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	client "github.com/JaimeStill/spike-ai-pipeline-client"
	"github.com/JaimeStill/spike-ai-pipeline-client/protocol"
	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// pagePNG is the stand-in renderer's page: the PNG signature and a tail that names the page.
func pagePNG(page int) []byte {
	return fmt.Appendf([]byte("\x89PNG\r\n\x1a\n"), "page %d", page)
}

// flightLog tracks how many pages are in flight, from a page's rendering to its response, and the
// most there have been at once.
type flightLog struct {
	mu       sync.Mutex
	now, max int
}

func (f *flightLog) enter() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now++
	f.max = max(f.max, f.now)
}

func (f *flightLog) leave() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now--
}

func (f *flightLog) counts() (now, most int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now, f.max
}

// renderer is the stand-in Renderer. Count reads the spooled PDF and returns pages, or countErr.
// Render waits delay(page) or until ctx ends, then writes pagePNG(page), or fails with fail's
// error for the page. Each Render enters flight, and leaves it only when it fails: a page that
// renders leaves when the generator answers it.
type renderer struct {
	pages    int
	countErr error
	fail     map[int]error
	delay    func(page int) time.Duration
	flight   *flightLog

	mu      sync.Mutex
	spooled []string
	pdf     [][]byte
	renders []int
	dpi     []int
}

func (r *renderer) Count(ctx context.Context, pdf string) (int, error) {
	b, err := os.ReadFile(pdf)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.spooled = append(r.spooled, pdf)
	r.pdf = append(r.pdf, b)
	r.mu.Unlock()
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.pages, nil
}

func (r *renderer) Render(ctx context.Context, pdf string, page, dpi int, w io.Writer) error {
	r.flight.enter()
	r.mu.Lock()
	r.renders = append(r.renders, page)
	r.dpi = append(r.dpi, dpi)
	r.mu.Unlock()
	if r.delay != nil {
		select {
		case <-time.After(r.delay(page)):
		case <-ctx.Done():
			r.flight.leave()
			return ctx.Err()
		}
	}
	if err := r.fail[page]; err != nil {
		r.flight.leave()
		return err
	}
	_, err := w.Write(pagePNG(page))
	return err
}

// rendered returns the pages Render was asked for, in the order asked, and the DPIs.
func (r *renderer) rendered() (pages, dpi []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.renders), slices.Clone(r.dpi)
}

// generator is the stand-in Generator. It records every request, then answers with respond,
// which gets the page the request's image is, 0 for a request with no image, such as Document's
// final request. A page's request leaves flight when respond returns, every attempt of it, so a
// test that retries a page doesn't read the flight log.
type generator struct {
	flight  *flightLog
	respond func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error)

	mu   sync.Mutex
	reqs []*protocol.Request
}

func (g *generator) Generate(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	g.mu.Lock()
	g.reqs = append(g.reqs, req)
	g.mu.Unlock()
	page := imagePage(req)
	if page > 0 {
		defer g.flight.leave()
	}
	return g.respond(ctx, page, req)
}

// requests returns the requests the generator was sent, in the order sent.
func (g *generator) requests() []*protocol.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reqs)
}

// imagePage returns the page whose image the request's last message holds, decoded from the data
// URL the stand-in renderer's page makes, or 0 when it holds none.
func imagePage(req *protocol.Request) int {
	if len(req.Input) == 0 {
		return 0
	}
	m, ok := req.Input[len(req.Input)-1].(protocol.Message)
	if !ok || len(m.Content) == 0 {
		return 0
	}
	img, ok := m.Content[0].(protocol.InputImage)
	if !ok {
		return 0
	}
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

// fixture is a Vision over the stand-ins, the PDF it reads, and a request for it.
type fixture struct {
	render *renderer
	gen    *generator
	flight *flightLog
	vision *document.Vision
	pdf    []byte
	req    document.Request
}

// newFixture builds a Vision over a stand-in renderer of pages pages and a stand-in generator
// answering with pageAnswer, and a request for the PDF with its own working directory, a
// template with a developer message, and a final template.
func newFixture(t *testing.T, pages int) *fixture {
	t.Helper()
	flight := &flightLog{}
	f := &fixture{
		render: &renderer{pages: pages, flight: flight},
		gen:    &generator{flight: flight, respond: pageAnswer},
		flight: flight,
		pdf:    []byte("%PDF-1.7 stand-in"),
		req: document.Request{
			Dir: t.TempDir(),
			Template: &protocol.Request{
				Model:        "vision",
				Instructions: document.TranscribeInstructions,
				Input: []protocol.Item{protocol.Message{Role: protocol.RoleDeveloper, Content: []protocol.ContentPart{
					protocol.InputText{Text: "be exact"},
				}}},
				Native: jsontext.Value(`{"temperature":0}`),
			},
			Final: &protocol.Request{Model: "final", Instructions: "Summarize the document."},
		},
	}
	var err error
	f.vision, err = document.NewVision(f.gen, f.render, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// pages ranges over f's stream to its end and returns what it yielded.
func (f *fixture) pages(ctx context.Context) ([]document.Page, []error) {
	var pages []document.Page
	var errs []error
	for p, err := range f.vision.Pages(ctx, bytes.NewReader(f.pdf), f.req) {
		pages = append(pages, p)
		errs = append(errs, err)
	}
	return pages, errs
}

func TestNewVisionRefusals(t *testing.T) {
	if _, err := document.NewVision(nil, &renderer{}, nil); err == nil {
		t.Error("NewVision with no generator succeeded, want an error")
	}
	if _, err := document.NewVision(&generator{}, nil, nil); err == nil {
		t.Error("NewVision with no renderer succeeded, want an error")
	}
}

// TestPagesLazy checks that nothing runs until the stream is ranged over: no read of the PDF, no
// spool, no count, no render, no request.
func TestPagesLazy(t *testing.T) {
	f := newFixture(t, 3)
	read := false
	pdf := readFunc(func(p []byte) (int, error) { read = true; return 0, io.EOF })
	pages := f.vision.Pages(t.Context(), pdf, f.req)
	if read {
		t.Error("Pages read the PDF before the stream was ranged over")
	}
	assertEmpty(t, f.req.Dir)
	if r, _ := f.render.rendered(); len(r) != 0 || len(f.render.spooled) != 0 {
		t.Errorf("Pages counted or rendered before the stream was ranged over")
	}
	if got := f.gen.requests(); len(got) != 0 {
		t.Errorf("Pages sent %d requests before the stream was ranged over", len(got))
	}
	for _, err := range pages {
		if err != nil {
			t.Error(err)
		}
	}
	if !read {
		t.Error("ranging over the stream didn't read the PDF")
	}
}

// readFunc is an io.Reader of a function.
type readFunc func([]byte) (int, error)

func (r readFunc) Read(p []byte) (int, error) { return r(p) }

// TestPagesOrder checks that pages are yielded in page order when later pages finish first: page
// 1 is the slowest to render and page 6 the fastest.
func TestPagesOrder(t *testing.T) {
	f := newFixture(t, 6)
	f.req.Concurrency = 6
	f.render.delay = func(page int) time.Duration { return time.Duration(7-page) * 15 * time.Millisecond }
	pages, errs := f.pages(t.Context())
	if len(pages) != 6 {
		t.Fatalf("the stream yielded %d pages, want 6", len(pages))
	}
	for i, p := range pages {
		if errs[i] != nil {
			t.Errorf("page %d: %v", i+1, errs[i])
			continue
		}
		want := fmt.Sprintf("text of page %d", i+1)
		if p.Number != i+1 || p.Count != 6 || p.Response == nil || text(p.Response) != want {
			t.Errorf("yield %d is page %d of %d answered %q, want page %d of 6 answered %q",
				i+1, p.Number, p.Count, text(p.Response), i+1, want)
		}
	}
	// The renders finished out of order, so the order held is the stream's.
	if got := f.gen.requests(); imagePage(got[0]) == 1 {
		t.Errorf("page 1 was sent first, want a later page first: the delays didn't reorder the work")
	}
}

// TestPagesConcurrency checks the request's concurrency bounds the pages in flight, rendering
// included, and that zero means 1.
func TestPagesConcurrency(t *testing.T) {
	for _, tc := range []struct{ concurrency, want int }{{0, 1}, {1, 1}, {3, 3}} {
		t.Run(fmt.Sprint(tc.concurrency), func(t *testing.T) {
			f := newFixture(t, 10)
			f.req.Concurrency = tc.concurrency
			f.render.delay = func(int) time.Duration { return 5 * time.Millisecond }
			f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
				time.Sleep(5 * time.Millisecond)
				return pageAnswer(ctx, page, req)
			}
			pages, errs := f.pages(t.Context())
			if len(pages) != 10 || errors.Join(errs...) != nil {
				t.Fatalf("the stream yielded %d pages, errors %v, want 10 and none", len(pages), errors.Join(errs...))
			}
			if _, most := f.flight.counts(); most != tc.want {
				t.Errorf("%d pages were in flight at most, want %d", most, tc.want)
			}
		})
	}
}

// TestPagesDPI checks the request's DPI reaches the renderer, and zero means 150.
func TestPagesDPI(t *testing.T) {
	for dpi, want := range map[int]int{0: 150, 300: 300} {
		f := newFixture(t, 2)
		f.req.DPI = dpi
		f.pages(t.Context())
		if _, got := f.render.rendered(); !slices.Equal(got, []int{want, want}) {
			t.Errorf("DPI %d rendered at %v, want %d for each page", dpi, got, want)
		}
	}
}

// TestPagesFailedPage checks that a page that fails in rendering or in sending yields its number,
// the count, and its error, and the stream carries on.
func TestPagesFailedPage(t *testing.T) {
	f := newFixture(t, 5)
	f.req.Concurrency = 2
	renderErr := errors.New("gs: page 2 is damaged")
	sendErr := errors.New("connection reset")
	f.render.fail = map[int]error{2: renderErr}
	f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
		if page == 4 {
			return nil, sendErr
		}
		return pageAnswer(ctx, page, req)
	}
	pages, errs := f.pages(t.Context())
	if len(pages) != 5 {
		t.Fatalf("the stream yielded %d pages, want 5", len(pages))
	}
	for i, p := range pages {
		if p.Number != i+1 || p.Count != 5 {
			t.Errorf("yield %d is page %d of %d, want page %d of 5", i+1, p.Number, p.Count, i+1)
		}
		var want error
		switch p.Number {
		case 2:
			want = renderErr
		case 4:
			want = sendErr
		}
		if want == nil {
			if errs[i] != nil || p.Response == nil {
				t.Errorf("page %d: %v, response %v, want a response", p.Number, errs[i], p.Response)
			}
			continue
		}
		if !errors.Is(errs[i], want) || !strings.Contains(errs[i].Error(), fmt.Sprintf("page %d of 5", p.Number)) {
			t.Errorf("page %d: %v, want an error naming the page and wrapping %v", p.Number, errs[i], want)
		}
		if p.Response != nil {
			t.Errorf("page %d failed and has a response", p.Number)
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

// TestPagesRetries checks a page whose response isn't completed is sent again, up to the
// request's PageRetries, with the image already rendered, and yielded with its last response and
// its attempts, completed or not, with no error; the other pages are sent once.
func TestPagesRetries(t *testing.T) {
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
			f := newFixture(t, 3)
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

// TestPagesRetryCorrectsOnce checks a retried page's image goes through its corrections once, the
// corrected image re-sent.
func TestPagesRetryCorrectsOnce(t *testing.T) {
	f := newFixture(t, 1)
	corrector, runs := standinCorrector(t)
	vision, err := document.NewVision(f.gen, f.render, corrector)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Corrections = []document.Correction{document.Deskew}
	f.req.PageRetries = 2
	f.gen.respond, _ = cutOff(1, 2)
	for p, err := range vision.Pages(t.Context(), bytes.NewReader(f.pdf), f.req) {
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

// TestPagesRetryNotOnError checks a page whose Generate fails is never re-sent, its attempts 1,
// and a page that fails in rendering reports no attempts.
func TestPagesRetryNotOnError(t *testing.T) {
	f := newFixture(t, 3)
	f.req.PageRetries = 3
	tooLarge := &client.APIError{Status: http.StatusRequestEntityTooLarge, Message: "request body too large"}
	f.render.fail = map[int]error{3: errors.New("gs: page 3 is damaged")}
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
	if !errors.As(errs[1], &api) || pages[1].Attempts != 1 {
		t.Errorf("page 2: %v, %d attempts, want the 413 after 1 attempt", errs[1], pages[1].Attempts)
	}
	if errs[2] == nil || pages[2].Attempts != 0 {
		t.Errorf("page 3: %v, %d attempts, want the render's error and no attempts", errs[2], pages[2].Attempts)
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

// TestPagesRetryHoldsSlot checks a page's retries hold its concurrency slot: at a concurrency of
// 1, every attempt of page 1 is sent before page 2 is.
func TestPagesRetryHoldsSlot(t *testing.T) {
	f := newFixture(t, 2)
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

// TestPagesRetryContextEnds checks a ctx that ends stops a page's retries: the stream yields
// ctx's error, and the page isn't sent again.
func TestPagesRetryContextEnds(t *testing.T) {
	f := newFixture(t, 1)
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

// TestPagesTooLarge checks that the package sends a page however large, and the server's 413 is
// the page's error, matchable with errors.As.
func TestPagesTooLarge(t *testing.T) {
	f := newFixture(t, 3)
	f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
		if page == 2 {
			return nil, &client.APIError{Status: http.StatusRequestEntityTooLarge, Message: "request body too large"}
		}
		return pageAnswer(ctx, page, req)
	}
	pages, errs := f.pages(t.Context())
	if len(pages) != 3 {
		t.Fatalf("the stream yielded %d pages, want 3", len(pages))
	}
	var api *client.APIError
	if !errors.As(errs[1], &api) || api.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("page 2: %v, want a *client.APIError with status 413", errs[1])
	}
	if errs[0] != nil || errs[2] != nil {
		t.Errorf("pages 1 and 3: %v, %v, want no errors", errs[0], errs[2])
	}
}

// TestPagesCountFails checks that a failed count yields one error and ends the stream, having
// rendered nothing and removed the spooled PDF.
func TestPagesCountFails(t *testing.T) {
	f := newFixture(t, 3)
	f.render.countErr = errors.New("gs: not a PDF")
	pages, errs := f.pages(t.Context())
	if len(pages) != 1 || pages[0] != (document.Page{}) || !errors.Is(errs[0], f.render.countErr) {
		t.Errorf("the stream yielded %v, %v, want one zero page with the count's error", pages, errs)
	}
	if r, _ := f.render.rendered(); len(r) != 0 {
		t.Errorf("rendered %v after a failed count, want nothing", r)
	}
	assertEmpty(t, f.req.Dir)
}

// TestPagesBreak checks that breaking out of the range cancels the pages in flight and returns
// once their work has ended, with the spooled PDF removed.
func TestPagesBreak(t *testing.T) {
	f := newFixture(t, 8)
	f.req.Concurrency = 3
	f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
		if page == 1 {
			return pageAnswer(ctx, page, req)
		}
		<-ctx.Done() // The later pages wait for the break to cancel them.
		time.Sleep(20 * time.Millisecond)
		return nil, ctx.Err()
	}
	for p, err := range f.vision.Pages(t.Context(), bytes.NewReader(f.pdf), f.req) {
		if err != nil || p.Number != 1 {
			t.Fatalf("first yield is page %d, %v, want page 1", p.Number, err)
		}
		break
	}
	if now, _ := f.flight.counts(); now != 0 {
		t.Errorf("%d pages still in flight after the range returned, want none", now)
	}
	if r, _ := f.render.rendered(); len(r) > 4 {
		t.Errorf("rendered pages %v after the break, want at most the 4 started before it", r)
	}
	assertEmpty(t, f.req.Dir)
}

// TestPagesContextEnds checks that a ctx that ends stops the stream with ctx's error, yielded
// once, whether it ended before the stream began or while pages were in flight.
func TestPagesContextEnds(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		f := newFixture(t, 3)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		pages, errs := f.pages(ctx)
		if len(pages) != 1 || !errors.Is(errs[0], context.Canceled) {
			t.Errorf("the stream yielded %v, %v, want context.Canceled once", pages, errs)
		}
		assertEmpty(t, f.req.Dir)
	})
	t.Run("during", func(t *testing.T) {
		f := newFixture(t, 6)
		f.req.Concurrency = 2
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
			if page == 1 {
				return pageAnswer(ctx, page, req)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		var pages []document.Page
		var errs []error
		for p, err := range f.vision.Pages(ctx, bytes.NewReader(f.pdf), f.req) {
			pages = append(pages, p)
			errs = append(errs, err)
			cancel()
		}
		if len(pages) != 2 || errs[0] != nil || pages[1] != (document.Page{}) || !errors.Is(errs[1], context.Canceled) {
			t.Errorf("the stream yielded %v, %v, want page 1, then context.Canceled once with a zero page", pages, errs)
		}
		if now, _ := f.flight.counts(); now != 0 {
			t.Errorf("%d pages still in flight after the stream ended, want none", now)
		}
		assertEmpty(t, f.req.Dir)
	})
}

// TestPagesRequestCopy checks each page's request is a copy of the template with one user
// message appended, the page's PNG as a data URL with its detail unset, and that the template
// is unchanged and no two requests share an input array.
func TestPagesRequestCopy(t *testing.T) {
	f := newFixture(t, 3)
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
		if r == f.req.Template || r.Model != "vision" || r.Instructions != document.TranscribeInstructions ||
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

// TestPagesCorrections checks a request's corrections run on each page's image before it is
// sent, one magick run a page.
func TestPagesCorrections(t *testing.T) {
	f := newFixture(t, 2)
	corrector, runs := standinCorrector(t)
	vision, err := document.NewVision(f.gen, f.render, corrector)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Corrections = []document.Correction{document.Grayscale, document.Deskew}
	var errs []error
	for _, err := range vision.Pages(t.Context(), bytes.NewReader(f.pdf), f.req) {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil || len(errs) != 2 {
		t.Fatalf("the stream yielded %d pages, %v, want 2 and no error", len(errs), err)
	}
	want := []string{"png:-", "-deskew", "40%", "-colorspace", "Gray", "png:-"}
	got := runs()
	if len(got) != 2 || !slices.Equal(got[0], want) || !slices.Equal(got[1], want) {
		t.Errorf("magick ran %q, want two runs of %q", got, want)
	}
	// The stand-in magick copies its image through, so each page is sent as rendered.
	for _, r := range f.gen.requests() {
		if imagePage(r) == 0 {
			t.Errorf("request %+v holds no page image", r)
		}
	}
}

// TestPagesRefusals checks the requests Pages refuses before anything runs: the refusal is the
// stream's one error, and nothing is read, spooled, rendered, or sent.
func TestPagesRefusals(t *testing.T) {
	cases := map[string]func(*document.Request){
		"no directory":       func(r *document.Request) { r.Dir = "" },
		"no template":        func(r *document.Request) { r.Template = nil },
		"no model":           func(r *document.Request) { r.Template.Model = "" },
		"negative dpi":       func(r *document.Request) { r.DPI = -1 },
		"negative workers":   func(r *document.Request) { r.Concurrency = -2 },
		"negative retries":   func(r *document.Request) { r.PageRetries = -1 },
		"no corrector":       func(r *document.Request) { r.Corrections = []document.Correction{document.Deskew} },
		"unknown correction": func(r *document.Request) { r.Corrections = []document.Correction{0} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 3)
			dir := f.req.Dir
			change(&f.req)
			pages, errs := f.pages(t.Context())
			if len(errs) != 1 || errs[0] == nil || pages[0] != (document.Page{}) {
				t.Errorf("the stream yielded %v, %v, want one refusal", pages, errs)
			}
			if len(f.render.spooled) != 0 || len(f.gen.requests()) != 0 {
				t.Error("the refused request counted or sent")
			}
			assertEmpty(t, dir)
		})
	}
	// A negative DPI is a refusal of its DPI.
	f := newFixture(t, 1)
	f.req.DPI = -1
	if _, errs := f.pages(t.Context()); !errors.Is(errs[0], document.ErrDPI) {
		t.Errorf("negative DPI: %v, want document.ErrDPI", errs[0])
	}
}

// run is one of the entry points, run over f to its end, returning its errors.
type run func(t *testing.T, f *fixture, pdf io.Reader) error

// entries are Pages and Document, for the spool's tests, which hold for both.
var entries = map[string]run{
	"Pages": func(t *testing.T, f *fixture, pdf io.Reader) error {
		var errs []error
		for _, err := range f.vision.Pages(t.Context(), pdf, f.req) {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	},
	"Document": func(t *testing.T, f *fixture, pdf io.Reader) error {
		_, err := f.vision.Document(t.Context(), pdf, f.req)
		return err
	},
}

// TestSpool checks that the PDF is spooled into the caller's directory with the reader's content,
// a file of each call's own, and removed when the call ends.
func TestSpool(t *testing.T) {
	for name, entry := range entries {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 2)
			pdf, err := os.ReadFile(filepath.Join("testdata", "clean.pdf"))
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := entry(t, f, bytes.NewReader(pdf)); err != nil {
					t.Fatal(err)
				}
			}
			if len(f.render.spooled) != 2 || f.render.spooled[0] == f.render.spooled[1] {
				t.Errorf("spooled to %v, want two files", f.render.spooled)
			}
			for i, path := range f.render.spooled {
				if got := filepath.Dir(path); got != f.req.Dir {
					t.Errorf("spooled into %s, want the caller's %s", got, f.req.Dir)
				}
				if !bytes.Equal(f.render.pdf[i], pdf) {
					t.Errorf("spooled %d bytes, want the reader's %d, equal", len(f.render.pdf[i]), len(pdf))
				}
			}
			assertEmpty(t, f.req.Dir)
		})
	}
}

// TestSpoolFailedRead checks that a reader that fails part way is the call's error, and leaves
// nothing in the caller's directory, nothing counted.
func TestSpoolFailedRead(t *testing.T) {
	for name, entry := range entries {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 2)
			readErr := errors.New("connection reset")
			pdf := io.MultiReader(bytes.NewReader([]byte("%PDF-1.7")), iotest.ErrReader(readErr))
			if err := entry(t, f, pdf); !errors.Is(err, readErr) {
				t.Errorf("returned %v, want the reader's %v", err, readErr)
			}
			if len(f.render.spooled) != 0 {
				t.Error("counted pages after a failed read")
			}
			assertEmpty(t, f.req.Dir)
		})
	}
}

// TestSpoolNoDirectory checks that the call needs the caller's directory: an empty one is
// refused, not taken as the system's temporary directory, and a missing one fails.
func TestSpoolNoDirectory(t *testing.T) {
	for name, entry := range entries {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 2)
			f.req.Dir = ""
			if err := entry(t, f, bytes.NewReader(f.pdf)); err == nil {
				t.Error("ran with no directory, want an error")
			}
			f.req.Dir = filepath.Join(t.TempDir(), "missing")
			if err := entry(t, f, bytes.NewReader(f.pdf)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("ran in a missing directory: %v, want fs.ErrNotExist", err)
			}
		})
	}
}

// TestDocument checks Document's final request: a copy of the final template with one user
// message, a "Page N of M:" text part per page in page order, and its result.
func TestDocument(t *testing.T) {
	f := newFixture(t, 4)
	f.req.Concurrency = 4
	f.render.delay = func(page int) time.Duration { return time.Duration(5-page) * 5 * time.Millisecond }
	finalBefore := *f.req.Final
	res, err := f.vision.Document(t.Context(), bytes.NewReader(f.pdf), f.req)
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
}

// TestDocumentTextSharesHeadings checks the result's Text is the final request's parts, the same
// "Page N of M:" headings over the same text, joined by a blank line, for a page's text with
// line breaks of its own.
func TestDocumentTextSharesHeadings(t *testing.T) {
	f := newFixture(t, 3)
	f.gen.respond = func(_ context.Context, page int, _ *protocol.Request) (*protocol.Response, error) {
		if page == 0 {
			return answer("summary"), nil
		}
		return answer(fmt.Sprintf("line one of page %d\nline two\n", page)), nil
	}
	res, err := f.vision.Document(t.Context(), bytes.NewReader(f.pdf), f.req)
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.gen.requests()
	msg := reqs[len(reqs)-1].Input[0].(protocol.Message)
	var parts []string
	for _, p := range msg.Content {
		parts = append(parts, p.(protocol.InputText).Text)
	}
	if got, want := res.Text(), strings.Join(parts, "\n\n"); got != want {
		t.Errorf("the result's text is %q, want the final request's parts joined, %q", got, want)
	}
}

// TestResultText checks Text on a Result Document returned with an error: every page, in page
// order, a page with no response as its heading alone and a page whose response isn't completed
// with the text it holds. A nil Result and one with no pages return "".
func TestResultText(t *testing.T) {
	partial := answer("half of page 3")
	partial.Status = protocol.StatusIncomplete
	res := &document.Result{Pages: []document.PageResult{
		{Number: 1, Count: 3, Response: answer("text of page 1")},
		{Number: 2, Count: 3, Err: errors.New("gs: page 2 is damaged")},
		{Number: 3, Count: 3, Response: partial, Err: errors.New("incomplete")},
	}}
	want := "Page 1 of 3:\ntext of page 1\n\nPage 2 of 3:\n\n\nPage 3 of 3:\nhalf of page 3"
	if got := res.Text(); got != want {
		t.Errorf("Text is %q, want %q", got, want)
	}
	var none *document.Result
	if got := none.Text(); got != "" {
		t.Errorf("a nil Result's text is %q, want empty", got)
	}
	if got := (&document.Result{}).Text(); got != "" {
		t.Errorf("an empty Result's text is %q, want empty", got)
	}
}

// TestDocumentFailedPages checks a failed page fails Document before the final request, with an
// error naming each failed page, a 413 among them matchable with errors.As, and every page's
// entry in the result. A page whose response isn't completed fails too.
func TestDocumentFailedPages(t *testing.T) {
	f := newFixture(t, 5)
	f.req.Concurrency = 2
	tooLarge := &client.APIError{Status: http.StatusRequestEntityTooLarge, Message: "request body too large"}
	f.render.fail = map[int]error{2: errors.New("gs: page 2 is damaged")}
	f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
		switch page {
		case 4:
			return nil, tooLarge
		case 5:
			return &protocol.Response{Status: protocol.StatusIncomplete,
				IncompleteDetails: &protocol.IncompleteDetails{Reason: protocol.ReasonMaxOutputTokens}}, nil
		}
		return pageAnswer(ctx, page, req)
	}
	res, err := f.vision.Document(t.Context(), bytes.NewReader(f.pdf), f.req)
	if err == nil {
		t.Fatal("Document with failed pages succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "[2 4 5]") {
		t.Errorf("Document returned %v, want an error naming pages 2, 4, and 5", err)
	}
	var api *client.APIError
	if !errors.As(err, &api) || api != tooLarge {
		t.Errorf("Document returned %v, want page 4's *client.APIError matchable with errors.As", err)
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

// TestDocumentRetries checks Document's pages are retried as Pages's are: a page completed on a
// retry succeeds, its attempts in the result, and a page incomplete after its retries fails
// Document before the final request, naming the page, its response kept. The final request is
// sent once.
func TestDocumentRetries(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		f := newFixture(t, 3)
		f.req.PageRetries = 1
		f.gen.respond, _ = cutOff(2, 1)
		res, err := f.vision.Document(t.Context(), bytes.NewReader(f.pdf), f.req)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range res.Pages {
			want := 1
			if p.Number == 2 {
				want = 2
			}
			if p.Attempts != want || p.Err != nil {
				t.Errorf("result page %d: %v, %d attempts, want no error and %d", p.Number, p.Err, p.Attempts, want)
			}
		}
		if res.Final == nil || text(res.Final) != "summary" || text(res.Pages[1].Response) != "text of page 2" {
			t.Errorf("the result is %+v, want page 2's completed text and the summary", res)
		}
	})
	t.Run("exhausted", func(t *testing.T) {
		f := newFixture(t, 3)
		f.req.PageRetries = 2
		f.gen.respond, _ = cutOff(2, -1)
		res, err := f.vision.Document(t.Context(), bytes.NewReader(f.pdf), f.req)
		if err == nil || !strings.Contains(err.Error(), "[2]") || !strings.Contains(err.Error(), "attempts 3") {
			t.Errorf("Document returned %v, want an error naming page 2 and its 3 attempts", err)
		}
		if res == nil || len(res.Pages) != 3 || res.Pages[1].Err == nil || res.Pages[1].Attempts != 3 ||
			res.Pages[1].Response == nil || res.Final != nil {
			t.Fatalf("the result is %+v, want page 2 failed after 3 attempts, its response kept, and no final", res)
		}
		for _, r := range f.gen.requests() {
			if r.Model == "final" {
				t.Error("the final request was sent after a page failed")
			}
		}
	})
	t.Run("final not retried", func(t *testing.T) {
		f := newFixture(t, 1)
		f.req.PageRetries = 3
		f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
			if page == 0 {
				return incomplete("sum"), nil
			}
			return pageAnswer(ctx, page, req)
		}
		if _, err := f.vision.Document(t.Context(), bytes.NewReader(f.pdf), f.req); err == nil {
			t.Error("Document with an incomplete final response succeeded, want an error")
		}
		if got := len(f.gen.requests()); got != 2 {
			t.Errorf("%d requests sent, want the page and the final, once each", got)
		}
	})
}

// TestDocumentFinalIncomplete checks a final response that isn't completed is an error, the
// response kept in the result.
func TestDocumentFinalIncomplete(t *testing.T) {
	f := newFixture(t, 2)
	incomplete := &protocol.Response{Status: protocol.StatusIncomplete,
		IncompleteDetails: &protocol.IncompleteDetails{Reason: protocol.ReasonMaxOutputTokens}}
	f.gen.respond = func(ctx context.Context, page int, req *protocol.Request) (*protocol.Response, error) {
		if page == 0 {
			return incomplete, nil
		}
		return pageAnswer(ctx, page, req)
	}
	res, err := f.vision.Document(t.Context(), bytes.NewReader(f.pdf), f.req)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("Document returned %v, want an error for the incomplete final response", err)
	}
	if res == nil || res.Final != incomplete || len(res.Pages) != 2 {
		t.Errorf("the result is %+v, want both pages and the incomplete final response", res)
	}
}

// TestDocumentRefusals checks Document refuses a request with no final template, or an invalid
// one, before anything runs, and a request Pages refuses.
func TestDocumentRefusals(t *testing.T) {
	cases := map[string]func(*document.Request){
		"no final":       func(r *document.Request) { r.Final = nil },
		"no final model": func(r *document.Request) { r.Final.Model = "" },
		"no template":    func(r *document.Request) { r.Template = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 2)
			dir := f.req.Dir
			change(&f.req)
			if _, err := f.vision.Document(t.Context(), bytes.NewReader(f.pdf), f.req); err == nil {
				t.Error("Document succeeded, want a refusal")
			}
			if len(f.render.spooled) != 0 || len(f.gen.requests()) != 0 {
				t.Error("the refused request counted or sent")
			}
			assertEmpty(t, dir)
		})
	}
}

// text returns a response's output text, or "" for nil.
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

// assertEmpty fails the test when dir holds anything.
func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s left behind in the caller's directory", e.Name())
	}
}
