import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('web_deployment', Path(__file__).with_name('web-deployment.py'))
web = importlib.util.module_from_spec(spec)
spec.loader.exec_module(web)


class DeploymentMetadataTests(unittest.TestCase):
    def row(self, **values):
        return dict(Environment='Production', Branch='main', Source='abcdef0', Status='just now', **values)

    def test_only_successful_production_main_sources_count(self):
        rows = [dict(self.row(), Status='Failure', Source='1111111'),
                dict(self.row(), Environment='Preview', Source='2222222'),
                dict(self.row(), Branch='other', Source='3333333'), self.row()]
        self.assertEqual(web.source(rows), 'abcdef0')
        self.assertEqual(web.source([dict(self.row(), Status='1 day ago')]), 'abcdef0')
        for status in ('Active', 'Failure', 'Queued', 'Canceled', ''):
            with self.assertRaises(ValueError):
                web.source([dict(self.row(), Status=status)])
        with self.assertRaises(ValueError):
            web.source([dict(self.row(), Source='unknown')])


class WebOnlyShipTests(unittest.TestCase):
    def run_ship(self, protocol='13'):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'scripts').mkdir()
            (root / 'bin').mkdir()
            (root / 'temp').mkdir()
            for name in ('ship.sh', 'web-deployment.py'):
                shutil.copy(Path(__file__).with_name(name), root / 'scripts' / name)
            fake = root / 'bin' / 'fake'
            fake.write_text('''#!/usr/bin/env python3
import json,os,pathlib,shutil,sys
name=pathlib.Path(sys.argv[0]).name
args=sys.argv[1:]
root=pathlib.Path(os.environ['SHIP_TEST_ROOT'])
with (root/'calls').open('a') as f: f.write(name+' '+ ' '.join(args)+'\\n')
if name=='git':
    if args[:1]==['-C']: args=args[2:]
    if args[:1]==['rev-parse']: print('a'*40)
    elif args[:2]==['worktree','add']:
        work=pathlib.Path(args[-2]); (work/'web').mkdir(parents=True)
    elif args[:2]==['worktree','remove']: shutil.rmtree(args[-1])
    elif args[:1]==['show']:
        print('const Version = '+(os.environ['SHIP_TEST_PROTOCOL'] if args[1].startswith('bbbbbbb:') else '13'))
elif name=='gh':
    if args[:2]==['run','list']: print('123')
elif name=='pnpm':
    if args[:2]==['run','deploy']: (root/'deployed').touch()
    elif args[:3]==['exec','wrangler','pages']:
        source='aaaaaaa' if (root/'deployed').exists() else 'bbbbbbb'
        print(json.dumps([dict(Environment='Production',Branch='main',Source=source,Status='just now')]))
elif name in ('ssh','daemon','curl'): sys.exit('forbidden non-web operation')
''')
            fake.chmod(0o755)
            for command in ('git', 'gh', 'pnpm', 'ssh', 'daemon', 'curl'):
                (root / 'bin' / command).symlink_to(fake)
            env = dict(os.environ, PATH=str(root / 'bin') + ':' + os.environ['PATH'],
                       TMPDIR=str(root / 'temp'), SHIP_TEST_ROOT=str(root), SHIP_TEST_PROTOCOL=protocol,
                       BIN=str(root / 'bin' / 'daemon'))
            result = subprocess.run(['sh', str(root / 'scripts/ship.sh'), '--web-only', 'v0.56.3'],
                                    cwd=root, env=env, text=True, capture_output=True)
            return result, (root / 'calls').read_text()

    def test_deploys_and_verifies_web_without_server_daemon_or_public_http(self):
        result, calls = self.run_ship()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('pnpm run deploy', calls)
        self.assertIn('Cloudflare production points to v0.56.3', result.stdout)
        self.assertFalse(any(line.startswith(('ssh ', 'daemon ', 'curl ')) for line in calls.splitlines()), calls)

    def test_protocol_change_stops_before_deployment(self):
        result, calls = self.run_ship('12')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('cannot change protocol', result.stderr)
        self.assertNotIn('pnpm run deploy', calls)
        self.assertFalse(any(line.startswith(('ssh ', 'daemon ', 'curl ')) for line in calls.splitlines()), calls)
