# testdata

The document fixtures: letter-size PDFs with each page's ground truth, generated from
`clean.typ`. Each set is one multi-page PDF, so a renderer test names a page of it and a page
stream test takes the whole PDF.

- `clean.pdf` is six pages, one layout each: prose, two columns, a table, 8 pt print, a filled-in
  form, and a figure (shapes only) with a caption.
- `degraded.pdf` is four pages made from the clean pages rendered at 150 dpi in grayscale, each
  degraded by ImageMagick and wrapped back into PDF at 150 dpi: page 1 is clean page 1 skewed
  2.5 degrees, page 2 is clean page 3 faded (`+level 45%,100%`), page 3 is clean page 5 speckled
  (seeded impulse noise), and page 4 is clean page 2 blurred (`-blur 0x1.4`).
- `clean/page-N.txt` and `degraded/page-N.txt` are each page's ground truth: its plain text in
  reading order, taken from the source. `clean.typ` keeps every page's text as data, sets it on the
  page, and records it in a `<truth>` metadata element, which the generator queries. Blocks are
  separated by a blank line; two columns read left column first, a table reads row by row with its
  cells joined by single spaces, and a form field reads as its label, a colon, and its value. A
  degraded page's truth is its clean page's.

typst never runs in the check. `generate.sh` regenerates every file here; it needs typst 0.15.1 and
ImageMagick 7.1.2 on the laptop, and jq, which mise declares:

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
c418fb8ac89afcb4bf533c8432ddc4680ee95c365259cb25d0062b180f2a037f  degraded.pdf
744df9c9858c518bb5dbe7b6d61b917e2ca2a2d37b4d05c243f34514396048ef  degraded/page-1.txt
53d92d6a53cc7e56e4fb482d3fc6ccd87256f753b2a147c5bff40be872eb7571  degraded/page-2.txt
9e7a3885a7ecf55dde1cb5619fd6035f029bf97895f898e64b28eee6d8835cca  degraded/page-3.txt
de0904965c6dafe979524fa693cf8d5b01a0c7fe16ee9296574589c8d40e4e24  degraded/page-4.txt
```
