//go:build linux

package document_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/JaimeStill/spike-ai-pipeline-processing/document"
)

// pagePNG is the stand-in renderer's page: the PNG signature and a tail that names the page.
func pagePNG(page int) []byte {
	return fmt.Appendf([]byte("\x89PNG\r\n\x1a\n"), "page %d", page)
}

// flightLog tracks how many pages are being rendered at once, and the most there have been.
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

// renderer is the stand-in Renderer, a RegionRenderer and a TextExtractor too. Count reads the
// spooled PDF and returns pages, or countErr. Render calls wait for the page when it is set,
// failing with its error, then writes pagePNG(page), or fails with fail's error for the page.
// Each Render is in flight from its call to its return. RenderRegion logs the region and renders
// as Render does. Text returns texts' text for the page, or textErr's error for it.
type renderer struct {
	pages    int
	countErr error
	fail     map[int]error
	wait     func(ctx context.Context, page int) error
	flight   *flightLog
	texts    map[int]string
	textErr  map[int]error

	mu       sync.Mutex
	spooled  []string
	pdf      [][]byte
	renders  []int
	finished []int
	dpi      []int
	regions  []document.Region
	read     []int
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
	defer r.flight.leave()
	r.mu.Lock()
	r.renders = append(r.renders, page)
	r.dpi = append(r.dpi, dpi)
	r.mu.Unlock()
	if r.wait != nil {
		if err := r.wait(ctx, page); err != nil {
			return err
		}
	}
	defer func() {
		r.mu.Lock()
		r.finished = append(r.finished, page)
		r.mu.Unlock()
	}()
	if err := r.fail[page]; err != nil {
		return err
	}
	_, err := w.Write(pagePNG(page))
	return err
}

func (r *renderer) RenderRegion(ctx context.Context, pdf string, page, dpi int, region document.Region, w io.Writer) error {
	r.mu.Lock()
	r.regions = append(r.regions, region)
	r.mu.Unlock()
	return r.Render(ctx, pdf, page, dpi, w)
}

func (r *renderer) Text(ctx context.Context, pdf string, page int) (string, error) {
	r.mu.Lock()
	r.read = append(r.read, page)
	r.mu.Unlock()
	if err := r.textErr[page]; err != nil {
		return "", err
	}
	return r.texts[page], nil
}

// textRead returns the pages Text was asked for, sorted, and the regions RenderRegion was given.
func (r *renderer) textRead() (pages []int, regions []document.Region) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(slices.Values(r.read)), slices.Clone(r.regions)
}

// plain is a Renderer that is neither a RegionRenderer nor a TextExtractor: embedding the
// interface hides the stand-in's other methods.
type plain struct{ document.Renderer }

// rendered returns the pages Render was asked for, in the order asked, and the DPIs.
func (r *renderer) rendered() (pages, dpi []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.renders), slices.Clone(r.dpi)
}

// finishOrder returns the pages whose Render finished, in the order they finished.
func (r *renderer) finishOrder() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.finished)
}

