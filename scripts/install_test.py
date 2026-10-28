"""Test release/service installers with temporary files and fake OS managers.

Run: python3 scripts/install_test.py
"""

import hashlib
import os
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
OFF = "Automatic updates are off; enable with: remote-agent update auto enable"
ON = "Automatic updates enabled (daily at 04:00 local time)."
SKIPPED = "Automatic-update setup skipped (REMOTE_AGENT_AUTO_UPDATE=0)."


class InstallEnvironment(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.work = Path(self.tmp.name)
        self.home = self.work / "home"
        self.state = self.work / "custom state"
        self.bin = self.work / "custom bin"
        self.tools = self.work / "fake managers"
        for path in (self.home, self.state, self.bin, self.tools):
            path.mkdir()
        self.calls = self.work / "calls"
        self.running = self.work / "running"
        self.env = dict(os.environ, HOME=str(self.home), REMOTE_AGENT_HOME=str(self.state),
                        BIN_DIR=str(self.bin), BIN=str(self.bin / "remote-agent"),
                        XDG_DATA_HOME=str(self.home / "data"), PATH=f"{self.tools}:{os.environ['PATH']}",
                        REMOTE_AGENT_RELEASES=(self.work / "releases").as_uri(),
                        REMOTE_AGENT_VERSION="v1.2.3", REMOTE_AGENT_AUTO_UPDATE="1",
                        TEST_CALLS=str(self.calls), TEST_RUNNING=str(self.running),
                        TEST_OS=platform.system(), TEST_CHANNEL="release",
                        TEST_ENABLE_FAIL="0", TEST_SERVICE_FAIL="0")
        self.write_executable(self.bin / "remote-agent", '''#!/bin/sh
set -eu
if [ "$1" = --version ]; then echo 1.2.3; exit; fi
echo "$*" >> "$TEST_CALLS"
case "$*" in
ls) [ -f "$TEST_RUNNING" ] ;;
update-build-info) printf '{\n  "channel": "%s"\n}\n' "$TEST_CHANNEL" ;;
'update auto enable')
    [ "$TEST_ENABLE_FAIL" = 0 ] || exit 1
    state=${REMOTE_AGENT_HOME:-$HOME/.remote-agent}
    mkdir -p "$state"
    printf '{"enabled":true,"time":"04:00"}\\n' > "$state/update-auto.json"
    ;;
*) exit 2 ;;
esac
''')
        (self.bin / "remote-agent-service").write_bytes((ROOT / "scripts/daemon.sh").read_bytes())
        (self.bin / "remote-agent-service").chmod(0o755)
        # All service commands resolve to these stubs, never to the host's
        # launchctl/systemctl. Only a marker models the daemon becoming ready.
        for manager in ("launchctl", "systemctl"):
            self.write_executable(self.tools / manager, '''#!/bin/sh
set -eu
echo "manager $(basename "$0") $*" >> "$TEST_CALLS"
[ "$TEST_SERVICE_FAIL" = 0 ] || exit 1
case " $* " in
*' bootstrap '*|*' restart '*|*' kickstart '*) touch "$TEST_RUNNING" ;;
esac
''')
        self.write_executable(self.tools / "uname", '''#!/bin/sh
if [ "${1:-}" = -m ]; then exec /usr/bin/uname -m; fi
printf '%s\\n' "$TEST_OS"
''')

    @staticmethod
    def write_executable(path, text):
        path.write_text(text)
        path.chmod(0o755)

    @property
    def unit(self):
        return self.home / (
            "Library/LaunchAgents/com.github.lewin671.remote-agent.plist"
            if self.env["TEST_OS"] == "Darwin" else ".config/systemd/user/remote-agent.service"
        )

    def existing_service(self, binary=None):
        self.unit.parent.mkdir(parents=True, exist_ok=True)
        self.unit.write_text(str(binary or self.bin / "remote-agent"))

    def run_script(self, path, *args, success=True):
        result = subprocess.run(["sh", str(path), *args], env=self.env,
                                cwd=self.work, text=True, capture_output=True, timeout=25)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout

    def recorded(self):
        return self.calls.read_text().splitlines() if self.calls.exists() else []

    def assert_no_enable(self):
        self.assertNotIn("update auto enable", self.recorded())


class InstallerTest(InstallEnvironment):
    def setUp(self):
        super().setUp()
        os_name = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
        arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}[platform.machine()]
        name = f"remote-agent-v1.2.3-{os_name}-{arch}"
        stage = self.work / name
        stage.mkdir()
        for filename in ("remote-agent", "remote-agent-service"):
            (stage / filename).write_bytes((self.bin / filename).read_bytes())
            (stage / filename).chmod(0o755)
        (stage / "THIRD_PARTY_NOTICES.txt").write_text("test notices")
        release = self.work / "releases/download/v1.2.3"
        release.mkdir(parents=True)
        archive = release / f"{name}.tar.gz"
        with tarfile.open(archive, "w:gz") as tar:
            tar.add(stage, arcname=name)
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        (release / "SHA256SUMS").write_text(f"{digest}  {archive.name}\n")

    def install(self):
        return self.run_script(ROOT / "install.sh")

    def test_fresh_install_only_prints_original_next_steps(self):
        output = self.install()
        self.assertIn("""Next:
  remote-agent-service install        run the daemon in the background, now and at login
  In the phone app: create the account on https://ra.example.com with the setup token,
  then run: remote-agent account join --server https://ra.example.com --code CODE
                                      (see the self-hosting guide)
  remote-agent codex | claude         start a session here
""", output)
        self.assertNotIn("Automatic", output)
        self.assertEqual(self.recorded(), [])
        self.assertFalse(self.unit.exists())
        self.assertFalse((self.state / "update-auto.json").exists())

    def test_upgrade_enables_only_after_successful_restart(self):
        self.existing_service()
        self.assertIn(ON, self.install())
        calls = self.recorded()
        self.assertTrue(calls[0].startswith("manager "))
        self.assertLess(calls.index("ls"), calls.index("update auto enable"))
        path = self.state / "update-auto.json"
        before = path.read_bytes()
        self.install()
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(self.recorded().count("update auto enable"), 1)

    def test_upgrade_preserves_explicit_disable(self):
        self.existing_service()
        path = self.state / "update-auto.json"
        choice = b'{"enabled":false,"time":"08:00"}\n'
        path.write_bytes(choice)
        self.install()
        self.install()
        self.assertEqual(path.read_bytes(), choice)
        self.assert_no_enable()

    def test_upgrade_opt_out_and_retry(self):
        self.existing_service()
        self.env["REMOTE_AGENT_AUTO_UPDATE"] = "0"
        self.assertIn(SKIPPED, self.install())
        self.assertFalse((self.state / "update-auto.json").exists())
        self.env["REMOTE_AGENT_AUTO_UPDATE"] = "1"
        self.assertIn(ON, self.install())

    def test_restart_failure_does_not_attempt_enable(self):
        self.existing_service()
        self.env["TEST_SERVICE_FAIL"] = "1"
        self.install()
        self.assertNotIn("update-build-info", self.recorded())
        self.assert_no_enable()

    def test_failed_enable_does_not_fail_upgrade_and_retries(self):
        self.existing_service()
        self.env["TEST_ENABLE_FAIL"] = "1"
        self.assertIn(OFF, self.install())
        self.assertFalse((self.state / "update-auto.json").exists())
        self.env["TEST_ENABLE_FAIL"] = "0"
        self.assertIn(ON, self.install())
        self.assertEqual(self.recorded().count("update auto enable"), 2)

    def test_source_channel_upgrade_skips_quietly(self):
        self.existing_service()
        self.env["TEST_CHANNEL"] = "source"
        self.assertNotIn("Automatic", self.install())
        self.assertIn("update-build-info", self.recorded())
        self.assert_no_enable()

    def test_another_binary_service_is_left_alone(self):
        self.existing_service("/another/remote-agent")
        self.assertNotIn("Automatic", self.install())
        self.assertEqual(self.recorded(), [])
        self.assertEqual(self.unit.read_text(), "/another/remote-agent")


