// Package inspect inspects a part before its stage, on the standard library and route alone,
// and says whether the part may go on to it. Unsafe means unsafe to read: content no stage may
// open without harm to the host. Content that could act, were something to run it, is only
// flagged, since no stage of this layer executes content.
//
// [Inspect] reads the file at a path, with the name it was given, and returns a [Report]: the
// [route.Kind] its signature names, its [Flag]s, each naming its [Check], its [Class], and its
// evidence, and the [Verdict] they come to. The report is a function of the file's bytes and
// name alone, with the [Options], so the same content gives the same report on every run and an
// index may record it by the content's SHA-256. Inspect streams: its memory is bounded whatever
// the file's size, and a ZIP is read by its central directory and a tar by its headers, no entry
// extracted.
//
// Its two classes:
//
//   - [Active]: content that could act, flagged, the part proceeding by default, since no stage
//     executes it. A signature that disagrees with the name's extension; a polyglot, a ZIP's
//     end-of-central-directory record inside content that isn't a container, or a PDF header
//     after another signature; executable content, by route's PE, ELF, Mach-O, and "#!" kinds,
//     an executable extension (.exe, .dll, .js, .vbs, .scr, .bat, .cmd, .ps1, .lnk, .hta, .jar,
//     .msi), and one after another, as in "invoice.pdf.exe"; an OOXML package's VBA project,
//     macro sheet, ActiveX controls, OLE objects, and external relationships other than
//     hyperlinks, such as a remote template or a linked picture; the PDF names pdfid counts,
//     /JavaScript, /JS, /OpenAction, /AA, /Launch, /EmbeddedFile, /RichMedia, /XFA, and /Encrypt,
//     and /EmbeddedFiles and /EF, which mark an embedded file whose stream names no type,
//     hex-escaped forms such as /J#61vaScript included, over the whole file; and an SVG's
//     script elements, event handler attributes, hrefs that leave the document, and
//     foreignObjects. Encrypted content is in this class too, not unsafe, since it is only
//     unreadable: a ZIP's encrypted entries, an /Encrypt PDF, and an encrypted OOXML package.
//   - [Unsafe]: content unsafe to read, flagged, the part quarantined by default: no stage runs
//     on it, its content is kept, and it is reported with its evidence. A ZIP entry whose name
//     has a ".." element, is absolute, or begins with a drive letter; a ZIP entry that is a
//     symbolic link by its Unix mode; a ZIP entry past both the expansion ratio and the
//     expanded size the Options allow, 1,000:1 and 1 GiB by default, by the sizes its central
//     directory declares; and a ZIP whose central directory can't be read. A tar entry whose name
//     has a ".." element, is absolute, or begins with a drive letter; a tar entry that is a
//     symbolic or hard link, a character or block device, or a FIFO; a tar entry past both the
//     ratio, against the whole tar's size, and the size, by the size its header declares, as only
//     a sparse entry or a lying header can be; and a tar whose headers can't all be read, as a
//     truncated tar's can't. A gzip stream past both by the bytes it decompresses to, which a
//     stage raises, not Inspect: a gzip declares its expanded size only modulo 4 GiB. A 7z,
//     cabinet, or zstd stream's listing's findings, which [CheckListing] raises: a 7z or cabinet
//     entry whose name has a ".." element, is absolute, or begins with a drive letter; a 7z entry
//     that is a symbolic or hard link, or a device, a FIFO, or a socket; a 7z or cabinet entry
//     past both thresholds by its declared size against the whole archive's; and a zstd stream
//     past both by the content size its frame header declares, or as written. A 7z's encrypted
//     entry is active, as a ZIP's is.
//
// [CheckListing] holds a listing of an archive only a program reads, a 7z, a cabinet, or a zstd
// stream, as 7-Zip lists it, to the rules Inspect holds a tar's headers to, by the same name
// checks, and returns its report under the Options. Inspect runs no program, so it lists no such
// archive itself: the dispatcher's archive stage lists it through 7-Zip and calls CheckListing
// before extracting anything, quarantining the archive on its verdict.
//
// A policy, [Options.Policy], overrides any check's default action, so a caller may quarantine
// a macro or let an unsafe finding proceed.
//
// The checks read what pdfid and a careful reader read, not everything: a PDF name inside a
// compressed object stream is unseen, as it is by pdfid; legacy OLE2 documents' macros and ODF
// packages' scripts aren't checked; and a container's members are not inspected here, but as
// parts of their own once the stage that extracts them yields them, the dispatcher's archive
// stage enforcing the bomb rule on the bytes it writes, since a declared size may lie. A gzip
// stream is checked as a gzip no further: the tar a .tar.gz holds is inspected once the stage
// decompresses it.
//
// Its exported identifiers:
//
//   - [Inspect] inspects the file at a path, and returns its [Report]; [Report.Has] reports
//     whether it holds a check's flag.
//   - [CheckListing] checks an archive's listing, its [Listed] entries, each of an [EntryType],
//     [FileEntry], [DirEntry], [SymlinkEntry], [HardlinkEntry], or [DeviceEntry], and returns its
//     Report.
//   - [Options] tune it: a [Options.Policy] and the bomb rule's thresholds, by default
//     [DefaultBombRatio] and [DefaultBombSize]; an invalid one matches [ErrOptions].
//   - [Flag] is one finding, its [Check], [Class], and evidence.
//   - [Class] is [Active] or [Unsafe]; [Verdict] is [Proceed] or [Quarantine].
//   - [Check] names a check. Of class Active: [SignatureMismatch], [Polyglot], [Executable],
//     [ExecutableExtension], [DoubleExtension], [VBAProject], [MacroSheet], [ActiveX],
//     [OLEObject], [ExternalRelationship], [OOXMLEncrypted], and [ZIPEncrypted]; the PDF names,
//     [PDFJavaScript], [PDFJS], [PDFOpenAction], [PDFAA], [PDFLaunch], [PDFEmbeddedFile],
//     [PDFRichMedia], [PDFXFA], and [PDFEncrypt]; and [SVGScript], [SVGEventHandler],
//     [SVGExternalHref], and [SVGForeignObject]. Of class Unsafe: [ZIPTraversal],
//     [ZIPAbsolutePath], [ZIPSymlink], [ZIPBomb], and [ZIPUnreadable]; [TarTraversal],
//     [TarAbsolutePath], [TarSymlink], [TarHardlink], [TarDevice], [TarBomb], and
//     [TarUnreadable]; [GzipBomb]; and, of a listing, [SevenZipTraversal],
//     [SevenZipAbsolutePath], [SevenZipSymlink], [SevenZipHardlink], [SevenZipDevice], and
//     [SevenZipBomb]; [CabTraversal], [CabAbsolutePath], and [CabBomb]; and [ZstdBomb]. Of class
//     Active too: [SevenZipEncrypted]. [Checks] lists them, and [ClassOf] returns a check's
//     class.
package inspect
