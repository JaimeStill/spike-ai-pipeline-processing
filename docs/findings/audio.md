# Finding: long audio, windowed at its silences, against one unsplit pass

This page records what the measurements behind the `audio` package found on four long real
recordings. The package cuts each at its silences into windows under 30 s that overlap by 2 s,
the test harness sends them to Gemma 4 E4B 4 at once, and the package joins the transcripts by
aligning the overlapping words at each seam. The page reports how accurately E4B transcribes them
so, how the seams fare beside the rest of the transcript, what a gpt-oss-120b pass over the words
at each seam does, and how the windowed transcription compares, in accuracy and time, with one
unsplit request over the same first 10 minutes. The measurements answer evidence 5 of the spike
(the root's `context/README.md`) for task `audio` (the root's goal record,
`context/goals/experiment.ai.spike-ai-pipeline.md`).

Every number here comes from one run of the findings tier, **findings-audio 2026-11-05**:
`mise run findings-audio` from 2026-11-05 23:42:31 to 2026-11-06 01:29:50 UTC, from the laptop,
against gb10-2's llama.cpp router, build b12098, serving set A, at processing commit `6e4e70f`. Its
results are committed in [`audio-results/`](audio-results/README.md): the log verbatim and each
source's JSON, with full transcripts for the two redistributable sources and none for the two
earnings calls.

The `audio` package releases pre-inference preparation only: the plan, the windows, and the join.
Every request here is test code, in `internal/audiotest` and `audio/findings_test.go`.

## At a glance

| Question | Answer |
|---|---|
| Does windowing match one unsplit pass? | **Not everywhere.** On the same 10-minute excerpts, windowed against unsplit WER: AMI **0.298 against 0.258**, Earnings-21 **0.069 against 0.053**, Earnings-22 **0.101 against 0.101**, LibriVox **0.057 against 0.061**. Worse on the two sources with many speakers and short turns, the same or slightly better on the read speech and the accented call. |
| Is it faster? | **Yes, 2.6 to 3.4 times**, at 4 windows at once: 11.5 s against 37.6 s on AMI's excerpt, 14.6 to 19.1 s against 47 to 52 s on Earnings-21's, 19.6 s against 56.4 s on Earnings-22's, 18.1 s against 47.2 s on LibriVox's. |
| What does a whole source cost? | **Plan plus wall: 25 s for AMI's 21.2 min, 62 to 77 s for Earnings-21's 41.0 min, 128 s for Earnings-22's 64.9 min, 626 s for the 4.4 h audiobook** (645 windows, 9.6 s of it the plan), about 25 to 50 times real time. Mean window latency 1.9 to 3.2 s. No window failed in any run. |
| Whole-source WER? | AMI **0.308**, Earnings-21 **0.087**, Earnings-22 **0.142**, LibriVox **0.047** (approximate truth), each within 0.002 across its 3 runs. |
| Are the seams worse than the rest? | **Yes, and most of the excess is at unmatched seams.** Edits within 5 words, per seam against per control position mid-window: AMI 4.14 against 3.71, Earnings-21 1.45 against 0.56, Earnings-22 1.68 to 1.85 against 1.27 to 1.32, LibriVox 0.67 to 0.76 against 0.48 to 0.51. A matched seam is near its control rate (Earnings-22 about 1.4, LibriVox about 0.6); an unmatched seam, 8 to 23 of a source's seams, and 19 of AMI's 49, averages 3 to 5 edits. |
| Does the gpt-oss seam pass help? | **No: it made every run worse**, on every source. WER rose by 0.001 to 0.004 and the edits at seams by 7 to 34%: AMI 203 to 222-227, Earnings-21 138 to 148-154, Earnings-22 260-286 to 282-312, LibriVox 431-490 to 576-633. It removes repetitions the verbatim truths keep, and correct words at its spans' edges. 12 of 1,932 answers on LibriVox and 3 of 465 on Earnings-22 were refused, mostly spelling corrections it was told not to make. It took longer than the transcription: 681 to 690 s on the audiobook. |