class ServiceInstallerTest(InstallEnvironment):
    def service_install(self, success=True):
        # Run the checkout script itself, with the installed command's channel
        # deciding policy. No real service manager or real daemon is invoked.
        return self.run_script(ROOT / "scripts/daemon.sh", "install", success=success)

    def test_both_os_service_installs_enable_after_daemon_is_up(self):
        for system in ("Darwin", "Linux"):
            with self.subTest(system=system):
                self.env["TEST_OS"] = system
                self.running.unlink(missing_ok=True)
                self.calls.unlink(missing_ok=True)
                path = self.state / "update-auto.json"
                path.unlink(missing_ok=True)
                self.assertIn(ON, self.service_install())
                self.assertTrue(self.unit.exists())
                calls = self.recorded()
                self.assertEqual(calls[0], "ls")
                self.assertLess(max(i for i, c in enumerate(calls) if c == "ls"),
                                calls.index("update auto enable"))

    def test_any_existing_choice_is_kept(self):
        self.existing_service()
        path = self.state / "update-auto.json"
        for choice in (b'{"enabled":false}', b'{"enabled":true,"time":"07:00"}', b'corrupt'):
            with self.subTest(choice=choice):
                path.write_bytes(choice)
                self.assertNotIn("Automatic", self.service_install())
                self.assertEqual(path.read_bytes(), choice)
                self.assert_no_enable()

    def test_opt_out_and_failed_enable_keep_service_install_successful(self):
        self.env["REMOTE_AGENT_AUTO_UPDATE"] = "0"
        self.assertIn(SKIPPED, self.service_install())
        self.assertFalse((self.state / "update-auto.json").exists())
        self.env["REMOTE_AGENT_AUTO_UPDATE"] = "1"
        self.env["TEST_ENABLE_FAIL"] = "1"
        self.assertIn(OFF, self.service_install())
        self.assertFalse((self.state / "update-auto.json").exists())
        self.env["TEST_ENABLE_FAIL"] = "0"
        self.assertIn(ON, self.service_install())

    def test_source_channel_skips_quietly_even_with_opt_out(self):
        self.env["TEST_CHANNEL"] = "source"
        for opt_out in ("0", "1"):
            self.env["REMOTE_AGENT_AUTO_UPDATE"] = opt_out
            self.assertNotIn("Automatic", self.service_install())
            self.assert_no_enable()
        self.assertIn("update-build-info", self.recorded())
        self.assertFalse((self.state / "update-auto.json").exists())

    def test_default_state_directory(self):
        del self.env["REMOTE_AGENT_HOME"]
        self.assertIn(ON, self.service_install())
        self.assertTrue((self.home / ".remote-agent/update-auto.json").exists())
        self.assertFalse((self.state / "update-auto.json").exists())

    def test_service_manager_failure_never_attempts_update(self):
        self.env["TEST_SERVICE_FAIL"] = "1"
        self.service_install(success=False)
        self.assertNotIn("update-build-info", self.recorded())
        self.assert_no_enable()


if __name__ == "__main__":
    unittest.main()
