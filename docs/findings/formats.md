# Finding: the formats a share drive holds, through the dispatcher

This page records what the measurements behind task `formats` found: for each route the task
added, the outcome over the committed fixtures and over ~/docs, held to the truth wherever there
is one. The routes are the archive stage's tar, gzip, zstd, 7z, cabinets, and ZIP's other methods,
with their unsafe cases; the picture stage's TIFF pages, HEIC, WebP, and AVIF; the text stage;
RTF through LibreOffice; the document stage's password check, forms record, and attachments; and
the email stage's conversation records, Skype for Business conversation history in a PST. The
conversation records answer evidence 6 of the spike (the root's `context/README.md`); the rest
answers what a real share drive holds beyond the formats the earlier tasks prepared. It also
records what the dispatcher costs on each ~/docs input, in time and peak memory, as the
[onenote finding](onenote.md) measures it.

Every number here comes from one recorded run, **findings-formats 2026-11-07**: one run of
`mise run findings-formats` on 2026-11-07 from 02:55:03 to 02:57:41 UTC, on the laptop (x86_64),
at processing commit `d50f5a4`, with 7-Zip 26.05, mail 0.3.1, and office 0.1.0 from the mise pins,
qpdf 12.4.2, pdfdetach 26.10.0, Ghostscript 10.08.0, ImageMagick 7.1.2-34, and LibreOffice in its
pinned image. No model was called. Its results are committed in
[`formats-results/`](formats-results/README.md): the log verbatim and each test's JSON.

The packages release pre-inference preparation only; the finding's tests are test code, in
`dispatch/findings_formats_test.go`.

## At a glance

| Question | Answer |
|---|---|
| Do the new archive kinds read? | **Yes.** 34 archive fixtures, 65 parts: tar, gzip, 7z, zstd, cabinets, and ZIP's Deflate64, implode, xz, LZMA, PPMd, zstd, and bzip2 entries each extracted and routed again. Every unsafe case is quarantined, 14 of them, before anything is extracted; the encrypted 7z is encrypted after its readable entry, and the one with encrypted headers at once. |
| Is ~/docs' mixed bundle the same in all four forms? | **Yes.** `.zip`, `.tar.gz`, `.tar.zst`, and `.7z` give the same 7 parts below their entries, each with the same SHA-256, route, and outcome. |
| What happens without 7-Zip? | **Today's behavior, as the brief asks.** 16 of the 34 fixtures, every 7z, zstd stream, and cabinet, are no processor yet; a ZIP's six entries of other methods are records naming their method; tar, gzip, bzip2, and ZIP's stored and deflated entries are unchanged. The bundle's `.zip` and `.tar.gz` read whole; its `.tar.zst` and `.7z` are no processor yet. |
| Do the bombs fire? | **Yes, under 1 MiB and 100:1**: the 7z and cabinet by their listings' declared sizes (5958:1 and 572:1), the gzip and the zstd stream on the bytes written (131:1 and 4127:1), each stopped at 1,081,344 bytes. Under the defaults, 1 GiB and both thresholds, their 8 MiB of zeros are read. |
| Pictures? | **Every page and picture.** ~/docs' TIFFs give 3, 3, 1, and 1 pages, their counts; HEIC's primary image, never its thumbnail; WebP's and AVIF's first frame; stored orientation applied. 16 inputs, 22 images. |
| Text? | **6 of ~/docs' 11 encodings correct, 5 garbled**: UTF-8 with and without a mark, UTF-16LE with one, windows-1252, with CRLF too, and ISO-8859-1 decode to the truth; UTF-16BE without a mark, KOI8-R, EUC-JP, Shift_JIS, and windows-1256 are decoded as windows-1252, the text limit, by decision. |
| RTF? | **Yes.** `memo.rtf`'s heading and table, `test-rtf.rtf`'s paragraph without its hidden `\tc` title, each converted from "rtf"; ~/docs' three other RTFs prepared. |
| PDFs? | **Each as its truth says.** The three that need a password are encrypted in 4 to 7 ms, nothing rendered; the two restricted only by permissions render. The generated AcroForm's forms record has its 7 fields; ~/docs' two forms 1 each; a PDF without one, none. Both embedded files are found, by the name tree and by an annotation; the Screen video isn't extracted. A PDF embedding 8 MiB of zeros is stopped at 1,052,672 bytes under the lowered rule. |
| Evidence 6, conversation history? | **Every record equals the hand truth.** The fixture's 4 conversation items, 1 of them missed, are conversation records whose class, topic, participants, start and end, missed flag, stored HTML, and Markdown are the truth's; the IPM.Note beside them is a message, unchanged. The fixture is generated, and a record is an item, not a turn. |
| What does it cost? | **Under 1.2 s of dispatch for any one ~/docs input**, median of 3: text 0 to 1 ms, an encrypted PDF 4 to 7 ms, a rendered PDF 126 to 169 ms, a picture 15 ms to 1.19 s, an RTF 767 to 784 ms, the mixed bundle 1.146 to 1.187 s in any form. The dispatching process peaks at 13.5 to 22.9 MiB; the largest tool, magick on a 3840x2160 AVIF, at 345.0 MiB. |

All 86 checks against the truth held, and every test the run's first steps ran passed.

## Archives

### What was measured

`TestFindingsFormats` dispatches each archive fixture, inspect's generated tars and gzips and
sevenzip's commons-compress, libarchive, and generated archives, 34 in all, and ~/docs' mixed
bundle in its four forms, each through a dispatcher and a memory index of its own, so no part is
seen from another input, over every adapter that needs no model, the archive stage reading through
the 7zz mise pin and the document stage through qpdf and pdfdetach. It dispatches them again with
the archive stage built without 7-Zip, and the five bomb fixtures again under a bomb rule lowered
to 1 MiB and 100:1. Each part's route, outcome, reason, and outputs are in `formats.json`; its line
in `findings.log` begins `recorded: archives`. The run's first step ran the archive stage's unit
and integration tests beside it: tar, gzip, the 7z stage, zstd, ZIP's methods, the unsafe cases,
the bombs, encrypted and damaged archives, the no-7-Zip case, and ~/docs' ZIPs, tar.gz, 7z, and
tar.zst against their routes.

### Results

**With 7-Zip, 65 parts from 34 fixtures**: 23 archive parts prepared, 14 quarantined, and 2
encrypted, and 26 other parts found in them and routed again.

| Kind | Fixtures | Outcome |
|---|---|---|
| tar | `clean.tar` | prepared, `docs/notes.txt` text and `docs/pixel.png` vision, each "tar entry <name>" |
| gzip | `clean.tar.gz`, `unnamed.tgz`, `bomb.tar.gz`, `traversal.tar.gz` | prepared, one part "gzip content <name>", named by the header or the stem (`unnamed.tar` from `.tgz`), routed again: a tar |
| 7z | `clean.7z`, `bla.deflate64.7z` | prepared, each file "7z entry <name>": `notes.txt`, `pixel.png`, `test1.xml`, `test2.xml` |
| zstd | `clean.tar.zst`, `bomb.zst` | prepared, one part "zstd content <stem>": `clean.tar`, read again as a tar |
| cabinet | `notebook.cab`, `bomb.cab` | prepared, "cabinet entry <name>": `notebook.cab`'s `Open Notebook.onetoc2` and `New Section 1.one` both onenote, prepared |
| ZIP, Deflate64 | `lorem-ipsum-deflate64.zip` | prepared, `lorem-ipsum.txt` (144,060 bytes) text |
| ZIP, implode | `lorem-ipsum-implode.zip` | prepared, `LOREM.TXT` text |
| ZIP, xz | `test-method-xz.zip` | prepared, `LICENSE.txt` text |
| ZIP, LZMA, PPMd, zstd | libarchive's three `.zipx` | prepared, each `vimrc` (912 bytes), unrouted: no extension and no signature |
| ZIP, bzip2 | `bzip2-zip.zip` | prepared through `compress/bzip2`, `lots-of-as` unrouted |

**The unsafe cases, 14 quarantined**, each before anything of it was extracted, with its
evidence: `traversal.tar`, `traversal.7z`, and `traversal.cab` (`docs/../../evil.txt`);
`absolute.tar`, `absolute.7z`, and `absolute.cab` (`/tmp/evil.txt`); `symlink.tar` and
`symlink.7z` (`passwd`, to `/etc/passwd`); `hardlink.tar` (`shadow`, to `/etc/shadow`);
`device.tar` (a character device, `null`, 1:3) and `device.7z` (a FIFO, `pipe`); `sparse-bomb.tar`
(4 GiB declared in a 2,048-byte tar) and `declared-bomb.zst` (4 GiB declared in 18 bytes), each by
its declared size; and `traversal.tar.gz`'s tar, the gzip itself prepared, since a gzip stream
names nothing unsafe.

**Encrypted**: `encrypted.7z` reads its plain `notes.txt`, text prepared, then ends encrypted, "1 of
2 entries", `secret.txt` a record of check 7z-encrypted; `encrypted-headers.7z` is encrypted at
once, its listing refused.

**The bombs.** Under the defaults, a bomb must pass both 1 GiB and the ratio, so the fixtures'
8 MiB of zeros are read and found unrouted. Under 1 MiB and 100:1:

| Fixture | Outcome | Evidence |
|---|---|---|
| `bomb.7z` | quarantined by its listing | `zeros.bin`: 8,388,608 bytes declared in a 1,408-byte archive, 5958:1 |
| `bomb.cab` | quarantined by its listing | `zeros.bin`: 8,388,608 bytes declared in a 14,662-byte archive, 572:1 |
| `bomb.tar.gz` | prepared; its content a record, gzip-bomb, quarantined | 1,081,344 bytes written from 8,240, 131:1 |
| `bomb.zst` | prepared; its content a record, zstd-bomb, quarantined | 1,081,344 bytes written from 262, 4127:1 |
| `sparse-bomb.tar` | quarantined, as under the defaults | 4 GiB declared |

A gzip declares its size only modulo 4 GiB and `bomb.zst` declares none, so their rule holds on
the bytes as written, stopped just past the 1 MiB size.

**Without 7-Zip, 46 parts from the same 34**: every 7z, zstd stream, and cabinet, 16 fixtures, is
no processor yet, its reason "a 7z archive without 7-Zip: no processor yet" or the like, so the
generated 7z and cabinet hazards are no processor yet rather than quarantined, their listing
unread; the ZIPs of other methods are prepared, each entry a record, "compression method 9 has no
processor yet without 7-Zip" for Deflate64; and the tars, the gzips, and bzip2, which need no
7-Zip, are as with it: the same 7 quarantined, `clean.tar`'s two parts prepared.

### ~/docs' mixed bundle in four forms

The bundle holds six files, a text, an .eml, a PNG, a PDF, a legacy .xls, and a .docx. In every
form, below its entries, it gives the same **7 parts**: `meeting-notes.txt` text,
`mail/04-attachment-cyrillic.eml` nested through email and its attachment `отчёт.csv` text,
`docs/shapes.png` vision, `docs/mgrs-field-report.pdf` document, and `docs/SampleSS.xls`, through
LibreOffice, and `docs/word-tables.docx` office, all prepared, each with the same SHA-256 in all
four. The containers differ as their kinds do: the `.zip` and the `.7z` are 8 parts, the
`.tar.gz` and the `.tar.zst` 9, a gzip or zstd part holding the tar; a tar's and the 7z's names
carry the `./` they were stored with, which the comparison drops. Without 7-Zip, the `.zip` and
`.tar.gz` give the same 8 and 9; the `.tar.zst` and `.7z` are no processor yet.

### The archive limits

- **Each 7z entry is a 7zz run of its own.** One entry of a solid block is extracted by decoding
  the block from its start, so a solid archive of N entries decodes the block's start N times.
  `mixed-bundle.7z` is solid, one LZMA:23 block of 41,061 bytes (`7zz l -slt`), and its dispatch
  took 1.187 s against the `.zip`'s 1.160 s, a difference inside what LibreOffice's and
  Ghostscript's work on its entries varies by: at this size the cost isn't measurable. A large
  solid archive would show it; none was measured.
- **Non-UTF-8 names aren't measured.** 7zz writes its listing in UTF-8 (`-sccUTF-8`), and a tar's
  or a ZIP's stored name is read as the bytes stored; no fixture and no ~/docs archive holds a name
  that isn't UTF-8, so what a legacy code page's name becomes isn't recorded here.
- RAR, ISO, and bare xz or bzip2 streams are unrouted, outside the task.

## Pictures

### What was measured

`TestFindingsFormats` dispatches picture's five fixtures and ~/docs' four TIFFs, one HEIC, two
WebPs, and four AVIFs through the picture stage over magick, recording each image output's name,
pixel size, and Meta. The run's first step ran picture's integration tests over the same files,
which hold each image's size and pixels, page order, and that a page converted twice is the same
bytes, and the dispatch stage's `TestPictureStageReal`.

### Results

| Input | Images | Pixels |
|---|---|---|
| `picture/testdata/pages.tiff` | 3, "page 1" to "page 3", each of count 3 | 40x30, 50x30, 60x30, in page order |
| `~/docs/.../scanned-3page-g4.tif` | **3 pages** | 1700x2200 each |
| `~/docs/.../scanned-3page-lzw-gray.tif` | **3 pages** | 1275x1650 each |
| `~/docs/.../photo-deflate.tif` | 1 page | 960x540 |
| `~/docs/.../old-style-jpeg-compression.tif` | 1 page | 4160x870 |
| `two-images.heic`, `oriented.webp` | 1, "picture" | 32x48: upright, white above red |
| `animated.webp`, `animated.avif` | 1, the first frame | 24x16, red |
| `~/docs/.../example.heic` | 1, the primary image | 1280x854 |
| `~/docs/.../photo.webp`, `text-lossless.webp` | 1 each | 1920x1080, 210x746 |
| `~/docs/.../example.avif`, `bbb-alpha-inverted.avif`, `hdr-cosmos.avif`, `monochrome.avif` | 1 each | 800x533, 3840x2160, 2048x858, 1280x720 |

The page counts are the TIFFs' own, which their names and the manifest's covers give. **The HEIC's
primary image**: `example.heic` holds two top-level images of one size, and its PNG's top quarter
is bluer than red by 82.5, the primary's sky, where the other image's is redder
(`TestConvertRasterDocs`). **Orientation**: `two-images.heic`, stored 48x32 under an irot box, and
`oriented.webp`, under EXIF orientation 6, both convert upright, 32x48 (`TestConvertRasterHost`).
No picture's sums are recorded, since magick isn't pinned.

## Text: the encodings table, and the text limit

### What was measured

`TestFindingsFormats` dispatches ~/docs' 11 encodings files, each routed to text by the dispatcher,
and holds each text output to `encodings.truth.json`'s text. The unit tier's `TestTextDocs`, in the
run's first step, holds the same to the stage's rule.

### Results

| File | Truth | Decided | Text |
|---|---|---|---|
| `utf8-nobom.txt` | utf-8 | utf-8 | **correct** |
| `utf8-bom.txt` | utf-8-sig | utf-8 (byte order mark) | **correct** |
| `utf16le-bom.txt` | utf-16 | utf-16le (byte order mark) | **correct** |
| `windows-1252.txt` | cp1252 | windows-1252 (fallback) | **correct** |
| `crlf-windows-1252.txt` | cp1252 | windows-1252 (fallback) | **correct**, CRLF kept |
| `iso-8859-1.txt` | latin-1 | windows-1252 (fallback) | **correct**: its printable text decodes alike |
| `koi8-r.txt` | koi8_r | windows-1252 (fallback) | garbled |
| `euc-jp.txt` | euc_jp | windows-1252 (fallback) | garbled |
| `shift-jis.txt` | shift_jis | windows-1252 (fallback) | garbled, 9 U+FFFD |
| `windows-1256-arabic.txt` | cp1256 | windows-1252 (fallback) | garbled |
| `utf16be-nobom.txt` | utf-16-be | windows-1252 (fallback) | garbled, 2 U+FFFD |

**6 correct, 5 garbled.** Every fallback carries the warning "no byte order mark and not UTF-8:
decoded as windows-1252, which may not be the text's encoding", and the two that hit bytes
windows-1252 leaves undefined count their U+FFFD in Meta's warnings; the other three garble with no
replacement at all, since every byte decodes to something.

**This is the text limit, and it is by decision.** The stage detects no encoding: a byte order
mark, else valid UTF-8, else windows-1252, whose 256 code points always decode. Text with no mark
that isn't UTF-8 is windows-1252 to the stage, so the legacy Cyrillic, Japanese, and Arabic files,
and UTF-16 written without its mark, come out as Latin mojibake under a warning that says so. The
brief left encoding detection, such as uchardet, out of the task. What would change it: a
detector, which guesses from byte statistics and can itself be wrong on short text; a caller's
hint, the code page a share or a site is known to use, given to the stage in place of the
fallback; or, for UTF-16 alone, the NUL bytes every other byte of Latin text carries, a narrow
check that needs no statistics. Each is a change to the stage; none is in it.

## RTF

`TestFindingsFormats` dispatches `office/testdata/rtf/memo.rtf` and ~/docs' four RTFs through the
office stage, LibreOffice converting each to docx in its pinned image and office converting that to
Markdown. Each is one Markdown output, Meta `converted_from` "rtf". **`memo.rtf`**: its heading,
`# Harbor memo`, and its table's rows are in the Markdown. **`test-rtf.rtf`**: its paragraph, one
sentence eleven times, is a line of the Markdown, and **its `\tc` title is hidden**: the bold title
above the paragraph is a table-of-contents entry, text RTF doesn't display, and no line of the
Markdown holds it. ~/docs' `lists.libreoffice.rtf`, `tables.libreoffice.rtf`, and
`lorem-ipsum-calibre.rtf` are prepared too, their lists, table, and paragraphs in the Markdown;
there is no truth for them. `TestRTFRoundTrip` and `TestOfficeStageRTFReal`, in the run's first
step, hold the same lines.

## PDFs

`TestFindingsFormats` dispatches ~/docs' encrypted, form, and attachment PDFs and document's
generated AcroForm and clean PDF through the document stage over gs, qpdf, and pdfdetach. The
run's first step ran the document stage's integration tests over the same files, which hold each
field's value and each attachment's SHA-256, and the attachment bomb.

**Password-required against permissions-only.** The manifest's covers name an open password on
three, and qpdf's `--requires-password` agrees on each:

| PDF | Covers | Outcome | Dispatch, median |
|---|---|---|---|
| `encryption-openpassword.pdf` | encrypted, password=password | **encrypted**, "the PDF needs a password", nothing rendered | 7 ms |
| `simple-open-password.pdf` | encrypted, password=password | **encrypted**, nothing rendered | 4 ms |
| `unicode-password-corrigendum5.pdf` | encrypted, unicode-password | **encrypted**, nothing rendered | 6 ms |
| `encryption-nocopy.pdf` | encrypted, permissions | prepared, its page rendered | 136 ms |
| `encryption-notextaccess.pdf` | encrypted, permissions | prepared, its page rendered | 136 ms |
| `unicode-password-baseline.pdf` | not encrypted | prepared | 128 ms |

inspect still flags /Encrypt on all five encrypted files.

**The forms record.** The generated `acroform.pdf` yields a forms record before its page, its 7
fields exactly the generator's: two text fields, "Ada Lovelace" and "Zürich"; a checkbox checked
and one not; a radio group, "Phone"; a combo box, "Green" of three options; and a multi-select list
box, two selected. ~/docs' `isartor-interactive-form.pdf` yields 1 field, `field1`, text "field",
and `pdfa2b-interactive-form-pass.pdf` 1, `Button`, a pushbutton. `clean.pdf`, with no AcroForm,
yields none.

**Attachments.** Both kinds are found: `file-attachment.pdf`'s name tree holds `KSBASE.WQ2`, and
`file-attachment-annotation.pdf`'s file attachment annotation holds `PF.WK1`, each found "attachment
<name>", written under its SHA-256, inspected, and routed again: both unrouted, legacy spreadsheets
by their extensions, a Quattro Pro and a Lotus 1-2-3 file, which no stage takes.
`embedded-video-avi.pdf`'s video sits behind a Screen annotation's rendition, which isn't an
embedded file: no part is found, and its page renders alone.

**The attachment bomb.** A PDF of 8,762 bytes embedding 8 MiB of zeros
(`TestDocumentStageAttachmentBombReal`) is saved whole under the defaults; under 1 MiB and 100:1,
pdfdetach is stopped at 1,052,672 bytes, 120:1, the file a record of check pdf-attachment-bomb,
quarantined, and nothing of it is left.

## Evidence 6: conversation history

### What was measured

`TestFindingsFormats` dispatches mail v0.3.1's generated `conversations.pst`, copied under
`email/testdata/mail`, through the email stage over the mail pin, and holds every record to the
fixture's hand truth, `truth.json`'s `items`: each conversation record's class, topic,
participants, start and end, missed flag, stored HTML, and Markdown, and the Markdown output after
it; and the message record of the IPM.Note beside them, its class and Markdown. The run's first
step ran email's `TestRealConversations`, which holds the Reader's records to the same truth in
the layout's order, and dispatch's `TestEmailConversations`.

### Results

**All 5 items match the hand truth.** The store's Conversation History folder holds:

| Item | Class | Record | Participants | Start, end (UTC) |
|---|---|---|---|---|
| Q1 inventory counts | IPM.Note.Microsoft.Conversation | conversation | 2 | 2026-03-10 14:02:11, 14:07:48 |
| Release checklist for build 4.2 | IPM.Note.Microsoft.Conversation | conversation | 4 | 2026-03-11 09:30:04, 09:41:52 |
| Installer sign-off | IPM.Note.Microsoft.Conversation | conversation | 2 | 2026-03-11 15:12:40, 15:14:05 |
| Missed conversation with Tomás Ferreira | IPM.Note.Microsoft.Missed | conversation, **missed** | 2 | 2026-03-12 17:48:20, 17:53:20 |
| Q1 inventory counts, Leeds | IPM.Note | **message**, unchanged | | |

Each of the 4 conversation records has its truth's class, topic, participants (the sender, then
the recipients), start and end (the submit and delivery times), missed flag, its HTML byte for byte
as stored, and its Markdown, mail's own, in the record and in the Markdown output that follows it.
The note, in the same folder, is read as a message, as before: the class decides, never the folder.
The dispatch took 3 ms, 14 outputs from the one store.