## The sources

| Source | Audio | Length | License | Truth |
|---|---|---|---|---|
| `ami` | AMI ES2002a, Mix-Headset WAV, 16 kHz mono; 4 speakers in a design meeting | 21.2 min | CC BY 4.0 | AMI's manual word-level transcription of each speaker, with word times |
| `earnings21` | Earnings-21 4330115, MP3; 8 speakers on an earnings call | 41.0 min | Transcripts CC BY-SA 4.0; audio not redistributable | Rev's verbatim reference (`.nlp`), no times |
| `earnings22` | Earnings-22 4479944 (subset10), MP3, 44.1 kHz stereo; an Indian bank's earnings call, accented English | 64.9 min | Transcripts CC BY-SA 4.0; audio not redistributable | Rev's verbatim reference (`.nlp`), no times; Rev's force-aligned copy, with times, as a cross-check |
| `librivox` | LibriVox, *The Statement of Stella Maberly*, M4B (AAC), one reader | 4.4 h | Public domain | **Approximate**: the 1896 edition's OCR, cleaned |

Each is in the test corpus at `~/docs/audio/long/`, and the run refuses it unless its SHA-256 and
size are the corpus manifest's (`~/docs/MANIFEST.json`). None is committed. Earnings-22 4479944 was
fetched for this finding from Rev's `speech-datasets` repository at commit
`c05ab6fd8b4b627d123c922a22a39e993dd37635`: the audio from Git LFS
(`earnings22/media/4479944.mp3`, which `earnings22/subset10/media/4479944.mp3` links to; 15,570,129
bytes, SHA-256 `a2ca9b63ba333d47d8e1c9ea1f7dabf956932034db21980b266344c634640065`, the LFS
pointer's oid), the verbatim reference (`transcripts/nlp_references/4479944.nlp`, which subset10's
verbatim reference links to; SHA-256
`d5c8ce7bd87c843e3a1993c69ab527a5c4e725a08982b7b53cb3c92b449b18e8`), the force-aligned reference
(`transcripts/force_aligned_nlp_references/4479944.aligned.nlp`; SHA-256
`14f5c1f8d5d478c286379cb666dbe4bc08440355a40dfc8c6c07c3a7641a533c`), and `LICENSE.md`. Rev's
license covers "the transcripts and associated text files"; the repository states no license for
the audio, so the manifest marks it not redistributable, as it does Earnings-21's.

The M4B's ten chapters are ignored: the source is one audio stream, planned and windowed whole.

## What was measured

Each source is run 3 times. A run is:

1. **The excerpt, unsplit.** The source's first 10 minutes, cut by the host's ffmpeg to 16 kHz
   mono 16-bit WAV, the windows' own format, sent to E4B's transcription endpoint as one request.
   llama.cpp cuts it into 30 s chunks of its own, blind to the speech, about 15,000 audio tokens in
   all, inside E4B's 32,768-token context; no output cap was needed (the server's default left
   room for every answer), and each request's usage is recorded.
2. **The excerpt, windowed.** The same WAV through `audiotest.Run`: planned by the package's
   `Splitter` (silences 15 dB below the mean volume, at least 0.3 s; windows at most 30 s, ending at
   the middle of the latest silence, overlapping by 2 s), its windows cut and sent 4 at once, E4B's
   4 slots in set A, and joined by `audio.Join`.
3. **The whole source, windowed**, as the excerpt is, from the source file itself (WAV, MP3, or
   M4B).
4. **The seam pass** over the whole source's joined transcript (below).

Every transcription request is greedy (temperature 0), in English, with the endpoint's own
instruction. A total time is the plan's (the probe and two ffmpeg passes over the source) plus
the windows' wall time, from the first window's cut to the last transcript. The unsplit time is
the one request's.

