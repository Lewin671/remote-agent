#!/bin/sh
# Publishes a packaged release in the public repository the app updates
# itself from. The source repository is private; that one holds releases
# of the APK and nothing else.
#
# This repository's workflow cannot create a release in another one, and a
# personal token that could would have to be made by hand and may expire.
# So the APK and its manifest are pushed, with a deploy key, to a branch
# named `incoming` there, and that repository's own workflow
# (deploy/releases-repo/) turns the branch into a release and deletes it.
# This script then waits until the release's manifest can be downloaded.
#
# Usage: scripts/publish-apk.sh TAG      (after scripts/package-release.sh TAG)
# RELEASES_DEPLOY_KEY is the private half of that repository's deploy key;
# RELEASES_REPO overrides where it is pushed, RELEASES_URL where the
# release is then looked for (empty: do not wait).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
tag=${1:?usage: publish-apk.sh TAG}
repo=${RELEASES_REPO:-git@github.com:Lewin671/remote-agent-releases.git}
url=${RELEASES_URL-https://github.com/Lewin671/remote-agent-releases/releases}
apk="$ROOT/dist/remote-agent-$tag.apk"
manifest="$ROOT/dist/latest.properties"
[ -f "$apk" ] && [ -f "$manifest" ] || {
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
mkdir "$work/repo"
cp "$apk" "$manifest" "$work/repo/"
# The tag's message, as the release's notes.
git -C "$ROOT" tag --list "$tag" --format='%(contents)' >"$work/repo/notes.md"
cd "$work/repo"
git init -q -b incoming
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
	if [ "$got" = "$want" ]; then
		echo "Published $tag at $url/tag/$tag"
		exit 0
	fi
	i=$((i + 1))
	sleep 5
done
echo "The release $tag did not appear at $url within 5 minutes; see the Publish workflow of that repository" >&2
exit 1