### Its limits

- **The fixture is generated.** No real Skype for Business sample was available, so mail's
  generator writes the store, and the HTML shape of each transcript, each line a speaker, a time,
  and the text, is invented. The records hold
  whatever HTML the store holds, so a real transcript of another shape still reads; only the shape
  isn't proven against a real one.
- **A record is an item, not a turn.** The transcript is kept as the stored HTML and mail's
  Markdown; no speaker, time, or message is parsed out of it, by decision.
- **Text chats only.** An item of a Voice class is a message as before, and Teams isn't read.
- **Where it is found.** Skype for Business archives its conversations into Exchange, the
  mailbox's Conversation History folder; such history reaches a share drive only inside a PST
  exported from the mailbox, which is the form this fixture takes.

## Time and peak memory

### What was measured

`TestFindingsFormatsCost` dispatches each ~/docs input above and the conversation store, each
alone, three times, in a process of its own over a fresh file index, through the onenote
finding's launcher: the dispatch's own time, from its first input to its last event, the process's
wall time, start-up and stage construction included, and its peak resident memory and its
children's, the largest of any one tool it ran (`formats-cost.json`). A process that does nothing
peaks at 10.2 to 15.2 MiB, the launcher's floor.

### Results

Medians of 3 runs; peak memory is the most of the three.