### Scoring

The **word error rate** is `textscore.NormalizedWordErrors`: the word edit distance over the
truth's word count, both sides lowercased, punctuation dropped, hyphenated words split, and the
fillers `textscore.Fillers` names dropped (um, uh, mm-hmm, and the like). Numbers are not
verbalized: a truth's "2020" against a transcript's "twenty twenty" is an error, which weighs on
the earnings calls, whose truths write figures as digits.

**Seam accuracy** is `textscore.SeamErrors`: in one alignment of the joined transcript's words
against the truth's, the edits within 5 words of each seam, counted per seam. A seam is where the
later window's words begin in the joined transcript. Reported per run: the seams, matched and
unmatched (`audio.Seam.Matched`: the join found at least 2 overlapping words to align, or else it
concatenated the windows whole); the edits at all seams and at the unmatched ones; and how many
seams had 0, 1, 2, ... edits. Because a transcript's error rate is not zero anywhere, the same
count is taken at **control positions**, the middle of each window between two seams at least 20
words apart, so the seams can be read against the transcript's own error rate a seam away. Seams
nearer each other than 10 words share their edits, as `SeamErrors` documents.

### The seam pass

For each seam, gpt-oss-120b (set A, on the same router, through the client's chat) is sent only
the 10 words around it, the 5 before the seam and the 5 from it on, with a minimal instruction:

> The user's message is a few words from a speech transcript, where two transcripts of overlapping
> stretches of audio were joined. Remove any words the join duplicated or garbled. Do not
> paraphrase, reorder, correct, or add words. Reply with the remaining words alone.

Spans of seams nearer than 10 words are merged into one request. Reasoning is low, temperature 0,
output capped at 2,048 tokens, 4 requests at once, gpt-oss-120b's slots. Its answer replaces
exactly the span's words. An answer is refused, recorded as a failure of the pass, and the span
kept as it was, when the request fails or doesn't complete, when the answer is empty, or when its
normalized words are not a subsequence of the span's: the pass may only remove. No request ever
holds more than the span; no whole transcript is sent or rewritten. The transcript after the pass
is scored as before, each seam at the same offset into its span's replacement, clipped to the
replacement's length.

### The excerpt's truth

AMI's truth has word times, so its excerpt's truth is cut by time: every word whose middle is
before 600 s. Its whole truth is each speaker's words, the segments of all four in order of their
start, so a segment's words stay together; where speakers overlap, the order is the segments',
which no single-stream transcript can match exactly.

The other truths have no times, so the excerpt's truth is derived from the excerpt's transcripts:
for each of the 6 excerpt transcripts of a source (3 unsplit, 3 windowed), `textscore.BestPrefix`
finds the prefix of the whole truth's normalized words with the fewest edits to the transcript,
the longest of equals; the excerpt's truth is the median of the 6 prefixes (the lower of the middle
two). The 6 prefixes' range is the method's uncertainty, in words, reported per source. Two checks
of it: on AMI, the derived prefixes beside the time cut's length; on Earnings-22, Rev's
force-aligned reference, whose tokens carry times, gives a second reckoning, the aligned tokens
before the first one timed at 600 s or later, mapped to the reference's words through one
alignment. The derived cut favours neither the unsplit nor the windowed transcripts: both kinds
vote, and the median is taken.

LibriVox's truth is the book's OCR (Internet Archive), cleaned in the test (`cleanBookOCR`): the
text from "INTRODUCTION." to "THE END.", the library stamps, title pages, and the publisher's
advertisements dropped; the running heads ("The Statement of Stella Maberly", misread a page at a
time) and page numbers dropped; and a word hyphenated at a line's end joined. What OCR misread
stays, chapter headings are Roman numerals where the reader says "Chapter one", and LibriVox's
spoken preamble and closing for each chapter are in no truth: **its WER is approximate**, an upper
bound inflated by the truth, good for comparing the runs with one another, not for an absolute
rate.

