# testdata

The inspect package's synthesized hazard fixtures, each the smallest file that raises the checks
it is for, and the clean tars and gzip streams the dispatcher's archive stage reads beside them.
`~/docs`' archive hazards aren't redistributable, so none is copied; these stand in for them, and
`TestDocs` reads `~/docs` itself when the machine has it. `generate.go` wrote them, every byte
fixed, so a rerun writes the same files: run `go run generate.go` in this directory. The check
never runs it, and reads the committed files. All are this repository's, Apache-2.0.

| File | Bytes | SHA-256 |
| --- | --- | --- |
| `absolute.tar` | 3072 | `d8146189af3ff088d8328427931819b89146fb232fb4f60d3b71f17bad9380ea` |
| `absolute.zip` | 336 | `50d6ba8a1d67d525d00a83f3a74ca96adf7aceb80c2ef3d5b84f3c9e866bdc51` |
| `bomb.tar.gz` | 8240 | `14cc6b275ea7d8836f81ef9cb860356f112984ee4a4d45470b28543a08734ff8` |
| `bomb.zip` | 8473 | `90bfb0d2d76025008d1e4833792acc505cf83935e0d7e72a94e8578269ff7e7b` |
| `clean.tar` | 3584 | `e5adff3dd80c27cde03daddde4b2a8558bc39cc174008a9e6f28789df598d4a9` |
| `clean.tar.gz` | 218 | `fa7330ad4b06e9bd76dacd32c5274fce8095ec8221c58ae6a36ecf4a62934af3` |
| `declared-bomb.zip` | 235 | `026dc824b90a2f23587100c0002be00fbb495f0d1c4d07e50e046099381480fd` |
| `device.tar` | 3584 | `d2bbf5b13104ca2f52fad20a7cbbc0b517eb7eed69e61f3585b20e393ea70828` |
| `encrypted.xlsx` | 1024 | `f0835a74c18757ac8401d4bdea75e25976b66c1c5b33156ff8325e217ddc96dd` |
| `encrypted.zip` | 300 | `5f4ece7d08324ac0484554f36a6eaa7565a10795a36962f53897d3e42b0af8a0` |
| `hardlink.tar` | 2560 | `0e2a486a5db3844c2ebc0f7a0f7115e0d0d60ccacc533c666c9f1b923c7c9c0c` |
| `hyperlink.docx` | 1417 | `47c14f5431b38bd39ae76de5adf30a461bc76e7c735c55f22052c314f7232447` |
| `invoice.pdf.exe` | 128 | `cbcb41c3a3adf01c68ffb4774e8fe6751e2c0aa9d4f107ffe8788fa1efa2313f` |
| `javascript.pdf` | 386 | `0e4e1af14eb92b29a67e1ef3b863477f6448e19fed307f0269c0d34a551af3c6` |
| `macro.docm` | 2746 | `79ba5fa3dc140f3d7e966bdc1f2258c18ada508cb76e022c128375dc01f98f2d` |
| `mismatch.pdf` | 33 | `9eaf66cb2ab9389f42744617015dfdbfa9a382221513149241ab355a2bd32ea4` |
| `polyglot.jpg` | 190 | `d1c17866e30e36d4e5b8040631e3c42bd45a9415b2b5d44ef52b19010f7b1216` |
| `polyglot.png` | 48 | `cbd5685675f5c3a29790e5f2b5a5f5cd7aacb999e70cd03621952976302ce264` |
| `script.svg` | 543 | `be93fc0e63c0e3e9d4d3ea8d9aa52c3b8cdd3227d1e88ff4c8104540d2ca5783` |
| `sparse-bomb.tar` | 2048 | `31c60295ae2a76c9cd58239726270eacb7d9a5acad061b54776e1c36c3f94bb3` |
| `symlink.tar` | 2560 | `d1ddfb6699bd0ce451daaf8c97c53ce85cf5218dd00bda61ed7f18308ac0ca0f` |
| `symlink.zip` | 315 | `2fbfbc130f0131cde3dcdfe15e76303ac92394490dafe3b3d98e3b8f13f3d34c` |
| `traversal.tar` | 3072 | `228dfa349c95a48149543459c26ea0603e205d0eca75130cc36fe936f227bd91` |
| `traversal.tar.gz` | 186 | `ecc948f70cb7298969f2bbd0f30ca31205f79ae0efc5be03744c387bf8c6ae68` |
| `traversal.zip` | 371 | `9b7f69a81b3f8fb353da43685093219d9a370d0d4df10e7dcdd04c8f33750577` |
| `unnamed.tgz` | 208 | `543030f5bcd7cbbbbf076657dd7ad8e1c4ac68a65b2d0517a268a5b06ea21e33` |

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
- `clean.tar`: a POSIX tar of a directory, `docs/`, and a text and a 2x1 PNG's signature under
  it, `docs/notes.txt` and `docs/pixel.png`: no hazard.
- `clean.tar.gz`: `clean.tar` as a gzip stream whose header names it `clean.tar`.
- `unnamed.tgz`: the same tar as a gzip stream whose header names nothing, so its content is
  named by the stream's stem, `unnamed.tar`.
- `traversal.tar`: an ordinary entry and `docs/../../evil.txt`, a ".." element.
- `traversal.tar.gz`: `traversal.tar` as a gzip stream: the stream is safe, the tar it holds
  isn't.
- `absolute.tar`: `/tmp/evil.txt`, an absolute path, and `C:\evil.txt`, a drive letter.
- `symlink.tar`: an ordinary entry and `passwd`, a symbolic link to `/etc/passwd`.
- `hardlink.tar`: an ordinary entry and `shadow`, a hard link to `/etc/shadow`.
- `device.tar`: an ordinary entry, `null`, a character device 1:3, `sda`, a block device 8:0, and
  `pipe`, a FIFO.
- `sparse-bomb.tar`: one old GNU sparse entry, written by hand, since archive/tar writes none,
  whose header declares 4 GiB, all a hole but its last 512 bytes, the only bytes stored, in a
  2 KiB tar: past both default thresholds by its header alone, and as written once its hole is
  read as zeros.
- `bomb.tar.gz`: a tar of 8 MiB of zeros in one entry as a gzip stream, about 1,018:1 as it
  decompresses: past the default ratio, under the default 1 GiB, as `bomb.zip` is, so a test
  lowers the size to see the rule fire. The tar it holds is no bomb.
