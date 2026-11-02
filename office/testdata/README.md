# testdata

The office package's fixtures. They are committed and never generated in the check.

## OOXML

`ooxml/` holds six of office's own fixtures, copied byte for byte from
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

What each covers, as office's runs over them show:

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
