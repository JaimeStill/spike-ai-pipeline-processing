# Finding: PDF pages through a model's vision

This page records what the measurements behind the `document` package found: which renderer
ships, whether the model should reason while it reads, whether image corrections help, where the
transcription instructions go, whether a staged two-pass flow pays, and what a whole document
costs. They answer evidence 4 and 11 of task `documents` (the root's goal record,
`context/goals/experiment.ai.spike-ai-pipeline.md`, "Task brief · experiment.ai.spike-ai-pipeline ·
documents"). Rulings L, M, N, and O are the architect's decisions in that record's Decisions
section, on the lines labelled with those letters. Every run went from the laptop to gb10-2's
llama.cpp router serving set A, on 2026-10-29 and 2026-10-30.

The `document` package releases pre-inference preparation only: `Preparer.Images` renders each
page to an image and can correct it first. The inference these measurements ran is test code in
`document/vision_test.go`, the vision harness the integration and bake-off tiers run on, built on
`Images`: `Vision` with its `Pages` and `Document`, `Request.PageRetries`, `Page.Attempts`,
`Result.Text`, and `TranscribeInstructions`. The names below are those the runs used.

Two models do the work. Gemma 4 26B-A4B reads the pages: gb10-2 now serves Google's QAT Q4_0
(`google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0`) at 1,120 image tokens, and the next section
records its reruns. The sections after it record the runs on ggml-org's post-training Q4_0
(`ggml-org/gemma-4-26B-A4B-it-GGUF:Q4_0`) at 512 image tokens, which they name as before.
gpt-oss-120b (`ggml-org/gpt-oss-120b-GGUF:MXFP4`) writes the document's digest. Every
number here comes from a recorded run: a test's `recorded:` log line, or the JSON a bake-off-tier
run kept, committed in [`documents-results/`](documents-results/README.md). Each section says
which run.

## At a glance

"QAT 1,120" is what gb10-2 serves now; "post-training" is the build the earlier runs used, at 512
image tokens. A row that names neither is from the post-training runs and wasn't rerun.

| Question | Answer |
|---|---|
| What reads the pages on gb10-2? | **Google's QAT Q4_0 of Gemma 4 26B-A4B** (`google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0`, its own projector) at the projector's full 1,120 image tokens (`ub = 1120`). It replaced ggml-org's post-training Q4_0, which fails the workspace's quantization floor. |
| Which renderer ships? | **Ghostscript (`gs`).** It ties the others on accuracy over the clean and degraded sets, and wins on time (0.126 s a page) and memory (27 MiB), with one program, on all three builds measured: post-training 512, QAT 512, and QAT 1,120. It is AGPL, so the package runs it as a separate program and never links it. |
| Should the model reason while it transcribes? | **No.** With reasoning on, 8 of 105 scanned-page transcriptions reasoned until the 8192-token cap and came back empty, and the model invented seal text. With reasoning off, every transcription completed. |
| Do image corrections help? | **Not reliably, on either build.** At 150 dpi the synthetic faults give corrections nothing to fix (QAT 1,120 reads every degraded page exactly in every cell). At 72 dpi, Unsharp helps the hardest page while Deskew and Despeckle hurt it. On the real scans, which corrections help and hurt flips with the DPI and with the build. |
| What does a page cost in context? | **920 image tokens** for a letter page at 150 dpi on QAT 1,120, and 1,104 at 200 dpi and up; 223 at 72 dpi and 416 at 100. At the old 512 cap, 150 and 200 dpi both cost 477. A page request is 976 input tokens of Gemma's 32,768, against 533. |
| Does Document's final step fit? | **Easily.** The digest request took 1,111 to 1,134 input tokens for six clean pages and 564 to 570 for seven scanned pages: under 1% of gpt-oss-120b's 131,072-token context. |
| Where do the instructions go? | **Beside the image**, in its user message. On post-training it was better on the scans (CER 0.0864 against 0.1170, 0.1091..0.1328, in the request's instructions); on QAT 1,120 the two tie (0.1332 against 0.1317). |
| Does the staged two-pass flow pay? | **Not as built.** Text layers cost no model call but read a two-column page and a form out of order (clean CER 0.1711 against 0). On QAT 1,120 the model's judgements and corrections fit the faults more closely, and its second passes changed nothing; its scanned CER (0.1109) beat one plain pass (0.1339) through the first pass's read of page 7, at 1.29 calls a page. On post-training the second passes hurt more scanned pages than they helped. |
| Is a larger image budget better on the scans? | **Not on these pages.** On QAT, 920 image tokens instead of 477 made every renderer's scanned CER worse (gs 0.1188 to 0.1325), on page 7, whose seal it reads further and invents on. Page 7's NOFORN is read on no build at any budget. Its requests took about 2 s longer on average, 4 in flight. |
| What do Pages and Document cost? | Six clean pages: 7.8 to 11.2 s for Pages and 15.5 to 18.1 s for Document with its digest. Seven scanned pages: 11.7 to 13.0 s for Document. The times vary from run to run. |

## Google's QAT Q4_0 and the full image-token budget

On 2026-10-30, task `vision-qat` changed what gb10-2 serves for the pages twice, and every
bake-off ran again. This section records those reruns beside the runs the rest of the page
describes, which were on the earlier build. The decisions are the `vision-qat` lines of the root's
goal record, rulings Q and R among them.

### Why the build changed

Set A is held to the workspace's quantization floor (`standards-lab/context/ai-hosting.md`): a
post-training Q4_0 fails it, and a quantization-aware-trained (QAT) Q4_0 passes. So Gemma 4
26B-A4B moved from ggml-org's post-training Q4_0 to Google's QAT Q4_0, which needs the same memory.
Source: the root's `docs/findings/host.md`, "2026-10-30: set A's vision model is Google's QAT Q4_0",
and its `host/llama-preset/models.ini`.

| | Before: post-training | Now: QAT |
|---|---|---|
| Repository and tag | `ggml-org/gemma-4-26B-A4B-it-GGUF:Q4_0` | `google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0` |
| Model file | `gemma-4-26B-A4B-it-Q4_0.gguf` | `gemma-4-26B_q4_0-it.gguf`, 14,439,363,584 bytes |
| Projector | `mmproj-gemma-4-26B-A4B-it-Q8_0.gguf`, a Q8_0 | `gemma-4-26B-it-mmproj.gguf`, 1,194,828,160 bytes, the repository's own |
| Provenance | ggml-org quantized Google's weights after training | Google trained the weights for the Q4_0 quantization and published the file |
| llama-server GPU memory at 512 image tokens | 16,972 MiB | 16,960 MiB |

### Why the budget changed

The QAT projector allows 1,120 image tokens per image, but llama-server b11669 caps an image's
tokens at n_ubatch, 512 by default, since the model attends over an image non-causally. Both
builds logged the cap at load, and a letter page cost 477 image tokens at 150 dpi and at 200. The
preset's vision section now sets `ub = 1120`. `b` stays at 2048, and `mtmd-batch-max-tokens` at
512, so each image still encodes in a batch of its own. With set A loaded, the vision instance grew
about 800 MiB, from 17,346 to 18,143 MiB, with about 16 GiB still available (the goal record's
`vision-qat` line on the vision section).

`TestVisionImageTokensHost`, rerun on 2026-10-30 against the QAT build at 1,120, recorded
clean.pdf's page 1 rendered by gs:

| DPI | Pixels | Image tokens at 512 | Image tokens at 1,120 |
|---|---|---|---|
| 72 | 612 × 792 | 223 | 223 |
| 100 | 850 × 1100 | 416 | 416 |
| 150 | 1275 × 1650 | 477 | 920 |
| 200 | 1700 × 2200 | 477 | 1,104 |

1,104 is the count from 200 dpi up. A whole page with the instructions beside it is now 976 input
tokens, against 533 at 512 (the bake-offs' JSON), and Gemma's context is still 32,768.

### What ran, and what each comparison can tell

Ruling R kept three columns for the renderers alone: post-training at 512, QAT at 512, and QAT at
1,120. The other four tasks ran on QAT at 1,120 only, so against their post-training runs **the
build and the budget changed together**, and a difference there can't be put on either one. Two
things isolate the build: pages at 72 dpi (223 image tokens) and every tile but one are under both
caps. The QAT 512 and QAT 1,120 renderer runs isolate the budget.

Every run went from the laptop to gb10-2 on 2026-10-30, with the same settings as before: greedy,
reasoning effort `none`, the 8192-token cap, 4 requests in flight, no page retries, gs 10.08.0 and
ImageMagick 7.1.2-34 on the laptop. Their JSON is in
[`documents-results/qat-1120/`](documents-results/qat-1120/). No `go test` run came near its
10-minute cap:

| Task | Wall clock | `go test` runs, each |
|---|---|---|
| `mise run bakeoff` | 872 s | 36 to 99 s |
| `mise run bakeoff-corrections` | 1,560 s | 15 to 104 s |
| `mise run bakeoff-placement` | 294 s | 33 to 68 s |
| `mise run bakeoff-flow` | 627 s | 0 to 65 s |
| `mise run bakeoff-tiles` | 12 s | 5 and 7 s |

### Renderers

Sources: [`documents-results/renderers/`](documents-results/renderers/),
[`documents-results/qat-512/renderers/`](documents-results/qat-512/renderers/), and
[`documents-results/qat-1120/renderers/`](documents-results/qat-1120/renderers/), each ranked by
`TestBakeoffRanking` with ruling Q's method: accuracy over the clean and degraded sets, the
scanned set reported apart.

| Rank | Renderer | Clean + degraded CER: post-training 512 | QAT 512 | QAT 1,120 | Render s/page (1,120) | Peak MiB (1,120) |
|---|---|---|---|---|---|---|
| 1 | **gs** | 0.0000 | 0.0001 (0.0000..0.0003) | 0.0000 | 0.127 | 27.0 |
| 2 | vips | 0.0000 | 0.0001 (0.0000..0.0002) | 0.0000 | 0.133 | 90.3 |
| 3 | magick | 0.0000 | 0.0000 | 0.0000 | 0.340 | 102.6 |
| 4 | pdftocairo | 0.0000 | 0.0000 (0.0000..0.0001) | 0.0000 | 0.415 | 58.1 |
| 5 | pdftoppm | 0.0000 | 0.0000 | 0.0000 | 0.459 | 30.6 |

The ranking is the same in all three columns, gs first with 4 wins, and in each, render time
decided all 10 pairs: accuracy tied every pair. No clean or degraded transcription was incomplete
in any column (0/45 per renderer). QAT 512's small degraded errors were inside the run-to-run
spread; at 1,120 every clean and degraded transcription scored 0.

Scanned, reported apart and not ranked (CER mean (min..max), incomplete of 21):

| Renderer | Post-training 512 | QAT 512 | QAT 1,120 |
|---|---|---|---|
| gs | 0.0803 (0.0702..0.0864), 0 | 0.1188, 0 | 0.1325 (0.1296..0.1339), 0 |
| vips | 0.1242 (0.1091..0.1544), 0 | 0.0896, 0 | 0.1317, 0 |
| magick | 0.0994 (0.0842..0.1112), 0 | 0.0576 (0.0270..0.1188), 2 | 0.1343 (0.1339..0.1350), 0 |
| pdftocairo | 0.1091, 0 | 0.0896, 0 | 0.1317, 0 |
| pdftoppm | 0.0925 (0.0918..0.0929), 0 | 0.1033 (0.1026..0.1048), 0 | 0.1152 (0.1123..0.1166), 0 |

A rate with no range was the same on all three runs. magick's QAT 512 mean is over its 19 completed
transcriptions: its two incomplete ones were page 7 looping to the cap, which leaves page 7's two
worst reads out of its mean. Pages 1 to 4 and 6 score 0 in every column but for one magick read
of page 1 at 1,120 (0.0052); the scanned differences are pages 5 and 7. Raising the budget from
512 to 1,120 on the same build made every renderer's scanned CER worse, on page 7: gs's page 7
went from 0.7928 on every run to 0.9069 (0.8829..0.9189).

Request time grew with the budget, not the build. The gs runs' mean request time, 4 in flight:

| Set | Post-training 512 | QAT 512 | QAT 1,120 |
|---|---|---|---|
| clean | 5.73 s | 5.70 s | 7.83 s |
| degraded | 5.30 s | 5.34 s | 7.45 s |
| scanned | 3.07 s | 3.03 s | 5.06 s |

The 15 renderer and set runs' elapsed times summed to 638 s, 805 s (magick's two loops took 180
and 173 s), and 867 s. The first run of each set, from a cold cache, is now the slow one: in the
placement runs below, a scanned run took 19.1 s first and 6.5 s after, against 10.4 and 5.6 s
before.

### Corrections

Sources: [`documents-results/corrections/`](documents-results/corrections/) and
[`documents-results/qat-1120/corrections/`](documents-results/qat-1120/corrections/), each
`TestCorrectionsMatrix`'s report: CER mean, its spread where the runs differed, and "vs none" as
the matrix judges it. The instructions are in the request's `Instructions`, as before. At 150 dpi
the build and the budget changed together:

| Cell | Degraded, post-training 512 | Degraded, QAT 1,120 | Scanned, post-training 512 | Scanned, QAT 1,120 |
|---|---|---|---|---|
| none | 0.0037 (0.0000..0.0110) | 0.0000 | 0.1098 (0.1091..0.1112) | 0.1310 (0.1296..0.1317) |
| Deskew | 0.0000, tie | 0.0000, tie | 0.1199, hurts | 0.1213 (0.1199..0.1220), helps |
| Despeckle | 0.0110, tie | 0.0000, tie | 0.0907, helps | 0.1292 (0.1285..0.1296), tie |
| ContrastStretch | 0.0000, tie | 0.0000, tie | 0.1091, tie | 0.1317 (0.1296..0.1339), tie |
| Unsharp | 0.0000, tie | 0.0000, tie | 0.1325 (0.1307..0.1361), hurts | 0.1159 (0.0918..0.1285), helps |
| Grayscale | 0.0110, tie | 0.0000, tie | 0.0961 (0.0270..0.1490), 1 incomplete, tie | 0.1310 (0.1296..0.1339), tie |
| all | 0.0000, tie | 0.0000, tie | 0.1094 (0.1080..0.1123), tie | 0.1199 (0.0918..0.1728), tie |

The degraded set is a null result at 150 dpi on both: the QAT build at 1,120 read every page
exactly in every cell, where the post-training build had broken page 2's table with `|` on some
runs. On the scans the cells that help flipped: Despeckle helped before and ties now; Deskew and
Unsharp hurt before and help now, on page 7 alone. Page 7's mean per cell at 1,120 is 0.8949 for
none, 0.5586 for all five, and 0.7688 to 0.9009 for the others. Deskew no longer reads page 5's
seal (0.2222, against 0.0000 before), and all five put a new error on page 6 (0.1860).

At 72 dpi a page is 223 image tokens, under both caps, so this table **changes only the build**:

| Cell | Degraded, post-training | Degraded, QAT | Scanned, post-training | Scanned, QAT |
|---|---|---|---|---|
| none | 0.0708 (0.0038..0.1050), 1 incomplete | 0.0923 (0.0785..0.1073) | 0.1022 (0.0994..0.1080) | 0.0904 (0.0842..0.1004) |
| Deskew | 0.1974, hurts | 0.2177, hurts | 0.0922, helps | 0.1015, tie |
| Despeckle | 0.2012, hurts | 0.1598, hurts | 0.1058, tie | 0.0659, helps |
| ContrastStretch | 0.0589, 1 incomplete, tie | 0.0754, helps | 0.0994, tie | 0.1004, tie |
| Unsharp | 0.0205, tie | 0.0177, helps | 0.1080, tie | 0.1246, hurts |
| Grayscale | 0.0564, 1 incomplete, tie | 0.0749, helps | 0.1008, tie | 0.0864, tie |
| all | 0.1669, hurts | 0.1815, hurts | 0.1120, hurts | 0.1091, hurts |

(Spreads are left out where the cell's own runs were close; the JSON has them.) The QAT build reads
the severely skewed table, degraded page 2, at 72 dpi (0.0009 against 0.2883), but page 9, all four
faults, worse (0.5078 against 0.3984); Unsharp is still the one correction that fixes page 9
(0.0848). Its runs agree with each other more: the QAT cells' spreads are narrower. On the scans,
Despeckle now helps (page 6 falls from 0.0700 to 0.0072, page 7 from 0.4505 to 0.3153) and
Unsharp hurts. The conclusion stands on both builds: each correction that helps somewhere hurts
somewhere else, and which ones do flips with the DPI and now with the build too.

None of the matrix's 672 transcriptions on the QAT build was incomplete, against 4 on the
post-training build.

### Instruction placement

Sources: [`documents-results/placement/`](documents-results/placement/) and
[`documents-results/qat-1120/placement/`](documents-results/qat-1120/placement/),
`TestPlacementReport`'s tables. The build and the budget changed together.

| Set | Placement | CER, post-training 512 | CER, QAT 1,120 | Run s mean, post-training | Run s mean, QAT 1,120 |
|---|---|---|---|---|---|
| clean | beside | 0.0000 | 0.0000 | 12.3 | 15.3 |
| clean | instructions | 0.0000 | 0.0000 | 12.6 | 15.8 |
| degraded | beside | 0.0000 | 0.0000 | 17.5 | 21.8 |
| degraded | instructions | 0.0073 (0.0000..0.0110) | 0.0000 | 17.7 | 22.6 |
| scanned | beside | 0.0864 | 0.1332 (0.1317..0.1339) | 7.1 | 10.8 |
| scanned | instructions | 0.1170 (0.1091..0.1328) | 0.1317 (0.1296..0.1339) | 7.4 | 11.0 |

**On the QAT build at 1,120 the placements tie.** Both read the degraded set exactly, and on the
scans their spreads overlap: page 7 scores 0.9129 beside the image and 0.9009 in `Instructions`,
page 5 0.2095 either way. Beside the image still costs 5 input tokens fewer (976 against 981) and
ran a little faster.

### The staged flow

Sources: [`documents-results/flow/`](documents-results/flow/) and
[`documents-results/qat-1120/flow/`](documents-results/qat-1120/flow/), `TestFlowReport`'s
tables. The build and the budget changed together.

| Set | Variant | CER, post-training 512 | CER, QAT 1,120 | Calls/page | Run s mean, post-training | Run s mean, QAT 1,120 |
|---|---|---|---|---|---|---|
| clean | flow | 0.1711 | 0.1711 | 0.00 | 0.1 | 0.1 |
| clean | none | 0.0000 | 0.0000 | 1.00 | 12.3 | 15.5 |
| clean | all-five | 0.0000 | 0.0000 | 1.00 | 15.6 | 23.1 |
| degraded | flow | 0.0005 (0.0000..0.0016) | 0.0000 | 1.67, then 1.89 | 38.9 | 53.9 |
| degraded | none | 0.0000 | 0.0000 | 1.00 | 17.6 | 22.1 |
| degraded | all-five | 0.0000 | 0.0000 | 1.00 | 23.0 | 34.4 |
| scanned | flow | 0.0965 (0.0886..0.1123) | 0.1109 (0.0929..0.1199) | 1.33, then 1.29 | 16.5 | 19.5 |
| scanned | none | 0.0864 | 0.1339 | 1.00 | 7.2 | 11.0 |
| scanned | all-five | 0.0677 (0.0670..0.0691) | 0.1001 (0.0745..0.1188) | 1.00 | 10.9 | 19.7 |

The text layers are the same on both, as they don't involve the model. No first pass failed to
parse on either, and no correction name was unknown.

| First pass | Post-training 512 | QAT 1,120 |
|---|---|---|
| Degraded judgements | 12 good, 15 poor | 9 good, 18 poor |
| Degraded pages flagged | 18 of 27 | 24 of 27 |
| Degraded corrections chosen | Deskew on page 2; ContrastStretch on 4, 6, 9; Despeckle on 5, 6, 9; Unsharp on 8 and once 5; never Grayscale | Deskew on 1 and 2; ContrastStretch on 4, 5, 6, 9 and once 8; Despeckle on 5, 6, 9; Unsharp on 4, 7, 8, 9; never Grayscale |
| Scanned judgements | 21 poor | 18 poor, page 3 good on every run |
| Scanned pages flagged | 7 of 21: ContrastStretch 5 times, Unsharp twice | 6 of 21: ContrastStretch on pages 5 and 6, every run |
| Second pass against first | degraded: 18 the same; scanned: 1 helped (page 7), 2 hurt (page 5's seal lost), 4 the same | degraded: 24 the same; scanned: 6 the same |

The QAT build's choices fit the faults more closely: it caught page 1's moderate skew and page 7's
moderate blur, which the post-training build had judged good, and missed only page 3's moderate
fade. Its reasons on the scans are still the redactions. Its second passes changed nothing, so
they neither helped nor hurt. On the scans the flow's spread now clears `none`'s, and the reason is
the first pass, not a correction: page 7 was never flagged, and the first pass's request, with its
schema and figures, read page 7 at 0.7267 (0.5766..0.8018) where `none` read 0.9189 on every run.
`all-five` has the best scanned mean, 0.1001, but its spread overlaps the flow's.

**Regions:** each band request is still 482 input tokens (426 image tokens, under both caps). The
bands read as before, page 2's blank footer again drawing a refusal. Page 7's footer band read
"SPERET NORDEN" on run 1 and "SPEREDT NORDEN" on runs 2 and 3; its header band read "SECRET
HEADQUARTERS NT OF DEF DEPARTMENT OF DEFENSE 50th". Page 5's header band read only
"SECRET//NOFORN//XI".

### Tiles

Sources: [`documents-results/tiles/`](documents-results/tiles/) and
[`documents-results/qat-1120/tiles/`](documents-results/qat-1120/tiles/). Image tokens, then the
reads of three runs; one read means all three agreed.

| Tile | DPI | Post-training 512 | QAT 1,120 |
|---|---|---|---|
| 7, foot marking | 300 | 485: a refusal | 530: "SECRET / NOTED" on runs 1 and 3, "SECRET / CONFIDENTIAL" on run 2 |
| 7, foot marking | 200 | 242: "SECRET" | 242: a refusal on runs 1 and 2, "SECRET" on run 3 |
| 7, NOFORN alone | 300 | 122: "NOURAN", "NOORDEN", "NOORDEN" | 122: "NOORDEN", "NOORDEN", "NOERDN" |
| 7, NOFORN alone | 200 | 82: "NOKEDOUI" | 82: "NO REASON", "NOKEDOU", "NOKEDOU" |
| 7, footer band | 300 | 426: "SPERET NORDEN" | 426: "SPERET / NORDEN" |
| 7, footer band | 200 | 212: "SPERET MÅLFORSLAG", "SPERET MÅLFORLØB", "SPERET MÅLFORSLAG" | 212: "SPERRET / MÅLFORLØB", "SPERRET / MÅLFORSLAG", "SPERRET / MÅLFORLØB" |
| 5, seal | 300 | 83: "DEPARTMENT OF DEFENSE UNITED STATES OF AMERICA" on runs 1 and 3, "DEPARTMENT OF DEFENSE" on run 2 | 83: "DEPARTMENT OF DEFENSE" |
| 5, seal | 200 | 83: "DEPARTMENT OF DEFENSE UNITED STATES OF AMERICA" | 83: "DEPARTMENT OF DEFENSE / UNITED STATES OF AMERICA" |
| 5, control | 300 and 200 | 101 and 82: "WASHINGTON, D.C. 20340" | the same |

The 300-dpi foot tile is the only one the 512 cap clipped (485 tokens then, 530 now); every other
tile changed only the build. The conclusion stands: a small tile on page 5's seal reads it on
every run, and nothing reads page 7's NOFORN.

### The scanned pages on the QAT build at 1,120

- **Page 7's NOFORN is never read.** 0 of 15 renderer reads, 0 of 42 in the matrix, 0 of 6 in
  placement, 0 of 9 full-page reads in the flow, 0 of 3 footer bands, and 0 of 18 tiles of the
  page's foot. As before, the model reads the foot's "SECRET" and not the word beside it.
- **Page 7's seal is read more fully, and invented on.** With 920 image tokens the model reads
  more of the seal's lines ("NT OF DEFENSE", "REPLY TO / ATTENTION OF:") and adds what isn't on
  the page in all 15 renderer reads: "UNITED STATES OF AMERICA", "COMMISSION" or "COMMEMORATION",
  a span of years ("1917-1967" with gs and magick, "1941-1991" with vips, pdftocairo, and once
  pdftoppm), and a stamped date ("MAR 22 1968", "MAR 22 1992", "MAR 28 1992", "MAR 28 1993"). At
  512 the same build added "UNITED STATES OF AMERICA" with three renderers and a date with one;
  the post-training build's reasoning-off renderer run added no "UNITED STATES OF AMERICA" and a
  date twice, both with pdftoppm. The flow's first pass and `all-five` invent the same way.
- **Page 5's seal isn't read from the whole page.** Its "DEPARTMENT OF DEFENSE" appears in none
  of the full-page reads at 1,120: renderers 0 of 15, the matrix 0 of 42 (Deskew read it on every
  run before), placement 0 of 6, the flow 0 of 9 (`all-five` read it on every run before). A tile
  on the seal reads it on all 6 runs. At 512 the QAT build read it with pdftoppm alone, 3 of 3.
- **No loops.** No transcription in any of the five runs was incomplete: 0 of 330 renderer reads,
  0 of 672 in the matrix, 0 of 126 in placement, none in the flow or the tiles. The longest page 7
  answer was 102 output tokens. At 512 the QAT build looped on page 7 twice (magick, 8192 tokens,
  173 and 180 s), and the post-training build 4 times in its matrix.

### 150 dpi and 200 dpi on the scans

None of these runs sent a whole page at 200 dpi, so they can't say whether 1,104 image tokens read
the scans better than 920. What they show is near it. The tiles are regions: at 300 against 200
dpi, page 5's seal reads exactly at 300 and adds "UNITED STATES OF AMERICA" at 200, and neither DPI
reads page 7's NOFORN. And on whole pages, raising the image tokens at 150 dpi from 477 to 920 on
the same build made the scanned set worse for every renderer, all on page 7, by reading and
inventing more of its seal. The 150-dpi default's recorded reason, that 150 dpi costs no more
context than any higher DPI, no longer holds: 200 dpi now costs 1,104 tokens against 920. This page
doesn't re-decide the default.

## The renderer bake-off

### Method

Five renderers each turned every fixture page into a PNG at 150 dpi. Each PNG went to Gemma 4
26B-A4B with `TranscribeInstructions` (now the test code's `transcribeInstructions`), and the
transcription was scored against the page's ground truth. The bake-off is its own test tier,
`mise run bakeoff` (`-tags bakeoff`; `document/bakeoff_test.go` and
`document/renderers_bakeoff_test.go`). It isn't CI.

- **Renderers:** `pdftoppm` and `pdftocairo` (poppler 26.10.0), `gs` (Ghostscript 10.08.0),
  `vips` (libvips 8.18.7, through poppler-glib), and `magick` (ImageMagick 7.1.2-34, which renders
  PDF through `gs`). Each runs as a program on the laptop, one process per page.
- **Fixtures** (`document/testdata/README.md`):
  - *clean*: six letter pages generated with typst from `clean.typ`: prose, two columns, a table,
    8 pt print, a filled-in form, and a figure. Ground truth comes from the source.
  - *degraded*: nine pages made from the clean ones with ImageMagick: skew, fade, speckle, and
    blur, each at a moderate and a severe level, and a last page with all four.
  - *scanned*: seven pages of real scans, copied byte for byte from herald's
    `_project/marked-documents/marked-documents.pdf` at herald commit `bdacbb6`: a Lexmark CX725
    at 300 dpi, image only, redacted marked documents. Their ground truth was transcribed by
    reading each page and confirmed by the architect.
- **Runs:** greedy decoding (temperature 0, sent through the request's native fields), 3 runs per
  page, 4 requests in flight, an 8192-token output cap, and reasoning off (the next section says
  why).
- **Scores:** the character error rate (CER) is the edit distance from the truth to the
  transcription over the truth's length, both with whitespace collapsed. The word error rate (WER)
  sits beside it. Both are scored over the transcriptions that completed. A transcription that
  failed, wasn't `completed`, or came back empty counts as **incomplete**, a criterion of its own.
  A difference inside the run-to-run spread (the range of the three runs) is a tie.
- **Ranking order:** CER and incomplete transcriptions over the clean and degraded sets, then
  render time and peak memory over every set, then streaming, license, PNG size, and the number
  of programs. Each pair of renderers is decided by the first criterion that separates them, and
  a renderer ranks by how many others it beats. The scanned set is reported apart and not ranked
  (ruling Q): its pages are an embedded scan every renderer decodes the same, so its differences
  are the model's sensitivity to tiny pixel differences, not rendering quality.

The bake-off ran as 15 `go test` runs, one per renderer and set, each capped at 10 minutes. They
took 26 to 65 s each, about 11 minutes in all (the log's `bakeoff: <renderer> <set> took` lines).
An earlier attempt with one run per renderer had overrun 10 minutes.

### Results

Source: the reasoning-off run's JSON, 15 files in
[`documents-results/renderers/`](documents-results/renderers/), ranked again for this page by
`TestBakeoffRanking` with ruling Q's method. Its per-set tables match the run's own log line for
line.

The clean and degraded sets scored a CER of 0 for every renderer on every run, with no incomplete
transcription, so accuracy tied every pair. The scanned set, reported apart and not ranked:

| Renderer | Scanned CER (min..max) | Scanned WER | Incomplete |
|---|---|---|---|
| gs | 0.0803 (0.0702..0.0864) | 0.1250 | 0/21 |
| pdftoppm | 0.0925 (0.0918..0.0929) | 0.1429 | 0/21 |
| magick | 0.0994 (0.0842..0.1112) | 0.1488 | 0/21 |
| pdftocairo | 0.1091 (0.1091..0.1091) | 0.1607 | 0/21 |
| vips | 0.1242 (0.1091..0.1544) | 0.1786 | 0/21 |

The ranking, CER and incomplete over the clean and degraded sets, render time and memory over
every set:

| Rank | Renderer | Wins | CER (min..max) | Incomplete | Render s/page | Peak MiB | PNG KiB/page | License | Programs | Installed MiB |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | **gs** | 4 | 0.0000 (0.0000..0.0000) | 0/45 | 0.126 | 27.0 | 364 | AGPL | gs | 41.08 |
| 2 | vips | 3 | 0.0000 (0.0000..0.0000) | 0/45 | 0.133 | 90.1 | 348 | GPL (its PDF loader links poppler) | vips, vipsheader | 16.09 |
| 3 | magick | 2 | 0.0000 (0.0000..0.0000) | 0/45 | 0.338 | 102.7 | 322 | AGPL (through gs) | magick, gs | 63.12 |
| 4 | pdftocairo | 1 | 0.0000 (0.0000..0.0000) | 0/45 | 0.415 | 58.2 | 354 | GPL (poppler) | pdftocairo, pdfinfo | 6.81 |
| 5 | pdftoppm | 0 | 0.0000 (0.0000..0.0000) | 0/45 | 0.457 | 30.5 | 439 | GPL (poppler) | pdftoppm, pdfinfo | 6.81 |

Render time decided every pair: gs beat each of the others on it. Render time and peak memory
are per page, and their spreads over the three runs were narrow: gs took 0.125 to 0.127 s and
peaked at 27.0 MiB on every run. Installed size is `pacman -Qi` on the laptop for the packages
each renderer needs. Every renderer could also stream, reading the PDF on stdin and writing the
page's PNG to stdout.

> **The earlier ranking.** This page first ranked the run with CER and incomplete over all three
> sets, each run's CER the mean of the three sets' rates. Since the clean and degraded sets tied,
> the scanned set decided accuracy: gs 4 wins (CER 0.0268), pdftoppm 2 (0.0308), magick 2
> (0.0331), vips 2 (0.0414), pdftocairo 0 (0.0364); gs beat pdftoppm, pdftocairo, and vips on CER,
> and magick on time. Ruling Q took the scanned set out of the ranking: every renderer decodes
> the same embedded scan, and on Google's QAT build at 512 image tokens the old method ranked
> vips first and gs fourth on scanned page 7 alone. gs stays the released renderer.

### The winner: gs

gs is the released renderer, `document.Ghostscript`. It tied on the clean and degraded sets, had
the lowest scanned CER in this run, was the fastest and the lightest in memory, and needs a single
program. Its cost is the license: Ghostscript is
AGPL-3.0-or-later, so the package runs `gs` as a separate process through the tool contract and
never links it. vips came close on speed but used more than three times the memory, and is GPL
through its poppler PDF loader. The bake-off's gs runs the released code, so a rerun ranks what
ships. On Google's QAT build, at 512 and at 1,120 image tokens, it ranked first the same way, by
time, though its scanned CER was no longer the lowest (the "Google's QAT Q4_0" section).

## What reasoning did

The first bake-off ran with the model's reasoning on, the server's default. Source: that run's
scanned-set JSON (7 pages per renderer), in
[`documents-results/renderers-reasoning-on/`](documents-results/renderers-reasoning-on/), and its
ranking log.

- **Empty transcriptions.** 8 of the 105 scanned transcriptions (5 renderers, 7 pages, 3 runs)
  reasoned until the 8192-token cap and returned no text: page 7 six times and page 5 twice,
  across pdftocairo (3), vips (3), and pdftoppm (2). Each took 197 to 253 s.
- **Invented text.** Where it did answer, the model wrote seal text the page doesn't show. On
  page 7 it added "UNITED STATES OF AMERICA", "1941-1991 COMBAT OPERATIONS", "MEMORANDUM", and once
  "NATIONAL SECURITY". On page 5 it read "UNITED STATES OF AMERICA" into a part of the seal too
  blurred to read.

Turning reasoning off, with the request's reasoning effort `none`, which llama.cpp b11669 honors
for Gemma 4, ended the empty transcriptions. On scanned page 7 an answer took 56 tokens and about
4 s instead of 8192 tokens and 152 s, and every transcription in the rerun completed.

Accuracy improved too, though the two runs' CERs need care. A CER is scored over the completed
transcriptions, so a renderer whose hardest pages came back empty looks better than it is. gs had
no incomplete pages in either run, so its numbers compare directly: its scanned CER fell from
0.1533 with reasoning on to 0.0803 with it off, and its CER over all three sets from 0.0511 to
0.0268.

## Evidence 11: do image corrections help?

### Method

The corrections matrix runs the degraded set (9 pages) and the scanned set (7 pages) through
`Vision.Pages` (now the vision harness's `pages`, over `Images`), with the released renderer and the
corrections stage on the laptop's ImageMagick 7.1.2-34. It has seven cells: no correction (`none`);
each of the five alone (Deskew `-deskew 40%`, Despeckle `-despeckle`, ContrastStretch
`-contrast-stretch 0.5%x0.5%`, Unsharp `-unsharp 0x1`, Grayscale `-colorspace Gray`); and all five
together (`all`). Each cell had 3 greedy runs, reasoning off, the 8192-token cap, 4 pages in flight,
and no page retries, scored as the bake-off scores.

The whole matrix ran at 150 dpi and again at 72 dpi, since every synthetic fault scored 0 at 150 dpi
in the bake-off. It is `mise run bakeoff-corrections` (`document/corrections_bakeoff_test.go`): 28
`go test` runs, one per DPI, set, and cell, 13 to 183 s each and 25 minutes in all, then
`TestCorrectionsMatrix` over their JSON, in
[`documents-results/corrections/`](documents-results/corrections/). The tables below are its
recorded lines.

One difference from the bake-off: `Vision` sends the page's image in a user message of its own,
so `TranscribeInstructions` goes in the request's instructions rather than beside the image. The
`none` cells therefore don't repeat the bake-off's gs numbers (scanned CER 0.1098 here against
0.0803 there), and the matrix compares its cells only with its own `none`.

"vs none" compares CER, and is a tie when the two cells' spreads over the three runs overlap.

### At 150 dpi

| Cell | Degraded CER (min..max) | Degraded WER (min..max) | Inc. | vs none | Scanned CER (min..max) | Scanned WER (min..max) | Inc. | vs none |
|---|---|---|---|---|---|---|---|---|
| none | 0.0037 (0.0000..0.0110) | 0.0098 (0.0000..0.0294) | 0/27 | | 0.1098 (0.1091..0.1112) | 0.1637 (0.1607..0.1696) | 0/21 | |
| Deskew | 0.0000 | 0.0000 | 0/27 | tie | 0.1199 | 0.1964 | 0/21 | hurts |
| Despeckle | 0.0110 | 0.0294 | 0/27 | tie | 0.0907 | 0.1161 | 0/21 | helps |
| ContrastStretch | 0.0000 | 0.0000 | 0/27 | tie | 0.1091 | 0.1607 | 0/21 | tie |
| Unsharp | 0.0000 | 0.0000 | 0/27 | tie | 0.1325 (0.1307..0.1361) | 0.1548 (0.1518..0.1607) | 0/21 | hurts |
| Grayscale | 0.0110 | 0.0294 | 0/27 | tie | 0.0961 (0.0270..0.1490) | 0.1296 (0.0316..0.1875) | 1/21 | tie |
| all | 0.0000 | 0.0000 | 0/27 | tie | 0.1094 (0.1080..0.1123) | 0.1101 (0.1071..0.1161) | 0/21 | tie |

A rate with no range was the same on all three runs. WER mostly moves with CER, but not in two
scanned cells: Unsharp's CER is worse than none's while its WER is better, and `all` ties none on
CER while its WER, 0.1101 against 0.1637, is the lowest of any cell.

On the degraded set, the only page that ever scores above 0 is page 2, the severely skewed table,
and its errors are formatting, not reading: the model separated the table's cells with `|`
characters, against its instructions, while every word was right. That happened on one run of
`none` and on every run of Despeckle and Grayscale. At 150 dpi the synthetic faults don't trouble
the model, so there's nothing for a correction to fix: **a null result.**

On the scanned set, the cells differ only on pages 5 and 7, the two pages with seals. Pages 1 to
4 and 6 score 0 in every cell. Each page's mean CER:

| Scanned page | none | Deskew | Despeckle | ContrastStretch | Unsharp | Grayscale | all |
|---|---|---|---|---|---|---|---|
| 5 | 0.2095 | **0.0000** | 0.2095 | 0.2095 | 0.3683 | 0.2698 | 0.3683 |
| 7 | 0.7177 | 1.0000 | **0.5586** | 0.7117 | 0.7568 | 0.8063 (1 inc.) | 0.5646 |

Deskew is the only cell that reads page 5's seal ("DEPARTMENT OF DEFENSE"), and it reads it on
every run. On page 7 the same correction lost the page's text: its transcription begins mid-word
("NT OF DEFENSE") and fills the page with invented seal text ("1947-1997", "NATIONAL SECURITY
AGENCY" twice), scoring 1.0. Despeckle and
`all` help page 7, mostly by repeating the seal's text less. Grayscale's incomplete run is page 7
looping on "50th ANNIVERSARY / WORLD WAR II" to the 8192-token cap.

### At 72 dpi

| Cell | Degraded CER (min..max) | Degraded WER (min..max) | Inc. | vs none | Scanned CER (min..max) | Scanned WER (min..max) | Inc. | vs none |
|---|---|---|---|---|---|---|---|---|
| none | 0.0708 (0.0038..0.1050) | 0.1049 (0.0102..0.1544) | 1/27 | | 0.1022 (0.0994..0.1080) | 0.1637 (0.1607..0.1696) | 0/21 | |
| Deskew | 0.1974 (0.1807..0.2058) | 0.2535 (0.2269..0.2668) | 0/27 | hurts | 0.0922 (0.0842..0.0961) | 0.1458 (0.1429..0.1518) | 0/21 | helps |
| Despeckle | 0.2012 (0.1840..0.2231) | 0.2479 (0.2227..0.2731) | 0/27 | hurts | 0.1058 (0.1048..0.1080) | 0.1696 | 0/21 | tie |
| ContrastStretch | 0.0589 (0.0036..0.0938) | 0.0821 (0.0089..0.1282) | 1/27 | tie | 0.0994 | 0.1607 | 0/21 | tie |
| Unsharp | 0.0205 (0.0167..0.0267) | 0.0319 (0.0273..0.0389) | 0/27 | tie | 0.1080 | 0.1607 | 0/21 | tie |
| Grayscale | 0.0564 (0.0038..0.0848) | 0.0836 (0.0102..0.1261) | 1/27 | tie | 0.1008 (0.0994..0.1015) | 0.1607 | 0/21 | tie |
| all | 0.1669 (0.1171..0.2231) | 0.2300 (0.1607..0.2983) | 0/27 | hurts | 0.1120 (0.1112..0.1134) | 0.1696 | 0/21 | hurts |

At 72 dpi a letter page is 612 by 792 pixels, and the synthetic faults start to matter. Most of
the degraded set's error is on three pages:

| Degraded page | none | Deskew | Despeckle | ContrastStretch | Unsharp | Grayscale | all |
|---|---|---|---|---|---|---|---|
| 2 (severe skew) | 0.2883 | **0.0000** | 0.0045 | **0.0000** | 0.0054 | 0.0027 | 0.0505 |
| 6 (severe speckle) | 0.0300 | 0.0109 | 0.0272 | 0.0300 | 0.0272 | 0.0300 | **0.0027** |
| 9 (all four faults) | 0.3984 (1 inc.) | 1.1109 | 1.1261 | 0.4760 (1 inc.) | **0.1004** | 0.4526 (1 inc.) | 0.9163 |

- **Unsharp** is the one correction that helps the hardest page: page 9's CER falls from 0.40 to
  0.10, and the cell's mean from 0.0708 to 0.0205. Its spread still overlaps none's, because
  none's page 2 varies so much from run to run (0.0027 to 0.4324), so the cell counts as a tie.
- **Deskew and Despeckle** hurt page 9 badly. A CER above 1 means the transcription is longer
  than the truth: the model wrote plausible garden-soil prose that isn't on the page.
- **Deskew and ContrastStretch** fix page 2's skewed table, which `none` reads well on only some
  runs.
- All three incomplete transcriptions at 72 dpi are page 9 looping on one sentence to the cap,
  about 3 minutes each, which is why those three runs took 177 to 183 s.

On the scanned set at 72 dpi the differences are small. Deskew helps (0.0922 against 0.1022),
clearing none's spread on pages 1 and 6; all five together hurts (0.1120), on page 7.

Across the whole matrix, 4 of 672 transcriptions were incomplete, every one a page looping to the
cap.

### What this means

No correction is a safe default. Each one that helps somewhere hurts somewhere else, and the
effect flips with the DPI: Deskew is the best cell on the scans at 72 dpi and the worst at
150 dpi. At the default 150 dpi, the synthetic faults give corrections nothing to fix, and the real
scans' errors are the seals and page 7's faded foot, which no correction reliably fixes. The
package keeps the corrections as per-request options with none applied by default, which these
results support.

## Instruction placement (ruling L)

### Method

Where the transcription instructions go is the inference layer's choice, not processing's: the
prepared image is the same either way. The renderer bake-off put `transcribeInstructions` in the
same user message as the image, a text part before it; the vision harness, and so the corrections
matrix and the integration tier, puts it in the request's `Instructions`, the image alone in a
user message. This comparison measures the two placements directly.

It is `mise run bakeoff-placement` (`document/placement_bakeoff_test.go`): the clean, degraded,
and scanned sets through the vision harness's `pages`, with the released renderer at 150 dpi, no
correction, Gemma 4 26B-A4B greedy (temperature 0 through the native fields), reasoning effort
`none`, the 8192-token cap, 4 pages in flight, no page retries, 3 runs per set and placement. The
harness gained `visionRequest.Prompt`, a text part placed before the image, for the `beside`
placement. Run 2026-10-30 from the laptop against gb10-2: six `go test` runs, 21 to 53 s each,
225 s in all, then `TestPlacementReport` over their JSON, in
[`documents-results/placement/`](documents-results/placement/). The tables are its recorded lines.

### Results

| Set | Placement | CER (min..max) | WER (min..max) | Incomplete | Run s (mean) |
|---|---|---|---|---|---|
| clean | beside | 0.0000 | 0.0000 | 0/18 | 12.3 |
| clean | instructions | 0.0000 | 0.0000 | 0/18 | 12.6 |
| degraded | beside | 0.0000 | 0.0000 | 0/27 | 17.5 |
| degraded | instructions | 0.0073 (0.0000..0.0110) | 0.0196 (0.0000..0.0294) | 0/27 | 17.7 |
| scanned | beside | 0.0864 (0.0864..0.0864) | 0.1339 (0.1339..0.1339) | 0/21 | 7.1 |
| scanned | instructions | 0.1170 (0.1091..0.1328) | 0.1667 (0.1607..0.1786) | 0/21 | 7.4 |

The differences sit on two pages. Degraded page 2, the severely skewed table, scores 0 on every
`beside` run and 0.1009 (0.0000..0.1514) with the instructions in `Instructions`, where it is
again the table's cells separated by `|`, against the instructions, as in the matrix. Scanned
page 7 scores 0.5225 on every `beside` run against 0.7778 (0.7117..0.9099): beside the image,
the model read the seal's lines once; with the instructions in `Instructions`, it repeated "50th
ANNIVERSARY WORLD WAR II / DEPARTMENT OF DEFENSE". Scanned page 5 scores 0.2095 either way, and
every other page 0.

**Beside the image is the better placement for Gemma 4 26B-A4B.** On the scanned set its spread
clears the other's, and it gave the same transcription on all three runs of every set, where the
instructions in `Instructions` varied on degraded page 2 and scanned page 7. Its scanned CER,
0.0864, is close to the renderer bake-off's gs (0.0803, the same placement). The staged flow
below uses it.

## The staged two-pass flow (ruling N)

### What it is

This is the flow the inference layer will follow, staged in test code as its proof of concept,
not a released API: processing prepares, and the layer above decides what to send. It uses the
preparation ruling N added: text layers (`ImagesRequest.SkipText`, `Ghostscript.Text`), `Assess`,
the correction catalog (`Corrections`, `Description`, `ParseCorrection`), and regions
(`ImagesRequest.Region`, `Ghostscript.RenderRegion`).

1. **Text layer first.** `Images` with `SkipText` reads each page's text layer with gs's
   `txtwrite`; a page with one (`HasText`) is never rendered or sent, and is scored from its text.
2. **First pass.** A page without one is rendered and measured (`Corrector.Assess`). One request
   sends the image with the first pass's instructions and the page's figures beside it, under a
   strict JSON schema built from the catalog: the transcription, a quality judgement (`good` or
   `poor`) with a one-sentence reason, and the corrections to apply, each one of the catalog's
   names. The instructions list each correction with its `Description`; the figures give each
   measure with its scale. Names are read with `ParseCorrection`; unknown ones are counted and
   dropped.
3. **Second pass, flagged pages only.** A page whose first pass named corrections is corrected
   as asked (`Corrector.Correct`) and sent again with the plain transcription instructions. Its
   second pass's text is scored; every other page keeps its first pass's.
4. **Regions, scanned set only.** Each page's header and footer bands, the top and bottom 12%,
   rendered by `RenderRegion` at 300 dpi and transcribed with the plain instructions. The ground
   truth is per page, so the bands are reported beside the full-page reads, not scored.

Two baselines ran beside it in the same task: `none`, one vision pass with no correction, and
`all-five`, one vision pass with every correction. Every request put its instructions beside the
image, the placement the previous section found better, so `none` repeats that section's
`beside` (it did, exactly, on the scanned set: 0.0864).

### Method

`mise run bakeoff-flow` (`document/flow_bakeoff_test.go`, the first pass's request and reading in
`document/flow_test.go`): Gemma 4 26B-A4B greedy (temperature 0), reasoning effort `none`, the
8192-token cap, gs at 150 dpi, the laptop's ImageMagick 7.1.2-34, 4 pages in flight, no page
retries, 3 runs per set and variant. Run 2026-10-30 from the laptop against gb10-2: 30 `go test`
runs, one per set, variant, and run, 0 to 39 s each and 455 s in all, then `TestFlowReport`. The
tables are its recorded lines. Its JSON, in [`documents-results/flow/`](documents-results/flow/),
keeps every transcription, judgement, and assessment.

Calls per page counts vision requests: a text-layer page 0, a flow page 1 or 2, a baseline page
1. Run time is a run's wall time, from preparation to the last response, Assess and the
corrections included.

### Results

| Set | Variant | CER (min..max) | WER (min..max) | Incomplete | Calls/page | Run s mean (min..max) |
|---|---|---|---|---|---|---|
| clean | flow | 0.1711 | 0.2065 | 0/18 | 0.00 | 0.1 |
| clean | none | 0.0000 | 0.0000 | 0/18 | 1.00 | 12.3 (10.7..15.3) |
| clean | all-five | 0.0000 | 0.0000 | 0/18 | 1.00 | 15.6 (15.5..15.7) |
| degraded | flow | 0.0005 (0.0000..0.0016) | 0.0007 (0.0000..0.0021) | 0/27 | 1.67 | 38.9 (38.1..39.4) |
| degraded | none | 0.0000 | 0.0000 | 0/27 | 1.00 | 17.6 (15.3..22.0) |
| degraded | all-five | 0.0000 | 0.0000 | 0/27 | 1.00 | 23.0 |
| scanned | flow | 0.0965 (0.0886..0.1123) | 0.1458 (0.1429..0.1518) | 0/21 | 1.33 | 16.5 (14.3..18.1) |
| scanned | none | 0.0864 | 0.1339 | 0/21 | 1.00 | 7.2 (5.5..10.6) |
| scanned | all-five | 0.0677 (0.0670..0.0691) | 0.0923 (0.0893..0.0982) | 0/21 | 1.00 | 10.9 (10.8..10.9) |

No request was incomplete, and no first pass failed to parse: the strict schema held on all 48.

**Text layers.** Every clean page has one, so the flow sent the clean set no model call and read
it in 0.1 s a run, against 12 to 16 s. The degraded and scanned pages have none. The text
layers' CER, the same on every run:

| Clean page | Text layer CER | Why |
|---|---|---|
| 1, prose | 0.0023 | one ligature: "office" reads "ofÏce" |
| 2, two columns | 0.6652 | `txtwrite` keeps the layout, so each line reads across both columns; every word is present |
| 3, a table | 0.0000 | |
| 4, 8 pt print | 0.0000 | |
| 5, a form | 0.3755 | each field's value sits on the line above its label |
| 6, a figure | 0.0000 | |

A text layer is free and exact in its words, but its reading order is the PDF's layout. The
inference layer needs to restore reading order, or send pages with columns and forms to vision.

**What the model asked for.** On the degraded set it judged 12 first passes good and 15 poor,
and asked for corrections on 18 of 27, each fitting the page's fault: Deskew on page 2, the severe
skew, on every run; ContrastStretch on page 4, the severe fade (with Unsharp), and on pages 6 and
9; Despeckle on pages 5, 6, and 9, the speckled ones; Unsharp on page 8, the severe blur, and once
on page 5. It never asked for Grayscale. It missed page 1's moderate skew (Assess measured
-2.49 degrees) and the moderate fade and blur of pages 3 and 7, judging them good. On the scanned
set it judged all 21 first passes poor, every reason being the redactions ("Large portions of the
text are redacted with black bars"), which no correction fixes, and asked for corrections on 7:
ContrastStretch 5 times (page 6 on runs 1 and 2, page 2 on run 2, page 5 on runs 2 and 3) and
Unsharp twice (page 7 on run 1, page 6 on run 3). No name was unknown: the schema's enum held the
model to the catalog, so ParseCorrection never dropped one.

**Second pass against first.** On the degraded set all 18 second passes scored 0, as their first
passes had: the same. On the scanned set:

| Page, run | Corrections | First pass CER | Second pass CER | |
|---|---|---|---|---|
| 7, run 1 | Unsharp | 0.7928 | 0.7387 | helped |
| 5, run 2 | ContrastStretch | 0.0000 | 0.2095 | hurt |
| 5, run 3 | ContrastStretch | 0.0000 | 0.2095 | hurt |
| 2, run 2 | ContrastStretch | 0.0000 | 0.0000 | same |
| 6, runs 1 and 2 | ContrastStretch | 0.0000 | 0.0000 | same |
| 6, run 3 | Unsharp | 0.0000 | 0.0000 | same |

Page 5's hurt is the seal: the first pass read its "DEPARTMENT OF DEFENSE" on runs 2 and 3, and
the plain second pass dropped it, as `none` does on every run. Had the flow kept every first pass,
its scanned CER would be 0.0828 (0.0648..0.1188), from the same JSON's first-pass edits, against
the flow's 0.0965. The first pass also invented on page 7: on run 1 it added "UNITED STATES OF
AMERICA", "1941-1991", and "CONFIDENTIAL".

Per scanned page, mean CER over the runs; pages 1 to 4 and 6 scored 0 in every variant:

| Scanned page | flow | none | all-five |
|---|---|---|---|
| 5 | 0.2095 | 0.2095 | 0.0063 (0.0000..0.0190) |
| 7 | 0.6066 (0.5405..0.7387) | 0.5225 | 0.5586 |

`all-five` read page 5's seal on every run (the 0.0190 run wrote "m.m." for "M.M."), which is why
it scored best on the scans. In the corrections matrix, with the instructions in the request's
`Instructions`, the same cell only tied `none`: the placement changes what a correction does.

**Regions.** Each band request was 482 input tokens against a full page's 533 with the same
instructions, so a 2550 by 396 band costs fewer image tokens than the page, not more. Every band
read the same on all three runs, except page 2's footer, which varied in wording:

| Scanned page | Header band read | Footer band read |
|---|---|---|
| 1 | "Blue files for Staffing - Post Office Box Page 1 of 1 Home Help New Collab Site (Evaluate)" | "http://hqportal.hq.jiant.net/sites/J3/J3X/J3XTeamSite/J3XCollabSite/Lists/Blue%20files... 11/30/2007" |
| 2 | "SECRET//NOFORN//20280901 EXPEDITE" | a refusal: "I'm sorry, but I cannot fulfill this request. The image provided is a blank white rectangle and contains no text to transcribe." |
| 3 | "McDowell" | "2" |
| 4 | "Message Page 1 of 2 Blickensderfer," | "12/9/2002" |
| 5 | "SECRET//NOFORN//XI" | "SECRET//NOFORN//XI" |
| 6 | "Hill, From: Sent:" | "1" |
| 7 | "NT OF DEFENT SECRET HEADQUARTERS DEPARTMENT OF DEFENSE 50th" | "SPERET NORDEN" |

- **Page 7's faded foot:** NOFORN does not appear. The footer band reads two words, "SPERET
  NORDEN", where the full-page reads give "SECRET" alone, so at 300 dpi the model sees a second
  word at the foot but misreads both. NOFORN appears in none of page 7's full-page reads either
  (0 of 3 in each of flow, none, and all-five).
- **Page 5's seal:** the header band reads only the banner, "SECRET//NOFORN//XI"; the seal's
  "DEPARTMENT OF DEFENSE" is not read from the band. It was read from the whole page by
  `all-five` on every run and by the flow's first pass on two.
- The bands read the other pages' headers and footers as the full page does: page 1's title and
  URL, page 4's date, pages 3 and 6's page numbers. Page 2's blank footer drew a refusal instead
  of an empty answer.

**Tiles (ruling O).** `mise run bakeoff-tiles` sent near-square tiles, and page 7's footer band for
comparison, through `RenderRegion` three times each with the plain instructions beside the image,
greedy and with reasoning off, on 2026-10-30. Its JSON is in
[`documents-results/tiles/`](documents-results/tiles/). Image tokens are each request's input tokens
less the 56 of the same text without the image. All three runs of a tile cost the same, and all
three read the same unless the cell says otherwise:

| Tile (page, region top..bottom × left..right) | 300 dpi: pixels, image tokens, read | 200 dpi: pixels, image tokens, read |
|---|---|---|
| 7, foot marking, 0.68..1 × 0.35..0.81 | 1173×1056, 485: a refusal, "I cannot fulfill this request. The image provided contains large blacked-out sections…" | 782×704, 242: "SECRET" |
| 7, NOFORN alone, 0.86..1 × 0.60..0.82 | 561×462, 122: "NOURAN", "NOORDEN", "NOORDEN" | 374×308, 82: "NOKEDOUI" |
| 7, footer band, 0.88..1 × 0..1 | 2550×396, 426: "SPERET NORDEN" | 1700×264, 212: "SPERET MÅLFORSLAG", "SPERET MÅLFORLØB", "SPERET MÅLFORSLAG" |
| 5, seal, 0.05..0.18 × 0.05..0.22 | 434×429, 83: "DEPARTMENT OF DEFENSE UNITED STATES OF AMERICA" on runs 1 and 3, "DEPARTMENT OF DEFENSE" on run 2 | 289×286, 83: "DEPARTMENT OF DEFENSE UNITED STATES OF AMERICA" |
| 5, control, 0.075..0.205 × 0.39..0.60 | 536×429, 101: "WASHINGTON, D.C. 20340" | 357×286, 82: "WASHINGTON, D.C. 20340" |

The tokens follow the image's area, not its shape: about 2,100 to 2,400 pixels a token for the bands
and the tiles alike, above a floor of 82 to 83 for the smallest tiles. The largest, the foot tile at
300 dpi, is 2,550 pixels a token at 485, near a whole page's 477 at 150 dpi (the 533 above, less the
56), where the count stops growing. A wide band is therefore not scaled down more than a square tile
is. What a tile changes is what is in it. Page 5's seal reads "DEPARTMENT OF DEFENSE" on all six
tile reads, where the header band never did, and the control reads exactly at both DPIs, at 82 to
101 image tokens a tile. Page 7's NOFORN still does not appear: NOFORN alone at 300 dpi reads as a
word of its shape, "NOORDEN", and the tile holding both words, with the redaction above them, drew a
refusal at 300 dpi and "SECRET" alone at 200. For the inference layer: a small tile on a known spot,
a seal or a printed line, reads text the page and its band miss, for about a fifth of a page's image
tokens; detail follows the DPI and the tile's area, not its shape. A marking as faded as page 7's
isn't recovered by tiling, so the layer can't rely on vision for it.

### What this means for the inference layer

- **Put the instructions beside the image.**
- **Use the text layer when there is one,** for its cost: no call and a tenth of a second for
  six pages. Restore its reading order first, or route two-column and form pages to vision.
- **The combined first pass works mechanically:** a strict schema from the catalog kept every
  answer parseable and every name valid, and the judgements and corrections matched the
  synthetic faults. But it cost 1.67 calls a page on the degraded set and 1.33 on the scans, and
  2.2 to 2.3 times the baseline's wall time, for no gain: the second pass helped once, on page 7,
  and hurt twice, on page 5.
- **Keep the first pass's text unless the second is clearly better.** Scoring the second pass in
  place of the first lost page 5's seal; the inference layer has no ground truth, so it can't
  tell, which argues for one pass.
- **For detail, tile a known spot rather than band the page:** image tokens follow a region's
  area, not its shape, so a wide band isn't scaled down, yet it read nothing a full page missed.
  A small tile on a seal or a printed line read what the page and its band missed, for about a
  fifth of a page's tokens. Nothing recovered page 7's faded NOFORN.

On the QAT build at 1,120 image tokens (the "Google's QAT Q4_0" section above), these held:

- **Use the text layer when there is one,** and restore its reading order: unchanged, as it
  involves no model.
- **One pass, not two:** the second passes changed nothing on either set, 30 of 30 the same, so
  they cost calls (1.89 a page on the degraded set, 1.29 on the scans) and 1.8 to 2.4 times the
  plain pass's wall time for no change.
- **A tile on a known spot reads what the page misses:** page 5's seal on every tile read and on
  none of the 72 whole-page reads that could have shown it. Nothing recovered page 7's NOFORN.
- **No correction is a safe default.**

And these changed:

- **Instruction placement no longer matters on the scans:** the two placements tie. Beside the
  image stays the choice, as it was better on the post-training build and costs 5 tokens fewer.
- **The first pass's request reads page 7 better than a plain one** (0.7267 against 0.9189), so
  the flow beat one plain pass on the scans; on post-training it didn't. The schema's request, not
  a correction, made the difference, which says the wording around the image moves page 7, a
  page the model can't read reliably either way.
- **A page costs about twice the context it did:** 976 input tokens at 150 dpi, against 533, and
  about 2 s more a request. Gemma's 32,768-token context still holds a page many times over, but
  the layer's batching and timeouts should budget for it.
- **More image tokens invent more on a seal.** Page 7's whole-page reads now add a span of years
  and a stamped date that aren't on the page, on every renderer. The layer should treat seal and
  stamp text from a whole-page read as unconfirmed.

## The scanned set's lessons

- **Page 7's faded NOFORN is never read.** The page's foot carries a faded "SECRET" and
  "NOFORN". In the reasoning-off bake-off (15 transcriptions of page 7) and the matrix (42), the
  model reads SECRET, often twice, and never NOFORN. No correction changed that. herald had read
  these markings inconsistently too, which is why the page joined the set.
- **Seal text is invented or repeated.** Page 7's seal reads "50th ANNIVERSARY / WORLD WAR II"
  under "DEPARTMENT OF DEFENSE", partly redacted. With reasoning off, the model repeats those lines
  two or three times on most runs, misreads the redacted word as "COMBAT COMBINATION", and twice
  added a date that isn't there, "MARCH 1997" and "MARCH 1995" (the reasoning-off bake-off's JSON,
  all renderers). On some runs it doesn't stop repeating: in the integration tier, with an
  8192-token cap, page 7 looped to the cap on about 1 run in 4, taking about 148 s (the decision
  that added `PageRetries`, in the goal record).
- **Page 5's seal is read only with help.** With no correction, the model skips the seal's
  "DEPARTMENT OF DEFENSE" on every run and reads the rest of the page, the handwritten initials
  included. Deskew at 150 dpi reads it on every run, as do all five corrections with the
  instructions beside the image and a tile on the seal; the flow's first pass read it on two runs
  of three.
- **The page loop and PageRetries.** A model that loops on a page runs to its output cap, which
  costs minutes and returns no usable text, but on one run and not the next. So the harness's
  `Request` has `PageRetries`: a page whose response isn't `completed` is sent again with the same
  image, up to that many times, and `Page.Attempts` says how many sends it took. The integration
  tier sets one retry and caps a page at 2048 output tokens, so a loop ends in seconds rather than
  minutes. The bake-off tier sets no retries, so its loops show as incomplete pages. A retry
  doesn't always end the loop: on 2 of 5 runs of the scanned Document test, page 7 was still
  incomplete after its retry (the next section).

## Pages and Document on gb10-2

The integration tier (`document/vision_integration_test.go`) runs the vision harness's `pages` and
`document`, over `Images`, against gb10-2: each page through Gemma 4 26B-A4B as the bake-off
transcribes it, 4 pages in flight, a 2048-token page cap, and one page retry, and Document's final
request to gpt-oss-120b at low reasoning effort for a digest under a JSON schema (summary, title,
kind, dates, markings, keywords), capped at 4096 output tokens.

Document **digitizes, then digests**. The page transcriptions are the document's content, which
`Result.Text` joins under "Page N of M:" headings. The final request derives a small digest for
search from them and never replaces them. It doesn't rewrite the document as one clean text: a
rewrite paraphrases and invents, and its output grows with the document.

The harness's figures vary from run to run: the wall times, and, with what the page model writes,
the scanned set's CER and the final request's input tokens. Runs 1 to 4 ran on 2026-10-29, before
ruling M moved the harness into test code; run 5 is the spec review's, on 2026-10-30, after it:

| Run | Pages, clean.pdf (6 pages) | Document, clean.pdf | Document, scanned.pdf (7 pages) |
|---|---|---|---|
| 1 | 8.06 s, CER 0 | 15.50 s, CER 0 | 11.88 s |
| 2 | 8.18 s, CER 0 | 16.11 s, CER 0 | 11.78 s |
| 3 | 8.43 s, CER 0 | 16.19 s, CER 0 | 11.70 s |
| 4 | 7.78 s, CER 0 | 15.87 s, CER 0 | 12.99 s, CER 0.0961 |
| 5 | 11.2 s, CER 0 | 18.1 s, CER 0 | 11.8 s, CER 0.1091 |

Runs 1 to 3 recorded no scanned CER; run 4 added it and the final step's input tokens to the
recorded lines. No page was retried in these five runs. In every run, `Result.Text` for clean.pdf
scored a CER of 0 against the joined ground truth with its headings stripped.

A retry doesn't always save a page. After ruling M, the scanned Document test ran 5 more times.
On 2 of them, page 7 looped on its seal's text to the 2048-token page cap on both of its
attempts, so Document failed before its final step. The test now records such a page, its
attempts and status, and skips the final step's assertions instead of failing: an inference
outcome only shows the preparation is usable. The clean set still asserts that every page
completes.

The digests from run 4, as gpt-oss-120b wrote them. For clean.pdf:

```json
{
  "summary": "The document is a multi‑section record that includes a harbor depth survey conducted in spring and finalized after winter 2026, gardening soil guidelines, quarterly rainfall tables for several stations, equipment rental terms of service, a library card application dated 3 June 2026, and a ten‑year seasonal water‑level chart for a reservoir. It records observed channel shifts, soil amendment recommendations, rainfall totals, rental policies, personal identification data, and reservoir level trends.",
  "title": "The Harbor Survey (and Supplemental Community Documents)",
  "kind": "Composite report / collection of community records",
  "dates": ["spring (year unspecified)", "summer (year unspecified)", "autumn (year unspecified)", "winter (year unspecified)", "3 June 2026", "12 March 1987"],
  "markings": [],
  "keywords": ["harbor survey", "channel depth", "dredging", "garden soil", "clay", "sand", "lime", "sulfur", "rainfall statistics", "quarterly rainfall", "terms of service", "equipment rental", "library card", "reservoir water levels", "seasonal water level chart"]
}
```

For scanned.pdf:

```json
{
  "summary": "A classified compilation of messages and administrative notes relating to a Department of Defense 50th‑anniversary World War II project. The material includes internal memos dated December 2002, a January 2004 heading, and a 2007 web‑link reference, all marked SECRET with a NOFORN caveat. The document appears to be a collection of correspondence and filing references for the anniversary program.",
  "title": "50th Anniversary World War II – Department of Defense Coordination Documents",
  "kind": "Compilation of internal memoranda and filing references (government correspondence)",
  "dates": ["January 2004", "12/9/2002", "December 16, 2002", "11/30/2007"],
  "markings": ["SECRET//NOFORN//20280901", "CLASSIFICATION: SECRET", "CAVEATS: NOFORN", "TERMS: NONE"],
  "keywords": ["Secret", "NoForn", "World War II", "50th Anniversary", "Department of Defense", "Memo", "Correspondence", "Staffing", "Blue files", "McDowell", "Blickensderfer", "Hill"]
}
```

The clean digest's dates are all on the pages; "12 March 1987" is the form's date of birth. The
scanned digest shows that a digest inherits the transcriptions' mistakes. It builds its title and
summary around the seal's "50th Anniversary World War II", which the page model over-read, and
calls seven unrelated pages one project. Its markings come from the other pages' banners, so it
does report NOFORN, though page 7's own NOFORN was never read.

## Tokens and limits

### Image tokens per page

Source: `TestVisionImageTokensHost`, integration runs 4 and 5, which recorded the same counts.
clean.pdf's page 1 was rendered by gs at each DPI and sent beside a short text in a request for one
output token. The image's tokens are the request's input tokens less those of the same request
without the image (18). The difference includes the few tokens that mark where an image begins and
ends.

| DPI | Pixels | PNG bytes | Image tokens |
|---|---|---|---|
| 72 | 612 × 792 | 21,017 | 223 |
| 100 | 850 × 1100 | 34,520 | 416 |
| 150 | 1275 × 1650 | 54,237 | 477 |
| 200 | 1700 × 2200 | 80,122 | 477 |

Gemma 4 26B-A4B's image budget saturated between 100 and 150 dpi: 150 and 200 dpi cost the same
477 tokens. That is why the default is 150 dpi: it cost no more context than any higher DPI. The
saturation was llama.cpp's 512-token cap on an image, which gb10-2's preset has since lifted to
the projector's 1,120: on the QAT build a page now costs 920 tokens at 150 dpi and 1,104 from 200
dpi up (the "Google's QAT Q4_0" section), so that reason no longer holds.
These match the plan's measurements (about 223, 416, and 477). A whole page request at 150 dpi,
image and instructions, is 538 input tokens (the matrix's records), against Gemma's 32,768-token
context.

### The final step against gpt-oss-120b's context

Source: the integration tier's `recordFinal`, runs 4 and 5, which reads a slot's context from the
router's `/props`.

| Document | Final request input tokens, run 4 | Run 5 | Share of the context |
|---|---|---|---|
| clean.pdf, 6 pages | 1,111 | 1,134 | 0.8 to 0.9% of 131,072 |
| scanned.pdf, 7 pages | 564 | 570 | 0.4% of 131,072 |

gb10-2's preset (the root's `host/llama-preset/models.ini`) gives gpt-oss-120b `c = 131072` with
4 slots and a unified KV cache: one pool of 131,072 tokens the 4 slots share, and each slot
reports the whole 131,072 as its context. One final request can use up to 131,072 tokens, its
input and its 4096-token output together, when the other slots hold little of the pool. The clean
pages cost under 200 tokens each in the final request, so the final step has room for several
hundred pages like them. Denser pages cost more, and that headroom is arithmetic, not a
measurement. Gemma 4 26B-A4B, the page model, has `c = 32768`.

### The server's request-body limit

llama.cpp b11669 serves HTTP through its vendored cpp-httplib 0.61.0, whose
`CPPHTTPLIB_PAYLOAD_MAX_LENGTH` is `100 * 1024 * 1024` bytes, 100 MiB
(`vendor/cpp-httplib/httplib.h`, line 130, at tag `b11669`, commit `620d2225c`). The server's own
sources (`tools/server/server-http.cpp` and `tools/server/server.cpp`) don't change it, so a
larger body is refused with HTTP 413. This comes from reading the source; no request near the
limit was sent. A page image is far smaller: 21 to 80 KB for the clean page above. Neither
`Images` nor the vision harness sets a limit of its own: the harness sends a page over the
server's limit anyway, and its 413 comes back as that page's `*client.APIError`.

## Measuring a tool's memory

The tool contract's usage report (`tool.Invocation.Usage`) was to carry each run's peak memory
from the process's rusage. It doesn't: `tool.Usage` keeps user and system CPU time and dropped
`PeakRSS`. The reason is how Go starts a program. It uses a vfork-style clone, so the child shares
the parent's memory until it calls exec, and Linux carries that shared memory's high-water mark
into the new program's `maxrss`. A program the test process starts therefore never reports less
than the test process's own peak memory, which in the bake-off held every rendered page.

So the bake-off measures memory through a launcher: the test binary re-executed as a small
process that runs the renderer and reports the renderer's `maxrss`. The launcher's own floor,
measured by running `true` through it, was 9.8 to 12.0 MiB on every run, well under every
renderer's peak (27 to 103 MiB). Each page renders twice: once directly, for its time, CPU, and
PNG, and once through the launcher, for its memory.

## Build, host stack, and network path

- **Dates:** 2026-10-29 for the renderer bake-offs, the corrections matrix, and integration runs 1
  to 4; 2026-10-30 for the placement, the flow, the tiles, and integration run 5.
- **llama.cpp build:** `b11669-620d2225c` (the router's `/props` `build_info`); the router
  reports `version: 0.7.0-dev (build 11669, commit 620d2225c)`.
- **gb10-2's stack:** Ubuntu 24.04.5 LTS, DGX OS 7.6.0; kernel 7.0.0-1024-nvidia since
  host-update's record of 2026-10-30 (7.0.0-1019-nvidia on 2026-10-29); driver 580.178.04;
  CUDA 13.0; Docker 29.6.2; NVIDIA Container Toolkit 1.20.1, unchanged, as is the llama.cpp
  build. After the reboot into the new kernel, the router container's port, bound to gb10-2's
  Tailscale address, failed at boot; gb10-2 now sets `net.ipv4.ip_nonlocal_bind`.
- **The laptop's network path:** direct, under 1 ms (`tailscale ping` from the laptop).
- **Model state:** gemma-4-26B-A4B, gemma-4-E4B, gpt-oss-120b, and granite-embedding-english-r2
  loaded (the router's `/models`), the preset unchanged.

The stack was read at 18:28 local by host-update's record step (step 6 of the root's
`scripts/host-update.sh`), run alone with the script's own `host_build` and `host_stack`
functions. That session's full `mise run host-update` stopped at its preflight, before touching
the router, because gb10-2 had one apt upgrade pending (google-chrome-stable) for the architect to
apply. The values match the root's `docs/findings/host.md` of 2026-10-26.

The laptop ran the renderers and corrections: poppler 26.10.0, Ghostscript 10.08.0, libvips
8.18.7, and ImageMagick 7.1.2-34, from Arch packages.

## Rerunning

From this repository's root, with gb10-2's router at `SPIKE_ROUTER_URL` (its default) and set A
loaded:

```sh
BAKEOFF_RESULTS=$(mktemp -d) mise run bakeoff              # the renderer bake-off, about 11 min
BAKEOFF_RESULTS=$(mktemp -d) mise run bakeoff-corrections  # evidence 11's matrix, about 25 min
BAKEOFF_RESULTS=$(mktemp -d) mise run bakeoff-placement    # ruling L's placements, about 4 min
BAKEOFF_RESULTS=$(mktemp -d) mise run bakeoff-flow         # ruling N's staged flow, about 8 min
BAKEOFF_RESULTS=$(mktemp -d) mise run bakeoff-tiles        # ruling O's tiles
GOWORK=off go test -race -tags integration -count=1 -v ./...  # the integration tier, its records
```

`BAKEOFF_RESULTS` keeps each run's JSON, transcriptions included; unset, a temporary directory
holds it and is removed afterward. `BAKEOFF_DPI`, `BAKEOFF_SET`, and `BAKEOFF_CELL` narrow a
matrix run to some DPIs, sets, or cells; `BAKEOFF_RENDERER` and `BAKEOFF_SET` narrow the bake-off;
`BAKEOFF_PLACEMENT` narrows the placements, and `BAKEOFF_VARIANT` and `BAKEOFF_RUN` the flow's
variants and runs. The integration tier's records print only with `-v`, which
`mise run integration` doesn't pass.
