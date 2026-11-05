# Changelog

All notable changes to `github.com/JaimeStill/spike-ai-pipeline-processing` are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The `audio` package plans a speech source's windows for a model's audio input, over FFmpeg's
  ffmpeg and ffprobe, `audio.FFmpegSpec` and `audio.FFprobeSpec`, minimum 9.0, `FFmpegMinimum`, on
  the host or in the default image `audio.FFmpegImage`, linuxserver/ffmpeg:9.0.2-cli-ls88, pinned
  by digest. `NewSplitter` builds a `Splitter` over a `Config` of both Tools, refusing a missing or
  outdated one. `Splitter.Probe` reads a source's duration and first audio stream, a `Source`, and
  a source without one is refused with `ErrNoAudio` before ffmpeg runs. `Splitter.Detect` finds its
  silences, a `Detection`, through volumedetect and silencedetect: at least 0.3 s,
  `DefaultMinSilence`, below 15 dB, `DefaultBelow`, under the mean volume, or below a fixed
  threshold a `Request` gives. `PlanWindows`, a pure function of a duration, its silences, and a
  `Request`'s overlap, plans windows of at most 30 s, `WindowLimit`, each ending at the middle of
  the latest silence its range allows or at the limit, the next starting 2 s, `DefaultOverlap`,
  before it, with the seams they share; `Splitter.Plan` runs both. An invalid request is refused
  with `ErrRequest` before anything runs. A ~50 s LibriSpeech speech fixture and its transcript,
  CC BY 4.0, are in `audio/testdata`, rebuilt by `mise run fixtures:audio`.
- `audio.Splitter.Windows` cuts a plan's windows, a lazy `iter.Seq2[WindowFile, error]` in plan
  order, as a `WindowsRequest` says: the source, the media directory `Dir`, and `Concurrency`, the
  ffmpeg runs in flight, zero meaning 1. Each window is resampled and downmixed by ffmpeg from the
  stream `Probe` found to 16 kHz, `SampleRate`, mono 16-bit PCM, trimmed to its span's samples to
  within one, and written as WAV with the canonical 44-byte header, the bytes of ffmpeg's own
  `-bitexact` WAV, named by its SHA-256; the exec and container runners write the same bytes. A
  `WindowFile` carries the window's index, the plan's count, its `Window`, path, SHA-256, and
  samples. A loop that stops early or a context that ends stops the runs in flight, and the media
  directory keeps only the windows yielded. A plan of another source, its duration not the
  source's, is refused with `ErrRequest`.
- `audio.Join` merges the windows' transcripts, one string for each in plan order, into one
  `Transcript`, its `Words` as the windows gave them and `Text` them spaced, as a `JoinRequest`
  says. Each seam is merged by Hugging Face transformers' best-offset merge,
  `_find_longest_common_sequence`, over words compared lowercased with their punctuation dropped:
  the offset that scores best on matches over offset plus a small tie-break, matching at least 2
  words, is spliced at the middle of the overlap it aligns. The search reaches `SeamWords` words
  into each window, `DefaultSeamWords`, 16, by default, so a repeat beyond the overlap is never the
  splice; a seam matching fewer than 2 words, as one beside an empty transcript does, is
  concatenated whole. Each `Seam` reports whether it matched, the words matched and aligned, and
  its position in the words. A `SeamWords` below 0, or 1, is refused with `ErrRequest`.

### Changed

- The default LibreOffice image, `picture.LibreOfficeImage`, moves to
  gotenberg/gotenberg:9.0.0-libreoffice, LibreOffice 26.8.2.2, pinned by digest. soffice runs in
  it headless as before, through the container runner's lockdown, with no change of path,
  arguments, or filters. Its metafile renders differ from 26.8.1.1's below a pixel.

## [v0.6.0] - 2026-11-05

### Breaking changes

Every move is without an alias: a caller on v0.5.0 renames as listed.

- `email`'s routes move to the new `route` package: `email.Route` is `route.Route`, and
  `email.RouteDocument`, `RouteOffice`, `RouteVision`, `RouteVisionConverted`, `RouteNested`, and
  `RouteUnrouted` are `route.Document`, `Office`, `Vision`, `VisionConverted`, `Nested`, and
  `Unrouted`. `email.Attachment.Route` returns a `route.Route`.
- `office`'s picture conversion moves to the new `picture` package:
  `office.Converter.ConvertPicture` and `office.PictureRequest` are `picture.Converter.Convert`
  and `picture.Request`; `office.DefaultMetafileScale` and `office.MaxMetafileScale` are
  picture's, the range check `picture.CheckMetafileScale`, its refusal `picture.ErrScale`; and
  `office.ErrNoMagick` is `picture.ErrNoMagick`. `office.ErrNoLibreOffice` stays, for a legacy
  document; a metafile without LibreOffice is the new `picture.ErrNoLibreOffice`.
