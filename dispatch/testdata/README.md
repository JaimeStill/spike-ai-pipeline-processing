# testdata

The dispatch package's test data. It holds no fixture of its own: the integration tier dispatches
the format packages' committed fixtures in place.

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
