// Package route identifies content by its signature and names the stage it goes to next, on the
// standard library alone. It names a stage and never runs one: the caller, or a dispatcher, runs
// it, and reports "no processor yet" for a route whose stage doesn't exist.
//
// [Identify] names the [Kind] a file's first bytes begin, by its signature, as the head of
// [HeadLen] bytes shows it: a PDF; a PNG, JPEG, GIF, or BMP; a TIFF, EMF, or WMF; an SVG; a HEIC,
// WebP, or AVIF; a ZIP, an OLE2 compound file, or an ODF package, a ZIP whose first entry,
// mimetype, names an OpenDocument type; an RTF or a WordPerfect document; a .pst or an .ost, by
// the PST header's client magic, and an mbox, by its first line's "From ", as mail sniffs them; a
// OneNote OneStore file or a cabinet (MSCF); a WAV, FLAC, MP3, Ogg, or M4A; an MP4, Matroska,
// WebM, or AVI; a tar, gzip, zstd, or 7z archive; a PE, ELF, or Mach-O executable, or a script's
// "#!" line; or text marked by a byte order mark.
//
// [File] routes the file at a path by its kind, never by its stored MIME type, and by its name's
// extension only where the signature alone can't tell formats apart: within a ZIP or OLE2
// container, for a cabinet, for an mbox's "From " line, which prose named with a text extension
// begins too, and for content of no signature, which is a message by the .eml rule or text by
// its extension. The routes:
//
//   - [Document]: a PDF, for the document package's stage.
//   - [Office]: an office document: a ZIP named with an OOXML extension office reads, an OLE2
//     compound file named with a legacy one, an ODF package, an RTF, a WordPerfect document, or
//     a ZIP named .xlsb, an Excel binary workbook. The last four go through LibreOffice, and
//     office's stage has no processor yet for the last two, which a dispatcher reports.
//   - [Vision]: a picture a vision model decodes as stored: PNG, JPEG, GIF, or BMP.
//   - [VisionConverted]: a picture a vision model decodes only once converted to PNG: TIFF, EMF,
//     WMF, SVG, HEIC, WebP, or AVIF.
//   - [Nested]: a message or a mail store, for the email package's Reader: an OLE2 compound file
//     named .msg; content of no signature named .eml or stored as message/rfc822 or
//     message/global whose header has a message's shape; a .pst; or an mbox not named with a
//     text extension.
//   - [OneNote]: a OneNote section or table of contents, by its OneStore signature, or a cabinet
//     named .onepkg, a OneNote package.
//   - [Archive]: a ZIP that is neither office's nor ODF, a tar, gzip, zstd, or 7z archive, or a
//     cabinet not named .onepkg.
//   - [Audio]: WAV, FLAC, MP3, Ogg, or M4A.
//   - [Video]: MP4, Matroska, WebM, or AVI.
//   - [Text]: content of no signature named with a text extension, such as .txt, .csv, or
//     .json, or marked by a byte order mark.
//   - [Unrouted]: anything else, an executable among it, whose reason names it an executable; an
//     .ost, whose reason names it, since mail refuses one; and HTML, which is web content, not
//     text, unless its root element is an SVG's.
package route
