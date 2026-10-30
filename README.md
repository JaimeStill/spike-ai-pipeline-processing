# spike-ai-pipeline-processing

The processing layer of [spike-ai-pipeline](https://github.com/JaimeStill/spike-ai-pipeline): it
prepares messages, documents, and streams for native model capabilities, such as a PDF's pages
through vision, whether the payload is a whole blob or a stream.

Go builds only what the layer must own. Mature system tools do the specialized work behind the
tool contract this layer defines in its `tool` package, as herald's processing layer did: an exec
runner for binaries and scripts, and a container runner for pinned images. The tools the layer's
adapters run are Ghostscript for PDF pages (chosen by bake-off), ImageMagick for images and image
correction, ffmpeg and ffprobe for audio and video, spike-ai-pipeline-office's Open XML CLI for
office documents, and LibreOffice in a container for legacy office formats only.

`tool` runs one program behind one contract, on Linux and the standard library alone. A `Spec`
declares the program: its name or path, the arguments that make it print its version, a pattern
whose first capture group is the version, the minimum version, and `KillAfter`, the KillAfter
period: how long a cancelled run has after SIGTERM before SIGKILL. A runner's constructor builds
a `Tool` bound to that one program; it finds the program and checks its version, so a missing or
outdated tool fails at startup, and the Tool reports its `Name` and the `Version` found.
`Tool.Run` runs one `Invocation`, its arguments, a working directory the caller owns, and optional
stdin, stdout, and stderr, under the caller's context. An Invocation names nothing about the
runner, so an adapter written against Tool runs the same code whichever runner built it.

```go
spec := tool.Spec{
	Program:        "ffprobe",
	VersionArgs:    []string{"-version"},
	VersionPattern: regexp.MustCompile(`ffprobe version n?(\d+(?:\.\d+)*)`),
	Minimum:        "9.0",
}
probe, err := tool.NewExec(ctx, spec)
if err != nil {
	return err
}
var out bytes.Buffer
err = probe.Run(ctx, tool.Invocation{
	Args:   []string{"-v", "error", "-show_format", "-of", "json", "tone.wav"},
	Dir:    dir,
	Stdout: &out,
})
```

The exec runner runs a program directly. `NewExec` looks a name up on PATH and takes a path with a
separator as given. `NewScript` runs a script through its interpreter: the interpreter is found
and checked as a binary is, the script must exist at construction, and Run executes the
interpreter with the script and then the invocation's arguments. The Tool is named for the
script, and its version is the interpreter's.

The container runner, `NewContainer`, runs the program inside an `Image` through the docker CLI,
the only runtime, which it runs through the exec runner. The CLI is found and checked as a
program is, against the server's version, minimum 20.10, so a daemon it can't reach fails at
construction. The image's reference must be pinned by digest, its tag kept for readers:
`repo:tag@sha256:` and 64 hex digits. Construction pulls the image by that reference when it
isn't present, then runs the spec's version command inside the image, the spec's program as the
entrypoint, so a wrong entrypoint fails at startup too. Every run is locked down: `--rm`,
`--init`, `--network=none`, the caller's uid:gid as `--user`, `--cap-drop=ALL`, `--pull=never`, a
name of its own (`tool-` and a random suffix), and the spec's program as `--entrypoint`. The
image's `Network` and `CapAdd` are the two relaxations, a network to join and capabilities to add
back, and nothing else of the lockdown changes. A run mounts its working directory at the same
absolute path and works in it, so the same arguments name the same files through either runner,
and files the tool writes there belong to the caller.

```go
probe, err := tool.NewContainer(ctx, "", tool.Image{
	Ref: "linuxserver/ffmpeg:9.0.2-cli-ls87@sha256:" +
		"6547359fac23a2747e606d380e10267b2923a38f68485d9cacefd039e74d3aa8",
}, spec)
```

A version is dotted numbers, compared component by component as numbers, a missing component
counting as zero, so 9.0 equals 9.0.0. The spec's pattern captures only the numbers, which is how
ffprobe's n9.0.2 or ImageMagick's 7.1.2-34 is read. A version check is a minimum, never an exact
version.

Cancelling a run's context stops the tool. Through the exec runner, the program leads a process
group of its own, which gets SIGTERM, then SIGKILL once the KillAfter period passes, 10 s when the
spec sets none; the group's SIGKILL lands before the program is reaped, so nothing it started
outlives it. Through the container runner, `docker stop` on the run's name stops the container,
given the KillAfter period rounded up to whole seconds, under a context of its own, and
`docker rm --force` removes whatever remains; the CLI's own signal forwarding is never relied on.
Either way, a run that fails once its context has ended, even one whose program exited on its
own as the context ended, returns an error matching the context's (`errors.Is`
`context.Canceled` or `context.DeadlineExceeded`), never an `*ExitError`, and the error names the
program's own outcome once: `tool: ffprobe: context canceled (exit status 143)`.

