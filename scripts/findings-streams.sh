#!/usr/bin/env bash
# findings-streams runs task streams' finding for evidence 13 on this machine,
# never in CI and never in the check, through `mise run findings-streams`: the
# findings-tagged tests in package media. Each source, its files in the test
# corpus at ~/docs checked there by their SHA-256 against the corpus manifest, is
# copied, or muxed by stream copy, into a temporary directory and read once
# unpaced with no model; its audio transcribed through audio's file path under
# silencedetect and the tracker; read at real time through media's paced stream,
# each window sent to Gemma 4 E4B and each frame to Gemma 4 26B-A4B on the router
# SPIKE_ROUTER_URL names as it arrives, with each event's lags recorded and the
# transcript scored; and read at real time once more and cancelled midway.
#
# Each source is a go test run of its own, capped at 2 hours, and a last run
# reports them from their results. STREAMS_FINDINGS_SOURCES (tears ami nasa)
# names the sources, STREAMS_FINDINGS_RUNS each one's paced runs in place of its
# own (tears 3, ami 1, nasa 1), and DOCS (~/docs) the corpus. None of the media
# is committed.
#
# A run keeps nothing unless asked: a STREAMS_FINDINGS_RESULTS the shell sets
# keeps each source's JSON and the run's -v log, findings.log, there; unset, they
# go to a temporary directory removed when the run ends.
set -euo pipefail
export GOWORK=off
cd "$MISE_PROJECT_ROOT"

if [ -z "${STREAMS_FINDINGS_RESULTS:-}" ]; then
	STREAMS_FINDINGS_RESULTS=$(mktemp -d)
	trap 'rm -rf "$STREAMS_FINDINGS_RESULTS"' EXIT
else
	# The tests run in media/, so a relative path is taken from where the script was run.
	mkdir -p "$STREAMS_FINDINGS_RESULTS"
	STREAMS_FINDINGS_RESULTS=$(cd "$STREAMS_FINDINGS_RESULTS" && pwd)
fi
export STREAMS_FINDINGS_RESULTS
log="$STREAMS_FINDINGS_RESULTS/findings.log"
: >"$log"

# No race detector: it would inflate the lags measured. A failed source doesn't
# stop the others; the task fails at the end.
failed=0
step() {
	echo "# findings-streams: $*" | tee -a "$log"
	"$@" 2>&1 | tee -a "$log" || failed=1
}
for source in ${STREAMS_FINDINGS_SOURCES:-tears ami nasa}; do
	start=$(date +%s)
	STREAMS_FINDINGS_SOURCE=$source step go test -tags findings -count=1 -timeout 2h -v -run '^TestFindingsStreams$' ./media/
	echo "findings-streams: $source took $(($(date +%s) - start)) s" | tee -a "$log"
done
step go test -tags findings -count=1 -v -run '^TestFindingsStreamsReport$' ./media/
exit "$failed"
