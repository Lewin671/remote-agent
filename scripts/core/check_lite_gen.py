#!/usr/bin/env python3
"""Verify that clientcore/gen/remoteagent matches a clean `buf generate --template buf.gen.lite.yaml` run.

Because clientcore/gen also contains hand-written packages (litewire, timestamppb),
buf.gen.lite.yaml cannot set `clean: true` directly on clientcore/gen. This script
regenerates into a fresh temporary directory and compares every generated file under
remoteagent/ (catching both stale leftover files and content drift) without mutating
the working tree.
"""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
LITE_CFG = ROOT / "buf.gen.lite.yaml"
REPO_GEN = ROOT / "clientcore" / "gen" / "remoteagent"


def collect_files(base: Path) -> dict[str, bytes]:
    if not base.exists():
        return {}
    out: dict[str, bytes] = {}
    for p in sorted(base.rglob("*")):
        if p.is_file():
            out[p.relative_to(base).as_posix()] = p.read_bytes()
    return out


def main() -> int:
    if not shutil.which("buf"):
        print("check_lite_gen.py: `buf` is not installed", file=sys.stderr)
        return 1
    raw_cfg = LITE_CFG.read_text(encoding="utf-8")
    if "out: clientcore/gen" not in raw_cfg:
        print("check_lite_gen.py: unexpected buf.gen.lite.yaml (missing `out: clientcore/gen`)", file=sys.stderr)
        return 1

    with tempfile.TemporaryDirectory(prefix="ra-lite-gen-", dir="/tmp") as tmp:
        tmp_dir = Path(tmp)
        tmp_out = tmp_dir / "gen"
        tmp_out.mkdir()
        tmp_cfg = tmp_dir / "buf.gen.lite.yaml"
        tmp_cfg.write_text(raw_cfg.replace("out: clientcore/gen", f"out: {tmp_out}"), encoding="utf-8")

        res = subprocess.run(
            ["buf", "generate", "--template", str(tmp_cfg)],
            cwd=ROOT,
            capture_output=True,
            text=True,
        )
        if res.returncode != 0:
            print("check_lite_gen.py: buf generate failed:", file=sys.stderr)
            if res.stdout:
                print(res.stdout, file=sys.stderr)
            if res.stderr:
                print(res.stderr, file=sys.stderr)
            return res.returncode

        want = collect_files(tmp_out / "remoteagent")
        have = collect_files(REPO_GEN)

        extra = sorted(set(have) - set(want))
        missing = sorted(set(want) - set(have))
        changed = sorted(k for k in (set(have) & set(want)) if have[k] != want[k])

        if not extra and not missing and not changed:
            print(f"check_lite_gen.py: OK ({len(want)} generated files match clean buf.gen.lite.yaml)")
            return 0

        print("check_lite_gen.py: clientcore/gen/remoteagent is out of sync with buf.gen.lite.yaml!", file=sys.stderr)
        for k in extra:
            print(f"  stale file (not generated): clientcore/gen/remoteagent/{k}", file=sys.stderr)
        for k in missing:
            print(f"  missing file:               clientcore/gen/remoteagent/{k}", file=sys.stderr)
        for k in changed:
            print(f"  drifted file:               clientcore/gen/remoteagent/{k}", file=sys.stderr)
        print("Re-run: rm -rf clientcore/gen/remoteagent && buf generate --template buf.gen.lite.yaml", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
