# Finding: the dispatcher's routes and cost, and OneNote through it

This page records what the measurements behind task `onenote` found for the `dispatch` and
`onenote` packages: whether every committed fixture, dispatched through every adapter, reaches the
route and outcome the hand truth gives it; what inspection flags over ~/docs' hazards; how well
the vision model reads a OneNote page's ink beside OneNote's own recognition; what the onenote
adapter costs beside the onenote CLI on three large sections; and what the dispatcher costs over
mail's 2.1 GB scale store, whole, resumed midway, and through the pool. They answer approved brief
22 of task `onenote` (the root's goal record, `context/goals/experiment.ai.spike-ai-pipeline.md`).
The onenote CLI's own fidelity and cost are onenote's
[onenote-cli finding](https://github.com/JaimeStill/spike-ai-pipeline-onenote/blob/main/docs/findings/onenote-cli.md).

Every number here comes from one recorded run, **findings-onenote 2026-11-05**: one run of
`mise run findings-onenote` on 2026-11-05 from 18:08:01 to 18:13:08 UTC, on the laptop (x86_64),
at processing commit `96797c6`, with onenote 0.1.0, mail 0.3.0, and office 0.1.0 from the mise
pins, Ghostscript 10.08.0, ImageMagick 7.1.2-34, and LibreOffice in its pinned image, against
gb10-2's llama.cpp router, build b11669, serving set A. Its results are committed in
[`onenote-results/`](onenote-results/README.md): the log verbatim, the tier's log, each test's
JSON, and the ink's PNG.

The packages release pre-inference preparation only. The vision requests here are test code: the
integration tier's in `dispatch/tier_integration_test.go`, and the ink's in
`dispatch/findings_ink_test.go`.

## At a glance

| Question | Answer |
|---|---|
| Do the routes match the hand truth? | **Yes, every row.** 119 inputs, 225 parts, each held to `dispatch/testdata/tier-routes.tsv`: no mismatch, no failed part the truth doesn't expect. The one failure is the truth's: `Encryption.one`, refused by onenote. |
| Do OneNote's media route right? | **Yes.** In the tier, every page's image to vision, its embedded .docx to office, its ink to the picture stage, its .mp3 to audio (no processor yet); onenote's `TestRealMediaRoutes` holds all 7 distinct media files to their hand routes. |
| What does inspection flag over ~/docs? | **Every hazard it has a check for**, 31 of 568 files flagged, all proceed under the default policy; the 200 MB zero ZIP is a bomb, quarantined, once the rule's size is lowered under it. Hazards with no flag: two OpenXML SDK macro-enabled files with no VBA part, the billion-laughs XML, and OneNote's encrypted and file-bearing sections. The clean fixtures yield none but the three documented. |
| Does vision read OneNote ink? | **Yes, all 14 words, in every run**: CER 0.0278, WER 0.0714, its one word edit a stray mark read as "L". OneNote's stored recognition holds 4 of the 14 words, two "Hello World" lines: CER 0.6806, WER 0.7143. The truth is the session's draft, pending the architect's confirmation. |
| What does the onenote adapter cost? | **Nothing measurable over the CLI.** Medians within 6% of the CLI's, either way, on all three large sections (1.38 to 1.43 s on `LargeData.one`, 0.28 to 0.32 s on the others). The adapter's own process peaks at 10.7 to 13.3 MiB; the CLI under it peaks where it does alone, 19.7 and 135.1 MiB. |
| What does the dispatcher cost on the scale store? | **16.29 s against 14.12 s for the email adapter alone and 13.88 s for mail alone**, median of 3, 15% and 17% more. Its own process peaks at **35.9 MiB**, mail under it at 67.0 MiB, where mail alone peaks at 66.5 MiB. |
| How much disk? | **15.8 MB at most** in the dispatch's working directory, the CLI's originals released as each is settled, against **1.88 GB** of attachments mail writes without release: 0.84% of it. The file index ends at 264 MB. |
| Does a resumed dispatch hold together? | **Yes.** Stopped by the cursor rule after 25,000 messages and resumed with `Input.After`, it commits the whole dispatch's 100,000 message outputs, in order, none twice, and the Threader's 34,928 threads equal the whole read's. The resumed half takes **8.45 s, 0.54 of the whole's 15.74 s**. |
| What does the pool buy? | **18.2 s at 2, 4, or 8 workers against 21.2 s at 1**, 228 MiB/s against 196 MiB/s over 4.35 GB of input: one store is most of the work, and its mail is serial. The copy of the scale store is **seen whole**, its 50,000 messages streamed from the index, and 26 attachments are seen. Peak memory 34.2 to 36.9 MiB, peak disk 17.2 to 18.2 MB, at every worker count. |

## Routes against hand truth

### What was measured

The integration tier's `TestTierRoutes` dispatches every committed fixture input, 119 of them, in
the order `tierInputs` lists them, through every adapter over one file index: document over gs,
office over the office CLI with LibreOffice and magick, picture over magick and LibreOffice, vision
passing pictures through, email over mail, onenote over onenote, and zip. It holds each part's
route and outcome to `dispatch/testdata/tier-routes.tsv`, written by hand from the fixtures, their
READMEs, and the expectations the CLIs ship (`dispatch/testdata/README.md`): every row must match an
event, every event a row matches must have its route and outcome, every input must have a row, and
a failed part must be expected. Content an input shares with one dispatched before it is seen. Its
log is `onenote-results/tier-routes.log`.

### Results

**225 parts from 119 inputs, dispatched in 27.836 s; every row matched, no mismatch.** By route ×
outcome:

| Route | Outcome | Parts |
|---|---|---|
| archive | prepared | 1 |
| archive | encrypted | 1 (`encrypted.zip`, 1 of 2 entries encrypted) |
| archive | quarantined | 4 (`absolute.zip`, `declared-bomb.zip`, `symlink.zip`, `traversal.zip`) |
| audio | no processor yet | 3 |
| document | prepared | 5 |
| nested | prepared | 36 |
| nested | seen | 2 |
| office | prepared | 37 |
| office | seen | 5 |
| onenote | prepared | 22 |
| onenote | failed | 1 (`Encryption.one`: "onenote: refused: Unexpected end of file", as the truth expects) |
| text | no processor yet | 9 |
| unrouted | no processor yet | 8 |
| vision | prepared | 35 |
| vision | seen | 43 |
| vision-converted | prepared | 8 |
| vision-converted | seen | 5 |
| **All** | | **225** |

The tier's vision sample sent one image output per stage and part kind, 11 images, to Gemma 4
26B-A4B: all completed, in 5.175 s, 2,217 image tokens. The page ink of `New Section 1.one`, a
980x121 PNG, was among them.

### The OneNote media routes

From the tier's onenote rows: every section and notebook prepared but `Encryption.one`; each page
image, `Chrysanthemum.jpg`, `testimages_screenshot.jpg`, and `dummy_1.png`, to vision, prepared
once and seen in every notebook that holds the same page again; the embedded `testing.docx` to
office, prepared, seen from its notebook; each page's ink SVG to vision-converted, prepared by the
picture stage as a PNG; and the recording `ff-16b-2c-44100hz.mp3` to audio, no processor yet.
`onenote`'s `TestRealMediaRoutes`, in the same run, holds every file the real onenote writes over
every copied section and notebook to `mediaRoutes`, its hand truth: two JPEGs and a PNG vision, the
.docx office, the .mp3 audio, and the two ink SVGs vision-converted, each named by exactly one
medium, none missing and none extra. It passed.

## Inspection over ~/docs

`TestFindingsDocs` (inspect, findings tier) inspects every file of ~/docs' manifest under the
default options, as `TestDocs` does against `docsFlags`, and records each file whose manifest
covers name a hazard (security, javascript, macro, vba, xlm, script, encrypted,
password-protected, embedded-files, the zip bomb) or that yields a flag
(`onenote-results/docs-inspect.json`). **568 files inspected, 31 flagged, every verdict proceed.**
`TestDocs`, `TestDocsBomb`, `TestCleanFixtures`, and `TestCleanAttachments` passed in the same step.

| File | Hazard its manifest names | Checks | Verdict |
|---|---|---|---|
| `archive/password-protected.zip` | encrypted | zip-encrypted | proceed |
| `archive/high-ratio-200mb.zip` | zip bomb | none at the default 1 GiB; **zip-bomb** at 100 MB | proceed; **quarantine** |
| `image/vector/script-bearing.svg` | security, script | svg-event-handler, svg-external-href, svg-script | proceed |
| `office/macro-template/macro.openxmlsdk.xlsm`, `.xltm` | macro, vba | vba-project | proceed |
| `office/macro-template/simple-macro.poi.docm`, `.pptm`, `.xlsm` | macro, vba | vba-project | proceed |
| `office/macro-template/xlm-macro.poi.xlsm` | macro, xlm | macro-sheet | proceed |
| `office/macro-template/macro-enabled.openxmlsdk.docm`, `.dotm` | macro, vba | **none** | proceed |
| `office/macro-template/protected-passtika.poi.xlsb` | encrypted | ooxml-encrypted | proceed |
| `pdf/edge/encrypted/` five PDFs | encrypted | pdf-encrypt (and pdf-openaction on `simple-open-password.pdf`) | proceed |
| `pdf/edge/attachments/` three PDFs, `pdf/edge/pdfa/pdfa3b-embedded-files-*.pdf` | embedded-files | pdf-embeddedfile (and pdf-javascript, pdf-js on `file-attachment.pdf`) | proceed |
| `pdf/edge/malformed/javascript.pdf` | javascript, security | pdf-javascript, pdf-js, pdf-openaction | proceed |
| `text/xml/billion-laughs-small.xml` | security | **none** | proceed |
| `onenote/interop/SUT/Encryption.one` | encrypted, password-protected | **none** | proceed |
| `onenote/interop/SUT/OneWithFileData.one`, `onenote/large/LargeData.one` | embedded-files | **none** | proceed |

The other 13 flagged files name no hazard and carry what a check reads: four PDFs with an
/OpenAction, `compacted-syntax.pdf`'s /AA and JavaScript, `header-no-version-info.pdf`'s
signature mismatch, three docx and pptx with a linked picture or an OLE object, and the scanned
set's `generate.sh`, executable. Each is in `docsFlags`.

The unflagged hazards are what inspection has no check for, not misses of a check it has: the two
OpenXML SDK macro-enabled files carry no `vbaProject.bin` for vba-project to find; no check reads
XML entity expansion; OneNote's own encryption and embedded files aren't inspected, the onenote
stage and its routes taking those (`Encryption.one` fails in the dispatcher, the embedded files are
found as parts).

**Clean fixtures:** 177 clean fixtures and 58 attachments of 40 email inputs inspected, none
flagged but the three documented, `media.libreoffice.docx`'s external relationship,
`word-with-attachments.docx`'s OLE object, and POI's `alfresco.gif`, a BMP, its signature mismatch.

## OneNote ink through vision

### What was measured

`TestFindingsInk` dispatches onenote.rs's `handwriting_recognition.one` through the onenote and
picture stages: its page's ink, the SVG onenote writes, is rendered by the picture stage at 150 dpi
on white to a 984x936 PNG of 31,577 bytes (`onenote-results/ink.png`, SHA-256 `55ca4f24…6835`). The
PNG goes to Gemma 4 26B-A4B (`google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0`) with "Transcribe the
handwritten text in this picture, line by line, top to bottom. Output only the text, with no
commentary.", reasoning off, temperature 0, three runs. OneNote's own recognition is the page's
`ink_text`, each line's best guesses joined. Both are scored against the hand transcription as the
documents finding scores pages: character and word edit distance over the truth's length, both
texts whitespace-collapsed, case counting; and the truth's words held in order (their longest
common subsequence).

The truth, `dispatch/testdata/handwriting-truth.txt`, 6 lines, 14 words, 72 characters collapsed:

```
Hello World
This is a quick test
Hallo Welt
test
Hello World
Hello World
```

**It was drafted by the session that wrote this finding, from the rendered PNG, and is pending the
architect's confirmation.** It leaves out the wavy line across the top and a stray stroke, like an
"L", before the large "Hello World".

### Results

| Reader | Text | CER | WER | Truth's words held |
|---|---|---|---|---|
| Vision, run 1 | `Hello World / This is a quick test / Hallo Welt / test / L Hello World / Hello World` | 0.0278 (2 of 72) | 0.0714 (1 of 14) | 14 of 14 |
| Vision, run 2 | the same | 0.0278 | 0.0714 | 14 of 14 |
| Vision, run 3 | the same | 0.0278 | 0.0714 | 14 of 14 |
| OneNote's stored recognition | `Hello World / Hello World` | 0.6806 (49 of 72) | 0.7143 (10 of 14) | 4 of 14 |

Each vision run completed with 422 image tokens and 21 output tokens, in 0.36 to 0.39 s. Its only
error is the stray stroke, read as an "L" word; with the stroke counted as text it would be exact.
OneNote stored recognition for two of the page's ink groups only, each "Hello World" (German
language, LCID 1031, with alternatives such as "Hallo"); the other four lines have none. Vision
reads the whole page; OneNote's recognition is a weak signal beside it, worth keeping as the page
record's metadata rather than standing in for a reading.