| Input | Dispatch | Wall | Own peak | Children's peak |
|---|---|---|---|---|
| `conversations.pst` | 3 ms | 536 ms | 18.6 MiB | 33.5 MiB |
| `mixed-bundle.zip` | 1.160 s | 1.703 s | 18.0 MiB | 33.5 MiB |
| `mixed-bundle.tar.gz` | 1.146 s | 1.688 s | 19.1 MiB | 33.4 MiB |
| `mixed-bundle.tar.zst` | 1.182 s | 1.756 s | 18.2 MiB | 35.9 MiB |
| `mixed-bundle.7z` | 1.187 s | 1.758 s | 17.0 MiB | 33.5 MiB |
| `scanned-3page-g4.tif` | 249 ms | 818 ms | 14.4 MiB | 116.1 MiB |
| `scanned-3page-lzw-gray.tif` | 834 ms | 1.386 s | 14.9 MiB | 95.9 MiB |
| `photo-deflate.tif` | 181 ms | 740 ms | 14.9 MiB | 33.5 MiB |
| `old-style-jpeg-compression.tif` | 881 ms | 1.435 s | 16.8 MiB | 101.2 MiB |
| `example.heic` | 274 ms | 826 ms | 13.9 MiB | 53.2 MiB |
| `photo.webp` | 775 ms | 1.339 s | 17.5 MiB | 63.1 MiB |
| `text-lossless.webp` | 15 ms | 586 ms | 14.1 MiB | 33.5 MiB |
| `example.avif` | 104 ms | 676 ms | 15.5 MiB | 36.3 MiB |
| `bbb-alpha-inverted.avif` | 563 ms | 1.128 s | 13.5 MiB | **345.0 MiB** |
| `hdr-cosmos.avif` | 1.188 s | 1.736 s | 14.8 MiB | 75.6 MiB |
| `monochrome.avif` | 101 ms | 661 ms | 15.3 MiB | 66.7 MiB |
| the 11 encodings | 0 to 1 ms | 547 to 592 ms | 13.7 to 17.4 MiB | 33.3 to 33.6 MiB |
| the 4 RTFs | 767 to 784 ms | 1.325 to 1.721 s | 15.5 to 19.7 MiB | 33.3 to 33.5 MiB |
| the 3 password-required PDFs | 4 to 7 ms | 565 to 594 ms | 14.4 to 17.1 MiB | 33.3 to 33.6 MiB |
| the 3 other encrypted-set PDFs | 128 to 136 ms | 707 to 717 ms | 17.1 to 18.0 MiB | 33.5 MiB |
| the 2 forms | 126 ms | 712 to 727 ms | 15.2 to 15.4 MiB | 33.4 to 33.6 MiB |
| the 3 attachment PDFs | 147 to 169 ms | 692 to 714 ms | 17.9 to 22.9 MiB | 33.3 to 35.5 MiB |