- The tool declarations move to `picture`: `office.MagickSpec` and `office.MagickMinimum`, and
  `document.MagickSpec` and `document.MagickMinimum`, are `picture.MagickSpec` and
  `picture.MagickMinimum`, one spec, minimum 7.1; `office.LibreOfficeSpec`,
  `office.LibreOfficeMinimum`, and `office.LibreOfficeImage` are picture's.
- `document`'s corrections stage moves to `picture`: `document.Correction`, `Deskew`,
  `Despeckle`, `ContrastStretch`, `Unsharp`, `Grayscale`, `Corrections`, `ParseCorrection`,
  `Corrector`, `NewCorrector`, `Assessment`, and `GrayscaleColored`, with `Corrector.Correct` and
  `Corrector.Assess`, are picture's. `document.NewPreparer` takes a `*picture.Corrector`, and
  `ImagesRequest.Corrections` is a `[]picture.Correction`.
- office's and email's exit errors are bound to one shared set, so `errors.Is` matches either
  package's refusal through the other's `ErrRefused`. Exit code 1's text is the shared "not
  written": `office.ErrMediaNotWritten.Error()` and an office `*ExitError` for code 1 read "not
  written", where v0.5.0 read "media not written". No exported name changes.
- `email.Attachment.Route` takes `route.File`'s routes, so an attachment unrouted in v0.5.0 may
  now be text (a .csv or .txt, as two of the fixtures' attachments are), archive, onenote, audio,
  video, nested (a .pst or an mbox), or office (an ODF package, an RTF, a WordPerfect document, an
  .xlsb, or an encrypted OOXML package). Every other fixture attachment's route and reason is
  v0.5.0's, as `TestRealRoutesSinceV050` holds.
- mise pins mail 0.3.0, which the tests need to resume a read; `MailMinimum` stays 0.2.0.

### Added

- `route`, which identifies content by its signature and names the stage it goes to next, on the
  standard library alone. `Identify` names the `Kind` a file's first `HeadLen` bytes, 4096, begin:
  PDF; PNG, JPEG, GIF, and BMP; TIFF, EMF, WMF, SVG, HEIC, WebP, and AVIF; ZIP, OLE2, ODF, RTF, and
  WordPerfect; PST, OST, and mbox; OneStore and cabinet; WAV, FLAC, MP3, Ogg, and M4A; MP4,
  Matroska, WebM, and AVI; gzip, zstd, 7z, and tar; PE, ELF, Mach-O, and "#!"; and the UTF-8,
  UTF-16, and UTF-32 byte order marks. `File` routes a file by its kind, never by its stored type,
  and by its name's extension only where the signature can't tell formats apart, to `Document`,
  `Office`, `Vision`, `VisionConverted`, `Nested`, and `Unrouted`, as email's routes did, and the
  new `OneNote`, `Archive`, `Audio`, `Video`, and `Text`, each with a reason in a few words. A
  .pst and an mbox are nested, an .ost unrouted, a tar an archive, and WordPerfect, an .xlsb, and
  an encrypted OOXML package, an OLE2 file holding an EncryptedPackage stream named with an OOXML
  extension or .xlsb, office's; HTML is unrouted, web content, not text. `PNGSignature` is the
  eight bytes a PNG begins with.
- `picture`, which converts TIFF, EMF, WMF, and SVG to PNG, each named by its original's SHA-256:
  `Converter.Convert` one picture, told by its signature, and `Converter.ConvertBatch` several, the
  metafiles in one soffice run; an SVG at `SVGDensity`, 150 dpi, through magick's rsvg
  coder, its relative references resolving to nothing. office converts a document's pictures
  through it. Its corrections stage is document's, moved.
- `inspect`, which inspects a part before its stage, on the standard library and `route`, and
  returns a `Report`, its `Flag`s and a `Verdict`. Active content, flagged and let proceed by
  default: a signature mismatch, a polyglot, executable content and extensions, an OOXML
  package's VBA project, macro sheet, ActiveX, OLE objects, and external relationships, the PDF
  names pdfid counts, an SVG's scripts, handlers, external hrefs, and foreignObjects, and
  encrypted content. Unsafe content, quarantined by default: a ZIP entry's traversal, absolute
  path, or symbolic link, a bomb by its declared sizes, and an unreadable central directory.
  `Options.Policy` overrides any check's action.
- `internal/cli`, the CLI contract the format packages share: the exit codes 1 to 4 as one set of
  errors, the stderr warning reader, the bounded buffer, and the spool. The README's adapter
  convention describes the shape every format package follows.
- `onenote`, which reads OneNote sections, notebooks, and packages through
  spike-ai-pipeline-onenote's CLI, on Linux, as email reads mail: `NewReader` over a `Config`, a
  onenote Tool that `OneNoteSpec` declares, minimum `OneNoteMinimum`, 0.1.0; `Reader.Read` a
  `Stream` of typed schema "1" records, `Notebook`, `SectionGroup`, `Section`, `Page`, and
  `Skipped`, each marshalling back to onenote's line byte for byte; a page's content typed through
  and through; and `Page.Media`, every image, embedded file, and ink drawing a page names, wherever
  it sits, each with its place and its route. Exit codes 1 to 4 are an `*ExitError`. mise pins
  onenote 0.1.0, and `mise run fixtures:onenote` refreshes the copy of its fixtures.
- `dispatch`, which walks an input and every part found in it to the caller's stages by route, on
  the standard library, `route`, and `inspect`, and never calls a model. `New` builds a
  `Dispatcher` over a `Config` of `Stages`, an `Index`, a working directory, `Limits`, and
  inspect's Options; `Dispatcher.Dispatch` hashes, routes, and inspects each part before its stage
  and yields its `Event`s depth-first: an output event for each output as its stage yields it, and
  each part's own event after its outputs and the parts found in it, with its `Outcome` (prepared,
  no processor yet, seen, cycle, quarantined, encrypted, failed, or limited) and the counts of its
  outputs and children. `Event.Keep` keeps an event's files, which the dispatcher otherwise
  removes. The stages:
  - the format packages' adapters, on Linux: `DocumentStage`, `OfficeStage`, `PictureStage`,
    `VisionStage`, `EmailStage`, and `OneNoteStage`, each over the reader or converter the caller
    built and its tools' version, `Versions`; a found part, such as a message's attachment or a
    page's ink, is dispatched again;
  - `ZipStage`, on any platform, each entry written by its SHA-256 and found as a part, the bomb
    rule enforced again on the bytes written. An encrypted entry is a record whose Meta outcome is
    "encrypted", and the ZIP's own outcome is Encrypted once its readable entries are dispatched.