A program that exits nonzero is an `*ExitError`: the program's name, its exit code, and, when the
caller passed no stderr writer, the last 4 KiB of its standard error; given a writer, all of
stderr goes to it and the error has no tail. Through the container runner, exit codes 125 to 127
are docker's own: 125 is a failure of docker's, and 126 and 127 an entrypoint it can't run. A
constructor's refusal over the version is a `*VersionError`, naming the program, the version
found, and the minimum, its cause `ErrTooOld`, `ErrNoVersion`, or the version command's error.

An `*os.File` stdin passes straight to the program. Any other reader is copied into a pipe Run
owns, so a reader the caller holds open never keeps Run from returning: once the program has
exited, Run waits for the copy at most the KillAfter period through the exec runner and 2 s
through the container runner, and a cancelled run doesn't wait for it. A program that exits
without reading all of stdin is no error, and a failed Read of stdin is Run's error only when the
program otherwise succeeds. The cost: after Run returns, the copy may make one more Read of the
caller's reader, and it ends when that Read returns.

An Invocation's `Usage`, when not nil, asks for the run's resource usage, which Run sets whatever
the run's outcome: `UserCPU` and `SystemCPU`, the program's CPU time. The exec runner fills it
from the program's rusage once it is reaped, which on Linux counts the children the program itself
reaped, such as a converter's delegate. The container runner leaves it zero, since the docker
CLI's usage isn't the container's. It reports no peak memory: Go starts a program with a
vfork-style clone, so Linux records the Go parent's resident memory in the program's peak, and the
figure reads the caller's size rather than the program's whenever the caller is the larger.

A known limit: if processing itself is SIGKILLed, a running container runs on until its tool
exits, and `--rm` removes it then.

`document` prepares a PDF's pages for a model's vision, on Linux, the standard library, and
`tool`. It provides pre-inference preparation only: a layer above sends the prepared pages to a
model and owns the requests, retries, and any final step. Every system tool it runs is a
`tool.Tool` the caller built, through either runner.

A `Renderer` counts a PDF's pages and renders one page, 1-based, as PNG at a DPI, one process per
page; `CheckRender` makes its refusals, a DPI of zero or less (`ErrDPI`) and a page below 1
(`ErrPageRange`). The released renderer is `Ghostscript`, over gs, built by `NewGhostscript` from
a gs Tool that `GhostscriptSpec` declares, minimum `GhostscriptMinimum`, 10.0. gs won the
renderer bake-off against pdftoppm, pdftocairo, vips, and magick: it tied them on accuracy, and
won as the fastest, at about 0.126 s a page, and the lightest in memory, 27 MiB, with a single
program.
Ghostscript is AGPL-licensed, so the package runs gs as a separate program and never links it.

`Ghostscript.Text` reads a page's text layer with gs's txtwrite device, and the optional
`TextExtractor` interface names it; `HasText` is the rule for a page having one, its text not
empty once whitespace is trimmed. `Ghostscript.RenderRegion` renders a `Region` of a page, a band
in fractions of the page such as a header or footer, cropped in gs's own PostScript at the whole
page's pixels; the optional `RegionRenderer` interface names it, and `CheckRegion` refuses a region
out of range (`ErrRegion`).

