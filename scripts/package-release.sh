#!/bin/sh
# Called only after checks pass, from a clean tagged checkout.
# Release distributes only the installable APK and its checksum.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
python3 scripts/release.py validate "$1" > /dev/null
mkdir -p dist
cp android/app/build/outputs/apk/release/app-release.apk "dist/remote-agent-$1.apk"
python3 - "$1" <<'PY'
import hashlib
import sys
from pathlib import Path
p = Path('dist')
apk = p / f'remote-agent-{sys.argv[1]}.apk'
(p / 'SHA256SUMS').write_text(f'{hashlib.sha256(apk.read_bytes()).hexdigest()}  {apk.name}\n')
PY
