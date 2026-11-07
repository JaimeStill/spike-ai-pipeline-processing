#!/usr/bin/env bash
# findings-formats runs task formats' finding on this machine, never in CI and
# never in the check, through `mise run findings-formats`, each step's -v output
# appended to the run's log, findings.log:
#
#   1. The tests that hold the new routes to their truth over the committed
#      fixtures and ~/docs, unit and integration tiers: the archive stage's tar,
#      gzip, 7z, zstd, cabinet, and ZIP methods, with and without 7-Zip, and
#      ~/docs' mixed bundle in its four forms; the picture stage's TIFF pages,
#      HEIC, WebP, and AVIF; the text stage over ~/docs' encodings; RTF through
#      LibreOffice; the document stage's password check, forms, attachments, and
#      attachment bomb over qpdf and pdfdetach; and the conversation records over
#      mail's conversation store.
#   2. The findings-tagged tests in package dispatch: TestFindingsFormats
#      dispatches every route's fixtures and ~/docs inputs, records each part,
#      and holds them to the truth; TestFindingsFormatsCost times the dispatcher
#      on each ~/docs input and the conversation store, with its peak memory.
#
# No step calls a model. DOCS names the corpus root, ~/docs when unset, each
# input checked by its SHA-256 against the corpus manifest; none is committed.
# FORMATS_FINDINGS_RUNS (3) sets the timed runs of each input.
#
# A run keeps nothing unless asked: a FORMATS_FINDINGS_RESULTS the shell sets
# keeps the run's results there, each test's JSON and findings.log; unset, they
# go to a temporary directory removed when the run ends.
set -euo pipefail
export GOWORK=off
cd "$MISE_PROJECT_ROOT"

if [ -z "${FORMATS_FINDINGS_RESULTS:-}" ]; then
	FORMATS_FINDINGS_RESULTS=$(mktemp -d)
	trap 'rm -rf "$FORMATS_FINDINGS_RESULTS"' EXIT
else
	# The tests run in their packages, so a relative path is taken from where the script was run.
	mkdir -p "$FORMATS_FINDINGS_RESULTS"
	FORMATS_FINDINGS_RESULTS=$(cd "$FORMATS_FINDINGS_RESULTS" && pwd)
fi
export FORMATS_FINDINGS_RESULTS
log="$FORMATS_FINDINGS_RESULTS/findings.log"
: >"$log"

# No race detector: it would inflate the times and the memory measured. A failed
# step doesn't stop the others; the task fails at the end.
failed=0
step() {
	echo "# findings-formats: $*" | tee -a "$log"
	"$@" 2>&1 | tee -a "$log" || failed=1
}
step go test -tags integration -count=1 -timeout 30m -v \
	-run '^(TestTar.*|TestGzip.*|TestArchiveDamaged|TestArchiveNoSevenZip|TestZipMethods|TestZipDocs|TestSevenZip.*|TestListingQuarantine|TestPictureStageReal|TestTextDocs|TestOfficeStageRTFReal|TestDocumentStage(Encrypted|Forms|Attachments|AttachmentBomb)Real|TestEmailConversations)$' ./dispatch/
step go test -tags integration -count=1 -v ./sevenzip/
step go test -tags integration -count=1 -v -run '^(TestCheckListing.*|TestTarBombThresholds|TestUnreadableTar)$' ./inspect/
step go test -tags integration -count=1 -v -run '^(TestConvertPages.*|TestConvertRaster.*|TestConvertNoReader)$' ./picture/
step go test -tags integration -count=1 -timeout 30m -v -run '^TestRTFRoundTrip$' ./office/
step go test -tags integration -count=1 -v -run '^(TestFormReal|TestRequiresPasswordReal|TestDetacherReal)$' ./document/
step go test -count=1 -v -run '^(TestRealConversations|TestMessageConversation)$' ./email/
step go test -tags findings -count=1 -timeout 60m -v -run '^TestFindingsFormats' ./dispatch/
exit "$failed"
