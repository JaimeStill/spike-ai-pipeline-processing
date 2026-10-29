# spike-ai-pipeline-processing

The processing layer of [spike-ai-pipeline](https://github.com/JaimeStill/spike-ai-pipeline): it
prepares messages, documents, and streams for native model capabilities, such as a PDF's pages
through vision, whether the payload is a whole blob or a stream.

Go builds only what the layer must own. Mature system tools do the specialized work behind the
tool contract this layer defines in its `tool` package, as herald's processing layer did: an exec
runner for binaries and scripts, and a container runner for pinned images. The tools the layer's
adapters run are a PDF renderer (chosen by bake-off), ImageMagick for images and image
correction, ffmpeg and ffprobe for audio and video, spike-ai-pipeline-office's Open XML CLI for
office documents, and LibreOffice in a container for legacy office formats only.

`tool` runs one program behind one contract, on Linux and the standard library alone. A `Spec`
declares the program: its name or path, the arguments that make it print its version, a pattern
whose first capture group is the version, the minimum version, and the KillAfter period. A
runner's constructor builds a `Tool` bound to that one program; it finds the program and checks
its version, so a missing or outdated tool fails at startup, and the Tool reports its `Name` and
the `Version` found. `Tool.Run` runs one `Invocation`, its arguments, a working directory the
caller owns, and optional stdin, stdout, and stderr, under the caller's context. An Invocation
names nothing about the runner, so an adapter written against Tool runs the same code whichever
runner built it.

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
Either way, Run returns an error matching the context's (`errors.Is` `context.Canceled` or
`context.DeadlineExceeded`), never an `*ExitError`, and it names the program's own outcome once:
`tool: ffprobe: context canceled (exit status 143)`.

A program that exits nonzero is an `*ExitError`: the program's name, its exit code, and, when the
caller passed no stderr writer, the last 4 KiB of its standard error; given a writer, all of
stderr goes to it and the error has no tail. A constructor's refusal over the version is a
`*VersionError`, naming the program, the version found, and the minimum, its cause `ErrTooOld`,
`ErrNoVersion`, or the version command's error.

An `*os.File` stdin passes straight to the program. Any other reader is copied into a pipe Run
owns, so a reader the caller holds open never keeps Run from returning: once the program has
exited, Run waits for the copy at most a short bound, the KillAfter period through the exec runner
and 2 s through the container runner, and a cancelled run doesn't wait for it. A program that
exits without reading all of stdin is no error, and a failed Read of stdin is Run's error only
when the program otherwise succeeds. The cost is one more Read: after Run returns, the copy may
make one more Read of the caller's reader, and it ends when that Read returns.

A known limit: if processing itself is SIGKILLed, a running container runs on until its tool
exits, and `--rm` removes it then.

It builds on spike-ai-pipeline-client by tag, and is released by tag, `vX.Y.Z`, with a CHANGELOG.
The spike's question, evidence, and answer are in the root repository's README.

Apache-2.0.