### Scoring memory

The whole alignment table, len(truth)+1 by len(transcript)+1 ints, would take about 14 GB for
the audiobook's 41,600 words against a transcript as long. `textscore.Align` instead holds every
b-th row, b about the square root of the truth's length, from one pass, and traces back one block
of b rows at a time, each block recomputed from the row held before it. It recomputes the same
rows, so it gives exactly the same alignment, in about 2·√n rows of int32, for twice one pass's
work. At the audiobook's size it took 7.3 s and allocated 73 MB on the laptop.
`TestAlignWholeTable` holds it to a whole-table traceback, step for step, on 2,000 random pairs up
to 200 long from alphabets of 2 to 26 symbols; `TestBestPrefix` and `TestBestPrefixEveryPrefix`
hold `BestPrefix` to `EditDistance` over every prefix. `EditDistance`, which the WER uses, keeps
one row.

## Results

### The 10-minute excerpt: windowed against unsplit

Each cell is the 3 runs' range, or their value when all 3 agree. WER is edits over the excerpt
truth's normalized words; the time is the one request's for unsplit, plan plus wall for windowed.

| Source | Truth words | Unsplit WER | Windowed WER | Unsplit time | Windowed time | Windows | Seams (unmatched) |
|---|---|---|---|---|---|---|---|
| `ami` | 1,129 | 0.255 to 0.260 (288 to 294) | 0.297 to 0.300 (335 to 339) | 37.4 to 37.9 s | 11.4 to 11.6 s | 24 | 23 (11 to 12) |
| `earnings21` | 1,574 | 0.053 (84) | 0.069 (108) | 47.3 to 51.9 s | 14.6 to 19.1 s | 24 | 23 (0) |
| `earnings22` | 1,588 | 0.101 (160) | 0.101 to 0.102 (160 to 162) | 56.1 to 56.9 s | 19.5 to 19.7 s | 23 | 22 (1) |
| `librivox` | 1,582 | 0.059 to 0.064 (94 to 102) | 0.056 to 0.058 (89 to 92) | 46.8 to 47.7 s | 17.9 to 18.3 s | 24 | 23 (1) |

Every unsplit request took 15,028 input tokens, the same for each 10-minute WAV, and 1,495 to
2,136 output tokens; it completed without an output cap. The windowed excerpt took 16,749 to
16,829 input tokens across its windows: the 2 s overlaps and each window's instruction.

The excerpt's truth: AMI's by time, 1,129 words, where the derived method's prefixes were 1,118 to
1,119, 10 to 11 words (under 1%) short of the time cut, the difference AMI's segment order makes at
the cut. Earnings-21's derived end was 1,574 words, Earnings-22's 1,588, both with no spread
across the 6 transcripts; **Rev's force-aligned times put Earnings-22's at 1,588 too**. LibriVox's
was 1,582, its prefixes 1,582 to 1,583. The method's uncertainty here is at most 1 word.

### The whole source, windowed

| Source | Length | Windows (cut at limit) | WER | Plan + wall | Mean latency | Seams, unmatched | Seam edits, per seam | Control edits, per position |
|---|---|---|---|---|---|---|---|---|
| `ami` | 1,272.6 s | 50 (0) | 0.308 to 0.309 (755 to 758 of 2,455) | 25.1 to 25.5 s (plan 0.3 s) | 1.9 to 2.0 s | 49, 19 | 203, 4.14 | 170 to 171 at 46, 3.71 |
| `earnings21` | 2,458.9 s | 96 (0) | 0.087 (568 to 570 of 6,519) | 61.5 to 77.4 s (plan 1.3 s) | 2.5 to 3.1 s | 95, 8 | 138 to 139, 1.45 | 53 at 94, 0.56 |
| `earnings22` | 3,892.5 s | 156 (2) | 0.141 to 0.143 (1,514 to 1,536 of 10,742) | 127.2 to 128.7 s (plan 3.6 s) | 3.1 to 3.2 s | 155, 14 to 15 | 260 to 286, 1.68 to 1.85 | 196 to 203 at 154, 1.27 to 1.32 |
| `librivox` | 15,787.1 s | 645 (9) | 0.046 to 0.048 (1,900 to 1,987 of 41,591) | 625.5 to 627.3 s (plan 9.5 to 9.7 s) | 2.7 s | 644, 22 to 23 | 431 to 490, 0.67 to 0.76 | 306 to 329 at 640, 0.48 to 0.51 |

