# Finding: threads and routes through the email adapter, and what the adapter costs

This page records what the measurements behind the `email` package found: whether the threads it
rebuilds and the routes it gives each attachment match the hand truth the tests hold, what they
come to on a real store of size, and what the adapter costs in time and peak memory, threads
included, beside mail alone. They answer the threading, routing, and adapter part of evidence 16
of the spike (the root's `context/README.md`) for task `email` (the root's goal record,
`context/goals/experiment.ai.spike-ai-pipeline.md`). The rest of evidence 16, the fields mail
recovers per kind and its own time per store, is mail's
[mail-cli finding](https://github.com/JaimeStill/spike-ai-pipeline-mail/blob/main/docs/findings/mail-cli.md).

Every number here comes from one run of the findings tier, `mise run findings-email`, on
2026-11-03 from 18:11:15 to 18:14:16 UTC, on the laptop, with mail 0.1.0 from the mise pin; its
results are committed in [`email-results/`](email-results/README.md), the log verbatim and each
test's JSON. The integration tier's lines are from one run in slice 8 of the task, against gb10-2's
llama.cpp router, build b11669, serving set A.

The `email` package releases pre-inference preparation only: it names each attachment's route and
never runs the stage. The stages the integration tier runs, and its vision requests, are test code
in `email/route_integration_test.go`.

## At a glance

| Question | Answer |
|---|---|
| Are the threads right? | **Yes, every message scored.** On Tika's two stores, 11 of 11 messages sit under the parent the hand truth gives; on the generated store, which has no threading fields, 8 of 8 are roots, as by subject they should be. |
| Are the routes right? | **Yes, 50 of 50** attachments across the 23 fixtures, mail's 22 and the built .msg, are routed as the tests' hand truth says: 1 to document, 2 to office, 36 to vision, 2 to vision through office's conversion, 4 nested, 5 unrouted. |
| What do they come to on a real store? | plaso's 59 MB store, 540 messages: **347 threads, 49 of them of more than one message**, holding 242 messages, the largest 52 messages, 6 levels deep. Its 6 attachments: **4 to office, 2 unrouted** (ZIP archives). Counted, not scored. |
| What does the adapter cost? | **About 2 to 4% over mail alone in time**: plaso's store 1.581 s against 1.528 s, the 2.1 GB scale store 13.27 s against 12.97 s (13.91 s against 13.59 s routing every attachment). Median of 3 each. |
| How much memory? | The adapter's Go process peaks at **22 MiB on plaso's store and 51 to 53 MiB on the scale store**, threading 50,000 messages, against a 13.5 MiB floor; mail beside it peaks where it does alone, **42 MiB and 65 to 67 MiB**, as mail's own finding measured (41.6 and 65.1 to 67.0 MiB). |
| Does routing by signature matter? | **Yes, on real data.** MsgKit stores the built .msg's EMF as `application/octet-stream`, and Apache POI's `alfresco.gif` is a BMP stored as `image/gif`; plaso's two spreadsheets are stored as `application/octet-stream`. Each routes by its content's signature. The scale store's 21,202 attachments, random bytes named `.pdf`, `.png`, `.docx`, and so on, are all unrouted. |
| Does vision read what the routes hand it? | **Yes, all 15 distinct pictures** the fixtures carry to vision completed on Gemma 4 26B-A4B on gb10-2: 12.669 s one at a time, 1,562 image tokens, 0 failed. |

## Threads and routes over the fixtures

### What was measured

`TestFindingsFixtures` reads every fixture through the real pinned mail with an attachments
directory: mail's 22 `.pst` and `.msg` fixtures, copied byte for byte at its tag
(`email/testdata/README.md`), and the built `.msg`, which attaches four of this repository's office
fixtures. It feeds every record to a `Threader`, routes every attachment as it is read, nested
messages' included, and scores both against the hand truth the hermetic tests already hold:

- **Routes** against `fixtureRoutes` (`email/route_test.go`), written by hand from mail's goldens,
  and `builtSources` (`email/built_test.go`), written from the built `.msg`'s source. Between them
  they cover every attachment of every fixture. A route is right when the attachment in that
  place, in the order read, has the filename and the route the truth gives.
- **Threads** against `realThreads` (`email/threads_test.go`): Tika's `testPST.pst` and
  `testPST_variousBodyTypes.pst`, derived by hand from the stored fields mail's
  `tika-threading.json` holds (which mail's finding scores 47 of 47) and the dates its goldens
  give; and the generated `mailbox.pst`. A message is right when its parent, a message or none, is
  the truth's.

The other fixtures hold threads too, one root each for a `.msg`, and are counted, not scored.

### Results

From `email-results/fixtures.json` and its `recorded: fixture` lines:

| Fixture | Messages | Attachments, by route | Threads | Scored |
|---|---|---|---|---|
| `pst/tika/testPST.pst` | 7 | nested 1 | 6 roots; `2097252[2097188]` the one of two | threads 7 of 7, routes 1 of 1 |
| `pst/tika/testPST_variousBodyTypes.pst` | 4 | none | 1 root, `2097188[2097220 2097252 2097284]` | threads 4 of 4 |
| `generated/pst/mailbox.pst` | 8 | vision 1, unrouted 2 | 8 roots | threads 8 of 8, routes 3 of 3 |
| `pst/libyal/outlook.pst` | 2 | vision 22 | 1 placeholder holding both | routes 22 of 22 |
| `pst/java-libpst/dist-list.pst` | 2 | nested 2 | 2 roots | routes 2 of 2 |
| `pst/java-libpst/passworded.pst` | 1 | none | 1 root | |
| `msg/tika/test-outlook2003.msg` | 1 | vision 11 | 1 | routes 11 of 11 |
| `msg/tika/testMSG_att_msg.msg` | 1 | document 1, nested 1 | 1 | routes 2 of 2 |
| `msg/poi/example_sent_regular.msg` | 1 | vision 1 (`alfresco.gif`, a BMP) | 1 | routes 1 of 1 |
| `generated/msg/03-html-inline-image.msg` | 1 | vision 1 | 1 | routes 1 of 1 |
| `generated/msg/04-attachment-cyrillic.msg` | 1 | unrouted 1 (a `.csv`) | 1 | routes 1 of 1 |
| `generated/msg/08-forward-attached-message.msg` | 1 | unrouted 2 (a `.txt`, a by-value `.eml`) | 1 | routes 2 of 2 |
| `built/msg/office-attachments.msg` | 1 | office 2, vision-converted 2 | 1 | routes 4 of 4 |
| The other 10 `.msg` fixtures | 1 each | none | 1 each | |
| **All 23** | **41** | **50: document 1, office 2, vision 36, vision-converted 2, nested 4, unrouted 5** | | **threads 19 of 19, routes 50 of 50** |

What the trees show:

- **RFC 5256's placeholder pruning.** In `testPST.pst`, the two JIRA mails and `FW: First email`
  each refer to a message the store doesn't hold. Each such Message-ID becomes a placeholder with
  one child; a placeholder at the root with a single child gives it up, so each message stands as
  a root rather than under an empty parent. A placeholder is kept only where it holds several
  roots together: in libyal's `outlook.pst`, two copies of Outlook 2003's welcome message, with no
  threading fields and the same subject, are gathered under one, the only placeholder among the
  fixtures.
- **The conversation index alone gives the same trees.** `TestRealThreadsIndex` (hermetic)
  clears every header field and the subject and threads Tika's stores by conversation index
  alone, to the same `realThreads` trees.
- **No threading fields, threads by subject.** The generated store's messages carry no
  Message-ID, In-Reply-To, or References (PST Builder writes none, mail's finding), and none
  shares a base subject with another, so each is a root.

What the routes show, the signature routing proven by real data:

- **The built .msg's EMF.** MsgKit stores `diagram.emf`, `image/emf` in the source, as
  `application/octet-stream`; its EMR_HEADER signature routes it to vision through office's
  picture conversion. Routed by the stored type, it would have gone nowhere.
- **POI's `alfresco.gif` is a BMP**, named `.gif` and stored as `image/gif`; its `BM` signature
  and DIB header route it to vision as stored, and the integration tier decodes it as a 202x74
  BMP. A vision model given it as a GIF would be given the wrong type.
- **A by-value `.eml` is unrouted**: MsgKit's forward attaches the forwarded message as an `.eml`
  file, text with no signature the routes know.

## plaso's store

`TestFindingsPlaso` uses mail's own pin: `test_data/nromanoff@stark-research-labs.com.pst` from
[log2timeline/plaso](https://github.com/log2timeline/plaso) (Apache-2.0) at
`f5bf35fcc102585eed58b930392da56a318690f6`, the commit that added it, SHA-256
`bab64f3589f8ce758df9e595b89a5e4b9be159929e49100397152fdcc7bd6013` (mail's
`scripts/findings.sh`). The test fetches it into `.state/findings/plaso/` when absent and refuses
it unless its SHA-256 is the pinned one; it is never committed. It is a real Outlook Unicode store,
59,442,176 bytes. Nothing here has a hand truth for it, so its threads and routes are counted and
not scored.

| Measure | Value |
|---|---|
| Records | 18 folders, 540 messages, 3 skipped (Suggested Contacts outlook-pst 1.2.0 can't open), 3 warnings |
| Threads | 347 roots; 49 of more than one message, holding 242 of the 540; the largest 52 messages; the deepest 6 levels |
| Placeholders left in the trees | 44, each holding several roots together |
| Attachments | 6 records, 5 distinct contents, 14,533,959 bytes written |
| Routes | office 4: three `.xlsx` (one spreadsheet attached twice), stored as `application/octet-stream`, and one `.docx`, each a ZIP with an OOXML extension; unrouted 2: two `.zip` archives, a ZIP without one |

Every run counted the same, each way; only the runs with an attachments directory route.

## Time and peak memory

### What was measured

Each large store is read four ways, three runs each, back to back:

- **adapter**: the email package as a caller uses it, a Reader over the mail on PATH, every record
  fed to a Threader, the threads built; no attachments directory.
- **adapter, attachments**: the same with an attachments directory, every attachment routed as it
  is read.
- **mail** and **mail, attachments**: mail alone, its stdout to `/dev/null`, without and with
  `--attachments`, the baseline the adapter adds to.

Peak memory is each process's `ru_maxrss`, the way mail's own finding measures it, from rusage.
It can't be read from the test process itself: Go starts a program with a vfork-style clone, and
on exec Linux carries the parent's high-water mark into the program's peak, so every program the
test ran would read at least the test's own size (`tool.Usage`, README). So the test binary is
re-executed as a small launcher that runs the measured process and reports its `wait4` rusage and
wall time. For the adapter's ways, the measured process is the test binary again, as the adapter:
it reports its own peak (`RUSAGE_SELF`) and its children's (`RUSAGE_CHILDREN`), which is mail's,
the version check's and the read's. The launcher's floor, a process that does nothing launched the
same way, is **13.5 MiB**: no peak below it can be read, and the adapter's own is read on top of it.
Mail's peak under the adapter has as its floor the adapter's size when it started mail, before any
record was read.

Wall time is the launcher's, from start to exit: for the adapter, the Go process's start, mail's
version check, the read, and the threads. Each attachments directory was on tmpfs, emptied after
each run.

### Results

| Store | Way | Median wall, of 3 | Runs | Adapter's peak, most of 3 | mail's peak, most of 3 | Adapter's own CPU, a run |
|---|---|---|---|---|---|---|
| plaso's, 59 MB, 540 messages | adapter | 1.581 s | 1.600, 1.571, 1.581 s | 22.2 MiB | 41.6 MiB | 0.15 to 0.18 s user, 0.02 to 0.04 s system |
| | adapter, attachments | 1.572 s | 1.590, 1.572, 1.561 s | 21.4 MiB | 41.6 MiB | 0.16 to 0.17 s user, 0.02 to 0.03 s system |
| | mail | 1.528 s | 1.515, 1.562, 1.528 s | | 41.7 MiB | |
| | mail, attachments | 1.527 s | 1.527, 1.531, 1.504 s | | 41.6 MiB | |
| scale, 2.1 GB, 50,000 messages | adapter | 13.271 s | 13.023, 13.271, 13.283 s | 50.7 MiB | 65.0 MiB | 1.73 to 1.78 s user, 0.25 to 0.27 s system |
| | adapter, attachments | 13.907 s | 13.848, 13.907, 13.994 s | 53.3 MiB | 67.3 MiB | 1.78 to 1.87 s user, 0.30 to 0.35 s system |
| | mail | 12.969 s | 13.007, 12.965, 12.969 s | | 65.3 MiB | |
| | mail, attachments | 13.592 s | 13.615, 13.592, 13.532 s | | 66.6 MiB | |

What the runs show:

- **The adapter adds about 2 to 4% to mail's time**: 0.05 s on plaso's store, 0.30 s on the scale
  store, 0.32 s with every attachment routed. The adapter's own CPU, decoding every line through
  encoding/json/v2 into typed values, threading, and routing, is about a sixth of mail's (1.7 to
  1.9 s against 9.8 to 10.1 s user on the scale store); it runs beside mail through the pipe, so most of it
  overlaps mail's reading.
- **Routing costs next to nothing**: one 64-byte read per attachment. On plaso's store the runs
  with and without it are within each other's spread; on the scale store, the 0.64 s the
  attachments add is mail's writing 1.88 GB, which mail alone shows too (13.59 s against 12.97 s).
- **The adapter's memory follows the messages, not the store**: about 8 MiB over the floor for
  plaso's 540 messages and 37 to 40 MiB for the scale store's 50,000, whose threading keys the
  Threader holds, plus the Go heap's headroom. The store's 2.1 GB and its 1.8 GB of attachments
  never pass through the adapter's memory: the records stream through a pipe, one line at a time,
  and attachment contents go to files.
- **mail under the adapter peaks where it does alone**: 41.6 against 41.7 MiB on plaso's store,
  65.0 against 65.3 MiB on the scale store, and mail's own finding's 41.6 and 65.1 MiB on the
  same stores. The adapter reads as fast as mail writes, so nothing backs up in mail.
- **mail alone was faster here than in mail's finding** on the scale store, 12.97 s against
  13.27 s, and 13.59 s against 14.38 s with `--attachments`: this run read the store from tmpfs
  and wrote the attachments to tmpfs, where mail's finding used the btrfs disk's page cache. On
  plaso's store, read from the disk's page cache in both, 1.528 s against 1.592 s. The binary
  differs too: this is the mise-pinned release binary, where mail's finding timed the check's own
  build at commit 32e5aa9, an ancestor of the tag.

### The scale store

The scale store is mail's generator's, never committed: mail's tag `v0.1.0`
(`75923186494d94ddf37a61b9865c1848bb0bbbd6`) exported into a scratch directory outside any
checkout and built there with `MISE_ENV=fixtures mise run scale`, seed 16, 50,000 messages, in
32.2 s. Its content SHA-256 is
`d40792a45510dc0d4b7a3abc527848095d4f255bae26d853ee7f9a87119362b4`, the one mail's finding pins,
and its size the same 2,141,594,624 bytes; `scale.json`'s parameters and counts are in
`email-results/scale.json`'s `record`.

It measures cost only. PST Builder writes no threading fields, so its 50,000 messages thread by
subject alone: 34,928 threads, 8,109 of more than one message holding 23,181, 94 placeholders,
the largest 56 messages, 3 levels deep. Those are the generator's 14,977 replies gathered under
the subjects they reply to, not a measure of threading. Its 21,202 attachment records (17,828
attachments and 3,374 inline images) are random bytes under typed names, `.pdf`, `.png`, `.jpg`,
`.zip`, `.docx`, and `.bin`, or text named `.txt` and `.csv`: none has a routed signature, so all
21,202 are unrouted, which is the signature rule doing its job on content that only claims a type.

## The integration tier: each route to its stage

Slice 8's integration tier, `TestRoutesToStages` (`email-results/routes-to-stages.log`), carries
every fixture's attachments to their real stage, each distinct content once:

| Route | Attachments | Stage runs | What ran |
|---|---|---|---|
| document | 1 | 1 | `smbprn.00009008.KdcPjl.pdf` through the document Preparer: 1 page at 150 dpi, with a text layer, 207 ms |
| office | 2 | 2 | `minutes.docx` through office, 28 ms; `budget.xls` to xlsx through soffice, then office, 695 ms |
| vision-converted | 2 | 2 | `scan.tif` to a 640x400 PNG through magick, 21 ms; `diagram.emf` to a 1489x566 PNG through soffice and magick, 888 ms |
| vision | 36 | 13 | Handed on as stored, distinct by SHA-256: the PNG, POI's BMP, and Outlook 2003's 11 JPEGs, carried by both `test-outlook2003.msg` and libyal's store |
| nested | 4 | 1 | `testMSG_att_msg.msg`'s embedded message, dispatched in turn; the 3 in `.pst` stores are listed without content and not read |
| unrouted | 5 | 0 | `.csv`, `.txt`, and the by-value `.eml` |

It then sent each of the 15 distinct pictures handed onward, the 13 as stored and the 2
converted, to Gemma 4 26B-A4B (`google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0`) on gb10-2's router
with a request to describe it, one at a time: **15 completed, 0 failed, in 12.669 s, 1,562 image
tokens and 606 output tokens**. Each small picture cost the floor office's finding found, 83
image tokens; the BMP 86, the TIFF's PNG 106, and the EMF's 1489x566 PNG 374, as in office's
finding. The descriptions, in the log, describe their pictures (the TIFF's "TEST TIFF", the EMF's
greetings in Russian, English, and Uzbek, the Alfresco logo, Outlook's icons); they weren't scored.
At the time gb10-2 had OS apt upgrades pending; the router, llama.cpp b11669, was untouched.

## Known limits

The adapter's, which the package documentation and the README state:

- **A by-value `.eml` attachment is unrouted**: its content is text with no signature. MsgKit's
  forward attaches the forwarded message so; a caller wanting it read must parse MIME itself.
- **`.ost` is refused** by mail, with its reason, an `*ExitError` matching `ErrRefused`.
- **In a `.pst`, an embedded message is nested without content**: outlook-pst 1.2.0 can't read
  it, so it is routed `RouteNested` with nothing to read; in a `.msg` it is nested in full.
- **A by-reference attachment is unrouted**: it has no content file. No store here has one.
- **The generated stores carry no threading fields**, the fixtures' and the scale store, so they
  thread by subject alone; the only threading truth is Tika's two small stores, 11 messages.

The finding's own:

- **plaso's threads and routes aren't scored**: there is no hand truth for its 540 messages.
- **The times are warm**: every store was in the page cache or on tmpfs, so no run read a disk
  cold.
- **Peak memory has a floor of 13.5 MiB**, the launcher's: the adapter's own size above it is the
  measure, and a small store's peak says little more than the floor.
- **One machine, x86_64**: the laptop; nothing was measured on arm64.

## What it answers for evidence 16

Answered:

- That the threads the package rebuilds from mail's threading fields match the hand truth on
  every scored message, by headers and by conversation index alone, with RFC 5256's placeholders
  pruned; and what they come to on plaso's real store.
- That every attachment in the fixtures is routed to the stage its format takes, by its content's
  signature, and that real stores' stored types and names would have misrouted some; and that
  each route reaches its real stage, and vision reads every picture handed to it.
- That the adapter, threads included, costs about 2 to 4% over mail alone in time, and that its
  memory follows the number of messages, about 51 MiB at 50,000, while mail's stays its own.

Not answered:

- Threading quality on a large real store with a truth to score against.
- Cold-cache time, and arm64.

## Rerunning

From this repository's root, on a laptop (never in CI, never in the check):

```sh
# once, outside any checkout: mail's tag, and its scale store (about 2.1 GB, 35 s)
git -C ~/experiments/spike-ai-pipeline-mail archive v0.1.0 | tar -x -C "$scratch/mail-scale"
(cd "$scratch/mail-scale" && mise trust mise.toml && mise trust mise.fixtures.toml &&
  MISE_ENV=fixtures mise run scale)

EMAIL_SCALE_STORE="$scratch/mail-scale/.state/scale/scale.pst" mise run findings-email
```

`mise run findings-email` (`scripts/findings-email.sh`) runs the findings-tagged tests in package
email, without the race detector. It fetches plaso's store into `.state/findings/plaso/` (or where
`EMAIL_PLASO_STORE` names) and refuses it unless its SHA-256 is the pin; without
`EMAIL_SCALE_STORE` it skips the scale store. `EMAIL_FINDINGS_RUNS` (3) sets the runs of each large
store each way. A plain run keeps nothing: its results go to a temporary directory, removed when
it ends. To keep them, record them:

```sh
EMAIL_FINDINGS_RESULTS=docs/findings/email-results EMAIL_SCALE_STORE=... mise run findings-email
```

which writes each test's JSON and the run's `-v` log, `findings.log`, there. The run above took
3 minutes, 2 min 42 s of it the scale store.