## The onenote adapter on the large sections

### What was measured

`TestFindingsOneNoteLarge` times four ways on each of the three large sections onenote's own
finding times, `LargeData.one` (11,288,928 B), `Large Desktop.one` (112,965,624 B), and
`Large OneDrive.one` (161,469,274 B), three runs each, back to back:

- **adapter**: the onenote package as a caller uses it, a Reader over the onenote on PATH, every
  record decoded and every page's media iterated; **adapter, media**: the same with a media
  directory, every medium routed.
- **onenote** and **onenote, media**: the CLI alone, stdout to /dev/null, without and with
  `--media`.

Peak memory is each process's `ru_maxrss`, measured as the email finding does: the test binary
re-executed as a launcher reporting its program's `wait4` rusage, and, for the adapter, the
adapter process reporting its own rusage and its children's, the CLI's. The launcher's floor read
9.5 to 12.4 MiB. Media directories were on tmpfs.

### Results

| Section | Way | Median wall, of 3 | Peak, most of 3 | Adapter's own peak | The CLI's under it |
|---|---|---|---|---|---|
| `LargeData.one`, 11 MB | adapter | 1.384 s | 19.7 MiB | 13.3 MiB | 19.7 MiB |
| | adapter, media | 1.407 s | 19.6 MiB | 13.1 MiB | 19.6 MiB |
| | onenote | 1.429 s | 19.6 MiB | | |
| | onenote, media | 1.394 s | 19.7 MiB | | |
| `Large Desktop.one`, 113 MB | adapter | 0.281 s | 134.9 MiB | 10.7 MiB | 134.9 MiB |
| | adapter, media | 0.312 s | 135.1 MiB | 11.3 MiB | 135.1 MiB |
| | onenote | 0.289 s | 134.9 MiB | | |
| | onenote, media | 0.306 s | 134.9 MiB | | |
| `Large OneDrive.one`, 161 MB | adapter | 0.298 s | 135.1 MiB | 13.3 MiB | 135.1 MiB |
| | adapter, media | 0.315 s | 135.1 MiB | 11.5 MiB | 135.1 MiB |
| | onenote | 0.281 s | 135.1 MiB | | |
| | onenote, media | 0.311 s | 135.1 MiB | | |