At most 4 calls were in flight in every run. Earnings-21's first run was slower than its others
(77 s against 62 s, mean latency 3.1 s against 2.5 s); its WER and seams were the same.

How the seams' edits fall, from run 1 (`per_seam` and `distribution` in the JSON):

| Source | Seams with 0 edits | 1 | 2 | 3 to 5 | 6 to 9 | 10 or more | Unmatched seams' edits, per seam | Matched seams' edits, per seam |
|---|---|---|---|---|---|---|---|---|
| `ami` | 6 | 8 | 11 | 13 | 3 | 8 | 81 at 19, 4.26 | 122 at 30, 4.07 |
| `earnings21` | 41 | 27 | 13 | 8 | 4 | 2 | 32 at 8, 4.0 | 106 at 87, 1.22 |
| `earnings22` | 60 | 35 | 25 | 27 | 6 | 2 | 62 at 14, 4.4 | 198 at 141, 1.40 |
| `librivox` | 444 | 94 | 57 | 37 | 10 | 2 | 67 at 22, 3.0 | 364 at 622, 0.59 |

The worst seams were read in the transcripts (for the redistributable sources) and are the model's,
not the join's. On AMI they sit in crosstalk and end-of-meeting chatter, garbled on both sides of
the seam. On LibriVox, one seam was misheard in all 3 runs (15 edits), and in runs 2 and 3 a window
between two seams 1 word apart, a few seconds of audio, was one **E4B answered with its own
instruction**: "to transcribe the provided audio. The language is English. I will listen to the
audio and transcribe the spoken words accurately.", 42 edits at the seams either side. The join
placed every window's words in order around them; no run showed a plan or join defect.

### The seam pass

| Source | Requests a run | Changed | Unchanged | Refused | Wall | WER before → after | Seam edits before → after | Control edits before → after |
|---|---|---|---|---|---|---|---|---|
| `ami` | 48 | 27 to 29 | 19 to 21 | 0 | 52.2 to 56.0 s | 0.308 → 0.310 to 0.313 | 203 → 222 to 227 | 170 to 171 → 162 to 163 |
| `earnings21` | 95 | 25 to 30 | 65 to 70 | 0 | 86.9 to 90.0 s | 0.087 → 0.088 to 0.089 | 138 to 139 → 148 to 154 | 53 → 53 to 55 |
| `earnings22` | 155 | 62 to 67 | 88 to 92 | 0 to 2 | 168.6 to 172.7 s | 0.141 to 0.143 → 0.142 to 0.144 | 260 to 286 → 282 to 312 | 196 to 203 → 196 to 204 |
| `librivox` | 644 | 155 to 157 | 481 to 486 | 2 to 7 | 680.5 to 689.8 s | 0.046 to 0.048 → 0.049 to 0.051 | 431 to 490 → 576 to 633 | 306 to 329 → 308 to 311 |

Mean latency was 4.2 s a request on LibriVox. Spans were merged where seams were nearer than 10
words, so the requests are a few fewer than the seams. The control positions' edits shift by up to
9 though the pass never touches their words: one alignment of a changed transcript breaks ties
differently, the noise floor of the count.