- A stage that holds content later records name again gives each `Found` part its `SHA256` and a
  `Release` func, which the dispatcher calls once the index holds the content's entry; the stage
  then removes its original, and a later record names the content with no file and is seen.
  The email and onenote stages do this, so a store's directory holds only the attachments of the
  messages in flight.
- The index: `Index`, content-addressed by the content's SHA-256 and the stage's identity, so
  content it holds is seen, its outputs streamed back, and a claim makes one of concurrent
  dispatches prepare it. `Append` and `AppendChild` add each output and found part to the claim's
  pending entry as its event is done, `Record` commits the entry and `Release` drops it, and
  `Outputs` and `Children` read them back streamed, so a store streams through in the memory of
  one message. `MemoryIndex` and `FileIndex`, a directory of records by atomic rename and claims
  by O_EXCL, taken over once `FileOptions.Stale`.
- The pool: `Dispatcher.Pool`, inputs over N workers sharing one index, their events as
  `PoolEvent`s on the caller's goroutine.
- Resuming a store: `Input.After` resumes a .pst or an mbox after a message, through
  `email.Request.After`, and the package doc's "Resuming a store" gives the caller's cursor rule
  and its checkpoint, the cursor and the Threader saved together every N messages.
- `email.Request.After`, a read resumed after a message, through mail's `--after`; it needs mail
  `email.MailAfterMinimum`, 0.3.0, checked only when `After` is set. `email.Threader`'s
  `MarshalBinary` and `UnmarshalBinary` save and restore its keys, so a resumed read threads whole.
- The dispatch integration tier: `TestTierRoutes` dispatches every committed fixture input
  through every adapter over one file index, held to the hand route truth in
  `dispatch/testdata/tier-routes.tsv`, with a vision sample.
- The onenote finding, `docs/findings/onenote.md`, from a run of `mise run findings-onenote` at
  `96797c6` and a later run of the scale store's dispatch at `fdbdab0`, both recorded in
  `docs/findings/onenote-results/`: every route matches the truth over 119 inputs and 225 parts;
  inspection flags every hazard it has a check for over ~/docs; vision reads all 14 words of a
  OneNote page's ink, CER 0.0278, where OneNote's recognition holds 4; the onenote adapter costs
  nothing measurable over its CLI; on mail's 2.1 GB scale store the dispatcher takes 15.61 s with
  removal on and 17.57 s with it off, against the email adapter's 13.72 s, its own process peaking
  at 36.8 MiB and its working directory at 19.0 MB with removal on, 1.88 GB with it off; a resumed
  half takes 0.54 of the whole; and the pool takes 18.2 s at 2 to 8 workers against 21.2 s at one.
  Saving the Threader at every message is quadratic over a store, 3.5 ms a save at 25,000
  messages.
- Known limits, in the README: mail's caps, an .eml at 4 GiB and a .msg's nesting at 16; office's
  and onenote's whole-input memory; the Threader's keys, about 60 bytes a message; OneNote's own
  encryption undetected, so `Encryption.one` fails; a bare RFC 822 file unrouted; a released
  content's later reference taking the first's route; a stale claim's takeover preparing content
  twice; and legacy OLE2 macros and XML entity expansion unchecked.

### Changed

- office converts a document's pictures through `picture`, each from a copy in a directory of
  its own; every media PNG and Markdown is byte for byte what it was before the move, as the
  golden sums recorded before it hold.
- The test helpers: `internal/goldentest`, the golden sums of office's and document's picture
  outputs, and `internal/textscore`, the character and word error rates document's bake-offs and
  the ink finding score by, are shared internal packages.