What the runs show:

- **The adapter costs nothing measurable**: each way's median is within the other's spread, the
  adapter 3% under the CLI on `LargeData.one` and 6% over it on `Large OneDrive.one` without media.
  Each section is one page; the adapter decodes one large line.
- **The adapter's own process stays near the floor**, 10.7 to 13.3 MiB, while the CLI peaks at
  135 MiB on the 113 and 161 MB sections, where it does alone and in onenote's own finding (134.8
  to 135.1 MiB). The memory is the CLI's, onenote_parser's, not the adapter's.
- **What the media route to**: `LargeData.one`'s 136 media, its 135 images and its embedded file,
  all route to office: every one is the same 8.1 MB content, a .docx by its signature, named
  `testing - large.docx`, whatever the image records say. The other two sections' 13 media route 5
  to document and 8 to vision, by signature, `Large Desktop.one`'s PNGs among them though named
  `.pdf` (onenote's finding, "The large sections").

## The dispatcher over the scale store

### What was measured

`TestFindingsScaleDispatch` dispatches mail's scale store, 2,141,594,624 bytes, 50,000 messages,
three ways, three runs each, back to back:

- **dispatch**: a Dispatcher over every stage that needs no model, email, office, document,
  picture, zip, vision passing through, and onenote, over a fresh file index each run, its loop
  persisting nothing, as `findingStages` wires them. Its working directory is walked every 10 ms for
  the bytes it holds, a hard link's once, the peak kept.
- **email adapter, attachments**: an email Reader alone, its attachments written to a directory and
  each routed, no threads.
- **mail, attachments**: mail alone with `--attachments`, stdout to /dev/null.

The store is mail's generator's, in mail's checkout (mail `v0.3.0`), content SHA-256 the pin,
`d40792a4…62b4`. Working directories and indexes were on tmpfs. There is no release switch: the
dispatcher always releases the CLI's originals once settled, so the "release off" baseline is what
mail writes with nothing released, the 1,877,202,474 bytes of its 21,202 attachment files, the
email finding's 1.88 GB, which this run's mail and email adapter wrote again.

### Results

| Way | Median wall, of 3 | Runs | Own peak, most of 3 | Children's peak | Own CPU, a run |
|---|---|---|---|---|---|
| dispatch | **16.290 s** | 16.156, 16.410, 16.290 s | **35.9 MiB** | 67.0 MiB (mail) | 5.6 to 5.7 s user, 1.5 s system |
| email adapter, attachments | 14.116 s | 14.098, 14.116, 14.122 s | 19.1 MiB | 66.8 MiB (mail) | 1.7 s user, 0.3 s system |
| mail, attachments | 13.875 s | 13.908, 13.875, 13.807 s | 66.5 MiB | | |

| Measure | Each of the 3 dispatches |
|---|---|
| Parts | 21,203: the store, nested × prepared 1; its attachments, text × no processor yet 4,533 (the `.txt` and `.csv`), unrouted × no processor yet 16,669 |
| Outputs | 100,018: the store's record, its 17 folders, and 50,000 messages' records and Markdown |
| Failed | 0 |
| Peak disk, the working directory | **15,782,168 bytes**, of 1,555 to 1,584 samples |
| The file index once done | 21,206 files, 264,048,840 bytes |

What the runs show:

- **The dispatcher adds 2.2 s to the email adapter's time, 15%, and 2.4 s to mail's, 17%.** Its
  own CPU is 5.6 s user against the adapter's 1.7 s: hashing and inspecting every attachment,
  which the adapter doesn't, and appending 100,018 outputs to the file index. It runs beside mail
  through the pipe, so most of it overlaps mail's reading.
- **Its memory is a store's worth of nothing**: 35.9 MiB at most, 16.8 MiB over the adapter's,
  holding one event at a time; mail under it peaks where it does alone.
- **Release holds the disk to the attachments in flight**: 15.8 MB at most against 1.88 GB, 119
  times less. The index is the larger footprint, 264 MB, most of it the 50,000 messages' records
  and Markdown, persisted as outputs, and each attachment's flags under its SHA-256.
- **No attachment had a stage**: the generator's attachments are random bytes under typed names,
  which route by signature to unrouted, or text for the `.txt` and `.csv`, for which no stage is
  registered. The email finding counted all 21,202 unrouted under mail 0.1.0's run; route.Text
  has since named the text ones. The store measures the email stage and the dispatcher's
  bookkeeping, not the other stages.

## A resumed dispatch

`TestFindingsResume` dispatches the scale store three times over the same stages:

1. **whole**, over a fresh file index;
2. **first**, over another fresh index, with a caller following the cursor rule of dispatch's doc,
   "Resuming a store": each message's outputs gathered, committed at its last output, the cursor
   set to its id. Its loop stops once 25,000 messages are committed. The cursor and the Threader,
   saved by `MarshalBinary`, are written to files;
3. **resumed**, over the first's index directory, the cursor and the Threader read back from the
   files, with `Input.After` set to the cursor.

| Dispatch | Time | Messages committed | Message outputs committed |
|---|---|---|---|
| whole | 15.740 s | 50,000 | 100,000 |
| first, stopped at cursor 3353860 | 8.664 s | 25,000 | 50,000, 0 pending |
| resumed after 3353860 | **8.447 s** | 25,000 | 50,000 |
| first and resumed together | 17.111 s | 50,000 | **100,000, equal to the whole's in order, 0 twice** |

The Threader saved at the cursor is 1,507,973 bytes, written in 3.48 ms. The resumed threads equal
the whole read's, **34,928 roots**, tree for tree. The resumed store's own event is prepared and,
as `Input.After` says, not recorded.

What it shows:

- **The resumed half costs about half**, 0.54 of the whole: mail with `--after` writes the store
  and its folders, then only the messages after the cursor. Stopping and resuming costs 1.37 s over
  the whole, 9%.
- **No attachment was seen on resuming**, 0 parts: the scale store's attachments have no stage, so
  the index records only their flags, never an entry, and the resumed dispatch routes them again.
  A store whose attachments are prepared would see those prepared before the stop, as
  `TestStoreCrashResume` holds on the fixtures.
- **Saving the Threader at every message is quadratic.** The package doc has the caller save the
  Threader with the cursor at each message's last output, as the hermetic tests' `storeCaller`
  does. Each save marshals every message's keys so far: 3.5 ms at 25,000 messages, growing with the messages, so
  about 3 minutes of saving over the scale store, against its 16 s dispatch; a first try of this measurement with
  `storeCaller` ran past 3 minutes. This run's caller saves only at the stop. A real caller would
  save the cursor and Threader together every N messages, or on a timer, and accept resuming from
  that checkpoint.

## The pool

`TestFindingsPool` dispatches 41 inputs through `Dispatcher.Pool` at 1, 2, 4, and 8 workers, a
fresh file index each, one run each: plaso's store (59,442,176 B, its pin), the scale store, its
byte copy (made by the run, outside the store's checkout), and mail's 38 fixtures (the email
package's copy: every .pst, .msg, .eml, and mbox), 4,346,842,427 bytes in all.

| Workers | Dispatch | Throughput | Messages a second | Own peak | Children's peak | Peak disk | Done: plaso, scale, copy |
|---|---|---|---|---|---|---|---|
| 1 | 21.171 s | 195.8 MiB/s | 4,752 | 35.1 MiB | 66.7 MiB | 18,237,850 B | 1.763, 17.846, 20.857 s |
| 2 | 18.175 s | 228.1 MiB/s | 5,535 | 34.2 MiB | 66.0 MiB | 18,237,850 B | 1.842, 16.485, 18.175 s |
| 4 | 18.186 s | 227.9 MiB/s | 5,532 | 35.7 MiB | 66.1 MiB | 18,237,850 B | 2.031, 16.554, 18.186 s |
| 8 | 18.177 s | 228.1 MiB/s | 5,535 | 36.9 MiB | 66.3 MiB | 17,204,925 B | 2.016, 16.569, 18.177 s |

Messages a second count the 50,604 messages prepared and the 50,000 the copy streams from the
index. Every worker count came to the same 21,306 parts and 201,425 outputs, 0 failed, and an index
of 318,926,239 bytes in 21,411 files.

| Route | Outcome | Parts, at every worker count |
|---|---|---|
| nested | prepared 36, **seen 1** (the copy) | 37 |
| office | prepared 3, **seen 1** | 4 |
| vision | prepared 20, **seen 25** | 45 |
| document | prepared 1 | 1 |
| archive | prepared 1, encrypted 1 (plaso's ZIP of four encrypted spreadsheets) | 2 |
| text | no processor yet | 4,541 |
| unrouted | no processor yet | 16,676 |

What it shows:

- **The copy is seen whole**: the scale store's SHA-256 is recorded once its first dispatch ends,
  and the copy, waiting on the claim when dispatched beside it, is seen, its 100,018 outputs
  streamed from the index and 21,202 children referenced, in 1.6 to 3.0 s, rather than 16 s again.
- **Shared attachments are prepared once**: 26 parts seen, the fixtures' repeated pictures and
  office files, and plaso's spreadsheet attached twice.
- **More than 2 workers buys nothing here**: the scale store is one input, read by one mail, and
  takes 16 s of the 18; the pool overlaps the small inputs with it, and its copy's wait. At 1
  worker the copy waits until the store ends, then streams; at 2 and more it waits on the claim.
  A pool pays off over many inputs of similar size, which this set isn't.
- **Memory and disk don't grow with workers**: the dispatcher's own peak 34.2 to 36.9 MiB, the
  disk 17 to 18 MB, the same at 8 workers as at 1, each worker holding one event at most.

## Known limits

The packages', as their documentation states:

- **OneNote for the web can't export a notebook**, so there is no authored OneNote truth: the
  OneNote fixtures are public sets, their truth onenote's expectations, and the ink truth here is a
  session's draft.
- **The onenote CLI's memory is unbounded** in the section it reads: onenote_parser holds the
  section whole (onenote_parser issue #48). Here it peaks at 135 MiB on 161 MB.
- **mail's limits**: an .eml is capped at 4 GiB, and a .msg's nesting at 16 levels.
- **office reads a whole document into its model** before writing it.
- **The Threader holds about 60 bytes a message**, its threading keys, for the whole read: 1.5
  MB at 25,000 messages. Saving it at every message, as the doc's rule reads, is quadratic in the
  store (above).
- **Legacy OLE2 macros are undetected**: vba-project and macro-sheet read OOXML packages; a .doc's
  or .xls's VBA storage isn't checked.
- **Routes without a processor yet**: audio, video, text, HEIC, WebP, AVIF, ODF, RTF, WordPerfect,
  Excel binary workbooks, and archives other than ZIP. Their parts are recorded by their flags
  alone, so a stage registered later prepares them.
- **A released content's later reference takes the first occurrence's route**: once an original
  is released, a later record naming the same content is met with no file, its route and reason
  the first's, whatever its own name or type says.
- **The file index may prepare content twice around a stale-claim takeover**: a claimant slower
  than `FileOptions.Stale` is taken over, and both prepare it.
- **`Encryption.one` fails rather than coming to encrypted**: onenote refuses it with "Unexpected
  end of file", no flag marks it encrypted, so the dispatcher reports `Failed`.
- **A bare RFC 822 file without an extension is unrouted**: the nested route for a message needs
  an .eml name or a message type, which a bare file has neither of.

The finding's own:

- **The scale store measures the email stage only**: none of its attachments has a stage, so the
  dispatcher's other stages aren't loaded by it; the fixtures in the tier and the pool are.
- **The times are warm**: every store was in the page cache, working directories on tmpfs.
- **One run of each pool size**, and three of each other way.
- **One machine, x86_64**, the laptop.

## Rerunning

From this repository's root, on a laptop (never in CI, never in the check), with mail's scale store
built in mail's checkout, plaso's store fetched by `mise run findings-email`, ~/docs present, and
gb10-2's router serving set A:

```sh
(cd ~/experiments/spike-ai-pipeline-mail && MISE_ENV=fixtures mise run scale)

ONENOTE_SCALE_STORE=~/experiments/spike-ai-pipeline-mail/.state/scale/scale.pst \
ONENOTE_FINDINGS_RESULTS=docs/findings/onenote-results mise run findings-onenote
```

`mise run findings-onenote` (`scripts/findings-onenote.sh`) runs, each step's `-v` output appended
to `findings.log`: the integration tier's `TestTierRoutes`, its log written to `tier-routes.log`,
and onenote's `TestRealMediaRoutes`; inspect's findings-tagged `TestFindingsDocs` beside the docs
and clean fixtures' tests; and dispatch's findings-tagged tests, `TestFindingsInk`,
`TestFindingsOneNoteLarge`, `TestFindingsScaleDispatch`, `TestFindingsResume`, and
`TestFindingsPool`, without the race detector. `ONENOTE_LARGE` defaults to ~/docs/onenote/large;
`ONENOTE_SCALE_COPY` to `.state/findings/onenote/scale-copy.pst`, copied there when absent;
`ONENOTE_PLASO_STORE` to `.state/findings/plaso/`, refused unless its SHA-256 is the pin; without
`ONENOTE_SCALE_STORE` the scale store's tests skip. `ONENOTE_FINDINGS_RUNS` (3) sets the timed runs
of each input each way. A plain run keeps nothing. The run above took 5 minutes 7 seconds.
