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
// Its exported identifiers:
//
//   - [Generator] sends one request to a model and returns its response; *client.Client
//     satisfies it.
//   - [TranscribeInstructions] asks a vision model for every word on a page, in reading order,
//     as plain text.
//   - [Renderer] counts a PDF's pages and renders one page as PNG at a DPI.
//   - [CheckRender] makes a Renderer's refusals of a DPI and a page.
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
package document
