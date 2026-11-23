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
    def run_ship(self, peer='17', relay='17', prebuilt_core=False, presplit=False, missing_commit=False):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'scripts').mkdir()
            (root / 'bin').mkdir()
            (root / 'temp').mkdir()
            for name in ('ship.sh', 'web-deployment.py'):
                shutil.copy(Path(__file__).with_name(name), root / 'scripts' / name)
            if prebuilt_core:
                (root / 'scripts/core').mkdir()
                (root / 'scripts/core/build.sh').write_text('#!/bin/sh\n')
                (root / 'go.mod').write_text('module test\n')
                (root / 'go.sum').write_text('')
                (root / 'clientcore').mkdir()
                (root / 'proto').mkdir()
                (root / 'build/core/web').mkdir(parents=True)
                (root / 'build/core/web/wasm_exec.js').write_text('// wasm exec\n')
                (root / 'build/core/web/core.wasm').write_bytes(b'wasm-bytes')
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
    elif args[:2]==['cat-file','-e']:
        spec = args[2]
        if os.environ.get('SHIP_TEST_MISSING_COMMIT') == '1' and spec.startswith('bbbbbbb'):
            sys.exit(1)
        if os.environ.get('SHIP_TEST_PRESPLIT') == '1' and spec == 'bbbbbbb:internal/relayproto/relayproto.go':
            sys.exit(1)
    elif args[:1]==['show']:
        spec = args[1]
        is_prev = spec.startswith('bbbbbbb:')
        if spec.endswith('internal/relayproto/relayproto.go'):
            if is_prev and os.environ.get('SHIP_TEST_PRESPLIT') == '1':
                sys.exit('fatal: path does not exist')
            print('const Version = '+(os.environ['SHIP_TEST_RELAY'] if is_prev else '17'))
        else:
            if is_prev and os.environ.get('SHIP_TEST_PRESPLIT') == '1':
                print('const Version = '+os.environ['SHIP_TEST_PEER'])
            else:
                print('const PeerVersion = '+(os.environ['SHIP_TEST_PEER'] if is_prev else '17'))
elif name=='gh':
    if args[:2]==['run','list']: print('123')
    elif args[:2]==['run','view']: print('completed success' if 'jobs' in args else 'completed')
elif name=='pnpm':
    if args[:2]==['run','deploy']:
        (root/'deployed').touch()
        if pathlib.Path('..', 'build', 'core', 'web', 'core.wasm').is_file():
            (root/'reused_core').touch()
    elif args[:3]==['exec','wrangler','pages']:
        source='aaaaaaa' if (root/'deployed').exists() else 'bbbbbbb'
        print(json.dumps([dict(Environment='Production',Branch='main',Source=source,Status='just now')]))
elif name in ('ssh','daemon','curl'): sys.exit('forbidden non-web operation')
''')
            fake.chmod(0o755)
            for command in ('git', 'gh', 'pnpm', 'ssh', 'daemon', 'curl'):
                (root / 'bin' / command).symlink_to(fake)
            env = dict(os.environ, PATH=str(root / 'bin') + ':' + os.environ['PATH'],
                       TMPDIR=str(root / 'temp'), SHIP_TEST_ROOT=str(root),
                       SHIP_TEST_PEER=peer, SHIP_TEST_RELAY=relay,
                       SHIP_TEST_PRESPLIT='1' if presplit else '0',
                       SHIP_TEST_MISSING_COMMIT='1' if missing_commit else '0',
                       BIN=str(root / 'bin' / 'daemon'))
            result = subprocess.run(['sh', str(root / 'scripts/ship.sh'), '--web-only', 'v0.56.3'],
                                    cwd=root, env=env, text=True, capture_output=True, timeout=120)
            return result, (root / 'calls').read_text(), (root / 'reused_core').exists()

    def test_deploys_and_verifies_web_without_server_daemon_or_public_http(self):
        result, calls, reused = self.run_ship()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('pnpm install --frozen-lockfile --prefer-offline --silent', calls)
        self.assertIn('pnpm run deploy', calls)
        self.assertFalse(reused)
        self.assertIn('Cloudflare production points to v0.56.3', result.stdout)
        self.assertFalse(any(line.startswith(('ssh ', 'daemon ', 'curl ')) for line in calls.splitlines()), calls)

    def test_reuses_current_local_web_core_when_inputs_match(self):
        result, calls, reused = self.run_ship(prebuilt_core=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(reused)
        self.assertIn('pnpm run deploy', calls)

    def test_peer_protocol_change_stops_before_deployment(self):
        result, calls, _ = self.run_ship(peer='16', relay='17')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('cannot change protocol', result.stderr)
        self.assertNotIn('pnpm run deploy', calls)
        self.assertFalse(any(line.startswith(('ssh ', 'daemon ', 'curl ')) for line in calls.splitlines()), calls)

    def test_relay_protocol_change_stops_before_deployment(self):
        result, calls, _ = self.run_ship(peer='17', relay='16')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('cannot change protocol', result.stderr)
        self.assertNotIn('pnpm run deploy', calls)
        self.assertFalse(any(line.startswith(('ssh ', 'daemon ', 'curl ')) for line in calls.splitlines()), calls)

    def test_presplit_deployed_commit_uses_legacy_protocol_fallback(self):
        result, calls, _ = self.run_ship(peer='17', presplit=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('pnpm run deploy', calls)
        result_mismatch, calls_mismatch, _ = self.run_ship(peer='16', presplit=True)
        self.assertNotEqual(result_mismatch.returncode, 0)
        self.assertIn('cannot change protocol', result_mismatch.stderr)
        self.assertNotIn('pnpm run deploy', calls_mismatch)

    def test_missing_local_commit_reports_missing_commit(self):
        result, calls, _ = self.run_ship(missing_commit=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('not found locally', result.stderr)
        self.assertNotIn('cannot change protocol', result.stderr)
        self.assertNotIn('pnpm run deploy', calls)