## [v0.5.0] - 2026-11-04

### Added

- `email` reads MIME messages, .eml and mbox files, through spike-ai-pipeline-mail 0.2.0, beside
  .pst and .msg. An .eml's read is its store line and its message, with any message/rfc822 or
  message/global part nested under its attachment record; an mbox's is each message in the file's
  order, its `Message.ID` its 1-based ordinal, which skips a number where an entry is empty, and a
  `Skipped` record, kind "message", for one mail can't read. Neither has folders, nor a store's
  encoding, password, or display name. The decoded types are unchanged; their documentation says
  what MIME input gives.
- MIME threads: the `Threader` threads an .eml's and an mbox's messages as it does a store's, an
  mbox message by its ordinal and an .eml's as one root with no id. No change to the algorithm.
- The nested .eml route: a by-value attachment of no routed signature is `RouteNested` when its
  name has the .eml extension, in any case, or its stored type is `message/rfc822` or
  `message/global`, parameters ignored, and its header has the shape mail reads a message by: its
  first line a header field, and a From or Date field before the header block ends. Signatures
  still decide first, so a PDF named .eml is a document; one so named or stored that fails the
  header check is `RouteUnrouted`, its reason saying it isn't header-shaped.
- The MIME inputs in the hermetic check: 17, mail's 9 generated and threading .eml, `nine.mboxrd`,
  `mbox.rlug` and its 5 single-message splits, and the built .msg's .eml source, read through the
  pinned mail against hand-written records, threads, and routes.
- The MIME integration tier: the integration tests carry the MIME inputs' attachments to their
  real stages after the .pst and .msg fixtures', read each nested message again, and send each
  picture not already sent to the vision model.
- The email finding's addendum, "Addendum: mail 0.2.0, .eml and mbox" in
  `docs/findings/email.md`, from one run of `mise run findings-email` recorded in
  `docs/findings/email-results/mail-0.2.0/`: threads 29 of 29 and routes 13 of 13 over the MIME
  inputs, the .pst and .msg routes re-recorded, and the adapter's time and peak memory on the
  2.7 GB scale mboxrd beside the scale store.
- `EMAIL_SCALE_MBOX`, which names the scale mboxrd mail's generator writes for the findings tier
  to time; unset, it is skipped.
- Known limits of `email`, replacing v0.4.0's "a by-value .eml attachment is unrouted": a by-value
  .eml whose header isn't a message's shape, such as one that begins with an mbox's "From " line,
  is unrouted, as is header-shaped content neither named .eml nor stored as a message. mail's
  own limits reach MIME input: it reads every mbox as an mboxrd, so an mboxcl's Content-Length is
  ignored and an mboxo body line that was ">From " loses its ">"; and an .eml's or an mbox's
  message has no class, no creation or modification time, and SMTP addresses alone.

### Changed

- `MailMinimum` is 0.2.0, and so `MailSpec`'s minimum: `NewReader` refuses mail 0.1.0 with a
  `tool.VersionError` matching `tool.ErrTooOld`. A caller on mail 0.1.0 must move to 0.2.0. mise
  pins mail 0.2.0 from its GitHub release.
- The copy of mail's fixtures and goldens in `email/testdata` is at mail's tag v0.2.0, its new
  MIME fixtures added and every v0.1.0 file unchanged; the built .msg stays as built at v0.1.0.
- `generated/msg/08-forward-attached-message.msg`'s by-value .eml, unrouted in v0.4.0, is routed
  `RouteNested`: the .pst and .msg fixtures' routes are 6 nested and 4 unrouted, 51 of 51 right.

## [v0.4.0] - 2026-11-03

### Added

- `email`, which reads mail stores, Outlook .pst stores and .msg messages, for a model through
  spike-ai-pipeline-mail's CLI, on Linux, the standard library, and `tool`. It provides
  pre-inference preparation only, and the program it runs is a `tool.Tool` the caller built.
  Additive to v0.3.0: `tool`, `document`, and `office`'s existing API are unchanged.
- The `Reader`, built by `NewReader` over a `Config`: a mail Tool, required, that `MailSpec`
  declares, refused when older than `MailMinimum`, 0.1.0. `Reader.Read` returns a `Stream` for one
  `Request`, an input path and an optional attachments directory, and refuses an invalid one, with
  no input or a path that can't be made absolute, with an error matching `ErrRequest` before
  anything runs. Ranging over `Stream.Records` runs mail once and yields each line it writes while
  it runs, decoded one line at a time from a pipe through encoding/json/v2 into typed values
  mirroring schema "1": `Store`, `Folder`, `Message` (with `Address`, `Body`, `Derived`, and
  `Attachment`), and `Skipped`, so memory follows the record being read, never the store. A line of
  a kind the package doesn't know is kept as an
  `UnknownRecord`; a store line of another schema is an error matching `ErrSchema`. A loop that
  stops early stops mail.
