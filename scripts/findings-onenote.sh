#!/usr/bin/env bash
# findings-onenote runs task onenote's finding on this machine, never in CI and
# never in the check, through `mise run findings-onenote`, each step's -v output
# appended to the run's log, findings.log:
#
#   1. The integration tier's TestTierRoutes in package dispatch: every committed
#      fixture through every adapter, held to the hand route truth, with a vision
#      sample on the router SPIKE_ROUTER_URL names; its log is tier-routes.log.
#      Then onenote's TestRealMediaRoutes, the media routes' hand truth.
#   2. Inspection over ~/docs, the findings-tagged TestFindingsDocs in package
#      inspect beside TestDocs, TestDocsBomb, and the clean fixtures' tests.
#   3. The findings-tagged tests in package dispatch: the page ink to vision; the
#      onenote adapter beside the CLI on ONENOTE_LARGE's three large sections;
#      the dispatcher over the scale store; a resumed dispatch of it; and the pool
#      over plaso's store, the scale store, its copy, and mail's fixtures.
#
# ONENOTE_LARGE defaults to ~/docs/onenote/large; ONENOTE_SCALE_STORE names the
# scale.pst mail's generator builds (MISE_ENV=fixtures mise run scale, in mail's
# checkout), and unset skips every step that reads it; ONENOTE_SCALE_COPY, its
# byte copy, defaults to .state/findings/onenote/scale-copy.pst, copied there
# when absent; ONENOTE_PLASO_STORE defaults to where mise run findings-email
# fetches plaso's store. None is ever committed. ONENOTE_FINDINGS_RUNS (3) sets
# the timed runs of each input each way.
#
# A run keeps nothing unless asked: an ONENOTE_FINDINGS_RESULTS the shell sets
# keeps the run's results there, each test's JSON, the ink's PNG, the tier's log,
# and findings.log; unset, they go to a temporary directory removed when the run
# ends.
set -euo pipefail
export GOWORK=off
cd "$MISE_PROJECT_ROOT"

export ONENOTE_LARGE="${ONENOTE_LARGE:-$HOME/docs/onenote/large}"
export ONENOTE_SCALE_COPY="${ONENOTE_SCALE_COPY:-$MISE_PROJECT_ROOT/.state/findings/onenote/scale-copy.pst}"
export ONENOTE_PLASO_STORE="${ONENOTE_PLASO_STORE:-$MISE_PROJECT_ROOT/.state/findings/plaso/nromanoff@stark-research-labs.com.pst}"
if [ -z "${ONENOTE_SCALE_STORE:-}" ]; then
	echo "findings-onenote: ONENOTE_SCALE_STORE is unset; the scale store's steps are skipped" >&2
fi
if [ -z "${ONENOTE_FINDINGS_RESULTS:-}" ]; then
	ONENOTE_FINDINGS_RESULTS=$(mktemp -d)
	trap 'rm -rf "$ONENOTE_FINDINGS_RESULTS"' EXIT
else
	# The tests run in their packages, so a relative path is taken from where the script was run.
	mkdir -p "$ONENOTE_FINDINGS_RESULTS"
	ONENOTE_FINDINGS_RESULTS=$(cd "$ONENOTE_FINDINGS_RESULTS" && pwd)
fi
export ONENOTE_FINDINGS_RESULTS
log="$ONENOTE_FINDINGS_RESULTS/findings.log"
: >"$log"

# No race detector: it would inflate the times and the memory measured. A failed
# step doesn't stop the others; the task fails at the end.
failed=0
step() {
	echo "# findings-onenote: $*" | tee -a "$log"
	"$@" 2>&1 | tee -a "$log" || failed=1
}
export DISPATCH_TIER_LOG="$ONENOTE_FINDINGS_RESULTS/tier-routes.log"
step go test -tags integration -count=1 -timeout 30m -v -run '^TestTierRoutes$' ./dispatch/
step go test -count=1 -v -run '^TestRealMediaRoutes$' ./onenote/
step go test -tags findings -count=1 -v -run '^(TestFindingsDocs|TestDocs|TestDocsBomb|TestCleanFixtures|TestCleanAttachments)$' ./inspect/
step go test -tags findings -count=1 -timeout 60m -v -run '^TestFindings' ./dispatch/
exit "$failed"
