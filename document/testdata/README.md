# testdata

The document fixtures: letter-size PDFs with each page's ground truth, in three sets. Each set is
one multi-page PDF, so a renderer test names a page of it and a page stream test takes the whole
PDF. The clean and degraded sets are generated from `clean.typ`; the scanned set is real scans.

- `clean.pdf` is six pages, one layout each: prose, two columns, a table, 8 pt print, a filled-in
  form, and a figure (shapes only) with a caption.
- `degraded.pdf` is nine pages made from the clean pages rendered at 150 dpi in grayscale, each
  degraded by ImageMagick and wrapped back into PDF at 150 dpi. Each fault comes at a moderate and
  a severe level, and the last page has all four:

  | Page | Clean page | Fault | ImageMagick operators |
  | ---- | ---------- | ----- | --------------------- |
  | 1 | 1, prose | skew, moderate | `-rotate 2.5` |
  | 2 | 3, a table | skew, severe | `-rotate 7` |
  | 3 | 3, a table | fade, moderate | `+level 55%,100%` |
  | 4 | 5, a form | fade, severe | `+level 78%,96%` |
  | 5 | 5, a form | speckle, moderate | `-seed 7 -attenuate 0.6 +noise Impulse` |
  | 6 | 6, a figure | speckle, severe | `-seed 11 -attenuate 1.2 +noise Impulse` |
  | 7 | 2, two columns | blur, moderate | `-blur 0x1.8` |
  | 8 | 1, prose | blur, severe | `-blur 0x3` |
  | 9 | 2, two columns | all four | `-rotate 4`, `-blur 0x1.2`, `+level 40%,100%`, `-seed 13 -attenuate 0.5 +noise Impulse` |

  A rotated page is cropped back to letter size around its center (`-gravity center -extent
  1275x1650`) on white.
- `scanned.pdf` is six pages of a real scan, described below.
- `clean/page-N.txt`, `degraded/page-N.txt`, and `scanned/page-N.txt` are each page's ground
  truth: its plain text in reading order. Blocks are separated by a blank line; two columns read
  left column first, a table reads row by row with its cells joined by single spaces, and a form
  field reads as its label, a colon, and its value. A clean page's truth is taken from the source:
  `clean.typ` keeps every page's text as data, sets it on the page, and records it in a `<truth>`
  metadata element, which the generator queries. A degraded page's truth is its clean page's.

## The clean and degraded sets

typst never runs in the check. `generate.sh` regenerates the clean and degraded sets; it needs
typst 0.15.1 and ImageMagick 7.1.2 on the laptop, and jq, which mise declares:

```sh
mise exec -- document/testdata/generate.sh
```

The outputs are byte-reproducible: typst sets no document date and uses only its embedded fonts,
the speckle's noise is seeded, and the degraded PDF's dates are fixed by
`-define pdf:create-epoch=1767225600 -define pdf:modify-epoch=1767225600` and its identifier left
out by `-define pdf:no-identifier=true`. Two runs give the same bytes, and the generator prints
these SHA-256 sums:

