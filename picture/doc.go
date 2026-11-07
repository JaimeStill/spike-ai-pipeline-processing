//go:build linux

// Package picture converts pictures to PNG for a model's vision and corrects page images for scan
// defects. It provides pre-inference preparation only; a layer above sends what it returns to a
// model. The package builds on Linux only, on the standard library and this module's tool and
// route packages; every program it runs is a tool.Tool the caller built: ImageMagick's magick,
// MagickSpec declaring it, minimum 7.1, and LibreOffice's soffice, LibreOfficeSpec declaring it,
// minimum 24.2, which the container runner runs in LibreOfficeImage under its full lockdown.
//
// A Converter, built over a Config of the two Tools, converts the kinds a vision model decodes
// only once converted, route.VisionConverted's TIFF, EMF, WMF, SVG, HEIC, WebP, and AVIF, to PNG,
// and no conversion encodes a picture lossily: each writes PNG, without the time of the run, so a
// picture converted twice is the same bytes both times. A TIFF's first image, the one an Office
// application displays, is converted through magick; ConvertPages converts a standalone TIFF's
// every page instead, one PNG each, in one magick run. A HEIC's primary image, never a thumbnail
// or another image, and an animated WebP's or AVIF's first frame are converted through magick,
// read through the kind's own coder and turned upright by their stored orientation: libheif
// applies a HEIC's or AVIF's rotation and mirror, and -auto-orient a WebP's EXIF orientation.
// NewConverter runs magick's format listing, magick -list format, once, and a HEIC, WebP, or AVIF
// whose reader the listing doesn't mark readable, as when ImageMagick was built without libheif
// or libwebp, is refused with ErrNoReader. An EMF or WMF is rendered through soffice's
// Draw PNG export as an A4 page at a scale times 96 dpi, DefaultMetafileScale, 3, by default and
// at most MaxMetafileScale, 8, the run's profile giving LibreOffice a British English locale so
// the page is A4 whatever the system's paper, and the page is trimmed to the picture through
// magick: what differs from the page corners' color. An SVG is rendered at SVGDensity, 150 dpi,
// through magick's rsvg coder, librsvg, named so that magick never falls back to its own
// renderer or a delegate, and flattened onto white.
//
// Each picture is converted in a directory of its own: it is copied, under its PNG's stem and its
// kind's extension, into a fresh directory in the caller's working directory, which holds the copy
// alone, and its converter reads the copy, so an SVG's relative references find nothing; the
// directory is removed however the conversion ends, and the picture's own file is never changed.
// librsvg runs no script, an SVG's script element and event handlers included, fetches no URL
// over a network, and loads no file but one beside the SVG it reads, which here is none: a
// script-bearing or referencing SVG converts to the PNG of what it draws itself.
//
// Convert converts one picture, its kind told by its content's signature as route.Identify tells
// it, never by its name, and writes its PNG into the working directory, named by the lowercase hex
// SHA-256 of the picture's bytes and .png, so a picture mail or office named by its SHA-256 gives
// the PNG of its own stem. ConvertPages converts one picture as Convert does, but a TIFF to a PNG
// per page, each named by the TIFF's SHA-256, -page, its number from 1, and .png, returned in page
// order: the dispatch package's picture stage converts a standalone picture through it. ConvertBatch converts several at once, each read as the kind its
// Picture names and written to the PNG it names, the EMFs and WMFs in one soffice run: the office
// package converts a document's pictures through it. A picture whose converter the Converter
// wasn't given is refused before anything runs, an EMF or WMF without LibreOffice with
// ErrNoLibreOffice, any picture without magick with ErrNoMagick, and a HEIC, WebP, or AVIF whose
// reader magick lacks with ErrNoReader, as is a scale out of range,
// with ErrScale, which CheckMetafileScale makes; a conversion that fails leaves no PNG.
//
// The corrections stage, a Corrector, runs a page image through ImageMagick's operators for the
// corrections a request names: Deskew (-deskew 40%), Despeckle (-despeckle), ContrastStretch
// (-contrast-stretch 0.5%x0.5%), Unsharp (-unsharp 0x1), and Grayscale (-colorspace Gray), always
// in that order, however the caller lists them. A page is one magick run, PNG on stdin to PNG on
// stdout, and no corrections runs nothing. NewCorrector takes a magick Tool built through either
// runner and refuses one older than 7.1. The stage works on any page image, rendered or not: the
// document package's Preparer runs it on its pages. Corrections lists them in that order, each
// with its name, String, and its one-line Description for a prompt, and ParseCorrection reads a
// name back, without regard to case. Assess measures a page image for the defects the
// corrections fix, in one magick run: its skew, contrast, noise, sharpness, and color, and its
// size, an Assessment a layer above chooses corrections by.
//
// Its exported identifiers:
//
//   - [MagickSpec] declares ImageMagick's magick for either runner's constructor.
//   - [MagickMinimum] is the oldest magick the package accepts, 7.1.
//   - [LibreOfficeSpec] declares LibreOffice's soffice for either runner's constructor.
//   - [LibreOfficeMinimum] is the oldest LibreOffice the package accepts, 24.2.
//   - [LibreOfficeImage] is the default image soffice runs in through the container runner.
//   - [Config] is the Tools a Converter is built over: magick and LibreOffice.
//   - [Converter] converts TIFF, EMF, WMF, SVG, HEIC, WebP, and AVIF to PNG.
//   - [NewConverter] builds a Converter over a Config, reading magick's format listing.
//   - [Converter.Convert] converts one picture, told by its signature, to a PNG named by its
//     SHA-256, as a Request says.
//   - [Converter.ConvertPages] converts one picture as Convert does, but a TIFF to a PNG per page.
//   - [Request] is one picture's conversion: its working directory, the picture's path, and the
//     scale an EMF or WMF is rendered at.
//   - [Converter.ConvertBatch] converts several pictures, each to the PNG its Picture names, as a
//     Batch says.
//   - [Batch] is several pictures' conversion: its working directory, its metafiles' scale, and
//     its pictures.
//   - [Picture] is one picture of a Batch: its path, the kind it is read as, and its PNG's path.
//   - [DefaultMetafileScale] is the scale EMF and WMF are rendered at by default, 3.
//   - [MaxMetafileScale] is the largest scale they may be rendered at, 8.
//   - [CheckMetafileScale] returns the scale a request asks for, or refuses it.
//   - [ErrScale] is a refused scale's cause.
//   - [SVGDensity] is the density an SVG is rendered at, 150 dpi.
//   - [ErrNoMagick] and [ErrNoLibreOffice] are the causes of a picture's refusal without the
//     converter it needs, and [ErrNoReader] of a HEIC's, WebP's, or AVIF's without magick's reader.
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
package picture