About 0.55 s of every wall time is the process's start and the stages' construction, each tool's
version check and magick's format listing among it: the text files, which run no tool, take 0 to
1 ms to dispatch and 547 to 592 ms of wall. The children's 33.3 to 33.6 MiB is likewise a floor,
set by construction's tool checks, which the text files show too; the pictures are the tools that
pass it, magick's peak growing with the image, to 345.0 MiB on a 3840x2160 AVIF. The bundle's four
forms cost the same within 41 ms, their time spent on the entries' stages, not on reading the
archive.

What this doesn't measure: LibreOffice runs inside its container, outside the process tree, so the
RTFs' and the bundle's `.xls` conversion count only the docker CLI's memory, not soffice's; and a
bundle's dispatch is one time for all its stages, not one per stage.

## Known limits

- The text limit: 5 of 11 encodings garbled, any text with no mark that isn't UTF-8 or
  windows-1252 (above).
- A 7z entry is a 7zz run of its own, decoding a solid block from its start each time; not
  measurable at the bundle's 41 KB, and no large solid archive was measured.
- Non-UTF-8 stored names aren't measured.
- Without 7-Zip, a 7z's or a cabinet's unsafe names aren't quarantined but no processor yet, their
  listing unread.
- An embedded file's route is its content's: the two found here are legacy spreadsheets, unrouted.
  The media behind a Screen or Rendition annotation isn't extracted, and XFA is flagged only.
