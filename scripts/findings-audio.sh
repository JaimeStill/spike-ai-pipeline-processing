#!/usr/bin/env bash
# findings-audio runs task audio's finding for evidence 5 on this machine, never
# in CI and never in the check, through `mise run findings-audio`: the
# findings-tagged tests in package audio. Each long source in the test corpus at
# ~/docs, checked there by its SHA-256 against the corpus manifest, is planned at
# its silences, its windows transcribed by Gemma 4 E4B on the router
# SPIKE_ROUTER_URL names, 4 at once, and joined; scored against its truth by
# normalized word error rate and by the edits at each seam; its seams sent a few
# words at a time to gpt-oss-120b for a seam pass, scored again; and its first
# 10 minutes sent whole as one unsplit request beside the same excerpt windowed.
#
# Each source is a go test run of its own, capped at 4 hours, and a last run
# reports them from their results. AUDIO_FINDINGS_SOURCES (ami earnings21
# earnings22 librivox) names the sources, AUDIO_FINDINGS_RUNS (3) the runs of
# each, and DOCS (~/docs) the corpus. None of the audio is committed.
#
# A run keeps nothing unless asked: an AUDIO_FINDINGS_RESULTS the shell sets
# keeps each source's JSON and the run's -v log, findings.log, there; unset,
# they go to a temporary directory removed when the run ends.
set -euo pipefail
export GOWORK=off
cd "$MISE_PROJECT_ROOT"

if [ -z "${AUDIO_FINDINGS_RESULTS:-}" ]; then
	AUDIO_FINDINGS_RESULTS=$(mktemp -d)
	trap 'rm -rf "$AUDIO_FINDINGS_RESULTS"' EXIT
else
	# The tests run in audio/, so a relative path is taken from where the script was run.
	mkdir -p "$AUDIO_FINDINGS_RESULTS"
	AUDIO_FINDINGS_RESULTS=$(cd "$AUDIO_FINDINGS_RESULTS" && pwd)
fi
export AUDIO_FINDINGS_RESULTS
log="$AUDIO_FINDINGS_RESULTS/findings.log"
: >"$log"

# No race detector: it would inflate the times measured. A failed source doesn't
# stop the others; the task fails at the end.
failed=0
step() {
	echo "# findings-audio: $*" | tee -a "$log"
	"$@" 2>&1 | tee -a "$log" || failed=1
}
step go test -tags findings -count=1 -v -run '^TestFindingsTruth$' ./audio/
for source in ${AUDIO_FINDINGS_SOURCES:-ami earnings21 earnings22 librivox}; do
	start=$(date +%s)
	AUDIO_FINDINGS_SOURCE=$source step go test -tags findings -count=1 -timeout 4h -v -run '^TestFindingsSource$' ./audio/
	echo "findings-audio: $source took $(($(date +%s) - start)) s" | tee -a "$log"
done
step go test -tags findings -count=1 -v -run '^TestFindingsReport$' ./audio/
exit "$failed"
