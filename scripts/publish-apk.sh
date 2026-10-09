#!/bin/sh
# Mirrors a packaged release to the public repository the app updates
# itself from. The source repository is private; that one holds only the
# latest APK, its manifest and a README, as a single commit that each
# release replaces, so it never grows by an APK per release.
#
# Usage: scripts/publish-apk.sh TAG      (after scripts/package-release.sh TAG)
# RELEASES_DEPLOY_KEY is the private half of that repository's deploy key;
# RELEASES_REPO overrides where it is pushed.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
tag=${1:?usage: publish-apk.sh TAG}
repo=${RELEASES_REPO:-git@github.com:Lewin671/remote-agent-releases.git}
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
sha=$(sed -n 's/^sha256=//p' "$manifest")
{
	printf '# remote-agent\n\n'
	printf 'remote-agent 的 Android 安装包。这里只放最新一版，App 也从这里检查和下载更新。\n\n'
	printf -- '- 版本：%s\n' "${tag#v}"
	printf -- '- 下载：[remote-agent-%s.apk](remote-agent-%s.apk)\n' "$tag" "$tag"
	printf -- '- SHA-256：`%s`\n' "$sha"
} >"$work/repo/README.md"
cd "$work/repo"
git init -q -b main
git add -A
git -c user.name="remote-agent release" -c user.email="release@users.noreply.github.com" commit -q -m "remote-agent ${tag#v}"
# One commit replaces the last: nothing here is history worth keeping.
git push -q --force "$repo" HEAD:main
echo "Published $tag to $repo"
