#!/usr/bin/env python3
"""Measure phase 0c recovery against an isolated local relay and counting backend.

The daemon's production Link and cryptographic endpoint are exercised; this
does not replace native CLI acceptance or real mobile network testing. The
loss transport retries independently lost 1460-byte units after 200 ms without
changing host networking. Formal runs should use an otherwise idle machine.
"""
import argparse
import json
import math
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def quantile(values, fraction):
    return sorted(values)[max(0, math.ceil(len(values) * fraction) - 1)] if values else None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--samples", type=int, default=20)
    parser.add_argument("--loss-seconds", type=int, default=3600)
    parser.add_argument("--label", default="local-reference")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.samples < 1 or args.loss_seconds < 1:
        parser.error("samples and loss-seconds must be positive")
    root = Path(__file__).resolve().parents[1]
    volume = "/System/Volumes/Data" if Path("/System/Volumes/Data").exists() else "/tmp"
    if shutil.disk_usage(volume).free < 15 * 1024**3:
        raise RuntimeError("less than 15 GiB available")
    with tempfile.TemporaryDirectory(prefix="ra-recovery-", dir="/tmp") as temporary:
        work = Path(temporary)
        home = work / "agent"
        home.mkdir()
        env = dict(os.environ, REMOTE_AGENT_HOME=str(home), CGO_ENABLED="0", TMPDIR="/tmp",
                   RA_RECOVERY_METRICS=str(work / "metrics.json"),
                   RA_RECOVERY_SAMPLES=str(args.samples), RA_RECOVERY_LOSS_SECONDS=str(args.loss_seconds))
        assert home.resolve().is_relative_to(Path("/tmp").resolve())
        for key in ("RA_LIVE_E2E", "RA_REAL_ROLLOUTS", "RA_TEST_LAUNCHD"):
            env.pop(key, None)
        with (work / "check.log").open("w") as log:
            result = subprocess.run(["go", "test", "-count=1", "-timeout=3h", "./internal/relay",
                                     "-run", "^TestRecoveryMeasurements$"], cwd=root, env=env,
                                    stdout=log, stderr=subprocess.STDOUT,
                                    timeout=args.loss_seconds + args.samples * 100 + 600)
        if result.returncode:
            # Retain only test failure lines, never the relay's raw logs.
            failures = [line for line in (work / "check.log").read_text().splitlines()
                        if "--- FAIL:" in line or "recovery_measure_test.go:" in line]
            raise RuntimeError("measurement failed: " + "\n".join(failures))
        report = json.loads((work / "metrics.json").read_text())
        report["label"] = args.label
        report["commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
        report["dirty"] = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True))
        report["quantiles"] = {}
        for scenario in dict.fromkeys(row["scenario"] for row in report["samples"]):
            rows = [row for row in report["samples"] if row["scenario"] == scenario]
            entry = {"samples": len(rows), "ready": sum(row["ready"] for row in rows),
                     "successful_calls": sum(row["call"] for row in rows)}
            for key in ("discovery_ms", "recovery_ms", "total_ms"):
                values = [row[key] for row in rows if row["ready"] and (key == "discovery_ms" or row["call"])]
                entry[key] = {"p50": quantile(values, .5), "p95": quantile(values, .95), "p99": quantile(values, .99)}
            report["quantiles"][scenario] = entry
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps({"output": str(args.output), "scenarios": len(report["quantiles"]), "loss_seconds": report["loss"]["seconds"]}))


if __name__ == "__main__":
    main()