- mail's outcomes as errors: each warning line on stderr is one of `Stream.Warnings`, and a read
  with skipped records and warnings succeeds; exit codes 1 to 4 are an `*ExitError` carrying
  mail's message that matches `ErrNotWritten`, `ErrUsage`, `ErrRefused` (an .ost's among them),
  or `ErrFailed`. A cancelled read returns an error matching the context's.
- Attachment routes: with an attachments directory, each `Attachment`'s `Path` names the file mail
  wrote, under its content's SHA-256, and `Attachment.Route` names the stage the content goes to
  next, and why, by the content's signature, never the stored MIME type: `RouteDocument` (PDF),
  `RouteOffice` (a ZIP with an OOXML extension, or an OLE2 file with a legacy one),
  `RouteVision` (PNG, JPEG, GIF, BMP, as stored), `RouteVisionConverted` (TIFF, EMF, WMF, for
  `office.Converter.ConvertPicture`), `RouteNested` (an embedded message, or an OLE2 file named
  .msg), or `RouteUnrouted`. The package never runs the stage.
- Threads: a `Threader`, fed each record while a Stream is read, keeps each message's threading
  keys alone; `Threader.Threads`, and `Threads` over a read's Records whole, build the store's
  threads as `Thread` trees by RFC 5256's REFERENCES algorithm over Message-ID, In-Reply-To, and
  References, with a message that names no parent taking the one its conversation index names.
  Placeholders are pruned as RFC 5256 says, roots with the same base subject are gathered, and
  children are ordered by sent date.
- `office.Converter.ConvertPicture` and `office.PictureRequest`: one TIFF, EMF, or WMF file, told by
  its content's signature, converted to PNG outside a document through `Convert`'s own picture
  conversion, the PNG named by the picture's SHA-256; a missing converter is an error, an EMF or
  WMF without LibreOffice matching `ErrNoLibreOffice` and any of the three without magick the new
  `office.ErrNoMagick`. It is what `RouteVisionConverted` hands a picture to.
- mail 0.1.0 pinned in mise from its GitHub release, checked against the release's digest, and
  reported by `mise run currency` when a newer one ships.
- The email fixtures in `email/testdata`: mail's committed fixtures and goldens copied byte for
  byte at its tag, pinned by a SHA-256 manifest the check verifies offline, refreshed by
  `mise run fixtures:mail`; and a built .msg attaching four of this repository's office fixtures
  and, by value, one of mail's .msg fixtures, rebuilt by mail's generator at its tag through `mise
  run fixtures:built`, which needs .NET and
  network and which the check never runs. The hermetic check runs a stand-in mail for exits,
  warnings, arguments, and cancellation, and the real pinned mail over every fixture against its
  goldens, routes, and threads.
- The email integration tier, which carries every fixture's attachments to their real stage and
  sends each distinct picture to Gemma 4 26B-A4B on the router `SPIKE_ROUTER_URL` names; and the
  findings tier, findings-tagged tests run by `mise run findings-email`, never in CI, which score
  threads and routes over the fixtures against hand truth and time the adapter on plaso's store
  and the scale store. What they measured is in `docs/findings/email.md`.
- Known limits of `email`: a by-value .eml attachment is unrouted; .ost is refused by mail; in a
  .pst, an embedded message is nested without its content; a by-reference attachment is
  unrouted; and the stores mail's generator writes carry no threading fields, so they thread by
  subject alone.

### Changed

- Currency: jdx/mise-action v5.2.5 to v5.2.6 in CI, and taiki-e/create-gh-release-action v1.11.0
  to v1.12.0 in the release workflow.
- The vision tests' router client and response helpers, which `document`'s, `office`'s, and
  `email`'s integration tiers each carried a copy of, are one internal package,
  `internal/visiontest`, built under the integration tag alone.

### Fixed

- The `tool` integration tests' container leak check counts only the containers of its own
  ffmpeg image, so the LibreOffice containers office's and email's tests start through the same
  runner, in parallel, no longer fail it.

## [v0.3.0] - 2026-11-02

### Added

- `office`, which prepares office documents, docx, xlsx, and pptx, and the legacy doc, xls, and
  ppt, for a model through spike-ai-pipeline-office's Open XML CLI, on Linux, the standard
  library, and `tool`. It provides pre-inference preparation only, and every program it runs is a
  `tool.Tool` the caller built. Additive to v0.2.0: `tool` and `document` are unchanged.
- The `Converter`, built by `NewConverter` over a `Config`: an office Tool, required, that
  `OfficeSpec` declares, refused when older than `OfficeMinimum`, 0.1.0; and optional LibreOffice
  and magick Tools. `Converter.Convert` spools a document read from an `io.Reader` into the
  working directory a `Request` names, runs office once with a media directory there, removes the
  spooled document, and returns a `Result`. With `JSON`, the default, the result is office's model
  decoded through encoding/json/v2 into a `Document` of typed values mirroring schema "1": a model
  of another schema is an error matching `ErrSchema` that names it, a property left out is none,
  an array keeps its nulls in place, and a block or inline of a kind the package doesn't know is
  kept as an `UnknownBlock` or `UnknownInline` with its JSON. With `Markdown` it is office's
  Markdown, unchanged but for the link rewrite below.
