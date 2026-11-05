# testdata

The dispatch package's test data. It holds no fixture of its own: the integration tier dispatches
the format packages' committed fixtures in place. It holds the integration tier's route truth and
the findings tier's ink truth.

## tier-routes.tsv

`tier-routes.tsv` is the integration tier's route truth, written by hand from the fixtures, their
READMEs, and the expectations the format CLIs ship beside them (mail's goldens and truth files,
onenote's `sections.json`, `notebooks.json`, and `content.json`), not generated from a dispatch's
output. It gives the route and outcome `TestTierRoutes` expects of every committed fixture input,
dispatched in the order the test lists them over one file index, and of the found parts that
matter: each attachment kind, each office medium kind, each OneNote medium, and the ZIP entries.
Its header gives its columns. Content an input shares with one dispatched before it is seen, and
the comments above each group say which content repeats and why. A new fixture input needs a row
here before the tier passes.

## handwriting-truth.txt

`handwriting-truth.txt` is the findings tier's hand transcription of the page ink of onenote.rs's
`handwriting_recognition.one` (`onenote/testdata/onenote/fixtures/onenote-rs/`), which
`TestFindingsInk` scores the vision model's reading and OneNote's stored recognition against: one
line per line of handwriting, top to bottom, as written. It was drafted by the session that wrote
the finding, from the ink rendered by the picture stage at 150 dpi on white (984x936), and is
pending the architect's confirmation. It leaves out what is no text: the wavy line across the top,
and the stray mark, an "L"-like stroke, before the large "Hello World".
