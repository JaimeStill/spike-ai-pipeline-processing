# Finding: PDF pages through a model's vision

Task `documents`, evidence 4 and 11 and behaviors 11 to 14 (the root's goal record,
`context/goals/experiment.ai.spike-ai-pipeline.md`, "Task brief · experiment.ai.spike-ai-pipeline ·
documents"). Measured 2026-10-29 from the laptop, against gb10-2's llama.cpp router serving set A.

The `document` package turns a PDF into text a model can work with. It renders each page to an
image, can clean the image up first, and asks a vision model to transcribe it. This page records
what the measurements behind those choices found: which renderer to ship, whether the model should
reason while it reads, whether image corrections help, and what a whole document costs.

Two models do the work. Gemma 4 26B-A4B (`ggml-org/gemma-4-26B-A4B-it-GGUF:Q4_0`) reads the
pages. gpt-oss-120b (`ggml-org/gpt-oss-120b-GGUF:MXFP4`) writes the document's digest. Every
number here comes from a recorded run: a test's `recorded:` log line, or the JSON results a run
kept. Each section says which run.

## At a glance

| Question | Answer |
|---|---|
| Which renderer ships? | **Ghostscript (`gs`).** It led on accuracy, was the fastest (0.126 s a page), used the least memory (27 MiB), and needs one program. It is AGPL, so the package runs it as a separate program and never links it. |
| Should the model reason while it transcribes? | **No.** With reasoning on, 8 of 105 scanned-page transcriptions reasoned until the 8192-token cap and came back empty, and the model invented seal text. With reasoning off, every transcription completed. |
| Do image corrections help? | **Not reliably.** At 150 dpi the synthetic faults give corrections nothing to fix. At 72 dpi, Unsharp helps the hardest page while Deskew and Despeckle hurt it badly. On the real scans, gains and losses are page by page and flip with the DPI. |
| What does a page cost in context? | **477 image tokens** for a letter page at 150 dpi, and the same at 200 dpi; 223 at 72 dpi and 416 at 100. |
| Does Document's final step fit? | **Easily.** The digest request took 1,111 input tokens for six clean pages and 564 for seven scanned pages: under 1% of gpt-oss-120b's 131,072-token context. |
| What do Pages and Document cost? | Six clean pages: about 8 s for Pages and 16 s for Document with its digest. Seven scanned pages: 12 to 13 s for Document. |

## The renderer bake-off

### Method

Five renderers each turned every fixture page into a PNG at 150 dpi. Each PNG went to Gemma 4
26B-A4B with `TranscribeInstructions`, and the transcription was scored against the page's ground
truth. The bake-off is its own test tier, `mise run bakeoff` (`-tags bakeoff`;
`document/bakeoff_test.go` and `document/renderers_bakeoff_test.go`). It isn't CI.

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
- **Ranking order:** CER, then incomplete transcriptions, then render time, peak memory,
  streaming, license, PNG size, and the number of programs. Each pair of renderers is decided by
  the first criterion that separates them, and a renderer ranks by how many others it beats.

The bake-off ran as 15 `go test` runs, one per renderer and set, each capped at 10 minutes. They
took 26 to 65 s each, about 11 minutes in all (the log's `bakeoff: <renderer> <set> took` lines).
An earlier attempt with one run per renderer had overrun 10 minutes.

### Results

Source: the reasoning-off run's JSON (15 files), ranked again for this page by
`TestBakeoffRanking`. The tables match the run's own log line for line.

The clean and degraded sets scored a CER of 0 for every renderer on every run, so the scanned set
decided accuracy:

| Renderer | Scanned CER (min..max) | Scanned WER | Incomplete |
|---|---|---|---|
| gs | 0.0803 (0.0702..0.0864) | 0.1250 | 0/21 |
| pdftoppm | 0.0925 (0.0918..0.0929) | 0.1429 | 0/21 |
| magick | 0.0994 (0.0842..0.1112) | 0.1488 | 0/21 |
| pdftocairo | 0.1091 (0.1091..0.1091) | 0.1607 | 0/21 |
| vips | 0.1242 (0.1091..0.1544) | 0.1786 | 0/21 |

The ranking over all three sets, each run's CER being the mean of the three sets' rates:

| Rank | Renderer | Wins | CER (min..max) | Incomplete | Render s/page | Peak MiB | PNG KiB/page | License | Programs | Installed MiB |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | **gs** | 4 | 0.0268 (0.0234..0.0288) | 0/66 | 0.126 | 27.0 | 364 | AGPL | gs | 41.08 |
| 2 | pdftoppm | 2 | 0.0308 (0.0306..0.0310) | 0/66 | 0.457 | 30.5 | 439 | GPL (poppler) | pdftoppm, pdfinfo | 6.81 |
| 3 | magick | 2 | 0.0331 (0.0281..0.0371) | 0/66 | 0.338 | 102.7 | 322 | AGPL (through gs) | magick, gs | 63.12 |
| 4 | vips | 2 | 0.0414 (0.0364..0.0515) | 0/66 | 0.133 | 90.1 | 348 | GPL (its PDF loader links poppler) | vips, vipsheader | 16.09 |
| 5 | pdftocairo | 0 | 0.0364 (0.0364..0.0364) | 0/66 | 0.415 | 58.2 | 354 | GPL (poppler) | pdftocairo, pdfinfo | 6.81 |

gs beat pdftoppm, pdftocairo, and vips on CER, and magick on time. Render time and peak memory
are per page, and their spreads over the three runs were narrow: gs took 0.125 to 0.127 s and
peaked at 27.0 MiB on every run. Installed size is `pacman -Qi` on the laptop for the packages
each renderer needs. Every renderer could also stream, reading the PDF on stdin and writing the
page's PNG to stdout.

### The winner: gs

gs is the released renderer, `document.Ghostscript`. It led on accuracy, was the fastest and the
lightest in memory, and needs a single program. Its cost is the license: Ghostscript is
AGPL-3.0-or-later, so the package runs `gs` as a separate process through the tool contract and
never links it. vips came close on speed but used more than three times the memory, and is GPL
through its poppler PDF loader. The bake-off's gs runs the released code, so a rerun ranks what
ships.

## What reasoning did

The first bake-off ran with the model's reasoning on, the server's default. Source: that run's
scanned-set JSON (7 pages per renderer) and its ranking log.

- **Empty transcriptions.** 8 of the 105 scanned transcriptions (5 renderers, 7 pages, 3 runs)
  reasoned until the 8192-token cap and returned no text: page 7 six times and page 5 twice,
  across pdftocairo (3), vips (3), and pdftoppm (2). Each took 197 to 253 s. (The goal record says
  7; the JSON holds 8.)
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
`Vision.Pages`, with the released renderer and the corrections stage on the laptop's ImageMagick
7.1.2-34. It has seven cells: no correction (`none`); each of the five alone (Deskew
`-deskew 40%`, Despeckle `-despeckle`, ContrastStretch `-contrast-stretch 0.5%x0.5%`, Unsharp
`-unsharp 0x1`, Grayscale `-colorspace Gray`); and all five together (`all`). Each cell had 3
greedy runs, reasoning off, the 8192-token cap, 4 pages in flight, and no page retries, scored as
the bake-off scores.

The whole matrix ran at 150 dpi and again at 72 dpi, since every synthetic fault scored 0 at
150 dpi in the bake-off. It is `mise run bakeoff-corrections` (`document/corrections_bakeoff_test.go`):
28 `go test` runs, one per DPI, set, and cell, 13 to 183 s each and 25 minutes in all, then
`TestCorrectionsMatrix` over their JSON. The tables below are its recorded lines.

One difference from the bake-off: `Vision` sends the page's image in a user message of its own,
so `TranscribeInstructions` goes in the request's instructions rather than beside the image. The
`none` cells therefore don't repeat the bake-off's gs numbers (scanned CER 0.1098 here against
0.0803 there), and the matrix compares its cells only with its own `none`.

"vs none" is a tie when the two cells' spreads over the three runs overlap.

### At 150 dpi

| Cell | Degraded CER (min..max) | Inc. | vs none | Scanned CER (min..max) | Inc. | vs none |
|---|---|---|---|---|---|---|
| none | 0.0037 (0.0000..0.0110) | 0/27 | | 0.1098 (0.1091..0.1112) | 0/21 | |
| Deskew | 0.0000 | 0/27 | tie | 0.1199 | 0/21 | hurts |
| Despeckle | 0.0110 | 0/27 | tie | 0.0907 | 0/21 | helps |
| ContrastStretch | 0.0000 | 0/27 | tie | 0.1091 | 0/21 | tie |
| Unsharp | 0.0000 | 0/27 | tie | 0.1325 (0.1307..0.1361) | 0/21 | hurts |
| Grayscale | 0.0110 | 0/27 | tie | 0.0961 (0.0270..0.1490) | 1/21 | tie |
| all | 0.0000 | 0/27 | tie | 0.1094 (0.1080..0.1123) | 0/21 | tie |

A CER with no range was the same on all three runs. WER moves with CER in every cell; the recorded
lines carry it.

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

| Cell | Degraded CER (min..max) | Inc. | vs none | Scanned CER (min..max) | Inc. | vs none |
|---|---|---|---|---|---|---|
| none | 0.0708 (0.0038..0.1050) | 1/27 | | 0.1022 (0.0994..0.1080) | 0/21 | |
| Deskew | 0.1974 (0.1807..0.2058) | 0/27 | hurts | 0.0922 (0.0842..0.0961) | 0/21 | helps |
| Despeckle | 0.2012 (0.1840..0.2231) | 0/27 | hurts | 0.1058 (0.1048..0.1080) | 0/21 | tie |
| ContrastStretch | 0.0589 (0.0036..0.0938) | 1/27 | tie | 0.0994 | 0/21 | tie |
| Unsharp | 0.0205 (0.0167..0.0267) | 0/27 | tie | 0.1080 | 0/21 | tie |
| Grayscale | 0.0564 (0.0038..0.0848) | 1/27 | tie | 0.1008 (0.0994..0.1015) | 0/21 | tie |
| all | 0.1669 (0.1171..0.2231) | 0/27 | hurts | 0.1120 (0.1112..0.1134) | 0/21 | hurts |

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
- **Page 5's seal is read only once deskewed.** With no correction, the model skips the seal's
  "DEPARTMENT OF DEFENSE" on every run and reads the rest of the page, the handwritten initials
  included. Deskew at 150 dpi reads it on every run.
- **The page loop and PageRetries.** A model that loops on a page runs to its output cap, which
  costs minutes and returns no usable text, but on one run and not the next. So `Request` gained
  `PageRetries`: a page whose response isn't `completed` is sent again with the same image, up to
  that many times, and `Page.Attempts` says how many sends it took. The integration tier sets one
  retry and caps a page at 2048 output tokens, so a loop ends in seconds rather than minutes. The
  matrix sets no retries, so its loops show as incomplete pages. No page needed a retry in the four
  integration runs below.

## Pages and Document on gb10-2

The integration tier (`document/vision_integration_test.go`) runs both entry points against
gb10-2: each page through Gemma 4 26B-A4B as the bake-off transcribes it, 4 pages in flight, a
2048-token page cap, and one page retry, and Document's final request to gpt-oss-120b at low
reasoning effort for a digest under a JSON schema (summary, title, kind, dates, markings,
keywords), capped at 4096 output tokens.

Document **digitizes, then digests**. The page transcriptions are the document's content, which
`Result.Text` joins under "Page N of M:" headings. The final request derives a small digest for
search from them and never replaces them. An earlier design asked the final model to rewrite the
whole document as one clean text. It was rejected: a rewrite paraphrases and invents, and its
output grows with the document.

Four runs, the fourth for this page, gave the same results apart from the wall time:

| Run | Pages, clean.pdf (6 pages) | Document, clean.pdf | Document, scanned.pdf (7 pages) |
|---|---|---|---|
| 1 | 8.06 s, CER 0 | 15.50 s, CER 0 | 11.88 s |
| 2 | 8.18 s, CER 0 | 16.11 s, CER 0 | 11.78 s |
| 3 | 8.43 s, CER 0 | 16.19 s, CER 0 | 11.70 s |
| 4 | 7.78 s, CER 0 | 15.87 s, CER 0 | 12.99 s, CER 0.0961 |

Run 4 added the scanned pages' CER and the final step's input tokens to the recorded lines. In
every run, `Result.Text` for clean.pdf scored a CER of 0 against the joined ground truth with its
headings stripped.

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

Source: `TestVisionImageTokensHost`, integration run 4. clean.pdf's page 1 was rendered by gs at
each DPI and sent beside a short text in a request for one output token. The image's tokens are
the request's input tokens less those of the same request without the image (18). The difference
includes the few tokens that mark where an image begins and ends.

| DPI | Pixels | PNG bytes | Image tokens |
|---|---|---|---|
| 72 | 612 × 792 | 21,017 | 223 |
| 100 | 850 × 1100 | 34,520 | 416 |
| 150 | 1275 × 1650 | 54,237 | 477 |
| 200 | 1700 × 2200 | 80,122 | 477 |

Gemma 4 26B-A4B's image budget saturates between 100 and 150 dpi: 150 and 200 dpi cost the same
477 tokens. That is why the default is 150 dpi: it costs no more context than any higher DPI.
These match the plan's measurements (about 223, 416, and 477). A whole page request at 150 dpi,
image and instructions, is 538 input tokens (the matrix's records), against Gemma's 32,768-token
context.

### The final step against gpt-oss-120b's context

Source: the integration tier's `recordFinal`, run 4, which reads a slot's context from the
router's `/props`.

| Document | Final request input tokens | Share of the context |
|---|---|---|
| clean.pdf, 6 pages | 1,111 | 0.8% of 131,072 |
| scanned.pdf, 7 pages | 564 | 0.4% of 131,072 |

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
limit was sent. A page image is far smaller: 21 to 80 KB for the clean page above. The package
sets no limit of its own. A page over the server's limit is sent anyway, and its 413 comes back as
that page's `*client.APIError`.

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

- **Date:** 2026-10-29.
- **llama.cpp build:** `b11669-620d2225c` (the router's `/props` `build_info`); the router
  reports `version: 0.7.0-dev (build 11669, commit 620d2225c)`.
- **gb10-2's stack:** Ubuntu 24.04.5 LTS, DGX OS 7.6.0; kernel 7.0.0-1019-nvidia; driver
  580.178.04; CUDA 13.0; Docker 29.6.2; NVIDIA Container Toolkit 1.20.1.
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
GOWORK=off go test -race -tags integration -count=1 -v ./...  # the integration tier, its records
```

`BAKEOFF_RESULTS` keeps each run's JSON, transcriptions included; unset, a temporary directory
holds it and is removed afterward. `BAKEOFF_DPI`, `BAKEOFF_SET`, and `BAKEOFF_CELL` narrow a
matrix run to some DPIs, sets, or cells; `BAKEOFF_RENDERER` and `BAKEOFF_SET` narrow the bake-off.
The integration tier's records print only with `-v`, which `mise run integration` doesn't pass.
