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
// the file's size, and a ZIP is read by its central directory, no entry extracted.
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
//     directory declares; and a ZIP whose central directory can't be read.
//
// A policy, [Options.Policy], overrides any check's default action, so a caller may quarantine
// a macro or let an unsafe finding proceed.
//
// The checks read what pdfid and a careful reader read, not everything: a PDF name inside a
// compressed object stream is unseen, as it is by pdfid; legacy OLE2 documents' macros and ODF
// packages' scripts aren't checked; and a container's members are not inspected here, but as
// parts of their own once the stage that extracts them yields them, the dispatcher's ZIP stage
// enforcing the bomb rule on the bytes it writes, since a declared size may lie.
//
// Its exported identifiers:
//
//   - [Inspect] inspects the file at a path, and returns its [Report]; [Report.Has] reports
//     whether it holds a check's flag.
//   - [Options] tune it: a [Options.Policy] and the bomb rule's thresholds, by default
//     [DefaultBombRatio] and [DefaultBombSize]; an invalid one matches [ErrOptions].
//   - [Flag] is one finding, its [Check], [Class], and evidence.
//   - [Class] is [Active] or [Unsafe]; [Verdict] is [Proceed] or [Quarantine].
//   - [Check] names a check; [Checks] lists them, and [ClassOf] returns a check's class.
package inspect
