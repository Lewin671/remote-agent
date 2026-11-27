"""The development entry point must not inherit a personal daemon/session."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class CheckIsolationTest(unittest.TestCase):
    def test_success_and_failure_use_temporary_state_and_clean_it_up(self):
        with tempfile.TemporaryDirectory(prefix="ra-check-probe-", dir="/tmp") as temporary:
            root = Path(temporary)
            go = root / "go"
            go.write_text(f"#!{sys.executable}\n" + '''import json, os
from pathlib import Path
keys = ["RA_SESSION", "RA_EXE", "REMOTE_AGENT_HOME", "REMOTE_AGENT_TMUX_SOCKET", "RA_DEV_DIR", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "RA_REAL_ROLLOUTS", "RA_TEST_LAUNCHD", "RA_LIVE_E2E", "GEN_VECTORS"]
Path(os.environ["RA_CHECK_PROBE"]).write_text(json.dumps({key: os.environ.get(key) for key in keys}))
raise SystemExit(int(os.environ["RA_CHECK_EXIT"]))
''')
            go.chmod(0o700)
            for status in (0, 23):
                with self.subTest(status=status):
                    probe = root / "probe.json"
                    env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ.get("PATH", ""),
                               RA_SESSION="personal-session", RA_EXE="/personal/remote-agent",
                               REMOTE_AGENT_HOME=str(root / "personal-state"), REMOTE_AGENT_TMUX_SOCKET="personal-socket",
                               RA_CHECK_PROBE=str(probe), RA_CHECK_EXIT=str(status), RA_REAL_ROLLOUTS="personal-history",
                               RA_TEST_LAUNCHD="1", RA_LIVE_E2E="1", GEN_VECTORS="1")
                    result = subprocess.run([sys.executable, str(ROOT / "scripts/dev/check.py"), "go", "quick"],
                                            env=env, capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode, status, result.stdout + result.stderr)
                    recorded = json.loads(probe.read_text())
                    for key in ("REMOTE_AGENT_HOME", "RA_DEV_DIR", "CODEX_HOME", "CLAUDE_CONFIG_DIR"):
                        path = Path(recorded[key])
                        self.assertTrue(str(path).startswith(("/tmp/", "/private/tmp/")))
                        self.assertFalse(path.exists(), "state must be removed on both success and failure")
                    self.assertNotEqual(recorded["REMOTE_AGENT_TMUX_SOCKET"], "personal-socket")
                    for key in ("RA_SESSION", "RA_EXE", "RA_REAL_ROLLOUTS", "RA_TEST_LAUNCHD", "RA_LIVE_E2E", "GEN_VECTORS"):
                        self.assertIsNone(recorded[key])


    # check.sh drives Xcode and uses the BSD tools of macOS; the release checks run on Linux.
    @unittest.skipUnless(sys.platform == "darwin", "ios/scripts/check.sh runs on macOS only")
    def test_ios_modes_select_their_steps_and_the_default_skips_ui_automation(self):
        with tempfile.TemporaryDirectory(prefix="ra-ios-modes-", dir="/tmp") as temporary:
            root = Path(temporary)
            probe = root / "calls.jsonl"
            recorder = f"#!{sys.executable}\n" + '''import json, os, sys
from pathlib import Path
with open(os.environ["RA_MODE_PROBE"], "a") as output:
    output.write(json.dumps({"tool": Path(sys.argv[0]).name, "args": sys.argv[1:]}) + "\\n")
'''
            for tool in ("python3", "swift", "buf", "git", "xcrun", "xcodebuild", "core-build"):
                path = root / tool
                path.write_text(recorder)
                path.chmod(0o700)
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ.get("PATH", ""),
                       REMOTE_AGENT_HOME=str(root / "agent"), REMOTE_AGENT_TMUX_SOCKET="isolated-mode-test",
                       RA_DEV_DIR=str(root / "dev"), RA_MODE_PROBE=str(probe),
                       IOS_DESTINATION="platform=iOS Simulator,id=isolated-device",
                       IOS_SWIFT_BUILD_PATH=str(root / "swift-cache"), IOS_DERIVED_DATA_PATH=str(root / "derived"),
                       RA_CORE_BUILD=str(root / "core-build"))
            for mode in (None, "full", "core", "app", "ui", "perf"):
                with self.subTest(mode=mode):
                    probe.unlink(missing_ok=True)
                    args = ["/bin/sh", str(ROOT / "ios/scripts/check.sh")]
                    if mode: args.append(mode)
                    result = subprocess.run(args, env=env, capture_output=True, text=True, timeout=15)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    calls = [json.loads(line) for line in probe.read_text().splitlines()]
                    tools = [call["tool"] for call in calls]
                    self.assertIn("python3", tools)
                    self.assertIn("xcrun", tools)
                    self.assertIn({"tool": "core-build", "args": ["ios"]}, calls)
                    if mode in (None, "full", "core"):
                        swift = next(call for call in calls if call["tool"] == "swift")
                        self.assertIn(str(root / "swift-cache"), swift["args"])
                    else: self.assertNotIn("swift", tools)
                    if mode in (None, "full"):
                        self.assertIn("buf", tools)
                        self.assertIn("git", tools)
                    else: self.assertNotIn("buf", tools)
                    if mode == "core":
                        self.assertNotIn("xcodebuild", tools)
                    else:
                        native = next(call for call in calls if call["tool"] == "xcodebuild")
                        self.assertIn(str(root / "derived"), native["args"])
                        selected = [arg for arg in native["args"] if arg.startswith("-only-testing:")]
                        # No argument is the delivery check: everything except UI automation and perf benchmarks.
                        expected = {None: ["-only-testing:RemoteAgentTests"], "full": [], "app": ["-only-testing:RemoteAgentTests"], "ui": ["-only-testing:RemoteAgentUITests"], "perf": ["-only-testing:RemoteAgentPerfTests"]}[mode]
                        self.assertEqual(selected, expected)
                        self.assertIn("-resultBundlePath", native["args"])


    @unittest.skipUnless(sys.platform == "darwin", "ios/scripts/check.sh runs on macOS only")
    def test_ios_check_shuts_down_only_the_simulator_it_booted(self):
        # A simulator left booted for days stacks PosterBoard crash dialogs on the desktop.
        udid = "0A1B2C3D-0000-4000-8000-000000000001"
        with tempfile.TemporaryDirectory(prefix="ra-ios-sim-", dir="/tmp") as temporary:
            root = Path(temporary)
            booted, shutdowns = root / "booted", root / "shutdowns"
            recorder = f"#!{sys.executable}\n"
            simulator = recorder + f'''import os, sys
from pathlib import Path
booted = Path({str(booted)!r})
if sys.argv[1:3] == ["simctl", "list"]:
    print("    iPhone 18 Pro ({udid}) (" + ("Booted" if booted.exists() else "Shutdown") + ")")
elif sys.argv[1:3] == ["simctl", "shutdown"]:
    with open({str(shutdowns)!r}, "a") as output: output.write(sys.argv[3] + "\\n")
    booted.unlink(missing_ok=True)
'''
            xcodebuild = recorder + f'''import os, sys
from pathlib import Path
Path({str(booted)!r}).touch()
sys.exit(int(os.environ["RA_XCODEBUILD_EXIT"]))
'''
            for tool, body in (("xcrun", simulator), ("xcodebuild", xcodebuild), ("python3", recorder), ("core-build", recorder)):
                path = root / tool
                path.write_text(body)
                path.chmod(0o700)
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ.get("PATH", ""),
                       IOS_DESTINATION="platform=iOS Simulator,name=iPhone 18 Pro",
                       IOS_DERIVED_DATA_PATH=str(root / "derived"), RA_CORE_BUILD=str(root / "core-build"))
            for already_booted, exit_code, expected in ((False, 0, [udid]), (False, 65, [udid]), (True, 0, [])):
                with self.subTest(already_booted=already_booted, exit_code=exit_code):
                    shutdowns.unlink(missing_ok=True)
                    if already_booted: booted.touch()
                    else: booted.unlink(missing_ok=True)
                    result = subprocess.run(["/bin/sh", str(ROOT / "ios/scripts/check.sh"), "app"],
                                            env=dict(env, RA_XCODEBUILD_EXIT=str(exit_code)), capture_output=True, text=True, timeout=15)
                    self.assertEqual(result.returncode, 1 if exit_code else 0, result.stdout + result.stderr)
                    self.assertEqual(shutdowns.read_text().split() if shutdowns.exists() else [], expected)
                    self.assertEqual(booted.exists(), already_booted)

if __name__ == "__main__":
    unittest.main()
