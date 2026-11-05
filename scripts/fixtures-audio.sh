#!/usr/bin/env bash
# fixtures-audio rebuilds the audio package's speech fixture, audio/testdata,
# from the LibriSpeech dev-clean utterances in the test corpus at ~/docs (DOCS
# names another root): 2277-149896-0000 to 0003, read in that order twice, with
# one second of digital silence after each utterance but the last, so the gaps,
# the silence intervals, and the plan are known. It writes speech.flac, 16 kHz
# mono 16-bit FLAC, and speech.truth.txt, the eight utterances' transcripts, one
# a line, from the corpus's truth file. Run it through `mise run fixtures:audio`.
#
# Each source is checked against its SHA-256, as the corpus's MANIFEST.json pins
# it, before anything is written; a source that differs stops the script. The
# outputs are then checked against the SHA-256s the testdata README records,
# from the host's ffmpeg n9.0.2, and a mismatch is reported and fails the
# script, the new files kept for review. The check never runs this: the fixture
# is committed.
set -euo pipefail
cd "$MISE_PROJECT_ROOT"

docs=${DOCS:-$HOME/docs}
src=$docs/audio/short/librispeech
out=audio/testdata

# The sources and their SHA-256s, as ~/docs/MANIFEST.json pins them.
sources=(
	"fead3e0c481f1c637fe6ec8edef42506c7c2d33a3fb7519bfdf5815b31150303  librispeech-dev-clean-2277-149896-0000.flac"
	"f9c3371b4d1c6e95e7c01298a26215c2be97bee22c5408885d8d29b6e0e4171c  librispeech-dev-clean-2277-149896-0001.flac"
	"ade2caad47934462292358ba49acf7f048ced0595615492b4b20866d118bf901  librispeech-dev-clean-2277-149896-0002.flac"
	"a99595b53e5d48524c2ba4903071f2498b05c25c509f0a78d60dc15ee46a4836  librispeech-dev-clean-2277-149896-0003.flac"
	"af40bcc2f27a7e490c83aae7afbb47b06312b5925421bf21ca0e0ab774acac1d  librispeech-dev-clean.truth.json"
)

# The outputs' SHA-256s, as the testdata README records them.
outputs=(
	"d5b77b3875e42a1907834fb241a24bf85cf7c922bfd4ab59b86de6f05c4d084a  speech.flac"
	"2902ea6452b2f29c53bbc2b5541fb036e449c5bdb17b5dc52b6d479c3e7457ea  speech.truth.txt"
)

printf '%s\n' "${sources[@]}" | (cd "$src" && sha256sum --check --strict --quiet -)

# The utterances, in the fixture's order: the four, then the four again.
utterances=(0000 0001 0002 0003 0000 0001 0002 0003)

inputs=()
graph=""
labels=""
last=$((${#utterances[@]} - 1))
for i in "${!utterances[@]}"; do
	inputs+=(-i "$src/librispeech-dev-clean-2277-149896-${utterances[$i]}.flac")
	if [ "$i" -lt "$last" ]; then
		graph+="[$i:a]apad=pad_dur=1[a$i];"
		labels+="[a$i]"
	else
		labels+="[$i:a]"
	fi
done
graph+="${labels}concat=n=${#utterances[@]}:v=0:a=1[out]"

ffmpeg -version | head -n 1
ffmpeg -nostdin -hide_banner -loglevel error -y "${inputs[@]}" \
	-filter_complex "$graph" -map '[out]' \
	-c:a flac -compression_level 12 -ar 16000 -ac 1 -sample_fmt s16 \
	-fflags +bitexact -flags:a +bitexact -map_metadata -1 "$out/speech.flac"

for u in "${utterances[@]}"; do
	jq -r --arg file "librispeech-dev-clean-2277-149896-$u.flac" \
		'.items[] | select(.file == $file) | .text' "$src/librispeech-dev-clean.truth.json"
done >"$out/speech.truth.txt"

if ! printf '%s\n' "${outputs[@]}" | (cd "$out" && sha256sum --check --strict -); then
	echo "fixtures-audio: the rebuilt fixture differs from the README's SHA-256s; review it" >&2
	exit 1
fi
