#!/usr/bin/env bash
# fixtures-mail refreshes the email package's copy of mail's fixtures,
# email/testdata/mail, from spike-ai-pipeline-mail's tag: its tests/fixtures
# into fixtures/ and its tests/golden into golden/, byte for byte. It fetches the
# tag alone, shallowly, from the remote, so the copy is the tag's whatever any
# local checkout holds. Run it through `mise run fixtures:mail`.
#
# MANIFEST pins the copy: the repository, the tag, the commit the tag named, and
# each file's SHA-256. Before replacing the copy, the script reports, one line
# each, any drift of the tag (another commit, a file changed, added, or gone)
# and of the copy itself against the manifest, and exits non-zero when the tag
# drifted. With --write-manifest it writes the manifest from the tag instead, as
# for a new tag; review its diff before committing. The check never runs this:
# it verifies the copy against the manifest offline.
set -euo pipefail
cd "$MISE_PROJECT_ROOT"

repository=https://github.com/JaimeStill/spike-ai-pipeline-mail
tag=v0.1.0
copy=email/testdata/mail
manifest=$copy/MANIFEST

write=0
case "${1:-}" in
"") ;;
--write-manifest) write=1 ;;
*)
	echo "usage: fixtures-mail.sh [--write-manifest]" >&2
	exit 2
	;;
esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git -C "$work" init -q
git -C "$work" fetch -q --depth 1 "$repository" "refs/tags/$tag"
commit=$(git -C "$work" rev-parse 'FETCH_HEAD^{commit}')
mkdir "$work/tag"
git -C "$work" archive FETCH_HEAD tests/fixtures tests/golden | tar -x -C "$work/tag"
mv "$work/tag/tests/fixtures" "$work/tag/fixtures"
mv "$work/tag/tests/golden" "$work/tag/golden"
rmdir "$work/tag/tests"

# sums prints each file's SHA-256 under fixtures/ and golden/ in the directory
# given, as sha256sum does, in byte order of the path.
sums() {
	(cd "$1" && find fixtures golden -type f -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum)
}

# drift reports each line in which the manifest's sums and those given differ,
# labelled with what was compared, and fails when there are any.
drift() {
	local label=$1 got=$2 lines
	lines=$(diff <(grep -E '^[0-9a-f]{64}  ' "$manifest") <(echo "$got") | grep -E '^[<>] ' || true)
	[ -z "$lines" ] && return 0
	while read -r line; do
		case "$line" in
		"< "*) echo "drift: $label: manifest has ${line#< }" ;;
		"> "*) echo "drift: $label: now ${line#> }" ;;
		esac
	done <<<"$lines"
	return 1
}

tag_sums=$(sums "$work/tag")
tag_drifted=0
if [ "$write" -eq 0 ]; then
	if [ ! -f "$manifest" ]; then
		echo "fixtures-mail: no $manifest; run with --write-manifest" >&2
		exit 1
	fi
	pinned_tag=$(awk '$1 == "tag" { print $2 }' "$manifest")
	pinned_commit=$(awk '$1 == "commit" { print $2 }' "$manifest")
	if [ "$pinned_tag" != "$tag" ]; then
		echo "drift: the manifest pins tag $pinned_tag, the script fetches $tag"
		tag_drifted=1
	fi
	if [ "$pinned_commit" != "$commit" ]; then
		echo "drift: $tag names commit $commit, the manifest $pinned_commit"
		tag_drifted=1
	fi
	drift "$tag" "$tag_sums" || tag_drifted=1
	if [ -d "$copy/fixtures" ] || [ -d "$copy/golden" ]; then
		drift "the copy" "$(sums "$copy")" || true
	fi
fi

mkdir -p "$copy"
rm -rf "$copy/fixtures" "$copy/golden"
cp -R "$work/tag/fixtures" "$work/tag/golden" "$copy/"

if [ "$write" -eq 1 ]; then
	{
		echo "# The mail fixtures: spike-ai-pipeline-mail's tests/fixtures, in fixtures/, and"
		echo "# tests/golden, in golden/, at the tag, copied byte for byte by mise run fixtures:mail,"
		echo "# which reports any drift against this manifest. Each file's SHA-256 is below."
		echo "repository $repository"
		echo "tag $tag"
		echo "commit $commit"
		echo "$tag_sums"
	} >"$manifest"
	echo "fixtures-mail: wrote $manifest from $tag ($commit)"
fi
exit "$tag_drifted"
