import subprocess
import unittest
from pathlib import Path


HELPER = Path(__file__).with_name('acceptance-common.sh')


class AcceptanceWaitTests(unittest.TestCase):
    def shell(self, body):
        return subprocess.run(['sh', '-c', '. "$1"\n' + body, 'test', str(HELPER)],
                              text=True, capture_output=True, timeout=5)

    def test_success_does_not_sleep(self):
        result = self.shell('sleep() { echo unexpected; }; wait_for 3 true')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, '')

    def test_retries_until_success_with_arguments(self):
        result = self.shell('''
calls=0
sleep() { printf 'sleep %s\\n' "$1"; }
ready() { calls=$((calls + 1)); [ "$1" = 'two words' ] && [ "$calls" -eq 2 ]; }
wait_for 3 ready 'two words'
status=$?
printf 'calls %s\\n' "$calls"
exit "$status"
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'sleep 1\ncalls 2\n')

    def test_timeout_keeps_the_attempt_budget(self):
        result = self.shell('''
sleep() { printf 'sleep %s\\n' "$1"; }
wait_for 3 false
''')
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(result.stdout, 'sleep 1\n' * 3)

    def test_zero_budget_does_not_call_the_command(self):
        result = self.shell('unexpected() { echo unexpected; }; wait_for 0 unexpected')
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(result.stdout, '')


if __name__ == '__main__':
    unittest.main()
