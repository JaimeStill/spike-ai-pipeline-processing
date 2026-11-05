# testdata

The picture package's SVG fixtures, for the integration tier's conversions on the host's magick.
They are committed and never generated in the check.

| File | Source | License | Bytes | SHA-256 |
| --- | --- | --- | --- | --- |
| `shapes.svg` | written for this package | Apache-2.0, this repository's | 681 | `e85f8bee8ba14bdd82b211d973e75d9d47a7a2b4f20dc825d0d787aa0e454d7e` |
| `ink.svg` | written for this package | Apache-2.0, this repository's | 1073 | `293e12c802db06a0ae032a401f335e394a4ff62f69fa90ab7242acd77b58b826` |
| `script-bearing.svg` | `~/docs`' `image/vector/script-bearing.svg` | ~/docs' "Generated for this pool", redistributable | 648 | `a44d794e6708452ed62b56b8066b5c886d859feb395e7b97d300724eb7607950` |

- `shapes.svg`: a rectangle, a circle, a triangle, a rule, and a line of text, on no
  background, 320x160 CSS pixels, so 500x250 at 150 dpi.
- `ink.svg`: an ink-like drawing in the form spike-ai-pipeline-onenote writes a page's ink: a
  viewBox in ink units of 0.01 mm, a size in millimetres, no background, and each stroke an open
  path with round caps and joins. Four strokes write "hi", 40x20 mm, so 236x118 at 150 dpi.
- `script-bearing.svg`: the security fixture of the test corpus at `~/docs`, copied byte for
  byte; its manifest marks it generated for the pool, its license status verified, and
  redistributable. It carries an `onload` handler, an inline `<script>`, and an `<image>` whose
  href is `http://example.invalid/tracker.png`, and its one line of text,
  `Visible text: SAFE-SVG-MARKER`, is all a safe conversion draws.
