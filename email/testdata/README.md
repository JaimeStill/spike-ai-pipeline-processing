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