- office's outcomes as errors: exit codes 1 to 4 are an `*ExitError` carrying office's message
  that matches `ErrMediaNotWritten`, `ErrUsage`, `ErrRefused` (with office's reason), or
  `ErrFailed` through `errors.Is`. Each warning office writes to stderr is one of the result's
  `Warnings`, and a conversion with warnings succeeds. A cancelled conversion returns an error
  matching the context's.
- The media list: both results carry one `MediaFile` per file in the media directory, with
  office's stored name, its content type (`ContentType`), the file handed onward, and whether it
  was converted. A linked picture is no file and is never fetched.
- The picture standard: no stage re-encodes a picture lossily, and every conversion writes PNG.
  PNG, JPEG, GIF, and BMP are handed on exactly as stored. EMF and WMF are rendered through
  LibreOffice's Draw PNG export, all of a document's in one soffice run, at
  `Request.MetafileScale` times 96 dpi (`DefaultMetafileScale`, 3, when zero, at most
  `MaxMetafileScale`, 8), on an A4 page the run's en-GB profile fixes, and each trimmed to its
  picture through magick. TIFF is converted through magick, its first image alone. `MagickSpec`
  declares magick, minimum `MagickMinimum`, 7.1. A converted picture's PNG is named by its
  original's SHA-256 with `.png`, written beside the original, which stays, and magick writes it
  without the time of the run, so a picture converted twice is the same bytes. A picture conversion
  that fails fails the conversion.
- The Markdown link rewrite: each converted picture's exact stored name, `<sha256>.<ext>`, is
  swapped for its PNG's, parsing no Markdown; links to pictures handed on as stored stay as
  office wrote them.
- Legacy documents: a file with the OLE2 compound-file signature whose `Request.Name` has a
  legacy extension (.doc and .dot to docx, .xls and .xlt to xlsx, .ppt, .pps, and .pot to pptx)
  is converted to that OOXML format through soffice headless, then run through office, and the
  result's `ConvertedFrom` names the legacy format. Without a LibreOffice Tool it is refused with
  `ErrNoLibreOffice` before any program runs. Any other OLE2 file goes to office, which refuses
  it. `LibreOfficeSpec` declares soffice, minimum `LibreOfficeMinimum`, 24.2.
- The default LibreOffice image, `LibreOfficeImage`: gotenberg/gotenberg:8.39.0-libreoffice,
  LibreOffice 26.8.1.1, pinned by its multi-arch index digest, which a caller may replace and
  `mise run currency` scans. soffice runs in it under the container runner's full lockdown, no
  network, the caller's uid:gid, and every capability dropped, with its user profile inside the
  mounted working directory, removed when the conversion ends.
- office 0.1.0 pinned in mise from its GitHub release, checked against the release's digest, and
  reported by `mise run currency` when a newer one ships. The hermetic check runs a stand-in
  office for exits 1 to 4, warnings, arguments, and the working directory, stand-in soffice and
  magick for their arguments, and the real pinned office over the OOXML fixtures.
- The office fixtures in `office/testdata`: OOXML files copied byte for byte from office
  v0.1.0's, with a sources file; legacy files LibreOffice saved from them by `generate.sh`; three
  Office-written Apache POI files, one per legacy format, with hand truth and their license
  notices; and a docx holding a TIFF.
- The office integration tier: every legacy fixture converted through the default image, with
  the round trip's loss per element kind and the container's footprint recorded; every EMF, WMF,
  and TIFF picture converted to a PNG that decodes, and to the same bytes twice; and every
  distinct picture handed onward sent to Gemma 4 26B-A4B on the router `SPIKE_ROUTER_URL` names,
  its time and image tokens recorded. What they measured is in `docs/findings/office.md`.
- Known limits of `office`: an EMF or WMF picture needs both the LibreOffice and magick Tools,
  and a TIFF needs magick, or no picture is handed onward. A legacy file is recognized only by
  its OLE2 signature and a legacy extension. Legacy conversion inherits LibreOffice's losses,
  which [the office finding](docs/findings/office.md) records. The trim removes a white border
  the picture itself has, and a blank metafile becomes a 1x1 PNG. The package evaluates nothing
  office doesn't: a formula's value is the one the file caches. Pictures are listed as the
  document holds them, with no layout: one that another covers, off the slide or page, or cropped
  away is still listed and handed onward.

### Changed

- Currency: jdx/mise-action v5.2.4 to v5.2.5 in CI, and the README's and the ffprobe integration
  tests' linuxserver/ffmpeg image 9.0.2-cli-ls87 to 9.0.2-cli-ls88, pinned by digest.

## [v0.2.0] - 2026-10-30

### Added

- `document`, which prepares a PDF's pages for a model's vision, on Linux, the standard library,
  and `tool`. It provides pre-inference preparation only: rendering, correction, text layers, and
  regions; a layer above sends the prepared pages to a model and owns the requests, retries, and
  any final step. The module requires spike-ai-pipeline-client at v0.4.0 for its tests alone, the
  vision harness the integration and bake-off tiers run on; no released file imports it.
- The page-renderer interface, `Renderer`: `Count` returns a PDF's page count, and `Render`
  writes one page, 1-based, as PNG at a DPI to a writer, one process per page.
  `CheckRender(page, dpi)` makes a renderer's refusals, running no tool: a DPI of zero or less
  (`ErrDPI`) and a page below 1 (`ErrPageRange`). It doesn't count the pages, so a `Render` past
  the last page gives its rendering tool's own error; a caller walking a PDF's pages refuses one
  past the last against its own count.
- The released renderer, `Ghostscript`, the renderer bake-off's winner on accuracy, speed, and
  memory: `NewGhostscript` builds it over a gs `Tool` from either runner, refusing one older than
  `GhostscriptMinimum`, 10.0, and `GhostscriptSpec` declares gs for the constructors. `Count`
  counts a PDF's pages with gs's own PDF interpreter; `Render` renders one page per gs run with
  the png16m device, text and graphics anti-aliased, at the DPI, to PNG on stdout. gs reads the
  PDF by its absolute path, in the PDF's directory, so the container runner's mount holds it. gs
  past the last page exits zero with a message in place of a PNG; `Render` returns that message
  as its error and writes nothing. Ghostscript is AGPL-licensed, and the renderer runs it as a
  separate program, never linked.
- Text layers: `Ghostscript.Text` reads a page's text layer with gs's txtwrite device, one gs
  run, gs's messages sent to stderr so stdout is the text alone, gs's physical layout kept; a
  page past the last is an error wrapping `ErrPageRange`. `HasText` is the rule for a page having
  a text layer: its text not empty once whitespace is trimmed. The optional `TextExtractor`
  interface names `Text`, which `Ghostscript` satisfies.
- Regions: a `Region` is a band of a page in fractions of the page as rendered, top, bottom,
  left, and right, each 0 to 1, which `CheckRegion` refuses out of range or inverted
  (`ErrRegion`). `Ghostscript.RenderRegion` renders one in a single gs run that crops in gs's
  own PostScript, the region's pixels the whole page's; the optional `RegionRenderer` interface
  names it, and `Renderer` is unchanged.
- The corrections stage: `Correction` values `Deskew`, `Despeckle`, `ContrastStretch`, `Unsharp`,
  and `Grayscale`, each one ImageMagick operator, applied in that fixed order however a request
  lists them. `NewCorrector` builds the stage over a magick `Tool` from either runner, refusing
  one older than `MagickMinimum`, 7.1, and `MagickSpec` declares magick for the constructors.
  `Corrector.Correct` runs one magick per page image, PNG on stdin to PNG on stdout, and no
  corrections copies the image without running magick. None is applied by default.
- The correction catalog, for a prompt that lets a model choose corrections: `Corrections`
  lists every `Correction` in the fixed order, `Correction.Description` gives each a one-line
  description of what it fixes and when to use it, beside its name from `String`, and
  `ParseCorrection` reads a name back, without regard to case, an unknown name an error naming
  it. The stage's one table of corrections holds all of it.
- `Corrector.Assess` measures a page image for the defects the corrections fix, in one magick
  run, returning an `Assessment`: the image's size, its skew in degrees (`-deskew 40%`'s
  `%[deskew:angle]`), contrast (the standard deviation of its luminance), noise (its mean
  difference from its 3x3 median), sharpness (the standard deviation of its Laplacian), the
  fraction of its pixels that are colored, and whether it is effectively gray, below
  `GrayscaleColored` of its pixels colored. Output missing a measure, or one that isn't a
  number, is an error.
