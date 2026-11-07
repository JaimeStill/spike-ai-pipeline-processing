# testdata

The picture package's fixtures, for the integration tier's conversions on the host's magick: its
SVGs, and its TIFF, HEIC, WebP, and AVIF pictures. They are committed and never generated in the
check.

| File | Source | License | Bytes | SHA-256 |
| --- | --- | --- | --- | --- |
| `shapes.svg` | written for this package | Apache-2.0, this repository's | 681 | `e85f8bee8ba14bdd82b211d973e75d9d47a7a2b4f20dc825d0d787aa0e454d7e` |
| `ink.svg` | written for this package | Apache-2.0, this repository's | 1073 | `293e12c802db06a0ae032a401f335e394a4ff62f69fa90ab7242acd77b58b826` |
| `script-bearing.svg` | `~/docs`' `image/vector/script-bearing.svg` | ~/docs' "Generated for this pool", redistributable | 648 | `a44d794e6708452ed62b56b8066b5c886d859feb395e7b97d300724eb7607950` |
| `pages.tiff` | generated for this package | Apache-2.0, this repository's | 840 | `5828cfd89abfcce70cc77a385da33b2ecbcae257e4cf0c9fc3b0da182e49eb3e` |
| `two-images.heic` | generated for this package | Apache-2.0, this repository's | 1488 | `4029abf08a0f58f3c8f7e33f5d7f2eb9d1e4d8cb0aefe684768d341618c377cc` |
| `oriented.webp` | generated for this package | Apache-2.0, this repository's | 100 | `26af1e195bf5ab390081b5a21a9fd9eada865baf752afab8be7689142d098dfc` |
| `animated.webp` | generated for this package | Apache-2.0, this repository's | 188 | `7f00b22baa77f7b7b522bb63eda272112c5efc8b968392cd05017ae3c527e4b6` |
| `animated.avif` | generated for this package | Apache-2.0, this repository's | 1234 | `107fc1d31eeee15ba1b071bc626ffc436c062db296c1ee005ea90e0d9f8472ac` |

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
- `pages.tiff`: three pages, 40x30 white, 50x30 mid-gray, and 60x30 black, so each page's size
  tells its place.
- `two-images.heic`: two top-level images, each with a thumbnail. The primary, item 1, is stored
  48x32, its left half white and its right red, with an irot box turning it a quarter turn
  clockwise, so upright it is 32x48, white above red; the other, item 4, is 32x32 blue.
- `oriented.webp`: the same 48x32 picture, lossless, under EXIF orientation 6, so upright it is
  32x48, white above red.
- `animated.webp` and `animated.avif`: three lossless frames of 24x16, red, green, and blue, so
  the first frame is red.

The rasters were generated once on Arch Linux, with ImageMagick 7.1.2-34, libheif 1.23.6's
heif-enc, and libavif 1.4.2's avifenc, in an empty directory:

```sh
magick -size 40x30 xc:white -size 50x30 xc:gray50 -size 60x30 xc:black -compress zip pages.tiff
# primary.jpg: plain.jpg with an APP1 Exif segment, written by hand after its SOI marker, whose
# IFD holds one entry, Orientation (0x0112), SHORT, 6.
magick -size 48x32 xc:red -fill white -draw "rectangle 0,0 23,31" -quality 100 plain.jpg
magick -size 32x32 xc:blue second.png
heif-enc -q 90 -t 16 primary.jpg second.png -o two-images.heic
magick primary.jpg -define webp:lossless=true oriented.webp
magick -size 24x16 xc:red -size 24x16 xc:lime -size 24x16 xc:blue -set delay 10 -loop 0 \
  -define webp:lossless=true animated.webp
for c in red lime blue; do magick -size 24x16 xc:$c f-$c.png; done
avifenc -l f-red.png f-lime.png f-blue.png animated.avif
```

heif-enc turns the JPEG's EXIF orientation into the HEIC's irot box. Their conversions are
checked by size, page order, and pixel, and by two conversions giving the same bytes; no output
sum is recorded, since magick and its libraries are the host's, not pinned, and their PNG bytes
may differ from one build to another.
