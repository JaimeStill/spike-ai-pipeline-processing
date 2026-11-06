#!/usr/bin/env bash
# fixtures-streams rebuilds the streams fixtures in audio/testdata from audio's
# committed speech fixture, speech.flac and speech.truth.txt: an audio-and-video
# fixture and two level-change variants of the speech, with their truth. Run it
# through `mise run fixtures:streams`.
#
# - speech-scenes.mkv: speech.flac's FLAC stream, copied, beside a generated
#   video of five static test patterns, 320x180 at 10 fps for 50 s, with hard
#   cuts at 6, 9, 31, and 37 s and spans of 22 and 13 s with no cut, so a frame
#   sampler takes its first frame, a frame at each cut, and a frame after each
#   10 s with none. The video is FFmpeg's own MPEG-4 Part 2 encoder at -q:v 1,
#   one thread, a keyframe every 2 s and no B-frames, in Matroska: a native
#   encoder, so the bytes don't depend on a library's version.
# - speech-gain.wav and speech-noise.wav: 75.23 s of 16 kHz mono 16-bit PCM WAV,
#   the four utterances read three times, each round speech.flac's first four,
#   with one second of digital silence between rounds; 6 dB down, so the gain
#   step can't clip, under a pink-noise floor, so no gap is digital silence. At
#   30 s, speech-gain.wav steps up by 12 dB and speech-noise.wav gains a louder
#   pink noise, above the threshold fixed from the first 30 s yet below speech.
# - speech-scenes.truth.json and speech-levels.truth.json: their truth, written
#   from the settings below and the first 30 s's mean volume, which volumedetect
#   measures.
#
# The sources are checked against the SHA-256s the testdata README records
# before anything is written; a source that differs stops the script. The
# script then prints what ffmpeg measures of the outputs, scdet's cuts and
# silencedetect's silences at the fixed threshold, and fails if they aren't
# the truth's. The outputs are last checked against the SHA-256s the testdata
# README records, from the host's ffmpeg n9.0.2, and a mismatch is reported and
# fails the script, the new files kept for review. The check never runs this:
# the fixtures are committed.
set -euo pipefail
cd "$MISE_PROJECT_ROOT"

out=audio/testdata

# The sources and their SHA-256s, as the testdata README records them.
sources=(
	"d5b77b3875e42a1907834fb241a24bf85cf7c922bfd4ab59b86de6f05c4d084a  speech.flac"
	"2902ea6452b2f29c53bbc2b5541fb036e449c5bdb17b5dc52b6d479c3e7457ea  speech.truth.txt"
)

# The outputs' SHA-256s, as the testdata README records them.
outputs=(
	"a6b6610b82b8a152b34caa19da34c32cb15e4ec631332ab08b10a5592ddb1164  speech-scenes.mkv"
	"d67aaf05e509776fc19415ac259c6a410b5c03ede554481826d5836ea9ef0cf4  speech-scenes.truth.json"
	"d98a6e3264f104a5ca09f3e160b6f0770fd40ee93f11acc2bdb63129fc6840e6  speech-gain.wav"
	"bb8655839e2a6a120984225f7996ea5183f52f6db350bd68e72bd4425bae867a  speech-noise.wav"
	"a3193d531303009c0338f2b6a35abb1f3fda962f9fe7cd35f9e4a7db9a45f7b8  speech-levels.truth.json"
)

printf '%s\n' "${sources[@]}" | (cd "$out" && sha256sum --check --strict --quiet -)

ffmpeg -version | head -n 1
bitexact=(-fflags +bitexact -flags +bitexact -map_metadata -1)

# The video: each pattern and the second it starts at, the last ending at 50 s.
# The patterns are chosen so each cut's scdet score is far above its default
# threshold, 10.
size=320x180
rate=10
end=50
patterns=(
	"0 smptebars"
	"6 color=c=yellow"
	"9 rgbtestsrc"
	"31 color=c=white"
	"37 pal100bars"
)
# The frame sampler's default policy: a frame after this many seconds with none.
gap=10

