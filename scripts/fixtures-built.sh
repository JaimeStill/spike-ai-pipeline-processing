#!/usr/bin/env bash
# fixtures-built rebuilds the email package's built .msg fixture,
# email/testdata/built, with spike-ai-pipeline-mail's fixtures generator at its
# tag, in its fixtures mode: one .eml source, written here from this
# repository's own committed office fixtures, in; one .msg, through MsgKit,
# out. Run it through `mise run fixtures:built`; the check never runs it, and
# reads the committed outputs.
#
# The source attaches five files, byte for byte: a docx and a legacy xls, each
# a committed office fixture; a TIFF and an EMF, each a member of a committed
# office fixture's package; and a .msg, one of the copied mail fixtures, which
# holds a PNG, so a reader that reads it again has its own attachment to route. The generator writes the .msg and its own
# manifest.json, which records the source's and the .msg's SHA-256 and its tool
# versions. MANIFEST then pins the whole directory: mail's repository, tag, and
# commit, the generator's .NET SDK, MsgKit, and MimeKit, each attachment's
# SHA-256 and where it came from, and each file's SHA-256.
#
# MsgKit's .msg isn't byte-stable (random record keys, the conversion time), so
# a rebuild changes the .msg's SHA-256; the tests pin its content instead,
# each attachment's bytes against its source's. The generator needs network
# access, for the tag and for its NuGet packages, and installs .NET SDK
# 10.0.402 through mise.
set -euo pipefail
cd "$MISE_PROJECT_ROOT"

repository=https://github.com/JaimeStill/spike-ai-pipeline-mail
tag=v0.1.0
sdk=10.0.402
built=email/testdata/built
source=office-attachments

# Each attachment: its filename in the message, its Content-Type, the committed
# fixture it comes from, and the member of that fixture's package, or - for the
# fixture itself.
attachments=(
	"minutes.docx|application/vnd.openxmlformats-officedocument.wordprocessingml.document|office/testdata/ooxml/tables.libreoffice.docx|-"
	"budget.xls|application/vnd.ms-excel|office/testdata/legacy/generated/formulas.libreoffice.xls|-"
	"scan.tif|image/tiff|office/testdata/pictures/tiff.docx|word/media/image1.tiff"
	"diagram.emf|image/emf|office/testdata/ooxml/word-with-attachments.docx|word/media/image4.emf"
	"forwarded.msg|application/vnd.ms-outlook|email/testdata/mail/fixtures/generated/msg/03-html-inline-image.msg|-"
)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# content writes an attachment's bytes, from its fixture or its member, to stdout.
content() {
	if [ "$2" = - ]; then
		cat "$1"
	else
		unzip -p "$1" "$2"
	fi
}

mkdir -p "$built/eml" "$built/msg"
eml=$built/eml/$source.eml
{
	printf 'Received: from mx.example.net by mail.example.org; Thu, 22 Oct 2026 09:15:00 +0000\r\n'
	printf 'Message-ID: <office-attachments-2026@example.net>\r\n'
	printf 'Date: Thu, 22 Oct 2026 10:14:00 +0100\r\n'
	printf 'From: Grace Hopper <grace@example.net>\r\n'
	printf 'To: Ada Lovelace <ada@example.org>\r\n'
	printf 'Subject: Minutes, budget, the scanned diagram, and a forwarded message\r\n'
	printf 'MIME-Version: 1.0\r\n'
	printf 'Content-Type: multipart/mixed; boundary="mixed-office"\r\n'
	printf '\r\n'
	printf -- '--mixed-office\r\n'
	printf 'Content-Type: text/plain; charset=utf-8\r\n'
	printf '\r\n'
	printf 'Ada,\r\n\r\nThe minutes, the budget, the scanned page, the diagram, and the message I was\r\nforwarded are attached.\r\n\r\nGrace\r\n'
	for entry in "${attachments[@]}"; do
		IFS='|' read -r filename type fixture member <<<"$entry"
		printf -- '--mixed-office\r\n'
		printf 'Content-Type: %s; name="%s"\r\n' "$type" "$filename"
		printf 'Content-Disposition: attachment; filename="%s"\r\n' "$filename"
		printf 'Content-Transfer-Encoding: base64\r\n'
		printf '\r\n'
		content "$fixture" "$member" | base64 -w 76 | sed 's/$/\r/'
	done
	printf -- '--mixed-office--\r\n'
} >"$eml"

printf '{\n  "stores": [],\n  "msg": ["%s.eml"]\n}\n' "$source" >"$built/layout.json"

# The generator at the tag, fetched alone and shallowly, so it is the tag's
# whatever any local checkout holds, and built in the scratch directory.
git -C "$work" init -q
git -C "$work" fetch -q --depth 1 "$repository" "refs/tags/$tag"
commit=$(git -C "$work" rev-parse 'FETCH_HEAD^{commit}')
mkdir "$work/mail"
git -C "$work" archive FETCH_HEAD generator | tar -x -C "$work/mail"
(
	cd "$work/mail/generator"
	DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 \
		mise exec "dotnet@$sdk" -- dotnet run -c Release -- fixtures "$MISE_PROJECT_ROOT/$built"
)

# tool prints the version of a tool the generator's manifest records.
tool() {
	jq -er --arg tool "$1" '.tools[$tool]' "$built/manifest.json"
}

{
	echo "# The built .msg: msg/$source.msg, built from eml/$source.eml"
	echo "# by mail's fixtures generator at the tag, through scripts/fixtures-built.sh"
	echo "# (mise run fixtures:built). Each attachment's line gives its filename, its"
	echo "# SHA-256, the committed fixture it came from, and the member of that fixture's"
	echo "# package, or - for the fixture itself; each file's SHA-256 is below. MsgKit's"
	echo "# .msg isn't byte-stable, so a rebuild changes its SHA-256."
	echo "repository $repository"
	echo "tag $tag"
	echo "commit $commit"
	echo "dotnet-sdk $(tool dotnet-sdk)"
	echo "MsgKit $(tool MsgKit)"
	echo "MimeKit $(tool MimeKit)"
	for entry in "${attachments[@]}"; do
		IFS='|' read -r filename _ fixture member <<<"$entry"
		echo "attachment $filename $(content "$fixture" "$member" | sha256sum | cut -d' ' -f1) $fixture $member"
	done
	(cd "$built" && find . -type f ! -name MANIFEST -printf '%P\0' | LC_ALL=C sort -z | xargs -0 sha256sum)
} >"$built/MANIFEST"
echo "fixtures-built: wrote $built from $tag ($commit)"