```
d849622ac7cc02d640e15ec590897ccdd6cb1ac514be4f865f659558a5ab16bc  clean.pdf
744df9c9858c518bb5dbe7b6d61b917e2ca2a2d37b4d05c243f34514396048ef  clean/page-1.txt
de0904965c6dafe979524fa693cf8d5b01a0c7fe16ee9296574589c8d40e4e24  clean/page-2.txt
53d92d6a53cc7e56e4fb482d3fc6ccd87256f753b2a147c5bff40be872eb7571  clean/page-3.txt
11acf397443c188b6af660b11a566989fa26f062f79d7f58b97c6ed7846d7502  clean/page-4.txt
9e7a3885a7ecf55dde1cb5619fd6035f029bf97895f898e64b28eee6d8835cca  clean/page-5.txt
5fe96350a3380240f926fa888fa33be02408740ca646345e228ec92b52d142a8  clean/page-6.txt
e544e7ec4db9971cccd5d8ba83bf875d1d02272b5ab354044474d2e2896a2849  degraded.pdf
744df9c9858c518bb5dbe7b6d61b917e2ca2a2d37b4d05c243f34514396048ef  degraded/page-1.txt
53d92d6a53cc7e56e4fb482d3fc6ccd87256f753b2a147c5bff40be872eb7571  degraded/page-2.txt
53d92d6a53cc7e56e4fb482d3fc6ccd87256f753b2a147c5bff40be872eb7571  degraded/page-3.txt
9e7a3885a7ecf55dde1cb5619fd6035f029bf97895f898e64b28eee6d8835cca  degraded/page-4.txt
9e7a3885a7ecf55dde1cb5619fd6035f029bf97895f898e64b28eee6d8835cca  degraded/page-5.txt
5fe96350a3380240f926fa888fa33be02408740ca646345e228ec92b52d142a8  degraded/page-6.txt
de0904965c6dafe979524fa693cf8d5b01a0c7fe16ee9296574589c8d40e4e24  degraded/page-7.txt
744df9c9858c518bb5dbe7b6d61b917e2ca2a2d37b4d05c243f34514396048ef  degraded/page-8.txt
de0904965c6dafe979524fa693cf8d5b01a0c7fe16ee9296574589c8d40e4e24  degraded/page-9.txt
```

## The scanned set

`scanned.pdf` is six pages of herald's `_project/marked-documents/marked-documents.pdf`, at herald
commit `bdacbb699c7979ff41ff29f6c32c8a6962b94c2c` (the last to touch `_project/marked-documents`):
27 pages a Lexmark CX725 scanned at 300 dpi, each page one JPEG image with no text layer. They are
redacted documents; the six chosen carry the most legible text outside the redactions:

| Page | Source page | What it shows |
| ---- | ----------- | ------------- |
| 1 | 2 | a printed web page: its title, navigation, and footer URL and date |
| 2 | 3 | classification banners, a red EXPEDITE stamp, and a date |
| 3 | 12 | a name and three classification blocks in a sans face |
| 4 | 13 | a printed message: header, a name, two classification blocks, a date |
| 5 | 16 | a letterhead: banners, a city line, and a date |
| 6 | 25 | an email's header labels and two classification blocks in a typewriter face |

qpdf 12.4.2 copied the pages without re-encoding their scans, so each page keeps its source
JPEG byte for byte; `--deterministic-id` makes the output reproducible:

```sh
qpdf --deterministic-id --empty \
  --pages ~/code/herald/_project/marked-documents/marked-documents.pdf 2,3,12,13,16,25 -- \
  document/testdata/scanned.pdf
```

Each page's truth was transcribed by reading the source page's image
(`_project/marked-documents/images/marked-document.N.png`): the legible text in reading order, in
the conventions above. Redacted text is left out, as are seals, stray marks, and handwritten
initials; a stamp is kept when its text is clearly legible, as page 2's EXPEDITE is. A label whose
value is redacted reads as the label alone. Their SHA-256 sums:

```
fa8e3ac6aa9143be621c18e998a9791bf991f7f381718abab081b4003e1f4e9d  scanned.pdf
9ef621af40c6bacd04b62367d2f333230907d8bba4e799a88e899111292d6937  scanned/page-1.txt
ed03efd95ea4fbb919dcfe228b85da9af702e012f96c7f3873ddfc4f04216a84  scanned/page-2.txt
80a17e23d142dc1ffb7e99d5635b7a904e1e2e13400d649344194efee68098a6  scanned/page-3.txt
c78b22d17c06925c3ed829a4d3cf163b61fa4f741ad33625df302aa039321b53  scanned/page-4.txt
24a234d6f2ee02d763a5858ed594519d1acae0f0daf9cccb2c548051352f6df1  scanned/page-5.txt
75184a354cef398e536a1953112aa930879cafd6a99260fe325f8960e8188f59  scanned/page-6.txt
```