graph=""
labels=""
segments="[]"
cuts="[]"
for i in "${!patterns[@]}"; do
	read -r start pattern <<<"${patterns[$i]}"
	if [ "$i" -lt $((${#patterns[@]} - 1)) ]; then
		read -r stop _ <<<"${patterns[$((i + 1))]}"
	else
		stop=$end
	fi
	# A pattern with options already has an =, after which the size and rate follow.
	case "$pattern" in
	*=*) source="$pattern:s=$size:r=$rate:d=$((stop - start))" ;;
	*) source="$pattern=s=$size:r=$rate:d=$((stop - start))" ;;
	esac
	graph+="${source}[v$i];"
	labels+="[v$i]"
	segments=$(jq -c --argjson s "$start" --argjson e "$stop" --arg p "$pattern" \
		'. + [{start_ms: ($s * 1000), end_ms: ($e * 1000), pattern: $p}]' <<<"$segments")
	if [ "$i" -gt 0 ]; then
		cuts=$(jq -c --argjson s "$start" '. + [$s * 1000]' <<<"$cuts")
	fi
done
graph+="${labels}concat=n=${#patterns[@]}:v=1:a=0,format=yuv420p[v]"

ffmpeg -nostdin -hide_banner -loglevel error -y -i "$out/speech.flac" \
	-filter_complex "$graph" -map '[v]' -map 0:a \
	-c:v mpeg4 -q:v 1 -g 20 -bf 0 -threads 1 -c:a copy \
	"${bitexact[@]}" "$out/speech-scenes.mkv"

# The expected frames under the default policy: the first frame; a frame at
# each cut; and, between them, a frame each time gap seconds pass with none. The
# patterns are chosen so no interval frame falls on a cut.
frames=$(jq -c --argjson cuts "$cuts" --argjson gap $((gap * 1000)) \
	--argjson last $(((end * 1000) - 1000 / rate)) -n '
	def fill($from; $to): [range($from + $gap; $to; $gap) | {reason: "interval", time_ms: .}];
	reduce ($cuts + [$last + 1])[] as $c ({frames: [{reason: "first", time_ms: 0}], at: 0};
		(fill(.at; $c)) as $f
		| .frames += $f + (if $c <= $last then [{reason: "scene", time_ms: $c}] else [] end)
		| .at = $c)
	| .frames')

# The speech's inserted gaps, each one second of digital silence, and its
# transcript, as the testdata README gives them: the four utterances, 6.59,
# 7.145, 4.835, and 2.84 s long, read twice.
utterances=(6590 7145 4835 2840)
round_ms=24410
speech_ms=49820

# gaps prints, one a line, the start of each gap after the utterances of rounds
# rounds, the last utterance ending the speech.
gaps() {
	local at=0 r i
	for ((r = 0; r < $1; r++)); do
		for i in "${!utterances[@]}"; do
			at=$((at + utterances[i]))
			if [ "$r" -lt $(($1 - 1)) ] || [ "$i" -lt $((${#utterances[@]} - 1)) ]; then
				echo "$at"
				at=$((at + 1000))
			fi
		done
	done
}

jq -n --slurpfile gaps <(gaps 2) --rawfile transcript "$out/speech.truth.txt" \
	--argjson segments "$segments" --argjson cuts "$cuts" --argjson frames "$frames" \
	--arg size "$size" --argjson rate "$rate" --argjson end "$end" --argjson gap "$gap" \
	--argjson speech "$speech_ms" '{
	file: "speech-scenes.mkv",
	video: {
		codec: "mpeg4",
		width: ($size | split("x")[0] | tonumber),
		height: ($size | split("x")[1] | tonumber),
		frame_rate: $rate,
		duration_ms: ($end * 1000),
		generated: true,
		segments: $segments,
		cuts_ms: $cuts,
		max_gap_ms: ($gap * 1000),
		frames: $frames
	},
	audio: {
		source: "speech.flac",
		codec: "flac",
		sample_rate: 16000,
		duration_ms: $speech,
		gaps: [$gaps[] | {start_ms: ., end_ms: (. + 1000)}],
		transcript: ($transcript | rtrimstr("\n") | split("\n"))
	}
}' >"$out/speech-scenes.truth.json"

# The level-change variants: the speech's four utterances read three times, a
# second of digital silence between the rounds, at -6 dB under a pink-noise
# floor; then, from change_s on, 12 dB more gain or a louder pink noise. Each
# noise is anoisesrc's, seeded, so a rebuild draws the same samples.
duration_ms=$((3 * round_ms + 2000))
change_s=30
attenuation_db=-6
floor_amplitude=0.004
floor_seed=1
gain_db=12
noise_amplitude=0.04
noise_seed=2
min_silence_ms=300

change_sample=$((change_s * 16000))
round_samples=$((round_ms * 16))
duration_s=$(jq -n "$duration_ms / 1000")
base="[0:a]apad=pad_dur=1[a0];[1:a]atrim=end_sample=${round_samples}[a1];"
base+="[a0][a1]concat=n=2:v=0:a=1,volume=${attenuation_db}dB:precision=fixed[s0];"
base+="anoisesrc=color=pink:amplitude=$floor_amplitude:seed=$floor_seed:sample_rate=16000:duration=${duration_s}[f];"
base+="[s0][f]amix=inputs=2:normalize=0:duration=first[s]"
wav=(-c:a pcm_s16le -ar 16000 -ac 1 "${bitexact[@]}")

ffmpeg -nostdin -hide_banner -loglevel error -y -i "$out/speech.flac" -i "$out/speech.flac" \
	-filter_complex "$base;[s]asplit[x][y];[x]atrim=end_sample=${change_sample}[p];
		[y]atrim=start_sample=$change_sample,asetpts=PTS-STARTPTS,volume=${gain_db}dB:precision=fixed[q];
		[p][q]concat=n=2:v=0:a=1[out]" \
	-map '[out]' "${wav[@]}" "$out/speech-gain.wav"
ffmpeg -nostdin -hide_banner -loglevel error -y -i "$out/speech.flac" -i "$out/speech.flac" \
	-filter_complex "$base;anullsrc=r=16000:cl=mono:d=${change_s}[z];
		anoisesrc=color=pink:amplitude=$noise_amplitude:seed=$noise_seed:sample_rate=16000:duration=$(jq -n "$duration_s - $change_s")[m];
		[z][m]concat=n=2:v=0:a=1[n];[s][n]amix=inputs=2:normalize=0:duration=first[out]" \
	-map '[out]' "${wav[@]}" "$out/speech-noise.wav"

# The variants are the same samples before the change: 44 header bytes, then 2
# bytes a sample.
if ! cmp -s -n $((44 + 2 * change_sample)) "$out/speech-gain.wav" "$out/speech-noise.wav"; then
	echo "fixtures-streams: the variants differ before the change at $change_s s" >&2
	exit 1
fi

# The threshold fixed from the first 30 s: their mean volume, less audio's
# default 15 dB.
mean=$(ffmpeg -nostdin -hide_banner -i "$out/speech-gain.wav" -af "atrim=end_sample=$change_sample,volumedetect" -f null - 2>&1 |
	sed -n 's/.*mean_volume: \(\S*\) dB/\1/p' | tail -n 1)
threshold=$(jq -n "$mean - 15")

jq -n --slurpfile gaps <(gaps 3) --rawfile transcript "$out/speech.truth.txt" \
	--argjson duration "$duration_ms" --argjson change "$((change_s * 1000))" \
	--argjson attenuation "$attenuation_db" --argjson floor "$floor_amplitude" --argjson floor_seed "$floor_seed" \
	--argjson gain "$gain_db" --argjson noise "$noise_amplitude" --argjson noise_seed "$noise_seed" \
	--argjson mean "$mean" --argjson threshold "$threshold" --argjson min "$min_silence_ms" '
	($gaps | map({start_ms: ., end_ms: (. + 1000)})) as $spans
	| ($transcript | rtrimstr("\n") | split("\n")[0:4]) as $round
	| {
		source: "speech.flac",
		sample_rate: 16000,
		duration_ms: $duration,
		attenuation_db: $attenuation,
		floor: {color: "pink", amplitude: $floor, seed: $floor_seed},
		gaps: $spans,
		transcript: ($round + $round + $round),
		change_ms: $change,
		first_mean_volume_db: $mean,
		fixed_threshold_db: $threshold,
		min_silence_ms: $min,
		gaps_after_change: [$spans[] | select(.start_ms >= $change)],
		variants: [
			{file: "speech-gain.wav", change: "gain", gain_db: $gain},
			{file: "speech-noise.wav", change: "noise", noise: {color: "pink", amplitude: $noise, seed: $noise_seed}}
		]
	}' >"$out/speech-levels.truth.json"

# The truth as ffmpeg measures it. scdet, at its default threshold, finds the
# cuts and no other.
echo "speech-scenes.mkv: cuts at $(jq -c '.video.cuts_ms' "$out/speech-scenes.truth.json") ms"
echo "speech-scenes.mkv: frames $(jq -c '[.video.frames[] | "\(.reason) \(.time_ms)"]' "$out/speech-scenes.truth.json")"
found=$(ffmpeg -nostdin -hide_banner -i "$out/speech-scenes.mkv" -map 0:v -vf scdet -f null - 2>&1 |
	sed -n 's/.*lavfi.scd.score: \(\S*\), lavfi.scd.time: \(\S*\)/\2 \1/p')
echo "$found" | while read -r time score; do echo "  scdet: cut at $time s, score $score"; done
if [ "$(echo "$found" | awk '{printf "%s%d", sep, $1 * 1000; sep=","}')" != "$(jq -r '.video.cuts_ms | join(",")' "$out/speech-scenes.truth.json")" ]; then
	echo "fixtures-streams: scdet's cuts aren't the truth's" >&2
	exit 1
fi

# At the fixed threshold, silencedetect finds no silence after the change in
# either variant.
echo "the first $change_s s: mean volume $mean dB, fixed threshold $threshold dB"
for v in gain noise; do
	silences=$(ffmpeg -nostdin -hide_banner -i "$out/speech-$v.wav" \
		-af "silencedetect=noise=${threshold}dB:d=$(jq -n "$min_silence_ms / 1000")" -f null - 2>&1 |
		sed -n 's/.*silence_start: \(\S*\).*/\1/p; s/.*silence_end: \(\S*\).*/\1/p' | paste -d - - -)
	echo "speech-$v.wav: silences at $threshold dB: $(echo "$silences" | tr '\n' ' ')"
	late=$(echo "$silences" | awk -F- -v c="$change_s" '$2 == "" || $2 > c' | wc -l)
	if [ "$late" -ne 0 ]; then
		echo "fixtures-streams: speech-$v.wav has $late silences past $change_s s at the fixed threshold" >&2
		exit 1
	fi
done
echo "after the change, the gaps $(jq -c '[.gaps_after_change[].start_ms]' "$out/speech-levels.truth.json") ms"

if ! printf '%s\n' "${outputs[@]}" | (cd "$out" && sha256sum --check --strict -); then
	echo "fixtures-streams: the rebuilt fixtures differ from the README's SHA-256s; review them" >&2
	exit 1
fi
