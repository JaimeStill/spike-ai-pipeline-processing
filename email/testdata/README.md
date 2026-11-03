# testdata

The email package's fixtures. They are committed and never generated or fetched in the check.

## mail

`mail/` is a copy of [spike-ai-pipeline-mail](https://github.com/JaimeStill/spike-ai-pipeline-mail)'s
committed fixtures and goldens at its tag `v0.1.0` (commit
`75923186494d94ddf37a61b9865c1848bb0bbbd6`), byte for byte: its `tests/fixtures/` in
`mail/fixtures/`, and its `tests/golden/`, mail's own stdout and stderr for each fixture, in
`mail/golden/`. `mail/MANIFEST` pins the copy: the repository, the tag, the commit, and each file's
SHA-256; the check verifies the copy against it offline. `mise run fixtures:mail` refreshes the copy
from the tag on the remote and reports any drift against the manifest.

The fixtures' sources, licenses, and truth files are mail's, kept beside them as mail keeps them:
each directory's `sources.json` and `LICENSE` and `NOTICE` files give the public stores' and
messages' provenance and licenses, and `fixtures/generated/manifest.json` the generated ones'.
The real-binary tests read every `.pst` and `.msg` here and compare their records and warnings
with the goldens.

## built

`built/` holds one `.msg` built for this package, apart from mail's copy and its MANIFEST:
`msg/office-attachments.msg`, built from `eml/office-attachments.eml` by mail's fixtures generator
at its tag `v0.1.0`, in its fixtures mode, through MsgKit. The source attaches five files, byte
for byte. Four are this repository's committed office fixtures: `minutes.docx`, which is
`office/testdata/ooxml/tables.libreoffice.docx`; `budget.xls`, which is
`office/testdata/legacy/generated/formulas.libreoffice.xls`; `scan.tif`, the
`word/media/image1.tiff` member of `office/testdata/pictures/tiff.docx`; and `diagram.emf`, the
`word/media/image4.emf` member of `office/testdata/ooxml/word-with-attachments.docx`. Their
licenses are the office fixtures' (`office/testdata/README.md`). The fifth, `forwarded.msg`, is
the copy of mail's `mail/fixtures/generated/msg/03-html-inline-image.msg`, attached as a file,
which mail reads back by value and writes as it is, not as an embedded message: a by-value `.msg`,
which holds a PNG of its own; its provenance is mail's generated fixtures'.

`mise run fixtures:built` (`scripts/fixtures-built.sh`) writes the source from those fixtures and
rebuilds the `.msg` with the generator, fetched from mail's tag and built in a temporary directory
with the .NET SDK it pins, 10.0.402; it needs network access, and the check never runs it. The
generator writes `layout.json`'s `.msg` and `manifest.json`, its record of the source's and the
`.msg`'s SHA-256 and its tool versions: .NET SDK 10.0.402, MsgKit 3.0.6, and MimeKit 4.18.2.
`MANIFEST` pins the directory: mail's repository, tag, and commit, those tool versions, each
attachment's SHA-256 and the fixture and member it came from, and each file's SHA-256. MsgKit's
`.msg` isn't byte-stable, as mail's manifest notes, so a rebuild changes the `.msg`'s SHA-256: the
tests pin its content, each attachment written by the real mail byte for byte its source, and its
routes, the docx and the xls to office, the TIFF and the EMF to vision through office's picture
conversion, and the `.msg` nested, for a Reader to read again. MsgKit stores the EMF and the `.msg`
as `application/octet-stream`, whatever the source's `image/emf` and
`application/vnd.ms-outlook`; the route reads their signatures.
