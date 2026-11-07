# spike-ai-pipeline-processing

The processing layer of [spike-ai-pipeline](https://github.com/JaimeStill/spike-ai-pipeline): it
prepares messages, documents, and streams for native model capabilities, such as a PDF's pages
through vision, whether the payload is a whole blob or a stream.

Go builds only what the layer must own. Mature system tools do the specialized work behind the
tool contract this layer defines in its `tool` package, as herald's processing layer did: an exec
runner for binaries and scripts, and a container runner for pinned images. The tools the layer's
adapters run are Ghostscript for PDF pages (chosen by bake-off), ImageMagick for images and image
correction, ffmpeg and ffprobe for audio and video, spike-ai-pipeline-office's Open XML CLI for
office documents, LibreOffice in a container for legacy office formats and RTF only,
spike-ai-pipeline-mail's CLI for Outlook mail stores and MIME messages,
spike-ai-pipeline-onenote's CLI for OneNote sections, notebooks, and packages, and 7-Zip's 7zz for
the archives the standard library doesn't read.

Its packages, each below in turn: `tool`, the contract every program runs behind; `route`, which
names the stage content goes to by its signature; `inspect`, which inspects every part before its
stage; `dispatch`, which walks an input and every part found in it to the stages, with the
format packages' adapters, an index, and a pool; the format packages, `picture`, `document`,
`office`, `email`, and `onenote`, each pre-inference preparation of its formats; `sevenzip`, which
lists and extracts the archives the standard library doesn't read through 7zz; `audio`, which
cuts speech into windows a model's audio input takes whole and joins their transcripts; and
`media`, which reads a live or recorded source's audio and video through one ffmpeg run and
streams its windows and frames as they arrive.
`internal/cli` holds the CLI contract the format packages share.

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
	Ref: "linuxserver/ffmpeg:9.0.2-cli-ls89@sha256:" +
		"822d04f0b2d0c4847430f5cc90f20ea48be866bb502ece63fdeb729400bf5361",
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

`route` identifies content by its signature and names the stage it goes to next, on the standard
library alone: pure functions, which run no stage and no program. `Identify` names the `Kind` a
file's first `HeadLen` bytes begin, and `File` routes the file at a path by that kind, never by its
stored MIME type, and by its name's extension only where the signature can't tell formats apart:
within a ZIP or OLE2 container, for a cabinet, for an mbox's "From " line, which prose named with a
text extension begins too, and for content of no signature. The routes are `Document`, a PDF;
`Office`, an OOXML package office reads, a legacy OLE2 document, an encrypted OOXML package (an OLE2
file holding an EncryptedPackage stream, named as OOXML), which office refuses as encrypted, an ODF
package, an RTF, a WordPerfect document, or an .xlsb; `Vision`, PNG, JPEG, GIF, and BMP as stored;
`VisionConverted`, TIFF, EMF, WMF, SVG, HEIC, WebP, and AVIF; `Nested`, a .msg, a header-shaped
.eml, a .pst, or an mbox; `OneNote`; `Archive`, ZIP, tar, gzip, zstd, 7z, and a cabinet; `Audio`;
`Video`; `Text`; and `Unrouted`, an executable, an .ost, HTML, and anything else, each with a reason
in a few words. The caller, or a dispatcher, runs the stage, and reports "no processor yet" for one
that doesn't exist: no stage takes ODF, WordPerfect, or .xlsb yet, nor an RTF or a legacy
document without LibreOffice, nor a zstd stream, a
7z archive, or a cabinet without 7-Zip, nor a HEIC, WebP, or AVIF without magick's reader.

```go
r, why, err := route.File(path, "report.pdf", "application/octet-stream")
if err != nil {
	return err
}
// r == route.Document, why == "PDF signature"
```

`inspect` inspects every part before its stage, on the standard library and `route` alone, and says
whether it may go on. The safety standard is that unsafe means unsafe to read. `Inspect` reads the
file at a path, with its name, streaming, its memory bounded whatever the file's size, and returns a
`Report`: the part's `route.Kind`, its `Flag`s, each a `Check`, a `Class`, and its evidence, and a
`Verdict`. A flag of class `Active` is content that could act, or that a reader can't see into, and
the part proceeds by default, since no stage executes it: a signature that disagrees with the
extension, a polyglot, executable content or an executable or double extension, an OOXML package's
VBA project, macro sheet, ActiveX controls, OLE objects, and external relationships other than
hyperlinks, the PDF names pdfid counts, hex-escaped forms included, an SVG's scripts, event
handlers, external hrefs, and foreignObjects, and encrypted content, which is only unreadable. A
flag of class `Unsafe` quarantines the part by default: no stage runs, its content is kept, and it
is reported with its evidence. Those are a ZIP entry with a ".." element, an absolute path, or a
drive letter, a symbolic link entry, an entry past both 1,000:1 and 1 GiB expanded by its central
directory's declared sizes, the thresholds `Options` sets, and a central directory that can't be
read; and a tar entry, read from its headers as a ZIP's is from its central directory, with a ".."
element, an absolute path, or a drive letter, a symbolic or hard link, a device or a FIFO, an entry
past both thresholds by the size its header declares against the whole tar's, as only a sparse entry
or a lying header can be, and headers that can't all be read. `CheckListing` holds a 7z's, a
cabinet's, or a zstd stream's listing, as 7-Zip lists it, to the same rules, by the same name
checks: `Inspect` runs no program, so the archive stage lists such an archive through 7-Zip and
calls it before extracting anything, a 7z's encrypted entry active as a ZIP's is. `Options.Policy`
overrides any check's action. The report is a function of the bytes and the name, so an index may record it by SHA-256. A
container's members are inspected as parts of their own once a stage extracts them, so a .tar.gz's
tar is inspected once the archive stage decompresses it, and that stage enforces the bomb rule again
on the bytes it writes, since a declared size may lie, a gzip stream's among them, which declares
its expanded size only modulo 4 GiB.

```go
rep, err := inspect.Inspect(ctx, path, "invoice.pdf.exe", inspect.Options{})
if err != nil {
	return err
}
if rep.Verdict == inspect.Quarantine {
	// keep the content, run no stage, and report rep.Flags
}
```

`dispatch` walks an input and every part found in it to the stages that prepare them, on the
standard library, `route`, and `inspect`, and, for the text stage's decoding, golang.org/x/text,
and never calls a model. A caller registers a `Stage` per
route; each names its `Identity`, its name, tool version, and configuration hash, and yields the
`Output`s it prepares, text, Markdown, an image, or a record, in a file or inline, and the parts it
finds. `Dispatcher.Dispatch` hashes each part, SHA-256, streamed, before any stage runs, routes and
inspects it, and yields `Event`s depth-first in each stage's own order: each output is an output
event, `ev.Output` set, as the stage yields it, among the subtrees of the parts it finds, and each
part's own event, after its outputs and its descendants', carries its outcome: prepared, no
processor yet, seen, cycle, quarantined, encrypted, failed, or limited, only under a cap the caller
set, with a count of its outputs and its children. The dispatcher holds nothing of an output once
the loop body returns for it, nor of a found part once its event is done but the `Child` reference
it appends to its parent's pending entry by `Index.AppendChild`, so a store of millions of messages
and attachments streams through in the memory of one. An `Index`, content-addressed, records a
stage's outputs by the content's SHA-256 and the stage's identity, and the flags by the SHA-256
alone, so content it holds is seen, its outputs streamed back from the index as output events,
neither inspected nor prepared again; a claim makes one of two concurrent dispatches prepare it
while the other waits. Each output is appended to the claim's pending entry by `Index.Append` once
the loop body returns for its event, so a caller that persists it sets `ev.Output.Ref` and a later
seen part's output carries it; the part's own event commits the entry by `Record`, or `Release`
drops it when the part fails or the loop stops, and `Index.Outputs` and `Index.Children` read its
outputs and children back streamed. `MemoryIndex` and `FileIndex`, a directory of records written by
atomic rename, O_EXCL claim files reclaimed once stale, and two pending files of JSON lines per
claim, its outputs and its children, renamed beside its entry when recorded, ship. Each part's file
and working directory are removed once its own event is done, unless the caller called `Event.Keep`,
so the disk holds only the parts in flight. A stage holding content later records name again, as the
email and onenote stages hold what mail and onenote write once per distinct content, gives each
`Found` part its SHA-256 and a `Release`, which the dispatcher calls once the index holds the
content's entry and the caller kept nothing of it: the stage removes its original, and a later
record is found with no file and seen from the index, so a store's directory holds the attachments
of the messages in flight. Content with no stage for its route is released once the caller's loop
moves past its no-processor event without keeping it, and a later record naming it is no processor
yet again, with no file. Content the dispatcher didn't settle, failed, limited, quarantined, or a
cycle, keeps its original for the next record naming it.

`Dispatcher.Pool` dispatches an iterator of inputs over N workers sharing the dispatcher's index, so
content the inputs share is prepared once and seen by the rest, and yields every input's events as
one sequence of `PoolEvent`s, each its `Input` and its `Event`. An input's events keep their order,
and the inputs' interleave. The caller's loop body runs on one goroutine, and each worker waits for
it to return before its dispatch goes on, so each output is still appended, the entry recorded, and
the files removed, after the body returns; a worker holds one event at most, so a slow loop holds
the workers back and the disk holds what N dispatches have in flight. An error that stops one
input's dispatch is yielded with that input and the others go on; ctx ending stops them all, its
error yielded last.

`Input.After` resumes a .pst or an mbox after a message, its node id or ordinal: the email stage
passes it to mail as `--after`, mail 0.3.0 or later, and the store yields its store and folder
records and only the messages after it, with their attachments found. A resumed input's own entry
is never recorded, its outputs being the tail alone. For each message, the email stage yields its
attachments first, each found part dispatched whole, then its record, its Meta `id` the message's,
then its Markdown, which the record's Meta `markdown: follows` announces, so the message's last
output is the last of everything it holds. A caller resuming after a crash keeps a `FileIndex`
that outlives the process, so the attachments recorded before the crash are seen; the cursor, the
id of the last message whose outputs it has persisted, moved at each message's last output; and
an `email.Threader`, fed each message record of the store's own, decoded. Every N messages, or on a
timer, at a message's last output, it saves the cursor and the Threader, by `MarshalBinary`,
together: a checkpoint. Not at every message: each save marshals every message's keys so far,
3.5 ms at 25,000 messages in the onenote finding, so saving at every message grows with the
square of the store. It restores the Threader by `UnmarshalBinary`, dispatches with `After` set
to the checkpoint's cursor, and feeds the Threader the rest, whose threads are then the whole
store's; every message past the checkpoint comes again, and it overwrites what it had of them. A
dead process's claims and pending outputs wait out `FileOptions.Stale` before the resumed
dispatch takes them over.

The format packages' adapters ship in `dispatch`, on Linux, each a `Stage` built over the reader
or converter the caller already built and the version of its Tools, which `dispatch.Versions`
names: `DocumentStage`, a PDF's pages, each its text layer and a PNG; `OfficeStage`, office's
Markdown, each medium office wrote found as a part; `PictureStage`, a TIFF to a PNG per page,
"page N", its Meta naming the page and the count, and an EMF, WMF, SVG, HEIC, WebP, or AVIF to one
PNG; `VisionStage`, a PNG, JPEG, GIF, or BMP passed through as an image output; `EmailStage`,
mail's records, each message's attachments found as parts, then its record, then its Markdown,
an embedded message mail gives no file for prepared inline in its attachment's place;
`OneNoteStage`, onenote's records, each page's record then its Markdown, and each of its media
found as a part; `AudioStage`, an audio part's plan as a record, then each of its windows in
plan order as an audio output, a 16 kHz mono WAV whose Meta carries its index, span, cut, and
seams, for a layer above to transcribe and join with `audio.Join`; and `VideoStage`, an MP4,
Matroska, WebM, or AVI read by one unpaced run of media's `Streamer`, which yields a plan record
of its settings, then each frame as a PNG image output and each window as an audio output, with
the threshold it was cut under, interleaved as the run completes them. A found part's `Where`
names its place, such as `message 2097220, attachment 1` or `page {...}, page ink`, and it is
dispatched again, so a PDF attached to a message reaches the document stage and a page's ink
becomes a PNG. Kinds no adapter reads yet, ODF, WordPerfect, and xlsb, are no processor yet, as
are an RTF and a legacy document when the caller's Converter has no LibreOffice, a HEIC, WebP, or AVIF whose reader the caller's magick lacks, and audio and video when the caller has no ffmpeg and registers no audio or
video stage. An encrypted OOXML package, which office refuses, is encrypted. An audio source with
no audio stream fails, its error wrapping `audio.ErrNoAudio`. A video yields frames only when it
has no audio stream and windows only when it has no video stream; one with neither fails, its
error wrapping `media.ErrNoMedia`.

`ArchiveStage`, for the archive route, on any platform, reads a ZIP, a tar, and a gzip stream on
the standard library, and, given 7-Zip, a zstd stream, a 7z archive, a cabinet not named .onepkg,
and a ZIP entry of a method `archive/zip` doesn't read. It reads a ZIP through `archive/zip` one
entry at a time: each file entry is written into the part's directory under its content's SHA-256
and found as a part, `zip entry <name>`, its stored name only ever a name, so a nested ZIP is
dispatched again and its entries below it. A directory is no part. A stored, deflated, or bzip2
entry is decompressed by the standard library, bzip2 through `compress/bzip2`; any other method's
entry, such as Deflate64, implode, LZMA, xz, zstd, or PPMd, is extracted alone through 7zz under the
same rules. An encrypted entry is a record, recorded encrypted, and the ZIP encrypted once its other
entries are dispatched. It reads a tar through `archive/tar` the same way, each regular file found
`tar entry <name>`, a directory no part, and a link, a device, or a FIFO a record, with no link or
node made. It decompresses a gzip stream through `compress/gzip` into one part, named by its
header's name, else by the stream's own name without its .gz, or with a .tgz's .tar, found `gzip
content <name>` and routed again, so a .tar.gz is a gzip part holding a tar part; a zstd stream
the same way through 7zz, named by its own name without its .zst, or with a .tzst's .tar, found
`zstd content <name>`. A 7z's and a cabinet's files are extracted one at a time through 7zz, found
`7z entry <name>` and `cabinet entry <name>`, 7zz writing only to its standard output. Before
anything of a 7z, a cabinet, or a zstd stream is extracted, 7-Zip's listing is checked by
`inspect.CheckListing`, and a traversing, absolute, or drive letter name, a link, a device, or a
declared bomb quarantines the archive, the stage ending with a `QuarantineError` whose flags are its
evidence. An encrypted 7z entry is a record and the 7z encrypted once its other entries are
dispatched, and a 7z whose headers are encrypted is encrypted at once. The stage enforces the bomb
rule again on the bytes it writes, or reads from 7zz, under `ArchiveConfig.Inspect`, the
dispatcher's `inspect.Options`: an entry or stream past both thresholds as written, the ratio
against a ZIP entry's compressed size, the whole archive's size for a tar, 7z, or cabinet, or the
stream's, whatever its header or listing declares, is stopped, its partial file removed, and
recorded quarantined with its evidence. A ZIP or tar with an unsafe name, a link, or a device is
quarantined by `inspect` before the stage runs. With no 7-Zip, a zstd stream, a 7z archive, or a
cabinet is no processor yet, and a ZIP entry 7-Zip would read is a record.

`TextStage`, for the text route, on any platform, runs no tool: it yields one text output, "text",
UTF-8, its line endings as stored. It decides the encoding, never detects it: a byte order mark,
UTF-8's, UTF-32's, or UTF-16's, either byte order, which the output drops; else UTF-8, when the
whole part is valid UTF-8, ASCII among it; else windows-1252, as a fallback, with a warning that the
text may be in another encoding. It decodes through golang.org/x/text. Its Meta `encoding` names
the encoding and how it was decided, such as `utf-8 (byte order mark)`, `utf-16le (byte order
mark)`, `utf-8`, or `windows-1252 (fallback)`; bytes the encoding can't decode become U+FFFD, and
its Meta `warnings` counts them, a line each with the fallback's. Valid UTF-8 without a mark is
passed through, the output's file the part's own, as the vision stage passes a picture; other
text is decoded, streamed, into a file in the part's directory, never held in memory, and a large
part is held to the dispatcher's `Limits` alone. Over ~/docs' encodings, the fallback's text is the
truth for windows-1252 and ISO-8859-1, and garbled, the stage's known limit, for UTF-16 without a
mark and for KOI8-R, EUC-JP, Shift_JIS, and windows-1256.

`sevenzip` lists and extracts through 7-Zip's 7zz, minimum 24.01, which mise pins from its GitHub
release. `NewReader` builds a `Reader` over a `Config` of the Tool, refusing a missing or older one.
`Reader.List` reads an archive's technical listing, `-slt`, into a `Listing` of `Entry` items, each
its stored name, declared size and CRC-32, encryption, method, Unix mode, and link; a zstd stream's
one item takes the content size its frame header declares. `Reader.Extract` writes one item's
content to the caller's writer, 7zz writing to its standard output alone, `-so`, never to a path,
the item named whole, never a wildcard (`-spd`) or a list file (`-i!`). Every run is told its
format (`-t`) and given an empty password (`-p`), so it never prompts: encrypted headers or an
encrypted item fail with `ErrEncrypted`, and a file that isn't the format with `ErrNotArchive`.

```go
render, err := document.NewGhostscript(gs)
if err != nil {
	return err
}
prep, err := document.NewPreparer(render, nil)
if err != nil {
	return err
}
docs, err := dispatch.NewDocumentStage(dispatch.DocumentConfig{
	Preparer: prep, Version: dispatch.Versions(gs), Request: document.ImagesRequest{Text: true},
})
if err != nil {
	return err
}
offices, err := dispatch.NewOfficeStage(dispatch.OfficeConfig{Converter: oc, Version: dispatch.Versions(of, lo, mg)})
if err != nil {
	return err
}
pictures, err := dispatch.NewPictureStage(dispatch.PictureConfig{Converter: pc, Version: dispatch.Versions(mg, lo)})
if err != nil {
	return err
}
mail, err := dispatch.NewEmailStage(dispatch.EmailConfig{Reader: er, Version: dispatch.Versions(ml)})
if err != nil {
	return err
}
notes, err := dispatch.NewOneNoteStage(dispatch.OneNoteConfig{Reader: nr, Version: dispatch.Versions(on)})
if err != nil {
	return err
}
// The audio stage's Splitter runs ffmpeg and ffprobe, and cuts Concurrency windows at once.
speech, err := dispatch.NewAudioStage(dispatch.AudioConfig{
	Splitter: sp, Version: dispatch.Versions(ff, fp), Concurrency: 4,
})
if err != nil {
	return err
}
// The video stage's Streamer runs ffmpeg and ffprobe too, one unpaced run per part.
videos, err := dispatch.NewVideoStage(dispatch.VideoConfig{Streamer: st, Version: dispatch.Versions(ff, fp)})
if err != nil {
	return err
}
// The archive stage enforces the bomb rule as written, and checks a 7-Zip listing, under the
// dispatcher's inspect.Options; it reads 7z, zstd, cabinets, and ZIP's other methods through zr,
// a sevenzip.Reader over sz, the 7zz Tool.
archives, err := dispatch.NewArchiveStage(dispatch.ArchiveConfig{
	Inspect: opts, SevenZip: zr, Version: dispatch.Versions(sz),
})
if err != nil {
	return err
}
d, err := dispatch.New(dispatch.Config{
	Stages: dispatch.Stages{
		route.Document: docs, route.Office: offices, route.VisionConverted: pictures,
		route.Vision: dispatch.NewVisionStage(), route.Nested: mail, route.OneNote: notes,
		route.Archive: archives, route.Audio: speech, route.Video: videos,
		route.Text: dispatch.NewTextStage(),
	},
	Index: dispatch.NewMemoryIndex(), Dir: work, Inspect: opts,
})
if err != nil {
	return err
}
for ev, err := range d.Dispatch(ctx, dispatch.Input{Path: "mailbox.pst"}) {
	if err != nil {
		return err
	}
	if ev.Output != nil {
		// persist ev.Output and set ev.Output.Ref to its key; the index records the Ref
		continue
	}
	// ev is a part's own event, after its outputs and every part found in it
}
for pe, err := range d.Pool(ctx, slices.Values(inputs), 4) {
	if err != nil {
		// pe.Input's dispatch stopped, or, with pe.Input zero, ctx ended
		continue
	}
	// pe.Event is one of pe.Input's events, in that input's order
}
```

[docs/findings/onenote.md](docs/findings/onenote.md) records the dispatcher's routes over every
committed fixture against the hand route truth, inspection over ~/docs' hazards, vision's reading of
a OneNote page's ink beside OneNote's own recognition, the onenote adapter's cost beside the CLI on
three large sections, and the dispatcher's time, peak memory, and peak disk over mail's 2.1 GB scale
store, resumed midway, and through the pool at 1 to 8 workers. Every route matched the truth; vision
read all 14 of the ink's words; the dispatcher took 15.61 s on the scale store with removal on and
17.57 s with it off, against the email adapter's 13.72 s, its own process peaking at 36.8 MiB and
its working directory at 19.0 MB with removal on, 1.88 GB of attachments with it off; and a resumed
half took 0.54 of the whole. The run results it cites are in `docs/findings/onenote-results/`,
recorded at commit `96797c6`, before an encrypted OOXML package was routed to office: its tier log
has `encrypted.xlsx` unrouted. The scale store's dispatch, removal on and off, is a later run at
`fdbdab0`.

`picture` converts pictures to PNG for a model's vision and corrects page images for scan
defects, on Linux, the standard library, `tool`, and `route`. A `Converter`, built by
`NewConverter` over a `Config` of a magick Tool that `MagickSpec` declares, minimum
`MagickMinimum`, 7.1, and a LibreOffice Tool that `LibreOfficeSpec` declares, minimum
`LibreOfficeMinimum`, 24.2, run through the container runner in `LibreOfficeImage`, converts the
pictures `route.VisionConverted` names, TIFF, EMF, WMF, SVG, HEIC, WebP, and AVIF, told by their
signature. `Converter.Convert` converts one, such as a mail attachment, to a PNG in the `Request`'s
working directory named by the picture's SHA-256, a TIFF's first image alone;
`Converter.ConvertPages` converts one as Convert does, but a TIFF to a PNG per page, in page order,
in one magick run, each named by the TIFF's SHA-256, `-page`, its number from 1, and `.png`;
`Converter.ConvertBatch` converts several, the EMFs and WMFs in one soffice run, as office converts
a document's pictures. A HEIC's primary image, never a thumbnail or another image, and an animated
WebP's or AVIF's first frame are read through the kind's own coder and turned upright by their
stored orientation. Either Tool may be nil, and a picture that needs it is refused, with
`ErrNoMagick` or `ErrNoLibreOffice`. `NewConverter` runs magick's format listing,
`magick -list format`, once under its context, and a HEIC, WebP, or AVIF whose reader the listing
doesn't mark readable is refused with `ErrNoReader`.

```go
mg, err := tool.NewExec(ctx, picture.MagickSpec())
if err != nil {
	return err
}
lo, err := tool.NewContainer(ctx, "", tool.Image{Ref: picture.LibreOfficeImage}, picture.LibreOfficeSpec())
if err != nil {
	return err
}
pics, err := picture.NewConverter(ctx, picture.Config{Magick: mg, LibreOffice: lo})
if err != nil {
	return err
}
pngs, err := pics.ConvertPages(ctx, picture.Request{Dir: dir, Path: attachment})
```

The corrections stage, a `Corrector` built by `NewCorrector` over the magick Tool, runs a page
image through ImageMagick for the corrections a request names: `Deskew`, `Despeckle`,
`ContrastStretch`, `Unsharp`, and `Grayscale`, always in that order, one magick run a page. None is
applied by default: the measurements found no correction that helps without hurting elsewhere.
`Corrections`, `Correction.Description`, and `ParseCorrection` are the catalog for a prompt that
lets a model choose corrections, and `Corrector.Assess` measures a page image, in one magick run,
for the defects they fix: an `Assessment` of its size, skew, contrast, noise, sharpness, and color.

`document` prepares a PDF's pages for a model's vision, on Linux, the standard library, `tool`,
and `picture`. It provides pre-inference preparation only: a layer above sends the prepared pages
to a model and owns the requests, retries, and any final step. Every system tool it runs is a
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

A `Preparer`, built by `NewPreparer` from a Renderer and an optional `*picture.Corrector`, puts the
stages together. `Preparer.Images` streams a PDF's prepared pages as a lazy
`iter.Seq2[PageImage, error]`: it spools the PDF, read from an `io.Reader`, into the working
directory the `ImagesRequest` names, counts its pages, and renders and corrects each page at the
request's DPI (150 by default) and concurrency, yielding each `PageImage`, its number, the page
count, its PNG, and its text layer, in page order. A request may read each page's text layer
(`Text`), skip rendering the pages that have one (`SkipText`), for the flow of text layer first,
vision only for the pages without one, and render one `Region` of every page. A failed page yields
its error and the stream carries on; breaking out of the range cancels the work in flight.

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

`office` prepares office documents, docx, xlsx, and pptx, the legacy doc, xls, and ppt, and RTF,
for a model, on Linux, the standard library, `tool`, `route`, and `picture`. It runs
spike-ai-pipeline-office's Open XML CLI and provides pre-inference preparation only. A `Converter`,
built by `NewConverter` over a `Config` of Tools the caller built, needs an office Tool that
`OfficeSpec` declares, minimum `OfficeMinimum`, 0.1.0; a LibreOffice Tool and a magick Tool are
optional. `Converter.Convert` spools a document read from an `io.Reader` into the working directory
the `Request` names, runs office once with a media directory there, and returns a `Result`: office's
JSON model decoded into typed Go values that mirror its schema "1" (`JSON`, the default), or
office's Markdown (`Markdown`), with the media list, office's stderr warnings, and, for a legacy
document or an RTF, the format it was converted from. A model of another schema is an error matching
`ErrSchema`; a block kind the package doesn't know is kept with its raw JSON. office's exit codes 1
to 4 are an `*ExitError` that matches `ErrMediaNotWritten`, `ErrUsage`, `ErrRefused`, or
`ErrFailed`.

```go
o, err := tool.NewExec(ctx, office.OfficeSpec())
if err != nil {
	return err
}
lo, err := tool.NewContainer(ctx, "", tool.Image{Ref: picture.LibreOfficeImage}, picture.LibreOfficeSpec())
if err != nil {
	return err
}
mg, err := tool.NewExec(ctx, picture.MagickSpec())
if err != nil {
	return err
}
conv, err := office.NewConverter(ctx, office.Config{Office: o, LibreOffice: lo, Magick: mg})
if err != nil {
	return err
}
res, err := conv.Convert(ctx, doc, office.Request{Dir: dir, Name: "report.doc", Format: office.Markdown})
```

Pictures follow one standard: no stage re-encodes a picture lossily, and every conversion
writes PNG. The media list has one `MediaFile` per file office wrote: office's stored name (the
SHA-256 of its bytes and its extension), its content type, the file handed onward, and whether it
was converted. A picture vision decodes, PNG, JPEG, GIF, or BMP, is handed on exactly as stored.
The `picture` package converts the rest, a document's in one batch. EMF and WMF are rendered to
PNG through LibreOffice's Draw export at `Request.MetafileScale` times 96 dpi,
`picture.DefaultMetafileScale`, 3, when it is zero and at most `picture.MaxMetafileScale`, 8, and
trimmed to the picture through magick, since Draw places a metafile in the middle of an A4 page.
TIFF is converted through magick, its first image alone. A converted picture is named by its
original's SHA-256 with `.png`, written beside the original, which stays, and is the same bytes
on every conversion. In a Markdown result, each link to a converted picture is rewritten to its
PNG by swapping the exact stored name; no Markdown is parsed.

A legacy document is one with the OLE2 compound-file signature and a legacy extension in
`Request.Name`: .doc and .dot become docx, .xls and .xlt xlsx, and .ppt, .pps, and .pot pptx.
Given a LibreOffice Tool, `Convert` converts it to that OOXML format through soffice headless,
then runs office on the result; without one it is refused with `ErrNoLibreOffice` before anything
runs. An RTF, known by its signature whatever its name, is converted the same way to docx,
through Writer's docx export filter as a .doc is, and `ConvertedFrom` is "rtf"; without a
LibreOffice Tool it too is refused with `ErrNoLibreOffice`. `picture.LibreOfficeSpec` declares soffice, minimum `picture.LibreOfficeMinimum`, 24.2, and
`picture.LibreOfficeImage` is the default image a caller may replace:
gotenberg/gotenberg:9.1.0-libreoffice, LibreOffice 26.8.2.2, pinned by digest, which
`mise run currency` scans. soffice runs in it under
the container runner's full lockdown, with its user profile inside the mounted working directory.

[docs/findings/office.md](docs/findings/office.md) records what the legacy round trip loses, per
element kind and format, the container's footprint, the picture conversions, and each picture's
vision time and image tokens. The run results it cites are in `docs/findings/office-results/`.

`email` reads mail stores, Outlook .pst stores and .msg messages, and MIME messages, .eml and
mbox files, for a model, on Linux, the standard library, `tool`, and `route`. It runs
spike-ai-pipeline-mail's CLI and provides pre-inference preparation only. A `Reader`, built by
`NewReader` over a `Config` holding a mail Tool that `MailSpec` declares, minimum `MailMinimum`,
0.2.0, reads one input per `Request`. `Reader.Read`
returns a `Stream`, or refuses an invalid `Request`, one with no input or a path that can't be
made absolute, with an error matching `ErrRequest` before anything runs. A `Request`'s `After`
resumes a .pst's or an mbox's read after the message whose id it is, through mail's `--after`: the
store, its folders, and only the messages after it, the whole read's tail byte for byte. It needs
mail `MailAfterMinimum`, 0.3.0, which `Read` checks only when `After` is set, refusing an older one
with an error matching `ErrRequest` and `tool.ErrTooOld`. Ranging over
`Stream.Records` runs mail once and yields each line it writes while it runs,
decoded from a pipe one line at a time into typed Go values that mirror its schema "1": the
`Store` first, then for a .pst each `Folder` before its `Message`s, and a `Skipped` record wherever
mail couldn't read one; for a .msg or an .eml its message, an embedded message nested under its
attachment; for an mbox each message in the file's order. Memory follows the record being read,
never the store. mail's warnings are the Stream's `Warnings`, and its exit codes 1 to 4 are an
`*ExitError` that matches `ErrNotWritten`, `ErrUsage`, `ErrRefused`, or `ErrFailed`.

```go
m, err := tool.NewExec(ctx, email.MailSpec())
if err != nil {
	return err
}
r, err := email.NewReader(email.Config{Mail: m})
if err != nil {
	return err
}
var threads email.Threader
s, err := r.Read(ctx, email.Request{Input: "mailbox.pst", Attachments: dir})
if err != nil {
	return err
}
for rec, err := range s.Records() {
	if err != nil {
		return err
	}
	threads.Add(rec)
	if msg, ok := rec.(*email.Message); ok {
		for _, a := range msg.Attachments {
			dest, _, err := a.Route()
			if err != nil {
				return err
			}
			switch dest {
			case route.Document, route.Office, route.Vision:
				// hand a.Path to the route's stage
			}
		}
	}
}
roots := threads.Threads()
```

Given an attachments directory, mail writes each distinct attachment content there once, named by
its SHA-256, and `Attachment.Route` names the stage it goes to next, by `route.File` over the file
mail wrote, which the caller runs: `route.Document` for a PDF, `route.Office` for an office
document, `route.Vision` for PNG, JPEG, GIF, and BMP as stored, `route.VisionConverted` for TIFF,
EMF, WMF, SVG, and the rest, which `picture.Converter.Convert` makes PNG first where it can,
`route.Nested` for an embedded message, an OLE2 file named .msg, a by-value .eml, a .pst, or an
mbox, `route.Unrouted` for anything no stage takes, and the other routes the `route` package names.
The route is decided by the content's signature, never the stored MIME type alone, which real stores
get wrong: MsgKit stores an EMF as `application/octet-stream`, and Apache POI's `alfresco.gif` is a
BMP. Content of no signature is a by-value .eml when its name has the .eml extension or its stored
type is `message/rfc822` or `message/global`, and its header has the shape mail reads a message by:
its first line a header field, and a From or Date field before the header block ends.

A `Threader`, fed each record while a Stream is read, keeps only each message's threading keys,
and `Threader.Threads` builds the store's threads, `Thread` trees, by RFC 5256's REFERENCES
algorithm over Message-ID, In-Reply-To, and References, with a message that names no parent taking
the one its conversation index names; placeholders for messages the store doesn't hold are pruned
as RFC 5256 says, and roots with the same base subject are gathered. `Threads` builds them from a
read's Records whole. `Threader.MarshalBinary` saves its keys, in a versioned, deterministic
format, and `UnmarshalBinary` restores them, so a read resumed with `After` threads whole: fed the
records up to message K, saved, restored, and fed the read resumed after K, a Threader returns the
whole read's threads, which the tests hold over the generated .pst, Tika's testPST.pst, and
nine.mboxrd at several K.

[docs/findings/email.md](docs/findings/email.md) records the threads and routes over every
fixture against hand truth, what they come to on plaso's 59 MB store, and the adapter's time and
peak memory, threads included, against mail alone on plaso's store and a 2.1 GB generated store.
The adapter adds about 1 to 3% to mail's time, and its Go process peaks at 52 to 54 MiB threading
50,000 messages, beside mail's 65 to 67 MiB. The run results it cites are in
`docs/findings/email-results/`. Its addendum, for mail 0.2.0, scores the threads and routes over
17 .eml and mbox inputs, 29 of 29 threads and 13 of 13 routes right, and times a 2.7 GB mboxrd of
the scale store's 50,000 messages, where the adapter adds 4.5 to 5.6% to mail's time and peaks at
56 to 58 MiB.

`onenote` reads OneNote sections, .one files from desktop OneNote or OneDrive, .onetoc2 notebooks
and notebook directories, and .onepkg packages, for a model, on Linux, the standard library, `tool`,
and `route`. It runs spike-ai-pipeline-onenote's CLI and provides pre-inference preparation only. A
`Reader`, built by `NewReader` over a `Config` holding a onenote Tool that `OneNoteSpec` declares,
minimum `OneNoteMinimum`, 0.1.0, reads one input per `Request`, with an optional `Media` directory
onenote writes each distinct image, embedded file, and ink drawing into, named by its SHA-256.
`Reader.Read` returns a `Stream`, or refuses an invalid `Request`, one with no input or a path that
can't be made absolute, with an error matching `ErrRequest` before anything runs. Ranging over
`Stream.Records` runs onenote once and yields each line it writes while it runs, decoded from a pipe
one line at a time into typed Go values that mirror its schema "1": the `Notebook` first, then the
notebook's entries depth-first, each `SectionGroup` before what it holds and each `Section` before
its `Page`s, then a `Skipped` record for each section onenote couldn't read. A `Page` is typed
through and through: its ids, level, title, times, author, `Ink`, recognised `InkText`, Markdown,
and `Content`, its `Containers`, each by its `Kind` (`Title`, `Outline`, `ImageContainer`,
`EmbeddedFileContainer`, `InkContainer`), down through their `Items` (`Element`, `Group`) and
`Blocks` (`Paragraph` with its `Run`s, equations among them, `Tag`s, `List` marker, and handwriting;
`Table` of `Cell`s; `Image`; `EmbeddedFile`; `InkBlock`). A field onenote leaves out is left out,
and a zero onenote writes is kept, so each record marshals back through encoding/json/v2 to
onenote's own line, byte for byte, once its `line` field is put back in front; onenote's f32 numbers
are a `Float`, which marshals as onenote writes them. A page's Markdown is onenote's, links
untouched. A container, item, or block of a kind the package doesn't know is an `Unknown` holding
its JSON, which marshals back as it came. A notebook line of another schema is an error matching
`ErrSchema`. onenote's warnings are the Stream's `Warnings`, and its exit codes 1 to 4 are an
`*ExitError` that matches `ErrNotWritten`, `ErrUsage`, `ErrRefused`, or `ErrFailed`, the errors
email's and office's match too. `Page.Media(dir)` iterates, as an `iter.Seq2[Medium, error]`, every
image, embedded file, and ink drawing the page names, wherever it sits: its image and embedded file
containers, items nested at any depth, table cells, table in table, paragraph handwriting, ink
blocks, and the page's own ink. Each `Medium` gives its `Kind` (`ImageMedium`, `EmbeddedFileMedium`,
`InkMedium`), its record's fields and the record, `Where` it sits, such as `outline 2, item 1, block
3, table cell r1c2, item 1, block 1` or `page ink`, and, in the read's media directory, its `Path`
and the `Route` `route.File` gives it, with a `Reason`. Ink is vision-converted, an SVG, its reason
naming it OneNote ink; a recording, an embedded file of type `audio` or `video`, is routed by its
signature first and by its type only when no signature is known in it. A medium onenote wrote no
content for, or read without a media directory, is unrouted, with no `Path`.

```go
n, err := tool.NewExec(ctx, onenote.OneNoteSpec())
if err != nil {
	return err
}
r, err := onenote.NewReader(onenote.Config{OneNote: n})
if err != nil {
	return err
}
s, err := r.Read(ctx, onenote.Request{Input: "Notebook.onetoc2", Media: dir})
if err != nil {
	return err
}
for rec, err := range s.Records() {
	if err != nil {
		return err
	}
	if page, ok := rec.(*onenote.Page); ok {
		// hand page.Markdown onward, and each of its media to its route's stage
		for m, err := range page.Media(dir) {
			if err != nil {
				return err
			}
			dispatch(m.Route, m.Path, m.Where)
		}
	}
}
```

`audio` prepares speech for a model's audio input, on Linux, the standard library, and `tool`: it
cuts a source into windows short enough for the model to take whole, each ending in a silence so
no cut splits a word, and joins the transcripts a layer above gets for them. A `Splitter`, built by
`NewSplitter` over a `Config` of an ffmpeg and an ffprobe Tool, both required, which `FFmpegSpec`
and `FFprobeSpec` declare, minimum `FFmpegMinimum`, 9.0, and the container runner runs in
`FFmpegImage`, works in three steps:

- `Splitter.Detect` probes the source through ffprobe, refusing one with no audio stream with
  `ErrNoAudio` before ffmpeg runs, and finds the stream's silences with the detector the
  `Request`'s `Detector` names. `DetectorSilencedetect`, the default, runs ffmpeg's volumedetect
  and silencedetect and finds the silences at least 0.3 s long and 15 dB below the mean volume,
  which a `Request` may change or replace with a fixed threshold. `DetectorTracker` feeds a
  `Tracker` the 16 kHz mono PCM ffmpeg decodes.
- `Splitter.Plan` plans the windows from them, as the pure `PlanWindows` does: each at most
  `WindowLimit`, 30 s, overlap included, each but the last ending at the middle of the latest
  silence its 30 s allow, or at the limit when they allow none. The next window starts
  `Request.Overlap`, 2 s by default, before it, and the range the two share is their seam. The
  same source and request give the same plan.
- `Splitter.Windows` cuts a plan's windows, a lazy `iter.Seq2[WindowFile, error]` in plan order,
  `WindowsRequest.Concurrency` ffmpeg runs at once, each a 16 kHz mono 16-bit WAV named by its
  SHA-256 in the request's media directory. Both runners write the same bytes.

A `Tracker`, built by `NewTracker` from a `Request`, finds a stream's silences as its 16 kHz mono
PCM arrives. It compares each 10 ms frame's energy, `FrameLength`, with a rolling threshold: the
`Percentile` of the frames' energy over the last `FloorWindow`, plus a `Margin`, by default the
10th percentile over 4 s plus 12 dB, or a fixed `Threshold` in their place. The threshold follows
a change of level or noise floor partway through a stream, which silencedetect's, fixed for a
run, can't; that is why the tracker is Go rather than a tool behind an adapter, and `audio`'s
package documentation records the measurements. Each `Silence` reports the threshold in force when
it opened, and the same samples give the same silences however they are chunked. A `Planner`,
built by `NewPlanner`, plans a stream's windows by `PlanWindows`' policy as its silences and
position arrive, and returns each window once the stream passes the window's limit or ends; given
everything at once, it gives `PlanWindows`' plan. Under `DetectorTracker`, `Splitter.Plan` gives
the plan a Tracker and a Planner give over the same PCM, so a file's windows are the ones `media`
cuts from it. `Sample` and `WAV`, a time's sample and a window's WAV, are exported, so `media`
writes the same bytes.

`Join` merges the windows' transcripts, in plan order, into one `Transcript`. At each seam it
slides the later window's first words over the earlier window's last ones, as Hugging Face
transformers' best-offset merge does, compares them lowercased with punctuation dropped, and
splices the two at the middle of the best alignment that matches at least 2 words. A seam that
matches fewer is concatenated whole. Each `Seam` reports whether it matched and where it is.
Each seam's merge reads only the windows up to it, so joining the transcripts received so far
again, as more arrive, leaves every word before the latest seam unchanged.

```go
ff, err := tool.NewExec(ctx, audio.FFmpegSpec())
if err != nil {
	return err
}
fp, err := tool.NewExec(ctx, audio.FFprobeSpec())
if err != nil {
	return err
}
sp, err := audio.NewSplitter(audio.Config{FFmpeg: ff, FFprobe: fp})
if err != nil {
	return err
}
plan, err := sp.Plan(ctx, audio.Request{Path: "meeting.mp3"})
if err != nil {
	return err
}
texts := make([]string, len(plan.Windows))
req := audio.WindowsRequest{Path: "meeting.mp3", Dir: dir, Concurrency: 4}
for w, err := range sp.Windows(ctx, req, plan) {
	if err != nil {
		return err
	}
	texts[w.Index] = transcribe(w.Path) // the layer above's model call
}
t, err := audio.Join(texts, audio.JoinRequest{})
```

[docs/findings/audio.md](docs/findings/audio.md) records Gemma 4 E4B's transcription of four long
recordings, up to a 4.4 h audiobook, windowed and joined, against one unsplit request over each
one's first 10 minutes. Windowed was 2.6 to 3.4 times faster. Its word error rate was the same or
slightly better on read speech and an accented call, and worse on a 4-speaker meeting (0.298
against 0.258) and an 8-speaker call (0.069 against 0.053). Seams carried more edits than the rest
of a transcript, most of the excess at unmatched seams, and a gpt-oss-120b pass over the words at
each seam made every run worse. The run results it cites are in `docs/findings/audio-results/`.

`media` reads a source's audio and video through one ffmpeg run and streams them as they arrive,
as the windows and frames a model takes, on Linux, the standard library, `audio`, and `tool`. It
provides pre-inference preparation only: a layer above sends the windows and frames to a model. A
`Streamer`, built by `NewStreamer` over a `Config` of an ffmpeg and an ffprobe Tool, both required,
which `audio.FFmpegSpec` and `audio.FFprobeSpec` declare, minimum `audio.FFmpegMinimum`, 9.0, and
the container runner runs in `audio.FFmpegImage`, takes a `Request`: one `Source`, the media
directory `Dir`, and the settings of its windows and frames. A Source names exactly one of:

- `File`, read unpaced, as fast as ffmpeg decodes it, or, with `Paced`, at real time through
  ffmpeg's `-readrate 1`, as a live source arrives. ffmpeg writes its frames into a hidden
  directory beside the file, so the file's directory must be writable.
- `Reader`, a stream the caller holds, fed to ffmpeg's stdin as it is read. A container that needs
  to seek, such as an MP4 whose index follows its media, can't be read from it.
- `URL`, a source ffmpeg reads itself, whose scheme is srt, rtmp, http, https, an HLS playlist's
  included, or udp.

Each run allows ffmpeg only its source's protocols, through `-protocol_whitelist`, so a playlist
can't open another protocol or a local file. Through the container runner, a URL run joins the
docker network the ffmpeg Tool's `Image` names, and is refused when it names none. A request that
names no source or more than one, paces anything but a file, names any other scheme, rtsp among
them, or sets a field out of range is refused with `ErrRequest`, audio's own, before anything
runs. ffprobe finds a file's first audio and video streams before the run. A reader or a URL
can't be probed ahead, so its run takes the streams `Tracks` names, both by default, and one the
source lacks fails the run with `ErrNoMedia` before any event.

`Streamer.Events` runs ffmpeg once and yields an `Event` for each window and frame as soon as it is
complete, a lazy `iter.Seq2[Event, error]`, windows in plan order and frames in source-time order,
interleaved:

- **Windows.** ffmpeg writes the audio to stdout as 16 kHz mono PCM. An `audio.Tracker` finds its
  silences as it arrives, under the request's fixed or rolling threshold, and an `audio.Planner`
  plans the windows. Each `Window` is cut once the stream passes its 30 s limit or ends, written
  through `audio.WAV`, the same bytes `Splitter.Windows` cuts under `DetectorTracker`, and carries
  its index, span, cut, seams, samples, and the threshold it was cut under. No silencedetect runs.
- **Frames.** ffmpeg's select filter takes the first frame, each whose scene score is past
  `SceneThreshold`, `DefaultSceneThreshold`, 0.2, and each `MaxGap`, `DefaultMaxGap`, 10 s, or
  more after the last taken. Each `Frame` is a PNG at the source's resolution, or scaled down to
  fit `MaxSide`, and carries its `Reason`, `ReasonFirst`, `ReasonScene`, or `ReasonInterval`, and
  its size.

Each event carries its source time and `Arrived`, the wall-clock time its last sample or frame
arrived from ffmpeg. Each window and frame is named by its SHA-256 in the media directory. Pacing
changes when an event comes, never what it is, and the two runners write the same files. A context
that ends stops ffmpeg through either runner, and the stream yields its error once; a loop that
stops early stops the run; and the media directory holds only the windows and frames the stream
yielded.

```go
st, err := media.NewStreamer(media.Config{FFmpeg: ff, FFprobe: fp})
if err != nil {
	return err
}
req := media.Request{Source: media.Source{URL: "srt://127.0.0.1:9000"}, Dir: dir}
for ev, err := range st.Events(ctx, req) {
	if err != nil {
		return err
	}
	switch {
	case ev.Window != nil:
		// send ev.Window.Path to a speech model; audio.Join merges the transcripts as they land
	case ev.Frame != nil:
		// send ev.Frame.Path to a vision model
	}
}
```

[docs/findings/streams.md](docs/findings/streams.md) records three real videos read at real time,
Tears of Steel's 180 s clip, AMI ES2002a's 21-minute meeting, and NASA's 25.5-minute in-flight
event, with each window sent to Gemma 4 E4B and each frame to Gemma 4 26B-A4B as it arrived. The
paced runs gave the unpaced read's windows and frames byte for byte, and no window, frame, or
request failed. A window came once the stream passed its 30 s limit, at most 15.0 s after its last
sample, and was transcribed 0.8 to 4.6 s later; a frame came 20 to 170 ms after ffmpeg reported it;
and neither model queued. The live transcript matched audio's file path under the same detector
(AMI word error rate 0.322 against 0.323), and a cancellation ended the stream within 0.44 s,
leaving only the events yielded. The run results it cites are in `docs/findings/streams-results/`.

Every format package follows one adapter convention, so a dispatcher, and the next format, meet
the same shape:

- **Spec and Minimum.** A function returns the `tool.Spec` of each program the package runs, such
  as `office.OfficeSpec`, beside a constant naming the oldest version it accepts, such as
  `OfficeMinimum`.
- **Config.** The Tools the caller built, through either runner, are the fields of a `Config`.
- **A constructor that refuses.** `NewConverter`, `NewReader`, and their kind refuse a required
  Tool that is missing, and any Tool older than the package's Minimum whatever its own Spec
  declared, with a `*tool.VersionError` wrapping `tool.ErrTooOld`, so a missing or outdated tool
  fails at startup.
- **A Request with a media directory.** Each call takes a `Request` naming the working directory
  the caller owns, which an input is spooled into and the media the CLI writes are written into
  and left; an invalid request is refused before anything runs.
- **Typed exit errors.** A CLI's exit codes 1 to 4 are an `*ExitError` carrying the CLI's message
  and matching one shared set, through each package's `ErrNotWritten` (office's
  `ErrMediaNotWritten`), `ErrUsage`, `ErrRefused`, and `ErrFailed`, so `errors.Is` matches office's
  refusal through `email.ErrRefused` too. The contract lives in `internal/cli`, with the stderr
  reader, the bounded buffer, and the spool every adapter shares.
- **Warnings.** Each `<prog>: warning: <where>: <what>` line on stderr is a warning, and a call
  with warnings has still succeeded.
- **SHA-256 media naming.** A medium is named by the lowercase hex SHA-256 of its bytes and its
  extension, and a converted picture's PNG by its original's, so the same content has the same
  name on every run and across packages.
- **`iter.Seq2` streaming.** The parts come as a lazy `iter.Seq2[T, error]` while the CLI runs,
  memory following the part being read; a loop that stops early stops the CLI.
- **A route per part.** Each part names the stage it goes to next, through `route`, which the
  caller runs.

Each package's deviations: `office` returns one `Result`, the whole model or Markdown, not a stream
of lines, and its media list names no route yet; `email` follows the convention whole, its
`Attachment.Route` the route per part; `onenote` follows it whole, its `Request.Media` the media
directory and `Page.Media` the route per part; `document` streams pages, `PageImage`s, rather than a
CLI's lines, its renderer built by `NewGhostscript` over one Tool rather than a Config, and gs's
failures are the Tool's `*tool.ExitError`, gs having no exit contract; `picture` is a converter, not
a reader: one picture in, one PNG out, its Tools both optional, a picture that needs a missing one
refused; `audio` is a splitter, not a reader: its `Request` names no media directory, since
planning writes nothing, and only `Splitter.Windows`'s `WindowsRequest` names one, its `Dir`; the
source is read in place rather than spooled; ffmpeg's failures are the Tool's `*tool.ExitError`,
ffmpeg having no exit contract; and a window names no route, since a speech model is its one
stage; `media` is a streamer, not a reader: its `Request` names the media directory, its `Dir`; a
file source is read in place rather than spooled; ffmpeg's failures are the Tool's
`*tool.ExitError`; and a window or frame names no route, since a speech model takes windows and a
vision model frames; `sevenzip` lists and extracts rather than reading parts: its `ListRequest` and
`ExtractRequest` name no media directory, since 7zz writes only to the caller's writer, never to a
path; the archive is read in place rather than spooled; 7zz's failures wrap the Tool's
`*tool.ExitError`, matching `ErrEncrypted` or `ErrNotArchive` by 7zz's message, 7zz having no exit
contract; it has no warnings, a run 7zz ends with a warning having succeeded; and an item names no
route, since the archive stage hands each one to the dispatcher; and `route` and `inspect` are pure
functions, with no Tool, Config, or
Request, which run before a part's stage rather than as one.

Known limits:

- Page 7 of the scanned fixtures carries a faded "NOFORN" marking at its foot that the vision
  model never reads, at any DPI, correction, band, or tile measured; a marking that faded can't
  be left to vision.
- A text layer keeps gs's physical layout, so a multi-column page or a form reads out of reading
  order; restore the order, or send such pages to vision.
- Ghostscript is AGPL-licensed; the package runs gs as a separate program, never linked.
- An EMF or WMF picture needs both the LibreOffice and the magick Tools, and a TIFF needs magick;
  without them, it is listed with no picture handed onward.
- A legacy file is recognized only by its OLE2 signature and a legacy extension in its request's
  name.
- Legacy conversion inherits LibreOffice's losses, which
  [docs/findings/office.md](docs/findings/office.md) records.
- The metafile trim removes a white border the picture itself has, and a blank metafile becomes a
  1x1 PNG.
- `office` evaluates nothing office doesn't: a formula's value is the one the file caches, or
  none, and a chart is its cached data.
- Pictures are listed as the document holds them, in order, and their layout isn't evaluated: a
  picture that another covers, that sits off the slide or page, or that is cropped away still
  appears in the model and the Markdown and is handed onward to vision. In `slide-objects`'s
  slide 3, a blue square covers a red one at the same position, and both are listed.
- A by-value .eml whose header isn't a message's shape is unrouted, unless it begins with an
  mbox's "From " line, which makes it an mbox; so is header-shaped content neither named .eml nor
  stored as `message/rfc822` or `message/global`. An mbox named with a text extension is text.
- mail reads every mbox as an mboxrd: an mboxcl's Content-Length is ignored, so an unquoted body
  line starting "From " starts a new message, and an mboxo body line that was ">From " loses its
  ">". An .eml's or an mbox's message has no class, no creation or modification time, and SMTP
  addresses alone. These are mail's, as its README states.
- mail refuses an .ost, an `*ExitError` matching `ErrRefused`, and `route` leaves one unrouted.
- In a .pst, an embedded message is nested without its content, which mail can't read; in a .msg
  it is nested in full.
- A by-reference attachment has no content file, so it is unrouted.
- The stores mail's generator writes, its fixtures and its scale store, carry no threading
  fields, so they thread by subject alone.
- The container runner's limit above: a SIGKILLed processing leaves a running container to its
  tool's exit.
- `inspect` reads what pdfid reads: a PDF name inside a compressed object stream is unseen. A
  legacy OLE2 document's macros and an ODF package's scripts aren't checked, nor an XML file's
  entity expansion, such as a billion laughs.
- mail caps an .eml at 4 GiB and a .msg's nesting at 16 levels.
- office reads a whole document into its model before writing it, and the onenote CLI holds a
  whole section in memory, as onenote_parser reads one: 135 MiB at its peak on a 161 MB section.
- A `Threader` holds about 60 bytes of threading keys a message for the whole read, 1.5 MB at
  25,000 messages; saving it at every message is quadratic in the store, so a caller checkpoints
  every N messages.
- OneNote's own encryption isn't detected: an encrypted section such as `Encryption.one` is
  refused by onenote, with no flag marking it encrypted, so the dispatcher reports it failed.
- A bare RFC 822 file without an extension or a message type is unrouted.
- Once a store's original is released, a later record naming the same content is met with no
  file and takes the first occurrence's route and reason, whatever its own name says.
- The file index may prepare content twice around a stale claim's takeover: a claimant slower
  than `FileOptions.Stale` is taken over, and both prepare it.
- `audio` plans and cuts a source's first audio stream as one stream; an M4B's chapters are
  ignored.
- `audio.Join` can't tell a model's own words from speech: in the audio finding, E4B answered a
  window a few seconds long with its instruction in 2 of 3 runs, and the join kept those words in
  order. A guard against it belongs to the layer that transcribes.
- `media` reads no rtsp source or capture device, and doesn't reconnect an http(s) source. A
  reader can't carry a container that needs to seek.
- A `media` window waits for the stream to pass its 30 s limit, so the first comes after 30 s of
  audio, and one that ends early in a long silence waits up to 30 s.
- ffmpeg stops pacing a file once its video stream ends, so a paced file's audio past its video
  arrives at once.
- The archive stage extracts a 7z's or a cabinet's entries one 7zz run at a time, so an entry of a
  solid block decodes the block from its start: an archive's entries read one at a time cost more
  than one extraction of them all.
- 7-Zip names an entry as it lists it, and the stage extracts it by that name: a ZIP entry 7zz
  reads, whose name archive/zip and 7-Zip decode differently, such as a non-UTF-8 name, or a
  stored name with a line break, isn't matched, and is a record of 0 bytes written.
- A zstd stream's content size is checked before it is decompressed only when its first frame's
  header declares one; otherwise the bomb rule holds it as written.

The tests run in tiers. `mise run check` is hermetic: build, vet, format, tidy, the unit tests
under the race detector, and lint, the integration, bakeoff, and findings tags included. `mise run
integration` runs the integration-tagged tests against this machine's tools and docker, and the
vision tests against the llama.cpp router `SPIKE_ROUTER_URL` names, gb10-2's by default. Among
them, `dispatch`'s `TestTierRoutes` dispatches every committed fixture input through every
adapter over one file index, holds each route and outcome to the hand route truth in
`dispatch/testdata/tier-routes.tsv`, and sends one image output per stage and part kind to Gemma
4 26B-A4B; run alone, with its routes-to-stages log written to a file, it is
`DISPATCH_TIER_LOG=<file> mise exec -- go test -race -tags integration -count=1 -run
'^TestTierRoutes$' ./dispatch/`. The
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

The findings tier, findings-tagged tests, measures `email` on this machine and never runs in CI:
`mise run findings-email` scores threads and routes over the fixtures and the MIME inputs, fetches
plaso's store by its pinned SHA-256, and times the adapter on it, on the scale store
`EMAIL_SCALE_STORE` names, and on the scale mboxrd `EMAIL_SCALE_MBOX` names, both of which mail's
generator builds; a run keeps its results only in the directory `EMAIL_FINDINGS_RESULTS` names
([docs/findings/email.md](docs/findings/email.md), "Rerunning"). `mise run findings-onenote`
measures `dispatch` and `onenote` the same way: the tier's routes, inspection over ~/docs, the page
ink to vision, the large sections, and the dispatcher over the scale store `ONENOTE_SCALE_STORE`
names, resumed and pooled; a run keeps its results only in the directory `ONENOTE_FINDINGS_RESULTS`
names ([docs/findings/onenote.md](docs/findings/onenote.md), "Rerunning"). `mise run findings-audio`
measures `audio` on ~/docs' long sources, each checked against the corpus manifest: windowed and
joined through the speech harness, an unsplit pass over each one's first 10 minutes, and a
gpt-oss-120b seam pass, scored by normalized WER and seam edits; a run keeps its results only in
the directory `AUDIO_FINDINGS_RESULTS` names
([docs/findings/audio.md](docs/findings/audio.md), "Rerunning"). `mise run findings-floor` runs
the tracker's tuning grid, with silencedetect beside it, over the level-change fixtures and
~/docs' long sources, unchanged and with a gain step or a noise floor added at 10 minutes, and
logs each setting's windows cut at the limit; it calls no model and keeps nothing. `mise run
findings-streams` measures `media` on ~/docs' videos read at real time, each window sent to Gemma 4
E4B and each frame to Gemma 4 26B-A4B as it arrives, beside audio's file path and a cancellation
midway; its paced runs take about 75 minutes, and a run keeps its results only in the directory
`STREAMS_FINDINGS_RESULTS` names ([docs/findings/streams.md](docs/findings/streams.md),
"Rerunning").

The tests' vision and speech harnesses, the bake-offs, the staged flow, and the findings tier are
test code, not released.

Host prerequisites: gs 10.0 or later on PATH for `document`; ImageMagick's magick 7.1 or later on
PATH for `picture`, its conversions and its corrections, and so for `document`'s corrections, with
libheif, an AV1 decoder, and libwebp for HEIC, AVIF, and WebP; and
docker, server 20.10 or later, for the container runner. For `office`: spike-ai-pipeline-office
0.1.0 or later, which mise pins from its GitHub release for the tests; docker, which runs soffice in
the default image, pulled on first use; and magick 7.1 or later for TIFF pictures and the metafile
trim. For `email`: spike-ai-pipeline-mail 0.2.0 or later, 0.3.0 or later to resume a read, which
mise pins, at 0.3.0, from its GitHub release for the tests. For `onenote`: spike-ai-pipeline-onenote
0.1.0 or later, which mise pins from its GitHub release for the tests. For `audio` and `media`:
ffmpeg and ffprobe 9.0 or later on PATH, or docker, which runs them in `audio.FFmpegImage`; a URL
source through docker needs the image to name a docker network. `mise run fixtures:streams`
rebuilds `audio/testdata`'s streams fixtures through the host's ffmpeg. The renderer
bake-off also runs poppler's pdftoppm and pdftocairo and libvips. typst and qpdf are needed only to
regenerate the fixtures in `document/testdata`, and .NET, which mise installs, only to rebuild
`email/testdata`'s built .msg, as their READMEs say.

The layer requires spike-ai-pipeline-client at v0.4.0 for its tests alone, the vision and speech
harnesses the integration, bake-off, and findings tiers run on; no released package imports it.
`internal/visiontest`, the router client and response helpers the vision tests share, and
`internal/audiotest`, the speech harness, build under the integration and findings tags alone,
and `internal/corpustest`, the readers of ~/docs that check each file against the corpus
manifest, under the findings tag alone.
`internal/goldentest`, the golden sums; `internal/textscore`, the character and word error rates
the bake-offs and the ink and audio findings score by; and `internal/mediatest`, the stand-in for
media's ffmpeg run that media's and dispatch's unit tests share, are test helpers too, built
untagged.
It is released by tag, `vX.Y.Z`, with a CHANGELOG. The spike's question, evidence, and answer are
in the root repository's README.

Apache-2.0.
