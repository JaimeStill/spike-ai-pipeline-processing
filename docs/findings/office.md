# Finding: legacy office files through LibreOffice, and office's pictures through vision

This page records what the measurements behind the `office` package found: what converting
legacy .doc, .xls, and .ppt files to OOXML through LibreOffice in a container loses, what that
container costs, how the pictures vision can't decode convert to PNG, and what each picture
handed onward costs a vision request. They answer the legacy part of evidence 12 of the spike
(the root's `context/README.md`) for task `office` (the root's goal record,
`context/goals/experiment.ai.spike-ai-pipeline.md`, "Task brief · experiment.ai.spike-ai-pipeline ·
office"). The rest of evidence 12, the Open XML CLI's own fidelity, memory, cold start, and size
against markitdown and xberg, is office's
[office-cli finding](https://github.com/JaimeStill/spike-ai-pipeline-office/blob/main/docs/findings/office-cli.md).

Every number here comes from one run of the office integration tier on 2026-11-02, from the
laptop (x86_64) to gb10-2's llama.cpp router, build b11669, serving set A: office 0.1.0, soffice
26.8.1.1 in `LibreOfficeImage` (`gotenberg/gotenberg:8.39.0-libreoffice`, pinned by digest), and
ImageMagick 7.1.2-34. Its logged lines are committed in
[`office-results/`](office-results/README.md), one file per test. Earlier runs during the task
found the same results; where this run differs, the section says so.

The `office` package releases pre-inference preparation only. The vision requests are test code
in `office/vision_integration_test.go`, which asserts only that each request completes.

## At a glance

| Question | Answer |
|---|---|
| What does the legacy round trip lose? | **Structure in .doc, a little in .xls, nothing in .ppt.** In .doc, every heading comes back as a paragraph in a one-item list, an ordered list splits into one-item lists, and a comment's replies become top-level comments; text, tables, notes, headers, footers, charts, and pictures are kept. In .xls, error-literal formulas become `NA()`, `#DIV/0!` becomes `#N/A`, and a boolean formula result becomes a number. The .ppt fixtures keep every element. |
| What do Office-written legacy files lose? | **Nothing measured.** Three Apache POI files, one per format, keep every element of their hand truth. |
| What does the container cost? | **441 MB to pull, 1.02 GB on disk** (440,917,922 B compressed for linux/amd64, 1,015,274,582 B on disk), and **0.75 to 1.04 s a conversion**, 1.42 to 1.48 s for the largest .ppt. The first conversion costs what a repeat does: every run starts a fresh container. |
| Do EMF, WMF, and TIFF convert? | **Yes, all eleven pictures,** to PNG that decodes, the same bytes on every conversion. A document's metafiles take 1.0 to 2.0 s together, one soffice run and a magick trim each; a TIFF takes 25 ms. |
| What does a picture cost vision? | **77 to 374 image tokens, 0.39 to 1.48 s.** Small pictures sit at a floor of about 82 to 83 tokens; only the largest picture, 1489x566, passes 200. All 16 distinct pictures completed, in 11.6 s one at a time. |

## Legacy round trip

### What was measured

The integration tier's `TestLegacyRoundTrip` converts each legacy fixture to OOXML through soffice
in the default image, under the container runner's full lockdown, then runs office on the result
and compares office's model, element by element, against a reference:

- **Generated** fixtures are office's own OOXML fixtures that LibreOffice saved as .doc, .xls,
  and .ppt (`office/testdata/README.md`, "Legacy, generated"). The reference is office's model of
  the OOXML original, so the loss is the whole trip, OOXML to legacy and back, both directions
  through LibreOffice; the measurement can't say which direction lost an element.
- **Real** fixtures are three files Word, Excel, and PowerPoint wrote, from Apache POI's test
  data. The reference is a hand-written truth (`office/testdata/README.md`, "Legacy, real").

An element is a key the tier derives from office's model: a heading's level and text, a
paragraph's text, a list's kind and item count, a table's shape, a cell's or formula's sheet,
reference, and value or text, a comment's author and text, and so on. An element is kept when
its key is in both, lost when only the reference has it, and added when only the result has it.
Formatting, styles, and layout aren't keys, so their loss isn't measured.

### Generated files, per format

From the run's per-format totals (`office-results/legacy-round-trip.log`):

| Format | Element kind | Reference | Kept | Lost | Added |
|---|---|---|---|---|---|
| doc (7 files) | heading | 8 | 0 | 8 | 0 |
| | paragraph | 84 | 84 | 0 | 8 |
| | list | 15 | 12 | 3 | 14 |
| | comment | 3 | 3 | 0 | 2 |
| | comment-ref | 3 | 3 | 0 | 2 |
| | table | 6 | 6 | 0 | 0 |
| | chart, series | 3, 4 | all | 0 | 0 |
| | media | 11 | 11 | 0 | 0 |
| | header, footer | 8, 8 | all | 0 | 0 |
| | footnote, endnote, and their refs | 2 each | all | 0 | 0 |
| xls (4 files) | cell | 92 | 90 | 2 | 2 |
| | formula | 13 | 11 | 2 | 2 |
| | sheet, table | 5, 5 | all | 0 | 0 |
| | chart, series, media | 1, 1, 3 | all | 0 | 0 |
| ppt (3 files) | slide | 11 | 11 | 0 | 0 |
| | heading, paragraph, list | 8, 35, 11 | all | 0 | 0 |
| | table, chart, series, media | 1, 1, 2, 3 | all | 0 | 0 |

What the lost and added keys are:

- **doc headings.** Every heading, all 8 across 6 files, comes back as a paragraph of the same
  text inside a one-item unordered list: 8 headings lost, 8 paragraphs and 8 lists added. The
  text survives; the heading's level and its role don't.
- **doc ordered lists.** In `lists.libreoffice.doc`, three two-item ordered lists each come back
  as two one-item ordered lists: 3 lists lost, 6 added. Each item's text survives; the numbering
  restarts at every item.
- **doc comment replies.** In `comments.libreoffice.doc`, the two replies come back as top-level
  comments with their own references: 2 comments and 2 comment-refs added. Their text and authors
  survive; the thread doesn't.
- **xls errors.** In `cells.libreoffice.xls`, two cells hold error literals as formulas, `#N/A`
  and `#DIV/0!`. Both formulas come back as `NA()`, and the `#DIV/0!` cell's value as `#N/A`: one
  error's meaning changes to another's.
- **xls booleans.** In `formulas.libreoffice.xls`, the formula `A1>B1` keeps its text, but its
  cached value, boolean `FALSE`, comes back as the number `0`.

Nothing else differs. The .ppt round trip loses nothing office models.

### Office-written files, against hand truth

| File | Written by | Elements in the truth | Lost | Added |
|---|---|---|---|---|
| `simple-table.doc` | Word 97 | 8 paragraphs, 1 table | 0 | 0 |
| `SampleSS.xls` | Excel | 3 sheets, 2 tables, 15 cells, 1 formula | 0 | 0 |
| `basic_test_ppt_file.ppt` | PowerPoint | 2 slides, 2 headings, 7 paragraphs, 1 list | 0 | 0 |

These three are small and plain: no headings in the .doc, no lists, comments, or errors anywhere.
They show that LibreOffice reads an Office-written file of each format without loss at that level;
they can't show whether an Office-written document's headings, lists, or comment threads survive
as the generated ones don't.

## Container footprint

The image (`office-results/legacy-round-trip.log`, its `footprint:` lines):

| Measure | Value |
|---|---|
| Compressed, linux/amd64 manifest, 16 layers | 440,917,922 B |
| On disk | 1,015,274,582 B |

Each conversion's wall time, through the container runner, first run and repeat. Each run starts
a new container with a fresh soffice profile, so there is no warm state for a repeat to reuse:

| Fixture | First | Repeat |
|---|---|---|
| `bullets.libreoffice.ppt` | 834 ms | 848 ms |
| `cells.libreoffice.xls` | 768 ms | 769 ms |
| `charts.libreoffice.doc` | 944 ms | 932 ms |
| `comments.libreoffice.doc` | 840 ms | 843 ms |
| `drawings.libreoffice.xls` | 850 ms | 873 ms |
| `formulas.libreoffice.xls` | 752 ms | 748 ms |
| `lists.libreoffice.doc` | 871 ms | 808 ms |
| `media.libreoffice.doc` | 817 ms | 819 ms |
| `merges.libreoffice.xls` | 763 ms | 747 ms |
| `notes.libreoffice.doc` | 816 ms | 856 ms |
| `sections.libreoffice.doc` | 846 ms | 838 ms |
| `slide-objects.libreoffice.ppt` | 1,420 ms | 1,482 ms |
| `slides.libreoffice.ppt` | 869 ms | 1,037 ms |
| `tables.libreoffice.doc` | 945 ms | 854 ms |
| `SampleSS.xls` | 807 ms | 812 ms |
| `basic_test_ppt_file.ppt` | 839 ms | 849 ms |
| `simple-table.doc` | 846 ms | 817 ms |

So a legacy file costs about 0.75 to 1.04 s before office runs, and 1.4 to 1.5 s for the largest
deck, `slide-objects` (four slides with a table, a chart, and pictures). The earlier runs during
the task gave 0.75 to 1.0 s and 1.4 to 1.6 s; this run's one repeat above 1 s, `slides.ppt` at
1,037 ms, is inside the spread a repeat shows. The image's one-time pull took about 18 s in an
earlier run; this run found the image present and timed no pull. The container's
peak memory was out of scope and wasn't measured.

## Picture conversions

`TestPictures` (`office-results/pictures.log`) converts every EMF, WMF, and TIFF picture in the
fixtures, at the default `MetafileScale`, 3, and checks that each PNG decodes and is the same bytes
on a second conversion. Between them the fixtures hold 8 EMF, 2 WMF, and 1 TIFF, 10 of them
distinct.

LibreOffice's Draw PNG export doesn't write a metafile's own size: it writes the whole page,
794x1123 pixels for A4 at 96 dpi, with the picture at its 96 dpi size in the middle. So the
package renders that page at the scale, 2382x3369 at 3, and trims it to the picture with magick
(the goal record's office escalation from slice 4). The trimmed sizes:

| Fixture | Picture | Stored | PNG | PNG size |
|---|---|---|---|---|
| `media.libreoffice.docx` (and its .doc) | `670be6349ed8….emf`, blank | 108 B | 1x1 | 160 B |
| `word-with-attachments.docx` | `2156195ef201….emf` | 1,952 B | 511x155 | 11,618 B |
| | `8e362ed59772….emf` | 5,396 B | 339x187 | 15,735 B |
| | `90f616810030….wmf` | 3,726 B | 236x193 | 13,165 B |
| | `cc90707a7aab….emf` | 5,132 B | 1489x566 | 9,952 B |
| | `f35ded1fae0a….emf` | 3,392 B | 639x200 | 9,860 B |
| `with-drawing.xlsx` | `14013b5a111a….emf` | 87,004 B | 767x535 | 103,972 B |
| | `3c9e27e68d03….emf` | 6,184 B | 672x404 | 40,065 B |
| | `fdc312bc0dbe….wmf` | 28,696 B | 539x577 | 114,013 B |
| `tiff.docx` | `9006e4b5615b….tiff` | 12,236 B | 640x400 | 3,366 B |

The blank EMF holds no drawing, so the trim leaves a single white pixel; vision answers it as a
blank image (below). The escalation's measurement of `with-drawing.xlsx` at 3x gave 767x536,
672x405, and 540x577; this run's trims are one pixel shorter in one dimension each. The PNG
names are the originals' SHA-256 with `.png`, so the difference changes no name.

Time, the faster of two runs. For an OOXML fixture it is the conversion with the pictures less the
conversion without them, so it is the pictures' alone:

| Fixture | Pictures | Time |
|---|---|---|
| `media.libreoffice.docx` | 1 EMF | 996 ms |
| `word-with-attachments.docx` | 4 EMF, 1 WMF | 2,008 ms |
| `with-drawing.xlsx` | 2 EMF, 1 WMF | 1,552 ms |
| `tiff.docx` | 1 TIFF | 25 ms |
| `media.libreoffice.doc` | 1 EMF, with the legacy conversion | 1,802 ms, the whole conversion |

A document's metafiles cost about one soffice container start, about 1 s, plus a magick trim of a
8.0-megapixel page each. A TIFF costs one magick run.

## Vision over the pictures

`TestVisionPictures` (`office-results/vision.log`) converts every fixture, OOXML and legacy, 34 in
all, and sends each distinct picture handed onward once to Gemma 4 26B-A4B
(`google/gemma-4-26B-A4B-it-qat-q4_0-gguf:Q4_0`, at 1,120 image tokens) with "Describe this
picture in two or three sentences.", one request at a time, temperature 0, reasoning off. Charts
are data in office's model, never pictures, so none is sent. Pictures are distinct by office's
stored name, the SHA-256 of the stored bytes: a picture repeated within or across fixtures, or
carried through a legacy round trip unchanged, is sent once. Image tokens are the request's input
tokens less those of the same request without the picture (22), so they include the few tokens
that mark the image in the chat template.

| # | Picture, as handed onward | Pixels | Bytes | Wall | Image tokens | Output tokens |
|---|---|---|---|---|---|---|
| 1 | `0fcca33bb0ba….png`, in 6 fixtures | 2x2 | 73 | 613 ms | 83 | 33 |
| 2 | `10a86bd779a8….png`, in 6 fixtures | 2x2 | 73 | 398 ms | 83 | 28 |
| 3 | `670be6349ed8….emf` as PNG, blank | 1x1 | 160 | 552 ms | 83 | 29 |
| 4 | `df026100fd6f….png`, in 2 fixtures | 2x2 | 73 | 393 ms | 83 | 27 |
| 5 | `805924e65dff….png`, in 2 fixtures | 2x2 | 73 | 466 ms | 83 | 33 |
| 6 | `2156195ef201….emf` as PNG | 511x155 | 11,618 | 674 ms | 82 | 38 |
| 7 | `8e362ed59772….emf` as PNG | 339x187 | 15,735 | 1,013 ms | 86 | 64 |
| 8 | `90f616810030….wmf` as PNG | 236x193 | 13,165 | 853 ms | 82 | 52 |
| 9 | `cc90707a7aab….emf` as PNG | 1489x566 | 9,952 | 1,478 ms | 374 | 67 |
| 10 | `f35ded1fae0a….emf` as PNG | 639x200 | 9,860 | 780 ms | 77 | 43 |
| 11 | `14013b5a111a….emf` as PNG | 767x535 | 103,972 | 804 ms | 178 | 38 |
| 12 | `3c9e27e68d03….emf` as PNG | 672x404 | 40,065 | 764 ms | 114 | 41 |
| 13 | `8345fcf9642a….jpeg`, as stored | 192x176 | 11,988 | 454 ms | 83 | 31 |
| 14 | `90a2c8be8792….png`, as stored | 130x92 | 3,043 | 651 ms | 82 | 47 |
| 15 | `fdc312bc0dbe….wmf` as PNG | 539x577 | 114,013 | 817 ms | 134 | 43 |
| 16 | `9006e4b5615b….tiff` as PNG | 640x400 | 3,366 | 863 ms | 106 | 49 |
| | **16 pictures** | | | **11.6 s** | **1,813** | **663** |

What the run shows:

- **Every request completed** with a description, and each describes its picture: the solid
  color squares as solid colors, the metafiles' text in Russian, English, and Uzbek with
  translations, the clip art (a cow, a wrench, Santa Claus, a tiger), and the TIFF's "TEST TIFF".
  The descriptions weren't scored; they are in `vision.log`.
- **A small picture costs a floor of about 82 to 83 image tokens,** whatever its pixels: a 1x1,
  a 2x2, a 236x193, and a 511x155 all cost 82 or 83. A tiny picture costs as much as a modest
  one, as if the vision encoder brings every image up to a minimum size; the run doesn't show
  whether it does. One picture fell below the floor in this run, `f35ded1fae0a…` at 639x200, a
  wide, short strip, at 77; the earlier runs put the floor at 82 to 83 for every small picture.
- **Cost follows area above the floor:** 539x577 costs 134, 767x535 costs 178, and 1489x566 costs
  374, the largest by far. The metafile scale sets this: at the default 3, the biggest metafile
  costs about 4.5 times the floor.
- **The blank metafile is answered as a blank square:** the 1x1 PNG of the empty EMF costs the
  floor and the model says it is a blank white image.
- **Dedupe by office's stored name pays:** 34 fixtures hand on 16 distinct pictures. The four
  solid-color PNGs alone appear in 16 places across the OOXML fixtures and their legacy round
  trips, which keep the pictures' bytes, so they hash to the same names.

## What is answered, and what isn't

Answered, for evidence 12's legacy part:

- What the LibreOffice round trip loses, per element kind and per format, for office's generated
  fixtures; and that three small Office-written files, one per format, lose nothing against hand
  truth.
- The container's footprint: its compressed and on-disk size, and each conversion's wall time,
  first and repeat, which are the same.
- That every EMF, WMF, and TIFF picture in the fixtures converts to a PNG vision reads,
  reproducibly, and what each costs in time and image tokens.

Not answered:

- **Loss below office's model.** Formatting, styles, fonts, and layout aren't measured, since
  office doesn't model them.
- **Which direction loses.** The generated fixtures went through LibreOffice twice, out to the
  legacy format and back; a file Office wrote might keep the headings and lists that these lose,
  and the three real files are too plain to say.
- **Peak memory** of the container, which the brief left out, and the footprint on arm64: only
  the linux/amd64 manifest's size and the laptop's times were measured.
- **The pull's time** on this run: the image was already present.
- **How good the descriptions are.** The vision tier asserts completion only; nothing was scored
  against a hand-written description.
- **Whether a picture is visible.** Pictures are listed and sent to vision as the document holds
  them; office's model has no position, so one that another covers, that sits off the slide or
  page, or that is cropped away is still sent. `slide-objects`'s slide 3 holds a red square under
  a blue one at the same position and size: both are listed and described, though only the blue
  one shows (found by the architect while checking the results by hand).
