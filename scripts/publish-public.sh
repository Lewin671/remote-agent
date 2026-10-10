#!/bin/sh
# Publishes a packaged release in the public repository: where the app
# updates itself from, and where other people get the daemon, the server
# and the web client to deploy their own. The source repository is private;
# that one holds releases and the pages on how to use them, nothing else.
#
# This repository's workflow cannot create a release in another one, and a
# personal token that could would have to be made by hand and may expire.
# So the release's files are pushed, with a deploy key, to a branch
# named `incoming` there, and that repository's own workflow
# (deploy/releases-repo/) turns the branch into a release and deletes it.
# The branch is one commit on top of that repository's main: GitHub runs a
# workflow for a push only if the pushed commit contains its file.
# This script then waits until the release's manifest can be downloaded.
#
# Usage: scripts/publish-public.sh TAG      (after scripts/package-release.sh TAG)
# RELEASES_DEPLOY_KEY is the private half of that repository's deploy key;
# RELEASES_REPO overrides where it is pushed, RELEASES_URL where the
# release is then looked for (empty: do not wait).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
tag=${1:?usage: publish-public.sh TAG}
repo=${RELEASES_REPO:-git@github.com:Lewin671/remote-agent-releases.git}
url=${RELEASES_URL-https://github.com/Lewin671/remote-agent-releases/releases}
manifest="$ROOT/dist/latest.properties"
sums="$ROOT/dist/SHA256SUMS"
[ -f "$sums" ] && [ -f "$manifest" ] || {
	echo "Package the release first: scripts/package-release.sh $tag" >&2
	exit 1
}
grep -qx "apk=remote-agent-$tag.apk" "$manifest" || {
	echo "dist/latest.properties is not for $tag" >&2
	exit 1
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
if [ -n "${RELEASES_DEPLOY_KEY:-}" ]; then
	(umask 077 && printf '%s\n' "$RELEASES_DEPLOY_KEY" >"$work/key")
	export GIT_SSH_COMMAND="ssh -i $work/key -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
fi
git clone -q --depth 1 --branch main "$repo" "$work/repo"
# Only what the checksums name is handed over, and only as they say it is.
mkdir "$work/files"
for f in $(awk '{print $2}' "$sums"); do
	cp "$ROOT/dist/$f" "$work/files/"
done
cp "$sums" "$manifest" "$work/files/"
(cd "$work/files" && if command -v sha256sum >/dev/null; then sha256sum --check --quiet SHA256SUMS; else shasum -a 256 --check --quiet SHA256SUMS; fi)
# The tag's message, as the release's notes.
git -C "$ROOT" tag --list "$tag" --format='%(contents)' >"$work/files/notes.md"
# Nothing leaves without the scan, however this script was reached.
python3 "$ROOT/scripts/public_scan.py" "$work/files"
cp "$work/files"/* "$work/repo/"
cd "$work/repo"
[ -f .github/workflows/publish.yml ] || {
	echo "$repo has no Publish workflow on main; push deploy/releases-repo/ there first" >&2
	exit 1
}
git checkout -q -b incoming
git add -A
# The Actions bot's own address: a made-up one may be some stranger's account, and
# GitHub then shows the commit as theirs.
git -c user.name="github-actions[bot]" -c user.email="41898282+github-actions[bot]@users.noreply.github.com" commit -q -m "remote-agent ${tag#v}"
# The branch is only a hand-over: whatever an earlier run left there is replaced.
git push -q --force "$repo" HEAD:incoming
echo "Handed $tag to $repo"

[ -n "$url" ] || exit 0
want=$(sed -n 's/^sha256=//p' "$manifest")
i=0
while [ "$i" -lt 60 ]; do
	got=$(curl -fsSL --max-time 20 "$url/download/$tag/latest.properties" 2>/dev/null | sed -n 's/^sha256=//p' || true)
	# The checksums of every file, as published, are the ones handed over.
	if [ "$got" = "$want" ] && curl -fsSL --max-time 20 "$url/download/$tag/SHA256SUMS" 2>/dev/null | cmp -s - "$sums"; then
		echo "Published $tag at $url/tag/$tag"
		exit 0
	fi
	i=$((i + 1))
	sleep 5
done
echo "The release $tag did not appear at $url within 5 minutes; see the Publish workflow of that repository" >&2
exit 1
