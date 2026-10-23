import os
import re
import shlex
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = (ROOT / 'scripts/acceptance-claude.sh').read_text()
FIXTURES = ROOT / 'internal/claude/testdata/mode'


def function(name):
    return re.search(r'^' + name + r'\(\) \{\n.*?^\}', SCRIPT, re.M | re.S).group() + '\n'


class ClaudeAcceptanceTests(unittest.TestCase):
    def shell(self, body, **env):
        helpers = ''.join(function(name) for name in ('no_background_shells', 'finish_interrupt'))
        return subprocess.run(['sh'], input=helpers + body, text=True, capture_output=True,
                              env={**os.environ, **env}, timeout=5)

    def test_shell_count_uses_live_footer_and_capture_must_succeed(self):
        with tempfile.TemporaryDirectory() as directory:
            stale = Path(directory) / 'stale.txt'
            stale.write_text('✻ Worked for 5s · 1 shell still running\n' +
                             (FIXTURES / 'finished-shell-2.1.300.txt').read_text())
            for capture, status in ((FIXTURES / 'background-shell-2.1.300.txt', 1),
                                    (FIXTURES / 'finished-shell-2.1.300.txt', 0),
                                    (stale, 0)):
                result = self.shell('''
TMUX_SOCK=test
pane() { echo test; }
tmux() { cat "$CAPTURE"; }
no_background_shells test
''', CAPTURE=str(capture))
                self.assertEqual(result.returncode, status, result.stderr)
            result = self.shell('''
TMUX_SOCK=test
pane() { echo test; }
tmux() { return 1; }
no_background_shells test
''')
            self.assertEqual(result.returncode, 1)

    def test_interrupt_releases_shell_before_later_mode_switches(self):
        # Execute the actual interrupt step with a CLI that leaves its shell
        # behind when the turn is aborted. Captures clear only after the
        # command's stop file exists; no model, tmux, or timing is involved.
        step = SCRIPT.split('# 6. Interrupt a running turn.', 1)[1].split('# 7.', 1)[0]
        step = '# 6. Interrupt a running turn.' + step
        for started in ('true', 'false'):
            with self.subTest(started=started), tempfile.TemporaryDirectory(prefix='claude flow ') as directory:
                result = self.shell('''
ID=test
TMUX_SOCK=test
target="$TEST_DIR/command"
BIN=fake_cli
fake_cli() {
    if [ "$1" = send ]; then printf '%s' "$3" > "$TEST_DIR/prompt"; fi
}
last_seq() { echo 1; }
pane() { echo test; }
wait_new() { "$STARTED"; }
pass() { echo "PASS $1"; }
fail() { echo "FAIL $1"; }
idle() { return 0; }
wait_for() { shift; "$@"; }
tmux() {
    if [ -f "$TEST_DIR/command-interrupt-stop" ]; then
        cat "$FINISHED"
    else
        cat "$LIVE"
    fi
}
''' + step + '\nno_background_shells test\n', TEST_DIR=directory, STARTED=started,
                    LIVE=str(FIXTURES / 'background-shell-2.1.300.txt'),
                    FINISHED=str(FIXTURES / 'finished-shell-2.1.300.txt'))
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                prompt = (Path(directory) / 'prompt').read_text()
                command = shlex.split(prompt.split(': ', 1)[1])
                self.assertEqual(command[:3], ['until', '[', '-f'])
                self.assertTrue(Path(command[3]).is_file(), 'Bash stop condition was not released')
                self.assertIn('PASS interrupt shell has finished', result.stdout)


if __name__ == '__main__':
    unittest.main()
