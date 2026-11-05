# testdata

The inspect package's synthesized hazard fixtures, each the smallest file that raises the checks
it is for. `~/docs`' archive hazards aren't redistributable, so none is copied; these stand in for
them, and `TestDocs` reads `~/docs` itself when the machine has it. `generate.go` wrote them, every
byte fixed, so a rerun writes the same files: run `go run generate.go` in this directory. The
check never runs it, and reads the committed files. All are this repository's, Apache-2.0.

| File | Bytes | SHA-256 |
| --- | --- | --- |
| `absolute.zip` | 336 | `50d6ba8a1d67d525d00a83f3a74ca96adf7aceb80c2ef3d5b84f3c9e866bdc51` |
| `bomb.zip` | 8473 | `90bfb0d2d76025008d1e4833792acc505cf83935e0d7e72a94e8578269ff7e7b` |
| `declared-bomb.zip` | 235 | `026dc824b90a2f23587100c0002be00fbb495f0d1c4d07e50e046099381480fd` |
| `encrypted.xlsx` | 1024 | `f0835a74c18757ac8401d4bdea75e25976b66c1c5b33156ff8325e217ddc96dd` |
| `encrypted.zip` | 300 | `5f4ece7d08324ac0484554f36a6eaa7565a10795a36962f53897d3e42b0af8a0` |
| `hyperlink.docx` | 1417 | `47c14f5431b38bd39ae76de5adf30a461bc76e7c735c55f22052c314f7232447` |
| `invoice.pdf.exe` | 128 | `cbcb41c3a3adf01c68ffb4774e8fe6751e2c0aa9d4f107ffe8788fa1efa2313f` |
| `javascript.pdf` | 386 | `0e4e1af14eb92b29a67e1ef3b863477f6448e19fed307f0269c0d34a551af3c6` |
| `macro.docm` | 2746 | `79ba5fa3dc140f3d7e966bdc1f2258c18ada508cb76e022c128375dc01f98f2d` |
| `mismatch.pdf` | 33 | `9eaf66cb2ab9389f42744617015dfdbfa9a382221513149241ab355a2bd32ea4` |
| `polyglot.jpg` | 190 | `d1c17866e30e36d4e5b8040631e3c42bd45a9415b2b5d44ef52b19010f7b1216` |
| `polyglot.png` | 48 | `cbd5685675f5c3a29790e5f2b5a5f5cd7aacb999e70cd03621952976302ce264` |
| `script.svg` | 543 | `be93fc0e63c0e3e9d4d3ea8d9aa52c3b8cdd3227d1e88ff4c8104540d2ca5783` |
| `symlink.zip` | 315 | `2fbfbc130f0131cde3dcdfe15e76303ac92394490dafe3b3d98e3b8f13f3d34c` |
| `traversal.zip` | 371 | `9b7f69a81b3f8fb353da43685093219d9a370d0d4df10e7dcdd04c8f33750577` |

What each holds:

- `traversal.zip`: an ordinary entry and `docs/../../evil.txt`, a ".." element.
- `absolute.zip`: `/tmp/evil.txt`, an absolute path, and `C:\evil.txt`, a drive letter.
- `symlink.zip`: an ordinary entry and `passwd`, a symbolic link to `/etc/passwd` by its Unix
  mode.
- `bomb.zip`: 8 MiB of zeros in one entry, about 1,030:1: past the default ratio, under the
  default 1 GiB, so a test lowers the size to see the rule fire.
- `declared-bomb.zip`: one entry whose central directory declares 4 GiB expanded from 11 bytes,
  past both defaults by the declared sizes alone; its real content is 1 KiB of zeros.
- `encrypted.zip`: an ordinary entry and `secret.txt`, marked encrypted by its general purpose
  flags; its content isn't ciphertext.
- `macro.docm`: a WordprocessingML package with a `word/vbaProject.bin`, an ActiveX part, an OLE
  object embedding, and a remote template, an external attachedTemplate relationship in
  `word/_rels/settings.xml.rels`, beside an external hyperlink, which isn't active content. The
  binary parts are placeholders.
- `hyperlink.docx`: the same package with its external hyperlink alone.
- `javascript.pdf`: a one-page PDF whose catalog runs JavaScript through /OpenAction and again
  through /AA, the second action's /S hex-escaped as /J#61vaScript.
- `script.svg`: an onload handler, a script element, an external xlink:href, and a
  foreignObject, beside an internal href and a data: href, which aren't external.
- `invoice.pdf.exe`: the start of a PE executable, MZ and a PE signature, named with a double
  extension.
- `mismatch.pdf`: a PNG's signature, named .pdf.
- `polyglot.jpg`: a JPEG with a ZIP appended, its end-of-central-directory record at offset 168.
- `polyglot.png`: a PNG's signature with a PDF header after it, at offset 33.
- `encrypted.xlsx`: an OLE2 compound file's signature and its directory's EncryptionInfo and
  EncryptedPackage stream names, the shape the checks read an encrypted OOXML package by; it is
  no readable compound file.
