#!/usr/bin/env bash
# generate regenerates the generated legacy fixtures: each of office's generated OOXML fixtures in
# ../ooxml (the *.libreoffice.* ones) saved by LibreOffice as .doc, .xls, or .ppt into generated/,
# named as its original with the legacy extension. LibreOffice runs as the office package runs
# it: soffice in the default image, the picture package's LibreOfficeImage in
# ../../../picture/spec.go, under the container runner's lockdown (no network, the caller's
# uid:gid, every capability dropped), with a user profile of its own in the mounted working
# directory. It needs docker, a laptop prerequisite that the check never runs, and pulls the image
# when it isn't present.
#
# It writes generated/MANIFEST: the image, soffice's --version line, and each file's SHA-256.
# LibreOffice's binary formats carry the time they were saved, so a regeneration's sums differ;
# the manifest records the committed files'.
set -euo pipefail
cd "$(dirname "$0")"

# The image's reference, from the Go literal the picture package declares, its two halves joined.
image=$(tr '\n' ' ' <../../../picture/spec.go | sed -E 's/"[[:space:]]*\+[[:space:]]*"//g' |
	grep -oE 'gotenberg/gotenberg:[^"@]+@sha256:[0-9a-f]{64}')

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# soffice runs soffice in the image with its arguments, locked down, in $work, which it mounts.
soffice() {
	docker run --rm --init --network=none --user="$(id -u):$(id -g)" --cap-drop=ALL \
		--pull=missing --entrypoint=soffice --volume="$work:$work" --workdir="$work" \
		"$image" "$@"
}

version=$(soffice --version 2>/dev/null | grep -m1 '^LibreOffice ')

rm -rf generated
mkdir generated
for original in ../ooxml/*.libreoffice.docx ../ooxml/*.libreoffice.xlsx ../ooxml/*.libreoffice.pptx; do
	name=$(basename "$original")
	case $name in
	*.docx) legacy=doc filter='MS Word 97' ;;
	*.xlsx) legacy=xls filter='MS Excel 97' ;;
	*.pptx) legacy=ppt filter='MS PowerPoint 97' ;;
	esac
	cp "$original" "$work/$name"
	soffice "-env:UserInstallation=file://$work/profile-$name" --headless --norestore \
		--convert-to "$legacy:$filter" --outdir "$work/out" "$work/$name" >/dev/null 2>&1
	cp "$work/out/${name%.*}.$legacy" generated/
done

{
	echo "image: $image"
	echo "soffice: $version"
	echo
	(cd generated && sha256sum -- *.doc *.xls *.ppt)
} >generated/MANIFEST
cat generated/MANIFEST
