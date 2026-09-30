#!/bin/sh
# Builds either Go command with the shared product version and source revision.
# Usage: scripts/build-go.sh <output> <package>
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
version=$(sed -n 's/^version=//p' "$ROOT/version.properties")
commit=$(git -C "$ROOT" rev-parse HEAD)
if ! git -C "$ROOT" describe --exact-match --match "v$version" HEAD >/dev/null 2>&1; then
    version="$version-dev.$(git -C "$ROOT" rev-parse --short HEAD)"
fi
if [ -n "$(git -C "$ROOT" status --porcelain --untracked-files=no)" ]; then
    version="$version-dirty"
fi
cd "$ROOT"
go build -trimpath -ldflags="-s -w -X github.com/Lewin671/remote-agent/internal/buildinfo.Version=$version -X github.com/Lewin671/remote-agent/internal/buildinfo.Commit=$commit" -o "$1" "$2"
