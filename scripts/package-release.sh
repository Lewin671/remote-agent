#!/bin/sh
# Called only after checks pass, from a clean tagged checkout.
# Usage: scripts/package-release.sh TAG      (after scripts/build-dist.sh)
#
# Puts the release together in dist/: the APK next to the archives that
# scripts/build-dist.sh made, a checksum for each, and the manifest the app
# reads from the public releases to update itself. All of it is public, so
# it is scanned for anything of ours before this succeeds.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
tag=${1:?usage: package-release.sh TAG}
python3 scripts/release.py validate "$tag" > /dev/null
mkdir -p dist
cp android/app/build/outputs/apk/release/app-release.apk "dist/remote-agent-$tag.apk"
files="remote-agent-$tag.apk remote-agent-web-$tag.tar.gz"
for target in darwin-arm64 darwin-amd64 linux-arm64 linux-amd64; do
	files="$files remote-agent-$tag-$target.tar.gz"
done
for target in linux-arm64 linux-amd64; do
	files="$files remote-agent-server-$tag-$target.tar.gz"
done
for f in $files; do
	[ -f "dist/$f" ] || {
		echo "dist/$f is missing: run scripts/build-dist.sh at $tag first" >&2
		exit 1
	}
done
# A release is built from its tag: an archive of another commit says so in its version.
if [ "$(uname -sm)" = "Linux x86_64" ]; then
	check=$(mktemp -d)
	tar -xzf "dist/remote-agent-$tag-linux-amd64.tar.gz" -C "$check"
	built=$("$check/remote-agent-$tag-linux-amd64/remote-agent" --version)
	rm -rf "$check"
	case $built in
	"remote-agent ${tag#v} "*) ;;
	*)
		echo "The archives were built as '$built', not as $tag" >&2
		exit 1
		;;
	esac
fi
# shellcheck disable=SC2086
python3 - $files <<'PY'
import hashlib
import sys
from pathlib import Path
p = Path('dist')
(p / 'SHA256SUMS').write_text(''.join(f'{hashlib.sha256((p / name).read_bytes()).hexdigest()}  {name}\n' for name in sys.argv[1:]))
PY
python3 scripts/release.py manifest "$tag" "dist/remote-agent-$tag.apk" > dist/latest.properties
# shellcheck disable=SC2046
python3 scripts/public_scan.py dist/SHA256SUMS dist/latest.properties $(printf 'dist/%s ' $files) deploy/releases-repo
