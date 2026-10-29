#!/usr/bin/env bash
# currency reports every dependency, toolchain, and pin that trails its latest
# release, one line each, and exits non-zero when it reports any. It reads the
# network and writes nothing. Run it through `mise run currency`.
#
# Each scan captures its command's output in a variable before reading it, so
# under set -e a scan command that fails fails currency instead of reporting
# nothing.
set -euo pipefail
cd "$MISE_PROJECT_ROOT"
export GOWORK=off

stale=0
report() {
	echo "$1"
	stale=1
}

# matches prints each match of a grep pattern in the given files that exist.
# A missing file or no match is no output; any other grep error fails.
matches() {
	local pattern=$1 file
	local files=()
	shift
	for file in "$@"; do
		if [ -e "$file" ]; then files+=("$file"); fi
	done
	[ ${#files[@]} -gt 0 ] || return 0
	grep -ho "$pattern" "${files[@]}" || [ $? -eq 1 ]
}

go_minor=$(mise latest go | cut -d. -f1,2)

for mod in $GO_MODULES; do
	# Direct requirements with a newer version within their major.
	updates=$(cd "$mod" && go list -m -u -f \
		'{{if and (not .Main) (not .Indirect) .Update}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' all)
	while read -r line; do
		[ -n "$line" ] && report "$mod/go.mod: $line"
	done <<<"$updates"

	# The go directive's minor against the current Go minor.
	directive=$(cd "$mod" && go mod edit -json | jq -r .Go | cut -d. -f1,2)
	[ "$directive" = "$go_minor" ] || report "$mod/go.mod: go $directive -> go $go_minor"
done

# mise tools pinned below their latest version.
outdated=$(mise outdated --bump --local --json |
	jq -r 'to_entries[] | "mise.toml: \(.key) \(.value.requested) -> \(.value.bump)"')
while read -r line; do
	[ -n "$line" ] && report "$line"
done <<<"$outdated"

# GitHub Actions pinned below the action's latest release. The repository is
# the project, so its workflows are the project root's; with none, the glob
# stays unexpanded and matches skips it.
workflows=(.github/workflows/*.yml)
actions=$(matches 'uses: *[^ ]*@[^ ]*' "${workflows[@]}" | sed 's/uses: *//' | sort -u)
for uses in $actions; do
	action=${uses%@*}
	pin=${uses#*@}
	repo=$(echo "$action" | cut -d/ -f1,2)
	latest=$(gh api "repos/$repo/releases/latest" --jq .tag_name)
	[ "$pin" = "$latest" ] || report ".github/workflows: $action $pin -> $latest"
done

# Image pins in the Go source behind the image's newest build tag on Docker Hub.
# A pin is a Go string literal repo:tag@sha256: and 64 hex digits, found after
# joining the literals a `+` splits across lines. A reference whose first
# component names another registry (it holds a dot or a colon, or is
# localhost) isn't on Docker Hub and is skipped; one with no namespace is an
# official image, library/<name>.
#
# The newest build tag comparable to the pinned one is the greatest, in version
# order (sort -V: digit runs compared as numbers, left to right), of the tags
# with the pinned tag's shape, each dotted number in it replaced by N. So
# linuxserver/ffmpeg's 9.0.2-cli-ls87 (N-cli-lsN) is compared with
# 9.0.2-cli-ls88 and 9.1.0-cli-ls1, either one trailing it, and never with
# arm64v8-9.0.2-cli-ls87 or latest; an official image's 1.27 (N) with 1.28.
# The tags read are the 100 most recently updated, where a newer build is.
#
# Each file is read as one line, its newlines made spaces, so the join and the
# match see a pin split across lines; a file that can't be read fails currency.
pins=$(git ls-files -z '*.go' | while IFS= read -r -d '' file; do
	refs=$(tr '\n' ' ' <"$file" | sed -E 's/"[[:space:]]*\+[[:space:]]*"//g' |
		{ grep -oE '"[^"[:space:]@]+:[[:alnum:]_][[:alnum:]_.-]*@sha256:[0-9a-f]{64}"' || [ $? -eq 1 ]; } |
		sed -E 's/^"([^@]*)@.*/\1/') || exit 1
	for ref in $refs; do
		echo "$file $ref"
	done
done | sort -u)
while read -r file ref; do
	[ -n "$ref" ] || continue
	repo=${ref%:*}
	tag=${ref##*:}
	case $repo in
	docker.io/* | index.docker.io/*) repo=${repo#*/} ;;
	esac
	case $repo in
	*/*)
		first=${repo%%/*}
		case $first in
		localhost | *.* | *:*) continue ;;
		esac
		;;
	*) repo=library/$repo ;;
	esac
	if ! tags=$(curl -fsS "https://hub.docker.com/v2/repositories/$repo/tags?page_size=100&ordering=last_updated" |
		jq -r '.results[].name'); then
		report "$file: image $ref: Docker Hub's tags for $repo unreadable"
		continue
	fi
	shape=$(echo "$tag" | sed -E 's/[0-9]+(\.[0-9]+)*/N/g')
	newest=$(printf '%s\n%s\n' "$tag" "$tags" |
		awk -v shape="$shape" '{ s = $0; gsub(/[0-9]+(\.[0-9]+)*/, "N", s); if (s == shape) print }' |
		sort -V | tail -n 1)
	[ "$newest" = "$tag" ] || report "$file: image $repo $tag -> $newest"
done <<<"$pins"

exit "$stale"