- `Preparer`, the prepared-page stream over the stages, built by `NewPreparer` from a `Renderer`
  and an optional `*Corrector`, refusing a nil renderer. An `ImagesRequest` names the working
  directory the caller owns, the DPI (zero means 150), the corrections, which a preparer with no
  corrections stage refuses, and the concurrency (zero means 1); with `Text` it reads each page's
  text layer into its `PageImage`, and with `SkipText` it also renders no page that has one, for
  the flow of text layer first, vision only for the pages without one; its optional `Region`
  renders that band of every page, such as a header or footer. A preparer whose renderer is no
  `TextExtractor` refuses `Text` and `SkipText`, and one whose renderer is no `RegionRenderer`
  refuses a `Region`. A request is refused before anything runs. `Preparer.Images` returns a
  lazy `iter.Seq2[PageImage, error]`: it spools the PDF, read from an `io.Reader`, into the
  request's directory, counts its pages, and renders and corrects each page, yielding a
  `PageImage` of its number, the page count, its PNG, and its text layer, with at most the
  request's concurrency of pages being prepared at once. Pages come back in page order; a failed
  page yields its number and count with its error, and the stream carries on; a refused request,
  a failed spool or count, or an ended context yields one error and ends the stream. Breaking out
  of the range cancels the work in flight and returns once it has ended, and the spooled PDF is
  removed when the stream ends. The package sets no size limit on a page's image.
