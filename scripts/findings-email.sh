#!/usr/bin/env bash
# findings-email runs the email package's finding for evidence 16 on this
# machine, never in CI and never in the check: the findings-tagged tests in
# package email, through `mise run findings-email`. They thread and route every
# fixture and MIME input, scored against the tests' hand truth; count plaso's
# 59 MB store's threads and routes; and time the adapter, threads included, and
# mail alone on plaso's store, the scale store, and the scale mboxrd, with their
# peak memory.
#
# plaso's store is fetched once into .state/findings/plaso/, at the commit mail's
# own finding pins, and refused unless its SHA-256 is the pinned one; it is never
# committed. The scale store is the scale.pst mail's generator builds
# (MISE_ENV=fixtures mise run scale, in a checkout of mail's tag, never this
# one); EMAIL_SCALE_STORE names it, and unset skips it. It is never committed.
# The scale mboxrd is the scale.mboxrd the generator writes beside it, the same
# messages; EMAIL_SCALE_MBOX names it, and unset skips it. It is never committed.
# EMAIL_PLASO_STORE overrides where plaso's store is kept, and
# EMAIL_FINDINGS_RUNS (3) sets the timed runs of each large store each way.
#
# A run keeps nothing unless asked: an EMAIL_FINDINGS_RESULTS the shell sets keeps
# the run's results there, each test's JSON and the run's -v log, findings.log;
# unset, they go to a temporary directory removed when the run ends.
set -euo pipefail
export GOWORK=off
cd "$MISE_PROJECT_ROOT"

export EMAIL_PLASO_STORE="${EMAIL_PLASO_STORE:-$MISE_PROJECT_ROOT/.state/findings/plaso/nromanoff@stark-research-labs.com.pst}"
if [ -z "${EMAIL_SCALE_STORE:-}" ]; then
	echo "findings-email: EMAIL_SCALE_STORE is unset; the scale store is skipped" >&2
fi
if [ -z "${EMAIL_SCALE_MBOX:-}" ]; then
	echo "findings-email: EMAIL_SCALE_MBOX is unset; the scale mboxrd is skipped" >&2
fi
if [ -z "${EMAIL_FINDINGS_RESULTS:-}" ]; then
	EMAIL_FINDINGS_RESULTS=$(mktemp -d)
	trap 'rm -rf "$EMAIL_FINDINGS_RESULTS"' EXIT
else
	# The tests run in email/, so a relative path is taken from where the script was run.
	mkdir -p "$EMAIL_FINDINGS_RESULTS"
	EMAIL_FINDINGS_RESULTS=$(cd "$EMAIL_FINDINGS_RESULTS" && pwd)
fi
export EMAIL_FINDINGS_RESULTS

# No race detector: it would inflate the times and the memory measured.
go test -tags findings -count=1 -timeout 60m -v -run '^TestFindings' ./email/ 2>&1 |
	tee "$EMAIL_FINDINGS_RESULTS/findings.log"
