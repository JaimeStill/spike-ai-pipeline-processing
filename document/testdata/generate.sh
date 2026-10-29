#!/usr/bin/env bash
# generate regenerates the document fixtures from clean.typ: clean.pdf and its ground truth, then
# degraded.pdf and its ground truth. It needs typst 0.15.1 and ImageMagick 7.1.2, laptop
# prerequisites that the check never runs, and jq, which mise declares. Every output is
# byte-reproducible: typst sets no date and uses only its embedded fonts, the speckle's noise is
# seeded, and the degraded PDF's dates are fixed and its identifier left out. README.md lists the
# outputs' SHA-256 sums, which a regeneration must reproduce.
set -euo pipefail
cd "$(dirname "$0")"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The clean pages, and each page's ground truth from its <truth> metadata, in page order.
typst compile --ignore-system-fonts clean.typ clean.pdf
truth=$(typst eval --ignore-system-fonts 'query(<truth>).map(it => it.value)' --in clean.typ)
pages=$(jq length <<<"$truth")
mkdir -p clean
for ((i = 0; i < pages; i++)); do
	jq -r ".[$i]" <<<"$truth" >"clean/page-$((i + 1)).txt"
done

# The clean pages at 150 dpi, as grayscale on white, for the degraded copies to start from.
typst compile --ignore-system-fonts --format png --ppi 150 clean.typ "$work/clean-{p}.png"
for ((p = 1; p <= pages; p++)); do
	magick "$work/clean-$p.png" -background white -alpha remove -alpha off -colorspace Gray \
		"$work/gray-$p.png"
done

# degrade writes degraded page $1, made from clean page $2 by the magick operators that follow,
# and copies the clean page's ground truth as its own.
mkdir -p degraded
degrade() {
	local page=$1 source=$2
	shift 2
	magick "$work/gray-$source.png" "$@" -depth 8 "$work/degraded-$page.png"
	cp "clean/page-$source.txt" "degraded/page-$page.txt"
}
degrade 1 1 -background white -rotate 2.5 -gravity center -extent 1275x1650 # skew: prose
degrade 2 3 +level 45%,100%                                                  # fade: a table
degrade 3 5 -seed 7 -attenuate 0.25 +noise Impulse                           # speckle: a form
degrade 4 2 -blur 0x1.4                                                      # blur: two columns

# The degraded pages wrapped back into one PDF at 150 dpi, letter size, Flate-compressed, with
# fixed dates and no identifier.
magick -define pdf:create-epoch=1767225600 -define pdf:modify-epoch=1767225600 \
	-define pdf:no-identifier=true \
	"$work"/degraded-{1,2,3,4}.png -alpha off -depth 8 -units PixelsPerInch -density 150 \
	-compress Zip degraded.pdf

sha256sum clean.pdf clean/*.txt degraded.pdf degraded/*.txt
