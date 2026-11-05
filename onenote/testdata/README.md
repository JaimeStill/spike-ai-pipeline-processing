# testdata

The onenote package's fixtures. They are committed and never generated or fetched in the check.

## onenote

`onenote/` is a copy of
[spike-ai-pipeline-onenote](https://github.com/JaimeStill/spike-ai-pipeline-onenote)'s committed
fixtures and expectations at its tag `v0.1.0` (commit
`8ea6b2df4236c23ed1da6d37c73ed8479dd75f82`), byte for byte: its `tests/fixtures/` in
`onenote/fixtures/`. `onenote/MANIFEST` pins the copy: the repository, the tag, the commit, and
each file's SHA-256; the check verifies the copy against it offline. `mise run fixtures:onenote`
refreshes the copy from the tag on the remote and reports any drift against the manifest.

The fixtures' sources and licenses are onenote's, kept beside them as onenote keeps them: two
public sets, each with its `LICENSE` and its `sources.json`, which lists every file with its
source path, its pinned commit, its license, and its SHA-256.

- `fixtures/interop/`: Microsoft's
  [Interop-TestSuites](https://github.com/OfficeDev/Interop-TestSuites) MS-ONESTORE sections and
  notebooks at commit `fe87ed32`, under the MIT License, among them `Encryption.one`, a
  password-protected section, refused as an input and skipped inside its notebook.
- `fixtures/onenote-rs/`: onenote_parser's maintainer samples from
  [msiemens/onenote.rs](https://github.com/msiemens/onenote.rs) at `v2.1.0` (`727389a`), under
  the MPL-2.0: sections, a section group, a notebook with a recycle bin, OneDrive-format
  sections, and `handwriting_recognition.one`, the ink and recognition sample.

The expectations are onenote's, hand-written from onenote_parser's API, not from the CLI's
output: `sections.json` and `notebooks.json` give each input's exit code, notebook, sections,
pages, titles, times, container counts, skipped sections, and warnings, and `content.json` content
fields, each a JSON pointer into a page line and its value. The real-binary tests read every
`.one` and `.onetoc2` here and hold each read to them, and to onenote's own output for the same
input.

The three large sections onenote's finding times, `LargeData.one` (11 MB) from
Interop-TestSuites and `Large Desktop.one` (113 MB) and `Large OneDrive.one` (161 MB) from
onenote.rs, are never committed here.
