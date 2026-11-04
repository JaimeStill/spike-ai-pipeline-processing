# Changelog

All notable changes to `github.com/JaimeStill/spike-ai-pipeline-processing` are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/JaimeStill/spike-ai-pipeline-processing/compare/v0.5.0...HEAD
[v0.5.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.5.0
[v0.4.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.4.0
[v0.3.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.3.0
[v0.2.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.2.0
[v0.1.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.1.0