- The conversation records rest on a generated fixture, read per item, text chats only.
- RAR, ISO, bare xz and bzip2, ODF, WordPerfect, .xlsb, and ODG are outside the task: unrouted or
  no processor yet.

## What it answers for evidence 6

Skype for Business conversation history in a PST reads as one conversation record per item, a
conversation or a missed one, by its message class: its participants, start and end, topic, missed
flag, and its transcript as mail's Markdown and as the stored HTML, every field equal to the hand
truth of mail's generated fixture, the note beside them read as a message, unchanged. What isn't
proven: a real store's transcript HTML, since the fixture's shape is invented; the turns within a
transcript, which aren't parsed; and Voice items and Teams, which aren't read as conversations.

## Rerunning

From this repository's root, on a laptop (never in CI, never in the check), with ~/docs present
(`DOCS` names another root) and docker able to run the pinned LibreOffice image:

```sh
FORMATS_FINDINGS_RESULTS=docs/findings/formats-results mise run findings-formats
```

`mise run findings-formats` (`scripts/findings-formats.sh`) runs, each step's `-v` output appended
to `findings.log`: the dispatch package's unit and integration tests of the archive, picture,
text, office, document, and email stages named in the script; sevenzip's tests; inspect's listing
and tar checks; picture's TIFF and raster tests; office's `TestRTFRoundTrip`; document's qpdf and
pdfdetach tests; email's conversation tests; and dispatch's findings-tagged `TestFindingsFormats`
and `TestFindingsFormatsCost`, without the race detector. Each ~/docs input is checked by its
SHA-256 against the corpus manifest first. `FORMATS_FINDINGS_RUNS` (3) sets the timed runs of each
input. No step calls a model. A plain run keeps nothing. The run above took 2 minutes 38 seconds.