- `tool.Usage` and `Invocation.Usage`: a run's resource usage, user and system CPU time
  (`UserCPU`, `SystemCPU`), which `Run` sets when the pointer isn't nil, whether the program
  exited zero, exited nonzero, or was cancelled. The exec runner fills it from the program's
  rusage, which on Linux counts the children the program reaped; the container runner leaves it
  zero, since the docker CLI's usage isn't the container's. It reports no peak memory: Go starts
  a program with a vfork-style clone, so the program's rusage peak starts at the Go parent's
  resident memory.
- The document fixtures in `document/testdata`, letter size, with each page's ground truth: a
  six-page clean PDF and a nine-page degraded PDF, regenerated byte for byte from a typst source
  by `generate.sh`, whose pages carry skew, fade, speckle, and blur each at a moderate and a
  severe level and all four on one page; and a seven-page scanned PDF, real 300-dpi scans copied
  without re-encoding from herald's marked documents, whose truth is transcribed by hand.
- The bake-off tier, bakeoff-tagged tests that send pages to Gemma 4 26B-A4B and never run in CI,
  with its tasks: `bakeoff` (the renderer bake-off), `bakeoff-corrections` (the corrections
  matrix), `bakeoff-placement` (instruction placement), `bakeoff-flow` (the staged two-pass flow),
  and `bakeoff-tiles` (tile shape). What they measured is in `docs/findings/documents.md`.

## [v0.1.0] - 2026-10-29

### Added

- `tool`, which runs mature system tools behind one contract, on Linux and the standard library
  alone. A `Spec` declares a tool: its program, the arguments that print its version, a pattern
  whose first capture group is the version, the minimum version, and the `KillAfter` period, how
  long a cancelled run has after SIGTERM before SIGKILL. A runner's constructor finds the program
  and checks its version, so a missing or outdated tool fails at startup; the `Tool` it returns
  runs an `Invocation` (arguments, working directory, and standard streams) with `Tool.Run` under
  the caller's context, and an adapter written against `Tool` runs the same code whichever runner
  built it.
- The version rule: `Version`, dotted numbers compared as numbers, `ParseVersion`, and
  `Version.Compare`. A tool older than its minimum, or whose output holds no version, is a
  `*VersionError` wrapping `ErrTooOld` or `ErrNoVersion`. A program that exits nonzero is an
  `*ExitError`: its name, exit code, and stderr's tail.
- The exec runner: `NewExec` runs a program directly, and `NewScript` runs a script through its
  interpreter.
- Cancellation: through the exec runner, a cancelled run's process group gets SIGTERM, then
  SIGKILL once the spec's `KillAfter` period passes, 10 s when unset. The group's SIGKILL lands
  before its leader is reaped, so no child outlives it. Through either runner, a run that fails
  once its context has ended, even one whose program exited on its own as the context ended,
  returns an error matching the context's (`errors.Is` `context.Canceled` or
  `context.DeadlineExceeded`), never an `*ExitError`; it names the program, the context's error,
  and the program's own outcome once each, such as
  `tool: ffprobe: context canceled (exit status 143)`.
- Stdin ownership: an `*os.File` stdin passes straight to the program; any other reader is
  copied into a pipe `Run` owns. Once the program has exited, `Run` waits for that copy at most
  the `KillAfter` period through the exec runner and 2 s through the container runner, and a
  cancelled run doesn't wait for it, so a caller's reader held open never keeps `Run` from
  returning. A program that exits without reading all of stdin is no error, and a failed Read of
  stdin is `Run`'s error only when the program otherwise succeeds. The cost: after `Run` returns,
  the copy may make one more Read of the caller's reader.
- The container runner: `NewContainer` runs a program inside an `Image` pinned by digest,
  `repo:tag@sha256:` and 64 hex digits, through the docker CLI, pulling the image when it isn't
  present and checking the program's version inside it. A run is locked down (no network, no
  capabilities, the caller's uid:gid, `--rm`, `--init`, `--pull=never`), the image's `Network`
  and `CapAdd` its only relaxations, and it mounts the working directory at the same path, so
  the same arguments name the same files through either runner. A cancelled run's container is
  stopped by `docker stop` on its name, given the `KillAfter` period rounded up to whole seconds,
  under a context of its own, and removed by `docker rm --force`; the CLI's own signal
  forwarding is never relied on. A tool that exits nonzero is an `*ExitError` named for the
  spec's program; exit codes 125 to 127 are docker's own. A known limit: if processing itself is
  SIGKILLed, a running container runs on until its tool exits, and `--rm` removes it then.
- The CI pair: a check workflow on every pull request into and push to main, and a release
  workflow that publishes a pushed `v*` tag's section of this changelog.

[Unreleased]: https://github.com/JaimeStill/spike-ai-pipeline-processing/compare/v0.6.0...HEAD
[v0.6.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.6.0
[v0.5.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.5.0
[v0.4.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.4.0
[v0.3.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.3.0
[v0.2.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.2.0
[v0.1.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.1.0