The corrections stage, a `Corrector` built by `NewCorrector` over a magick Tool that `MagickSpec`
declares, minimum `MagickMinimum`, 7.1, runs a page image through ImageMagick for the corrections a
request names: `Deskew`, `Despeckle`, `ContrastStretch`, `Unsharp`, and `Grayscale`, always in that
order, one magick run a page. None is applied by default: the measurements found no correction
that helps without hurting elsewhere. `Corrections`, `Correction.Description`, and
`ParseCorrection` are the catalog for a prompt that lets a model choose corrections, and
`Corrector.Assess` measures a page image, in one magick run, for the defects they fix: an
`Assessment` of its size, skew, contrast, noise, sharpness, and color.

A `Preparer`, built by `NewPreparer` from a Renderer and an optional Corrector, puts the stages
together. `Preparer.Images` streams a PDF's prepared pages as a lazy `iter.Seq2[PageImage, error]`:
it spools the PDF, read from an `io.Reader`, into the working directory the `ImagesRequest` names,
counts its pages, and renders and corrects each page at the request's DPI (150 by default) and
concurrency, yielding each `PageImage`, its number, the page count, its PNG, and its text layer, in
page order. A request may read each page's text layer (`Text`), skip rendering the pages that have
one (`SkipText`), for the flow of text layer first, vision only for the pages without one, and
render one `Region` of every page. A failed page yields its error and the stream carries on;
breaking out of the range cancels the work in flight.

```go
gs, err := tool.NewExec(ctx, document.GhostscriptSpec())
if err != nil {
	return err
}
render, err := document.NewGhostscript(gs)
if err != nil {
	return err
}
prep, err := document.NewPreparer(render, nil)
if err != nil {
	return err
}
for page, err := range prep.Images(ctx, pdf, document.ImagesRequest{Dir: dir, SkipText: true}) {
	if err != nil {
		return err
	}
	// page.Text when HasText(page.Text), page.PNG otherwise
}
```

[docs/findings/documents.md](docs/findings/documents.md) records what the measurements behind
these choices found: which renderer, whether to reason while transcribing, whether corrections
help, where the instructions go, whether the staged two-pass flow pays, and what a page costs in
image tokens. The run results it cites are in `docs/findings/documents-results/`.

Known limits:

- Page 7 of the scanned fixtures carries a faded "NOFORN" marking at its foot that the vision
  model never reads, at any DPI, correction, band, or tile measured; a marking that faded can't
  be left to vision.
- A text layer keeps gs's physical layout, so a multi-column page or a form reads out of reading
  order; restore the order, or send such pages to vision.
- Ghostscript is AGPL-licensed; the package runs gs as a separate program, never linked.
- The container runner's limit above: a SIGKILLed processing leaves a running container to its
  tool's exit.

The tests run in tiers. `mise run check` is hermetic: build, vet, format, tidy, the unit tests
under the race detector, and lint, the integration and bakeoff tags included. `mise run
integration` runs the integration-tagged tests against this machine's tools and docker, and the
vision tests against the llama.cpp router `SPIKE_ROUTER_URL` names, gb10-2's by default. The
bake-off tier, bakeoff-tagged tests, sends pages to Gemma 4 26B-A4B on the same router and never
runs in CI. Each task is a series of `go test` runs, each capped at 10 minutes, and takes from a
few minutes to about 25:

- `mise run bakeoff`: the renderer bake-off, five renderers over the clean, degraded, and scanned
  sets.
- `mise run bakeoff-corrections`: the corrections matrix, each correction alone, none, and all
  five, at 150 and 72 dpi.
- `mise run bakeoff-placement`: the transcription instructions beside the image against the
  request's instructions.
- `mise run bakeoff-flow`: the staged two-pass flow, text layer first, against its baselines.
- `mise run bakeoff-tiles`: tile shape, near-square tiles against a full-width band.

The tests' vision harness, the bake-offs, and the staged flow are test code, not released.

Host prerequisites: gs 10.0 or later and ImageMagick's magick 7.1 or later on PATH for `document`,
and docker, server 20.10 or later, for the container runner. The renderer bake-off also runs
poppler's pdftoppm and pdftocairo and libvips. typst and qpdf are needed only to regenerate the
fixtures in `document/testdata`, whose README says how.

The layer requires spike-ai-pipeline-client at v0.4.0 for its tests alone, the vision harness the
integration and bake-off tiers run on; no released file imports it. It is released by tag,
`vX.Y.Z`, with a CHANGELOG. The spike's question, evidence, and answer are in the root
repository's README.

Apache-2.0.