// delays is a renderer's wait of delay(page) for each page, cut short when ctx ends.
func delays(delay func(page int) time.Duration) func(context.Context, int) error {
	return func(ctx context.Context, page int) error {
		select {
		case <-time.After(delay(page)):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// fixture is a Preparer over the stand-in renderer, the PDF it reads, and a request for it.
type fixture struct {
	render *renderer
	flight *flightLog
	prep   *document.Preparer
	pdf    []byte
	req    document.ImagesRequest
}

// newFixture builds a Preparer over a stand-in renderer of pages pages, with no corrections
// stage, and a request for the PDF with its own working directory.
func newFixture(t *testing.T, pages int) *fixture {
	t.Helper()
	flight := &flightLog{}
	f := &fixture{
		render: &renderer{pages: pages, flight: flight},
		flight: flight,
		pdf:    []byte("%PDF-1.7 stand-in"),
		req:    document.ImagesRequest{Dir: t.TempDir()},
	}
	var err error
	f.prep, err = document.NewPreparer(f.render, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// withCorrector rebuilds f's Preparer over the stand-in magick's corrections stage, and returns
// the stand-in's run log.
func (f *fixture) withCorrector(t *testing.T) func() [][]string {
	t.Helper()
	corrector, runs := standinCorrector(t)
	var err error
	f.prep, err = document.NewPreparer(f.render, corrector)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

// images ranges over f's stream to its end and returns what it yielded.
func (f *fixture) images(ctx context.Context) ([]document.PageImage, []error) {
	var pages []document.PageImage
	var errs []error
	for p, err := range f.prep.Images(ctx, bytes.NewReader(f.pdf), f.req) {
		pages = append(pages, p)
		errs = append(errs, err)
	}
	return pages, errs
}

// zero reports whether p is the zero PageImage, which a stream's own error carries.
func zero(p document.PageImage) bool { return p.Number == 0 && p.Count == 0 && p.PNG == nil }

func TestNewPreparerRefusals(t *testing.T) {
	if _, err := document.NewPreparer(nil, nil); err == nil {
		t.Error("NewPreparer with no renderer succeeded, want an error")
	}
}

// TestImagesLazy checks that nothing runs until the stream is ranged over: no read of the PDF, no
// spool, no count, no render.
func TestImagesLazy(t *testing.T) {
	f := newFixture(t, 3)
	read := false
	pdf := readFunc(func(p []byte) (int, error) { read = true; return 0, io.EOF })
	pages := f.prep.Images(t.Context(), pdf, f.req)
	if read {
		t.Error("Images read the PDF before the stream was ranged over")
	}
	assertEmpty(t, f.req.Dir)
	if r, _ := f.render.rendered(); len(r) != 0 || len(f.render.spooled) != 0 {
		t.Errorf("Images counted or rendered before the stream was ranged over")
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

// TestImagesOrder checks that pages are yielded in page order, each with its own PNG, when later
// pages finish first: page 1 is the slowest to render and page 6 the fastest.
func TestImagesOrder(t *testing.T) {
	f := newFixture(t, 6)
	f.req.Concurrency = 6
	f.render.wait = delays(func(page int) time.Duration { return time.Duration(7-page) * 15 * time.Millisecond })
	pages, errs := f.images(t.Context())
	if len(pages) != 6 {
		t.Fatalf("the stream yielded %d pages, want 6", len(pages))
	}
	for i, p := range pages {
		if errs[i] != nil {
			t.Errorf("page %d: %v", i+1, errs[i])
			continue
		}
		if p.Number != i+1 || p.Count != 6 || !bytes.Equal(p.PNG, pagePNG(i+1)) {
			t.Errorf("yield %d is page %d of %d with %q, want page %d of 6 with %q",
				i+1, p.Number, p.Count, p.PNG, i+1, pagePNG(i+1))
		}
	}
	// The renders finished out of order, so the order held is the stream's.
	if got := f.render.finishOrder(); len(got) == 0 || got[0] == 1 {
		t.Errorf("renders finished in the order %v, want a later page first: the delays didn't reorder the work", got)
	}
}

// TestImagesConcurrency checks the request's concurrency bounds the pages being prepared, and
// that zero means 1.
func TestImagesConcurrency(t *testing.T) {
	for _, tc := range []struct{ concurrency, want int }{{0, 1}, {1, 1}, {3, 3}} {
		t.Run(fmt.Sprint(tc.concurrency), func(t *testing.T) {
			f := newFixture(t, 10)
			f.req.Concurrency = tc.concurrency
			f.render.wait = delays(func(int) time.Duration { return 10 * time.Millisecond })
			pages, errs := f.images(t.Context())
			if len(pages) != 10 || errors.Join(errs...) != nil {
				t.Fatalf("the stream yielded %d pages, errors %v, want 10 and none", len(pages), errors.Join(errs...))
			}
			if _, most := f.flight.counts(); most != tc.want {
				t.Errorf("%d pages were in flight at most, want %d", most, tc.want)
			}
		})
	}
}

// TestImagesDPI checks the request's DPI reaches the renderer, and zero means 150.
func TestImagesDPI(t *testing.T) {
	for dpi, want := range map[int]int{0: 150, 300: 300} {
		f := newFixture(t, 2)
		f.req.DPI = dpi
		f.images(t.Context())
		if _, got := f.render.rendered(); !slices.Equal(got, []int{want, want}) {
			t.Errorf("DPI %d rendered at %v, want %d for each page", dpi, got, want)
		}
	}
}

// TestImagesFailedPage checks that a page that fails in rendering yields its number, the count,
// no image, and its error, naming the page, and the stream carries on.
func TestImagesFailedPage(t *testing.T) {
	f := newFixture(t, 5)
	f.req.Concurrency = 2
	renderErr := errors.New("gs: page is damaged")
	f.render.fail = map[int]error{2: renderErr, 4: renderErr}
	pages, errs := f.images(t.Context())
	if len(pages) != 5 {
		t.Fatalf("the stream yielded %d pages, want 5", len(pages))
	}
	for i, p := range pages {
		if p.Number != i+1 || p.Count != 5 {
			t.Errorf("yield %d is page %d of %d, want page %d of 5", i+1, p.Number, p.Count, i+1)
		}
		if p.Number != 2 && p.Number != 4 {
			if errs[i] != nil || !bytes.Equal(p.PNG, pagePNG(p.Number)) {
				t.Errorf("page %d: %v, %q, want its image", p.Number, errs[i], p.PNG)
			}
			continue
		}
		if !errors.Is(errs[i], renderErr) || !strings.Contains(errs[i].Error(), fmt.Sprintf("page %d of 5: render", p.Number)) {
			t.Errorf("page %d: %v, want an error naming the page and wrapping %v", p.Number, errs[i], renderErr)
		}
		if p.PNG != nil {
			t.Errorf("page %d failed and has an image", p.Number)
		}
	}
}

// TestImagesCorrectFails checks that a page whose corrections fail yields its number and count
// with an error naming the page, and the stream carries on to the next page.
func TestImagesCorrectFails(t *testing.T) {
	f := newFixture(t, 3)
	f.withCorrector(t)
	t.Setenv(failEnv, "magick: no decode delegate")
	f.req.Corrections = []document.Correction{document.Deskew}
	pages, errs := f.images(t.Context())
	if len(pages) != 3 {
		t.Fatalf("the stream yielded %d pages, want 3", len(pages))
	}
	for i, p := range pages {
		if p.Number != i+1 || p.Count != 3 || p.PNG != nil ||
			errs[i] == nil || !strings.Contains(errs[i].Error(), fmt.Sprintf("page %d of 3: correct", i+1)) {
			t.Errorf("yield %d is page %d of %d, %v, want page %d's correction error", i+1, p.Number, p.Count, errs[i], i+1)
		}
	}
}

// TestImagesCountFails checks that a failed count yields one error and ends the stream, having
// rendered nothing and removed the spooled PDF.
func TestImagesCountFails(t *testing.T) {
	f := newFixture(t, 3)
	f.render.countErr = errors.New("gs: not a PDF")
	pages, errs := f.images(t.Context())
	if len(pages) != 1 || !zero(pages[0]) || !errors.Is(errs[0], f.render.countErr) {
		t.Errorf("the stream yielded %v, %v, want one zero page with the count's error", pages, errs)
	}
	if r, _ := f.render.rendered(); len(r) != 0 {
		t.Errorf("rendered %v after a failed count, want nothing", r)
	}
	assertEmpty(t, f.req.Dir)
}

// TestImagesBreak checks that breaking out of the range cancels the pages in flight and returns
// once their work has ended, with the spooled PDF removed.
func TestImagesBreak(t *testing.T) {
	f := newFixture(t, 8)
	f.req.Concurrency = 3
	f.render.wait = func(ctx context.Context, page int) error {
		if page == 1 {
			return nil
		}
		<-ctx.Done() // The later pages wait for the break to cancel them.
		time.Sleep(20 * time.Millisecond)
		return ctx.Err()
	}
	for p, err := range f.prep.Images(t.Context(), bytes.NewReader(f.pdf), f.req) {
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

// TestImagesContextEnds checks that a ctx that ends stops the stream with ctx's error, yielded
// once, whether it ended before the stream began or while pages were in flight.
func TestImagesContextEnds(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		f := newFixture(t, 3)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		pages, errs := f.images(ctx)
		if len(pages) != 1 || !errors.Is(errs[0], context.Canceled) {
			t.Errorf("the stream yielded %v, %v, want context.Canceled once", pages, errs)
		}
		if len(f.render.spooled) != 0 {
			t.Error("counted pages with ctx already ended")
		}
		assertEmpty(t, f.req.Dir)
	})
	t.Run("during", func(t *testing.T) {
		f := newFixture(t, 6)
		f.req.Concurrency = 2
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.render.wait = func(ctx context.Context, page int) error {
			if page == 1 {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		}
		var pages []document.PageImage
		var errs []error
		for p, err := range f.prep.Images(ctx, bytes.NewReader(f.pdf), f.req) {
			pages = append(pages, p)
			errs = append(errs, err)
			cancel()
		}
		if len(pages) != 2 || errs[0] != nil || !zero(pages[1]) || !errors.Is(errs[1], context.Canceled) {
			t.Errorf("the stream yielded %v, %v, want page 1, then context.Canceled once with a zero page", pages, errs)
		}
		if now, _ := f.flight.counts(); now != 0 {
			t.Errorf("%d pages still in flight after the stream ended, want none", now)
		}
		assertEmpty(t, f.req.Dir)
	})
}

// TestImagesCorrections checks a request's corrections run on each page's image, one magick run a
// page, and the corrected image is the one yielded.
func TestImagesCorrections(t *testing.T) {
	f := newFixture(t, 2)
	runs := f.withCorrector(t)
	f.req.Corrections = []document.Correction{document.Grayscale, document.Deskew}
	pages, errs := f.images(t.Context())
	if err := errors.Join(errs...); err != nil || len(pages) != 2 {
		t.Fatalf("the stream yielded %d pages, %v, want 2 and no error", len(pages), err)
	}
	want := []string{"png:-", "-deskew", "40%", "-colorspace", "Gray", "png:-"}
	got := runs()
	if len(got) != 2 || !slices.Equal(got[0], want) || !slices.Equal(got[1], want) {
		t.Errorf("magick ran %q, want two runs of %q", got, want)
	}
	// The stand-in magick copies its image through, so each page is yielded as rendered.
	for _, p := range pages {
		if !bytes.Equal(p.PNG, pagePNG(p.Number)) {
			t.Errorf("page %d is %q, want magick's output, %q", p.Number, p.PNG, pagePNG(p.Number))
		}
	}
}

// TestImagesNoCorrectionsRunsNothing checks a request that names no corrections runs no magick,
// even on a Preparer with a corrections stage.
func TestImagesNoCorrectionsRunsNothing(t *testing.T) {
	f := newFixture(t, 2)
	runs := f.withCorrector(t)
	if _, errs := f.images(t.Context()); errors.Join(errs...) != nil {
		t.Fatal(errors.Join(errs...))
	}
	if got := runs(); len(got) != 0 {
		t.Errorf("magick ran %q for a request with no corrections, want nothing", got)
	}
}

// TestImagesRefusals checks the requests Images refuses before anything runs: the refusal is the
// stream's one error, and nothing is read, spooled, or rendered.
func TestImagesRefusals(t *testing.T) {
	cases := map[string]func(*document.ImagesRequest){
		"no directory":       func(r *document.ImagesRequest) { r.Dir = "" },
		"negative dpi":       func(r *document.ImagesRequest) { r.DPI = -1 },
		"negative workers":   func(r *document.ImagesRequest) { r.Concurrency = -2 },
		"no corrector":       func(r *document.ImagesRequest) { r.Corrections = []document.Correction{document.Deskew} },
		"unknown correction": func(r *document.ImagesRequest) { r.Corrections = []document.Correction{0} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 3)
			dir := f.req.Dir
			change(&f.req)
			read := false
			pdf := readFunc(func(p []byte) (int, error) { read = true; return 0, io.EOF })
			var errs []error
			for p, err := range f.prep.Images(t.Context(), pdf, f.req) {
				if !zero(p) {
					t.Errorf("the refused stream yielded page %+v", p)
				}
				errs = append(errs, err)
			}
			if len(errs) != 1 || errs[0] == nil {
				t.Errorf("the stream yielded %v, want one refusal", errs)
			}
			if read || len(f.render.spooled) != 0 {
				t.Error("the refused request read or counted")
			}
			assertEmpty(t, dir)
		})
	}
	// A negative DPI is a refusal of its DPI.
	f := newFixture(t, 1)
	f.req.DPI = -1
	if _, errs := f.images(t.Context()); !errors.Is(errs[0], document.ErrDPI) {
		t.Errorf("negative DPI: %v, want document.ErrDPI", errs[0])
	}
}

// drain ranges over f's stream for pdf to its end, returning its errors joined.
func (f *fixture) drain(t *testing.T, pdf io.Reader) error {
	var errs []error
	for _, err := range f.prep.Images(t.Context(), pdf, f.req) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// TestSpool checks that the PDF is spooled into the caller's directory with the reader's content,
// a file of each call's own, and removed when the call ends.
func TestSpool(t *testing.T) {
	f := newFixture(t, 2)
	pdf, err := os.ReadFile(filepath.Join("testdata", "clean.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.drain(t, bytes.NewReader(pdf)); err != nil {
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
}

// TestSpoolFailedRead checks that a reader that fails part way is the stream's error, and leaves
// nothing in the caller's directory, nothing counted.
func TestSpoolFailedRead(t *testing.T) {
	f := newFixture(t, 2)
	readErr := errors.New("connection reset")
	pdf := io.MultiReader(bytes.NewReader([]byte("%PDF-1.7")), iotest.ErrReader(readErr))
	if err := f.drain(t, pdf); !errors.Is(err, readErr) {
		t.Errorf("returned %v, want the reader's %v", err, readErr)
	}
	if len(f.render.spooled) != 0 {
		t.Error("counted pages after a failed read")
	}
	assertEmpty(t, f.req.Dir)
}

// TestSpoolNoDirectory checks that the stream needs the caller's directory: an empty one is
// refused, not taken as the system's temporary directory, and a missing one fails.
func TestSpoolNoDirectory(t *testing.T) {
	f := newFixture(t, 2)
	f.req.Dir = ""
	if err := f.drain(t, bytes.NewReader(f.pdf)); err == nil {
		t.Error("ran with no directory, want an error")
	}
	f.req.Dir = filepath.Join(t.TempDir(), "missing")
	if err := f.drain(t, bytes.NewReader(f.pdf)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ran in a missing directory: %v, want fs.ErrNotExist", err)
	}
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

// TestImagesText checks a request with Text reads every page's text layer into its PageImage,
// blank or not, and renders every page as usual.
func TestImagesText(t *testing.T) {
	f := newFixture(t, 3)
	f.req.Concurrency = 2
	f.req.Text = true
	f.render.texts = map[int]string{1: "page one\n", 3: " \n\f"}
	pages, errs := f.images(t.Context())
	if err := errors.Join(errs...); err != nil || len(pages) != 3 {
		t.Fatalf("the stream yielded %d pages, %v, want 3 and no error", len(pages), err)
	}
	for _, p := range pages {
		if p.Text != f.render.texts[p.Number] || !bytes.Equal(p.PNG, pagePNG(p.Number)) {
			t.Errorf("page %d is %q with %q, want %q with its image", p.Number, p.Text, p.PNG, f.render.texts[p.Number])
		}
	}
	if read, _ := f.render.textRead(); !slices.Equal(read, []int{1, 2, 3}) {
		t.Errorf("read the text of pages %v, want 1 to 3", read)
	}
	if r, _ := f.render.rendered(); len(r) != 3 {
		t.Errorf("rendered pages %v, want all 3", r)
	}
}

// TestImagesSkipText checks a request with SkipText yields a page with a text layer with its text
// and no image, unrendered and uncorrected, and renders and corrects a page without one.
func TestImagesSkipText(t *testing.T) {
	f := newFixture(t, 4)
	runs := f.withCorrector(t)
	f.req.Concurrency = 2
	f.req.SkipText = true
	f.req.Corrections = []document.Correction{document.Deskew}
	f.render.texts = map[int]string{1: "The Harbor Survey\n", 2: "  \n\n", 4: "Page 4"}
	pages, errs := f.images(t.Context())
	if err := errors.Join(errs...); err != nil || len(pages) != 4 {
		t.Fatalf("the stream yielded %d pages, %v, want 4 and no error", len(pages), err)
	}
	for _, p := range pages {
		text := f.render.texts[p.Number]
		if p.Text != text {
			t.Errorf("page %d's text is %q, want %q", p.Number, p.Text, text)
		}
		if document.HasText(text) && p.PNG != nil {
			t.Errorf("page %d has a text layer and an image %q, want none", p.Number, p.PNG)
		}
		if !document.HasText(text) && !bytes.Equal(p.PNG, pagePNG(p.Number)) {
			t.Errorf("page %d has no text layer and image %q, want its corrected image", p.Number, p.PNG)
		}
	}
	r, _ := f.render.rendered()
	if slices.Sort(r); !slices.Equal(r, []int{2, 3}) {
		t.Errorf("rendered pages %v, want those without a text layer, 2 and 3", r)
	}
	if got := len(runs()); got != 2 {
		t.Errorf("magick ran %d times, want once for each of the 2 rendered pages", got)
	}
}

// TestImagesTextFails checks that a page whose text fails yields its number and count with an
// error naming the page, unrendered, and the stream carries on.
func TestImagesTextFails(t *testing.T) {
	f := newFixture(t, 3)
	f.req.Text = true
	textErr := errors.New("gs: /undefined in --runpdf--")
	f.render.textErr = map[int]error{2: textErr}
	pages, errs := f.images(t.Context())
	if len(pages) != 3 {
		t.Fatalf("the stream yielded %d pages, want 3", len(pages))
	}
	if p := pages[1]; p.Number != 2 || p.Count != 3 || p.PNG != nil || p.Text != "" ||
		!errors.Is(errs[1], textErr) || !strings.Contains(errs[1].Error(), "page 2 of 3: text") {
		t.Errorf("page 2 is %+v, %v, want its text's error naming the page", p, errs[1])
	}
	for _, i := range []int{0, 2} {
		if errs[i] != nil || pages[i].PNG == nil {
			t.Errorf("page %d: %v, want its image", i+1, errs[i])
		}
	}
	if r, _ := f.render.rendered(); slices.Contains(r, 2) {
		t.Errorf("rendered page 2 after its text failed")
	}
}

// TestImagesRegion checks a request with a Region renders that region of every page through
// RenderRegion, and that the stream reads its own copy of the region.
func TestImagesRegion(t *testing.T) {
	f := newFixture(t, 3)
	region := document.Region{Top: 0.9, Bottom: 1, Left: 0, Right: 1}
	f.req.Region = &region
	// The caller changes its region as page 1 renders, one page at a time, so pages 2 and 3
	// would render the change if the stream read the caller's region.
	f.render.wait = func(ctx context.Context, page int) error {
		region.Top = 0.5
		return nil
	}
	pages, errs := f.images(t.Context())
	if err := errors.Join(errs...); err != nil || len(pages) != 3 {
		t.Fatalf("the stream yielded %d pages, %v, want 3 and no error", len(pages), err)
	}
	want := document.Region{Top: 0.9, Bottom: 1, Left: 0, Right: 1}
	_, regions := f.render.textRead()
	if len(regions) != 3 || regions[0] != want || regions[1] != want || regions[2] != want {
		t.Errorf("RenderRegion was given %v, want %+v for each of 3 pages", regions, want)
	}
}

// TestImagesRefusesCapabilities checks the requests a Preparer refuses for what its Renderer
// can't do, before anything runs: a Region from a renderer that is no RegionRenderer, a region out
// of range, and Text or SkipText from one that is no TextExtractor.
func TestImagesRefusesCapabilities(t *testing.T) {
	bad := document.Region{Top: 0.5, Bottom: 0.4, Left: 0, Right: 1}
	footer := document.Region{Top: 0.9, Bottom: 1, Left: 0, Right: 1}
	cases := []struct {
		name   string
		plain  bool
		change func(*document.ImagesRequest)
		want   error
	}{
		{"region out of range", false, func(r *document.ImagesRequest) { r.Region = &bad }, document.ErrRegion},
		{"region, plain renderer", true, func(r *document.ImagesRequest) { r.Region = &footer }, nil},
		{"text, plain renderer", true, func(r *document.ImagesRequest) { r.Text = true }, nil},
		{"skip text, plain renderer", true, func(r *document.ImagesRequest) { r.SkipText = true }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, 3)
			if tc.plain {
				var err error
				if f.prep, err = document.NewPreparer(plain{f.render}, nil); err != nil {
					t.Fatal(err)
				}
			}
			tc.change(&f.req)
			pages, errs := f.images(t.Context())
			if len(errs) != 1 || errs[0] == nil || !zero(pages[0]) || (tc.want != nil && !errors.Is(errs[0], tc.want)) {
				t.Errorf("the stream yielded %v, %v, want one refusal", pages, errs)
			}
			if len(f.render.spooled) != 0 {
				t.Error("the refused request counted")
			}
			assertEmpty(t, f.req.Dir)
		})
	}
}
