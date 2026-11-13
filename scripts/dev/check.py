#!/usr/bin/env python3
"""Isolated, bounded development checks. Full always executes tests afresh.

Quick checks are a feedback loop, not delivery/release acceptance. Build caches
are retained; test result caches are bypassed, including Gradle's --tests runs.
"""
import argparse
import os
from pathlib import Path
import signal
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
GO_QUICK = [
    "agent", "antigravity", "claude", "codex", "contextio", "deviceauth", "e2e",
    "eventview", "i18n", "netpolicy", "paths", "protocol", "schedule", "tail",
]
SWIFT_QUICK = "RAMarkdownTests|AttemptTests|PagedListTests|PrefsTests|TimelineTests|ProjectTests|SchedulesTests|VectorsTests"
ANDROID_QUICK = ["*AttemptTest", "*PrefsTest", "*ProjectionTest", "*SchedulesTest", "*VectorsTest"]


def commands(platform, tier):
    if platform == "go":
        packages = ["./internal/" + p for p in GO_QUICK] if tier == "quick" else ["./..."]
        return [(ROOT, ["go", "test", "-count=1", "-parallel=8", "-timeout=5m", *packages])]
    if platform == "web":
        return [(ROOT / "web", ["pnpm", "typecheck"]), (ROOT / "web", ["pnpm", "test"])]
    if platform == "android":
        args = ["./gradlew", "--build-cache", "--parallel", "--max-workers=4", ":core:test", "--rerun"]
        if tier == "quick":
            for pattern in ANDROID_QUICK:
                args += ["--tests", pattern]
        else:
            args += [":terminal:testReleaseUnitTest", "--rerun", ":app:testDebugUnitTest", "--rerun", ":app:lintVitalRelease"]
        return [(ROOT / "android", args)]
    if tier == "quick":
        args = ["swift", "test", "--package-path", "ios/RemoteAgentCore", "--jobs", "4", "--filter", SWIFT_QUICK]
        if os.environ.get("IOS_SWIFT_BUILD_PATH"):
            args += ["--scratch-path", os.environ["IOS_SWIFT_BUILD_PATH"]]
        return [(ROOT, args)]
    return [(ROOT, ["ios/scripts/check.sh"])]


def execute(cwd, args, env, timeout):
    print("+ " + " ".join(args), flush=True)
    process = subprocess.Popen(args, cwd=cwd, env=env, start_new_session=True)
    try:
        return process.wait(timeout=timeout)
    except (subprocess.TimeoutExpired, KeyboardInterrupt) as error:
        # Kill the whole check group, including fixture children. Never leave a
        # timed-out test server or native helper running in the background.
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        # The parent may exit before its children; still reap the group.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait()
        print(f"check stopped: {type(error).__name__}", flush=True)
        return 124 if isinstance(error, subprocess.TimeoutExpired) else 130


def main():
    def interrupted(_number, _frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("platform", choices=["go", "web", "android", "ios"])
    parser.add_argument("tier", choices=["quick", "full"])
    options = parser.parse_args()
    if not shutil.which("go"):
        parser.error("Go is required to build the protocol fixtures")
    # Short /tmp paths keep native AF_UNIX sockets below the macOS limit.
    with tempfile.TemporaryDirectory(prefix="ra-check-", dir="/tmp") as temporary:
        directory = Path(temporary).resolve()
        assert str(directory).startswith(("/tmp/", "/private/tmp/"))
        env = dict(os.environ)
        for key, name in [("REMOTE_AGENT_HOME", "agent"), ("RA_DEV_DIR", "dev"),
                          ("CODEX_HOME", "codex"), ("CLAUDE_CONFIG_DIR", "claude")]:
            path = directory / name
            path.mkdir()
            env[key] = str(path)
        env["REMOTE_AGENT_TMUX_SOCKET"] = directory.name
        # Never inherit opt-in tests of personal sessions or installed services.
        for key in ("RA_REAL_ROLLOUTS", "RA_TEST_LAUNCHD", "RA_LIVE_E2E", "RA_RUN_SCHEDULE_PUSH_E2E",
                    "GEN_VECTORS", "UPDATE_REPLACEMENT_VECTORS", "RA_TEST_NO_GO"):
            env.pop(key, None)
        print("Isolated state: " + env["REMOTE_AGENT_HOME"], flush=True)
        import time
        deadline = time.monotonic() + (60 if options.tier == "quick" else 1800)
        for cwd, args in commands(options.platform, options.tier):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return 124
            result = execute(cwd, args, env, remaining)
            if result:
                return result
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