What it changed: mostly the removal of a repeated word: "a look at our at our business segments"
to "at our business segments", "the smart smart fabrics" to "the smart fabrics", "um um" to "um".
The truths are verbatim, so a speaker's own repetition is in them, and removing it is an error. It
also dropped correct words at a span's edge, where it couldn't see what followed: "of this fiscal
year. The" to "of this fiscal year.", and "Importantly, income" to "Importantly,". There were 15
refusals in all. 12 were answers that changed words: 11 corrected a spelling ("evelin" to
"evelyn", "ppop" to "pop", "spectrums" to "spectrum") or a word ("protested" to "protest"), and
one added a word ("jealous brooding" to "jealous bro brooding"). One dropped the span's tail
mid-word ("sanctioning" to "sanction"), and 2 were empty answers, on LibriVox's run 3. No answer
declined the task in words.

## Known limits

- **Overlap is 2 s, the package's default**, inside evidence 5's 1 to 2 s; no other overlap was
  measured.
- **E4B can answer a short window with its instruction.** One LibriVox window of a few seconds did
  so in 2 of 3 runs. The join can't tell an instruction from speech; a guard against it belongs to
  the inference layer, not the package.
- **The unsplit pass covers only the excerpt.** A whole source past 10 minutes outruns E4B's
  context, so the comparison is on the first 10 minutes of each.
- **Truths:** AMI's speaker order at overlaps; the earnings calls' digits against spoken numbers;
  LibriVox's OCR, preambles, and chapter headings. LibriVox's WER is approximate, an upper bound.
- **Seam edits are counted within 5 words**, so an error a seam causes further off isn't counted,
  and two seams nearer than 10 words both count an edit between them.
- **The seam pass had one instruction, low reasoning, and 10 words**; another prompt or a wider
  span might do otherwise. The measurement asked the evidence's question, not how to make a pass
  work.
- **One router build, one laptop**, greedy decoding: the 3 runs differ by a few words, from
  batching across the slots.

## What it answers for evidence 5

Answered:

- That audio far past the model's limit, 4.4 hours of it, is cut at its silences into windows under
  30 s that overlap by 2 s, transcribed 4 at once within set A's audio slots, and joined by aligning
  the overlapping words. No window failed in 12 runs, and the audiobook's 645 windows took 10.4
  minutes.
- Seam accuracy, per seam and against control positions: seams carry more errors than the rest of
  a transcript, most of the excess at the unmatched seams, where the join had no words to align.
- Total time against one unsplit pass on the same 10 minutes: windowed is 2.6 to 3.4 times faster.
  Its accuracy is the same or slightly better on read speech and on the accented call, and worse
  on a 4-speaker meeting (0.298 against 0.258) and an 8-speaker call (0.069 against 0.053).

**Contradicted:** evidence 5 says "an optional gpt-oss pass smooths the seams". On these sources,
scored against verbatim truths, it does not: a pass that sees only the 10 words around each seam
and may only remove words made the WER and the seam edits worse in all 12 runs.

## Rerunning

From this repository's root, on a laptop (never in CI, never in the check), with set A loaded on
gb10-2 (`mise run host-update` in the root):

```sh
mise run findings-audio
```

`mise run findings-audio` (`scripts/findings-audio.sh`) runs the findings-tagged tests in package
audio, without the race detector: `TestFindingsTruth` reads every truth, checked against the
manifest, with no model; `TestFindingsSource` runs each source as a go test run of its own, capped
at 4 hours; and `TestFindingsReport` logs the tables from their results.
`AUDIO_FINDINGS_SOURCES` (`ami earnings21 earnings22 librivox`) and `AUDIO_FINDINGS_RUNS` (3)
choose the sources and runs, and `DOCS` (`~/docs`) the corpus. A plain run keeps nothing; to keep
the results, record them:

```sh
AUDIO_FINDINGS_RESULTS=docs/findings/audio-results mise run findings-audio
```

which writes each source's JSON, after each of its runs, and the run's `-v` log, `findings.log`,
there.
