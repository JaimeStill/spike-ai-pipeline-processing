# Changelog

All notable changes to `github.com/JaimeStill/spike-ai-pipeline-processing` are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/JaimeStill/spike-ai-pipeline-processing/compare/v0.2.0...HEAD
[v0.2.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.2.0
[v0.1.0]: https://github.com/JaimeStill/spike-ai-pipeline-processing/releases/tag/v0.1.0
