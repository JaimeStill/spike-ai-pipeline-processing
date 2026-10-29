//go:build linux

// Package document prepares a PDF's pages for a model's vision: a page rendered to PNG, and the
// page image corrected for scan defects, for a model reached through spike-ai-pipeline-client. The
// package builds on Linux only, on the standard library, this module's tool package, and
// spike-ai-pipeline-client; every system tool it runs is a tool.Tool the caller built.
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
// place of a PNG, which Render returns as its error, writing nothing. NewGhostscript takes a gs
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
// image, not only a rendered one.
//
// A Generator is the one method the package needs of spike-ai-pipeline-client, Generate, which
// *client.Client satisfies. TranscribeInstructions is the text of a request for a page's
// transcription, sent in one user message beside the page's image.
//
// A Vision puts the stages together, built at the composition root from a Generator, a Renderer,
// and, optionally, a Corrector. Pages streams a PDF's pages through the model: it spools the PDF,
// read from an io.Reader, into the working directory the Request names, which the caller owns,
// counts its pages, and renders, corrects, and sends each page in a copy of the request's
// template with one user message appended, the page's PNG as a data URL. At most the request's
// concurrency of pages are in flight, and pages come back in page order, a failed page as its
// error, the stream carrying on. The stream is lazy, breaking out of it cancels the work in
// flight and returns once that work has ended, and the spooled PDF is removed when it ends.
// Document runs the pages and then one final request, a copy of the request's final template
// with each page's output text appended as "Page N of M:" parts, and a failed page fails it
// before the final request. The package sets no size limit on a page's image; the server's
// refusal of one is that page's error.
//
// Its exported identifiers:
//
//   - [Generator] sends one request to a model and returns its response; *client.Client
//     satisfies it.
//   - [TranscribeInstructions] asks a vision model for every word on a page, in reading order,
//     as plain text.
//   - [Renderer] counts a PDF's pages and renders one page as PNG at a DPI.
//   - [CheckRender] makes a Renderer's refusals of a DPI and a page.
//   - [Ghostscript] is the released Renderer, over Ghostscript's gs.
//   - [NewGhostscript] builds the released renderer over a gs Tool.
//   - [Ghostscript.Count] counts a PDF's pages with gs.
//   - [Ghostscript.Render] renders one page as PNG with gs.
//   - [GhostscriptSpec] declares Ghostscript's gs for either runner's constructor.
//   - [GhostscriptMinimum] is the oldest gs the renderer accepts, 10.0.
//   - [ErrDPI] and [ErrPageRange] are the refusals' causes: a DPI of zero or less, and a page
//     below 1, or past the last for a caller that holds the count.
//   - [Correction] is one scan correction: [Deskew], [Despeckle], [ContrastStretch], [Unsharp],
//     or [Grayscale].
//   - [Correction.String] returns a correction's name.
//   - [Corrector] is the corrections stage.
//   - [NewCorrector] builds the corrections stage over a magick Tool.
//   - [Corrector.Correct] runs a page image through the corrections a request names.
//   - [MagickSpec] declares ImageMagick's magick for either runner's constructor.
//   - [MagickMinimum] is the oldest magick the stage accepts, 7.1.
//   - [Vision] runs a PDF's pages through a model's vision.
//   - [NewVision] builds a Vision over a Generator, a Renderer, and an optional Corrector.
//   - [Request] is one PDF's run through a Vision: its working directory, page template, DPI,
//     corrections, concurrency, and Document's final template.
//   - [Page] is one page's number, the page count, and the model's response.
//   - [Vision.Pages] streams a PDF's pages through the model, in page order.
//   - [Vision.Document] runs a PDF's pages and then one final request over their text.
//   - [Result] is a Document's pages and its final response.
//   - [PageResult] is one page of a Document with its error.
package document
