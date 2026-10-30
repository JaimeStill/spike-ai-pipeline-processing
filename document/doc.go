//go:build linux

// Package document prepares a PDF's pages for a model's vision: each page rendered to PNG, and
// the page image corrected for scan defects. It provides pre-inference preparation only; a layer
// above sends the prepared pages to a model and owns the requests, retries, and any final step.
// The package builds on Linux only, on the standard library and this module's tool package; every
// system tool it runs is a tool.Tool the caller built.
//
// A Renderer works per page. Count returns a PDF's page count, and Render writes one page, 1-based,
// as PNG at a DPI to a writer, one process per page. A renderer counts pages with a tool from its
// own package. Render refuses a DPI of zero or less and a page below 1 before any tool runs, as
// CheckRender does. It doesn't count the pages: a page past the last gives the rendering tool's own
// error, and a caller that walks a PDF's pages refuses one past the last against its own count.
//
// The released renderer, Ghostscript, is the renderer bake-off's winner. It counts pages with
// gs's own PDF interpreter and renders a page with gs's png16m device, text and graphics
// anti-aliased, to stdout, reading the PDF by its absolute path in the PDF's directory, so
// either runner's Tool sees the same file. gs past the last page exits zero with a message in
// place of a PNG, which Render returns as its error, writing nothing. RenderRegion renders a
// Region of a page, a band in fractions of the page such as a header or footer, in one gs run
// that crops in gs's own PostScript; the optional RegionRenderer interface names it, so the
// Renderer interface stays as it is. Text reads a page's text layer with gs's txtwrite device,
// and the optional TextExtractor interface names it; HasText is the rule for a page having a text
// layer, its text not empty once whitespace is trimmed. NewGhostscript takes a gs
// Tool built through either runner, GhostscriptSpec declaring it, and refuses one older than
// 10.0. Ghostscript is AGPL-licensed, and the package runs it as a separate program, never
// linked.
//
// The corrections stage, a Corrector, runs a page image through ImageMagick's operators for the
// corrections a request names: Deskew (-deskew 40%), Despeckle (-despeckle), ContrastStretch
// (-contrast-stretch 0.5%x0.5%), Unsharp (-unsharp 0x1), and Grayscale (-colorspace Gray), always
// in that order, however the caller lists them. A page is one magick run, PNG on stdin to PNG on
// stdout, and no corrections runs nothing. NewCorrector takes a magick Tool built through either
// runner, MagickSpec declaring it, and refuses one older than 7.1. The stage works on any page
// image, not only a rendered one. Corrections lists them in that order, each with its name,
// String, and its one-line Description for a prompt, and ParseCorrection reads a name back,
// without regard to case. Assess measures a page image for the defects the corrections fix, in
// one magick run: its skew, contrast, noise, sharpness, and color, and its size, an Assessment a
// layer above chooses corrections by.
//
// A Preparer puts the stages together, built at the composition root from a Renderer and,
// optionally, a Corrector. Images streams a PDF's prepared pages: it spools the PDF, read from an
// io.Reader, into the working directory the ImagesRequest names, which the caller owns, counts its
// pages, and renders and corrects each page, yielding a PageImage of its number, the page count,
// and its PNG. A request may read each page's text layer into its PageImage, and skip rendering
// the pages that have one, for the flow of text layer first, vision only for the pages without
// one; and it may render one Region of every page. At most the request's concurrency of pages are being prepared at once, and pages
// come back in page order, a failed page as its number and count with its error, the stream
// carrying on. A refused request, a failed spool or count, or an ended context is the stream's one
// error. The stream is lazy, breaking out of it cancels the work in flight and returns once that
// work has ended, and the spooled PDF is removed when it ends. The package sets no size limit on a
// page's image.
//
// Its exported identifiers:
//
//   - [Renderer] counts a PDF's pages and renders one page as PNG at a DPI.
//   - [CheckRender] makes a Renderer's refusals of a DPI and a page.
//   - [Ghostscript] is the released Renderer, over Ghostscript's gs.
//   - [NewGhostscript] builds the released renderer over a gs Tool.
//   - [Ghostscript.Count] counts a PDF's pages with gs.
//   - [Ghostscript.Render] renders one page as PNG with gs.
//   - [Ghostscript.RenderRegion] renders a region of one page as PNG with gs.
//   - [Ghostscript.Text] reads one page's text layer with gs.
//   - [RegionRenderer] is a Renderer that also renders a region of a page.
//   - [TextExtractor] reads a page's text layer.
//   - [HasText] is the rule for a page having a text layer.
//   - [Region] is a band of a page in fractions of the page.
//   - [CheckRegion] refuses a Region out of range.
//   - [ErrRegion] is a refused Region's cause.
//   - [GhostscriptSpec] declares Ghostscript's gs for either runner's constructor.
//   - [GhostscriptMinimum] is the oldest gs the renderer accepts, 10.0.
//   - [ErrDPI] and [ErrPageRange] are the refusals' causes: a DPI of zero or less, and a page
//     below 1, or past the last for a caller that holds the count.
//   - [Correction] is one scan correction: [Deskew], [Despeckle], [ContrastStretch], [Unsharp],
//     or [Grayscale].
//   - [Correction.String] returns a correction's name.
//   - [Correction.Description] returns a correction's one-line description for a prompt.
//   - [Corrections] lists every correction in the fixed order.
//   - [ParseCorrection] returns the correction a name names, without regard to case.
//   - [Corrector] is the corrections stage.
//   - [NewCorrector] builds the corrections stage over a magick Tool.
//   - [Corrector.Correct] runs a page image through the corrections a request names.
//   - [Corrector.Assess] measures a page image for the defects the corrections fix.
//   - [Assessment] is a page image's measures: size, skew, contrast, noise, sharpness, and color.
//   - [GrayscaleColored] is the most of a page that may be colored for it to be grayscale.
//   - [MagickSpec] declares ImageMagick's magick for either runner's constructor.
//   - [MagickMinimum] is the oldest magick the stage accepts, 7.1.
//   - [Preparer] prepares a PDF's pages for a model's vision.
//   - [NewPreparer] builds a Preparer over a Renderer and an optional Corrector.
//   - [ImagesRequest] is one PDF's run through Images: its working directory, DPI, corrections,
//     concurrency, text layers, and region.
//   - [PageImage] is one prepared page's number, the page count, its PNG, and its text layer.
//   - [Preparer.Images] streams a PDF's prepared pages, in page order.
package document
