# testdata

The office package's fixtures. They are committed and never generated in the check.

## OOXML

`ooxml/` holds sixteen of office's own fixtures, copied byte for byte from
[spike-ai-pipeline-office](https://github.com/JaimeStill/spike-ai-pipeline-office) at its tag
`v0.1.0` (commit `b9bb1a20839482ba7e2312ee897366791992fff0`), each from the path below its
`fixtures/` directory, with its name kept. Between them they hold docx, xlsx, and pptx, charts, a
block of every kind office's JSON model, schema "1", has, the footnote, endnote, and comment
references, and pictures of every media kind (PNG, JPEG, EMF, and WMF), and a linked picture.

| File | Path at the tag | License | Bytes | SHA-256 |
| --- | --- | --- | --- | --- |
| `media.libreoffice.docx` | `generated/docx/media.libreoffice.docx` | Apache-2.0, office's | 22180 | `07f3a8c5d58d9345fcbbf42b725ee32c92ed722bc9de31d5fad5f84169159af7` |
| `notes.libreoffice.docx` | `generated/docx/notes.libreoffice.docx` | Apache-2.0, office's | 22134 | `f9cafb26909d822de42cd4357b6b207719429fbafa301c405dc695f3457f61c6` |
| `word-with-attachments.docx` | `realworld/docx/word-with-attachments.docx` | Apache-2.0, Apache POI's | 144959 | `90352263823e6d3d39a4ec88212134b74bcb0f86b603bc3b51d712a3ec3df9f2` |
| `drawings.libreoffice.xlsx` | `generated/xlsx/drawings.libreoffice.xlsx` | Apache-2.0, office's | 13789 | `9c48b55053519fecff0d4fe116a0c5da62f92fb5760bf5b0ac642229a4d4e923` |
| `with-drawing.xlsx` | `realworld/xlsx/with-drawing.xlsx` | Apache-2.0, Apache POI's | 101696 | `97b1ab359d6aecbe37864f30c85106e386f0a19bea8f290103927362a2701066` |
| `slide-objects.libreoffice.pptx` | `generated/pptx/slide-objects.libreoffice.pptx` | Apache-2.0, office's | 40630 | `7c0b5e40f22001166b9e4ee6f3b8e68a5c21fc6ea73e8ed77e15caffd4918995` |
| `tables.libreoffice.docx` | `generated/docx/tables.libreoffice.docx` | Apache-2.0, office's | 20673 | `01116cf56ffbb4ad1f360272e2fabd8cd6e60d25e8081202f4b2d0aad91b7d2c` |
| `lists.libreoffice.docx` | `generated/docx/lists.libreoffice.docx` | Apache-2.0, office's | 20741 | `261277388b4c59a885f5060a9db58ba96c3c128ea528a6a26dd0747870725ad6` |
| `comments.libreoffice.docx` | `generated/docx/comments.libreoffice.docx` | Apache-2.0, office's | 21603 | `e683efab836f1e27e1abd65c58d34df37cdabb52d1214f2fabba28f5a0f5ef43` |
| `charts.libreoffice.docx` | `generated/docx/charts.libreoffice.docx` | Apache-2.0, office's | 24016 | `c49582d00fc63052e4c2a0bb04a64d8fd45933c39539a82434f9bbc7e24c0f0d` |
| `sections.libreoffice.docx` | `generated/docx/sections.libreoffice.docx` | Apache-2.0, office's | 34217 | `dbe71997cd80eec4639a4974a9694d4c658f2f11d5165f93ca9ce4c6b909df5b` |
| `cells.libreoffice.xlsx` | `generated/xlsx/cells.libreoffice.xlsx` | Apache-2.0, office's | 7534 | `86f57558bc76ae5e344560c1db633bdc408782f5f241b5570a14a93429658f54` |
| `formulas.libreoffice.xlsx` | `generated/xlsx/formulas.libreoffice.xlsx` | Apache-2.0, office's | 6502 | `00e94e40ef94156339a9d2cdbbf271bb2cd48069c93e534baa6e8aa3402dfd28` |
| `merges.libreoffice.xlsx` | `generated/xlsx/merges.libreoffice.xlsx` | Apache-2.0, office's | 6831 | `b5ba5ca9175ab45537835858f737198560888ca45ddc11df2a6022db013288e1` |
| `slides.libreoffice.pptx` | `generated/pptx/slides.libreoffice.pptx` | Apache-2.0, office's | 42089 | `956895b6d18ea6609ac03cbb8e723dd639b131beade1b4d2bde17da2ae71736b` |
| `bullets.libreoffice.pptx` | `generated/pptx/bullets.libreoffice.pptx` | Apache-2.0, office's | 30190 | `b90c750a44e99340f0eb422337f85269bbd05ebbae99a38e8d0c48ee62f9af18` |

The real-binary tests in the check run over the first six; the other ten are originals of the
generated legacy fixtures below, which the integration tier compares against. What the first
six cover, as office's runs over them show:

- `media.libreoffice.docx`: headings, paragraphs, a table, PNG pictures (one repeated through
  the same part and through a part of its own, one in a table cell, one in a second section), an
  EMF, and a linked picture.
- `notes.libreoffice.docx`: footnotes and endnotes and their references, and a list.
- `word-with-attachments.docx`: comments and their references, a table, and OLE objects' WMF
  and EMF previews.
- `drawings.libreoffice.xlsx`: sheets, their tables of cells, a chart, and PNG pictures.
- `with-drawing.xlsx`: a JPEG, a PNG, two EMFs, and a WMF on one sheet.
- `slide-objects.libreoffice.pptx`: slides, a title, a table, a chart, and PNG pictures.

The generated files are office's own work, written by its `fixtures/generate.py` and re-saved by
LibreOffice, under office's license, Apache-2.0, the same license and holder as this
repository's `LICENSE`. The two real-world files are Apache POI's test data
(`test-data/document/WordWithAttachments.docx` and `test-data/spreadsheet/WithDrawing.xlsx` at
apache/poi commit `6327f056ba3cfdae326a0261a236460592b80f5c`), Apache-2.0, © The Apache Software
Foundation; its license and NOTICE, as office keeps them, are in `licenses/poi.LICENSE` and
`licenses/poi.NOTICE`. office's `fixtures/realworld/SOURCES.md` gives their provenance in full.

## Pictures

`pictures/tiff.docx` holds a TIFF picture, which no fixture above does: none of office's
fixtures holds one, and the two OOXML files in Apache POI's test data that hold one
(`test-data/slideshow/bug65228.pptx` and `ArtisticEffectSample.pptx`) keep it where office
extracts no picture from. `pictures/generate.sh` wrote it, once, and the check never runs it: it
downloads Apache POI's `test-data/slideshow/testtiff.tif` at commit
`6327f056ba3cfdae326a0261a236460592b80f5c`, checks it against its SHA-256, and packs it byte for
byte, as `word/media/image1.tiff`, into the smallest WordprocessingML package that shows it, one
paragraph and the picture inline, every entry at a fixed time, so a regeneration writes the same
bytes. The TIFF is Apache-2.0, © The Apache Software Foundation; POI's license and NOTICE are in
`licenses/poi.LICENSE` and `licenses/poi.NOTICE`. The package around it is this repository's.

| File | Source | License | Bytes | SHA-256 |
| --- | --- | --- | --- | --- |
| `tiff.docx` | `pictures/generate.sh` | Apache-2.0 | 11705 | `d1d54755de784b7662a1b7e430bef7afb6ed8d3b0336fba4550f39e605f6f8dc` |
| its `word/media/image1.tiff` | `test-data/slideshow/testtiff.tif` at POI's commit | Apache-2.0, Apache POI's | 12236 | `9006e4b5615beb93d5ff99a79ed62551a523ec8b0e2f881b074589d28c47b119` |

The TIFF is 640x400, 8-bit RGB, LZW-compressed, written by GIMP.

## Legacy, generated

`legacy/generated/` holds every `*.libreoffice.*` file in `ooxml/` saved by LibreOffice as `.doc`,
`.xls`, or `.ppt`, its name kept with the legacy extension: seven docx files as `.doc`
(`charts`, `comments`, `lists`, `media`, `notes`, `sections`, `tables`), four xlsx as `.xls`
(`cells`, `drawings`, `formulas`, `merges`), and three pptx as `.ppt` (`bullets`, `slide-objects`,
`slides`). `legacy/generate.sh` wrote them, running soffice as the office package runs it: in the
picture package's default image, `picture.LibreOfficeImage`
(`gotenberg/gotenberg:8.39.0-libreoffice@sha256:4c0eb92ef22e5d5fb0a49eac924b63d85d0b0334ea3365a1f5ca428f367b3054`),
LibreOffice 26.8.1.1, under the container runner's lockdown, with the filters `MS Word 97`,
`MS Excel 97`, and `MS PowerPoint 97`. The script writes `legacy/generated/MANIFEST`, which
records the image, soffice's `--version` line, and each file's SHA-256. LibreOffice's binary
formats carry the time they were saved, so a regeneration's sums differ from the committed
files'; the manifest is the committed files' record. They are office's fixtures re-saved, under
office's license, Apache-2.0.

## Legacy, real

`legacy/real/` holds three files Word, Excel, and PowerPoint wrote, one per legacy format, copied
byte for byte from [Apache POI](https://github.com/apache/poi)'s test data at commit
`6327f056ba3cfdae326a0261a236460592b80f5c`, each checked against the blob at that commit, with
its name kept. They are Apache-2.0, © The Apache Software Foundation; POI's license and NOTICE
are in `licenses/poi.LICENSE` and `licenses/poi.NOTICE`. Each file's creating application is its
OLE2 summary information's.

| File | Path at the commit | Written by | Bytes | SHA-256 |
| --- | --- | --- | --- | --- |
| `simple-table.doc` | `test-data/document/simple-table.doc` | Microsoft Word 8.0 (Word 97) | 19456 | `67d7fe8e0555c16516354498a5935f24eeef90beb5efbf526f93a1d6c734da43` |
| `SampleSS.xls` | `test-data/spreadsheet/SampleSS.xls` | Microsoft Excel | 17408 | `53c9bd7337aa79fedf29cef8d81ec47fff39f80e25da9d74bc354cfdf1c43b73` |
| `basic_test_ppt_file.ppt` | `test-data/slideshow/basic_test_ppt_file.ppt` | Microsoft Office PowerPoint | 15360 | `e8fd4c38ad72ce3735acbca0b60dd5870fb60f819c169e3947d166f4f1bcc79d` |

Each has a hand-written truth beside it, `<name>.truth.json` (`simple-table.truth.json`,
`SampleSS.truth.json`, `basic_test_ppt_file.truth.json`), written from the file as LibreOffice
on the laptop displays it and from the strings in the file, never from this package's output.
It gives the file's elements by kind, each as a key the integration tier also derives from
office's model, its whitespace collapsed:

- `heading`: its level and text, `1:Title`.
- `paragraph`: its text, a table cell's, a list item's, and a slide's notes' included; an empty
  one is left out.
- `list`: `ordered` or `unordered` and its item count, `unordered:3`.
- `table`: its rows and widest row's cells, `2x3`; a sheet's table of cells is `sheet`.
- `cell` and `formula`: a sheet cell's sheet, reference, and value or formula,
  `Sheet1!D7=13` and `Sheet1!D7=SUM(A7:C7)`.
- `sheet`: its name. `slide`: its number.

What they hold:

- `simple-table.doc`: a paragraph, a table of two rows and three columns, and a final paragraph.
- `SampleSS.xls`: three sheets, the third empty; text cells, and a row of numbers summed by a
  formula.
- `basic_test_ppt_file.ppt`: two slides, a title and a two-line subtitle, then a title and three
  bulleted lines, each slide with a line of speaker notes.
