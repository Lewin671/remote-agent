#!/bin/sh
# Called only after tests pass, from a clean tagged checkout.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
python3 scripts/release.py validate "$1" > /dev/null
mkdir -p dist
for target in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
    os=${target%-*}
    arch=${target#*-}
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch scripts/build-go.sh "dist/remote-agent-$target" ./cmd/remote-agent
 done
for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch scripts/build-go.sh "dist/remote-agent-server-linux-$arch" ./cmd/remote-agent-server
done
cp android/app/build/outputs/apk/release/app-release.apk "dist/remote-agent-$1.apk"
tar -czf "dist/remote-agent-web-$1.tar.gz" -C web/dist .
python3 scripts/release.py validate "$1" > dist/build-info.json
python3 - <<'PY'
import hashlib
from pathlib import Path
p = Path('dist')
files = sorted(f for f in p.iterdir() if f.is_file() and f.name != 'SHA256SUMS')
(p/'SHA256SUMS').write_text(''.join(f'{hashlib.sha256(f.read_bytes()).hexdigest()}  {f.name}\n' for f in files))
PY
