#!/usr/bin/env python3
"""Exercise service readiness without touching the user's service manager."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('daemon.sh').resolve()


class DaemonReadinessTest(unittest.TestCase):
    def run_start(self, platform, ready=False, running=False, ready_after=0):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            commands = {
                'uname': f'echo {platform}\n',
                'sleep': 'exit 0\n',
                'launchctl': 'if [ "$1" = print ]; then echo "    pid = $TEST_DAEMON_PID"; fi\n',
                'systemctl': 'case " $* " in *" show "*) echo "$TEST_DAEMON_PID" ;; esac\n',
                'remote-agent': 'exit 0\n' if ready else 'exit 1\n',
            }
            if ready_after:
                commands['remote-agent'] = (
                    'if [ -f "$TEST_ATTEMPTS" ]; then read -r count < "$TEST_ATTEMPTS"; else count=0; fi\n'
                    'count=$((count + 1))\n'
                    'echo "$count" > "$TEST_ATTEMPTS"\n'
                    '[ "$count" -ge "$TEST_READY_AFTER" ]\n'
                )
            for name, body in commands.items():
                path = root / name
                path.write_text('#!/bin/sh\n' + body)
                path.chmod(0o700)
            unit = root / ('Library/LaunchAgents/com.github.lewin671.remote-agent.plist'
                           if platform == 'Darwin' else '.config/systemd/user/remote-agent.service')
            unit.parent.mkdir(parents=True)
            unit.touch()
            env = dict(os.environ, HOME=tmp, REMOTE_AGENT_HOME=str(root / 'state'),
                       BIN=str(root / 'remote-agent'), PATH=tmp + os.pathsep + os.environ['PATH'],
                       TEST_DAEMON_PID=str(os.getpid()) if running else '0',
                       TEST_ATTEMPTS=str(root / 'attempts'), TEST_READY_AFTER=str(ready_after))
            return subprocess.run(['sh', str(SCRIPT), 'start'], env=env,
                                  text=True, capture_output=True, timeout=60)

    def test_ready(self):
        for platform in ('Darwin', 'Linux'):
            with self.subTest(platform=platform):
                result = self.run_start(platform, ready=True)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_recovery_outlasts_old_wait(self):
        for platform in ('Darwin', 'Linux'):
            with self.subTest(platform=platform):
                result = self.run_start(platform, ready_after=36)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_still_starting(self):
        for platform in ('Darwin', 'Linux'):
            with self.subTest(platform=platform):
                result = self.run_start(platform, running=True)
                self.assertEqual(result.returncode, 1)
                self.assertIn('session recovery or CLI checks may still be in progress', result.stderr)
                self.assertIn('60s', result.stderr)

    def test_process_failed(self):
        for platform in ('Darwin', 'Linux'):
            with self.subTest(platform=platform):
                result = self.run_start(platform)
                self.assertEqual(result.returncode, 1)
                self.assertIn('process is not running', result.stderr)
                self.assertNotIn('may still be in progress', result.stderr)


if __name__ == '__main__':
    unittest.main()
