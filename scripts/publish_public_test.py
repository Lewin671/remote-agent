import json
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch

import publish_public as publisher
import public_scan_test as fixtures


class PublishTests(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.PublicScanTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.tearDown)
        self.dist = self.fixture.release_dir()
        self.notes = self.fixture.root / 'notes.md'
        self.notes.write_text('A useful change. Upgrade all components.\n')
        self.repo = 'owner/releases'
        self.tag = 'v1.2.3'
        self.sums()
        self.states = []
        self.remote = {}
        self.calls = []
        self.create_requests = []
        self.fail_upload = False
        self.corrupt_download = False
        self.hide_created_release_from_list = False
        self.gh_patch = patch.object(publisher, 'gh', side_effect=self.gh)
        self.gh_patch.start()
        self.addCleanup(self.gh_patch.stop)

    def sums(self):
        payloads = sorted(p for p in self.dist.iterdir() if p.name not in ('SHA256SUMS', 'latest.properties'))
        (self.dist / 'SHA256SUMS').write_text(''.join(f'{publisher.sha256(p)}  {p.name}\n' for p in payloads))
        apk = self.dist / f'remote-agent-{self.tag}.apk'
        (self.dist / 'latest.properties').write_text(f'version={self.tag[1:]}\napk={apk.name}\nsha256={publisher.sha256(apk)}\nsize={apk.stat().st_size}\n')

    def state(self, tag=None, draft=True):
        state = {'id': len(self.states) + 1, 'tag_name': tag or self.tag, 'draft': draft, 'prerelease': False, 'assets': []}
        self.states.append(state)
        return state

    def populate(self, state):
        self.remote = {p.name: p.read_bytes() for p in self.dist.iterdir()}
        state['assets'] = [{'name': name} for name in self.remote]

    def gh(self, *args):
        args = tuple(str(a) for a in args)
        self.calls.append(args)
        state = next((s for s in self.states if s['tag_name'] == self.tag), None)
        if args[0] == 'api':
            if '--method' in args and args[args.index('--method') + 1] == 'POST':
                self.assertEqual(f'repos/{self.repo}/releases', args[1])
                self.create_requests.append(json.loads(Path(args[args.index('--input') + 1]).read_text()))
                return json.dumps(self.state())
            if '/tags/' in args[1]:
                if state is None or state['draft']:
                    raise subprocess.CalledProcessError(1, ['gh', 'api', args[1]])
                return json.dumps(state)
            if '?' not in args[1]:
                release_id = int(args[1].rsplit('/', 1)[1])
                return json.dumps(next(s for s in self.states if s['id'] == release_id))
            if self.hide_created_release_from_list:
                return json.dumps([[]])
            return json.dumps([self.states[:1], self.states[1:]])
        command = args[1]
        if command == 'create':
            self.state()
        elif command == 'upload':
            self.assertTrue(state['draft'])
            # A failed upload may have stored some files; it must stay draft.
            paths = args[3:args.index('--repo')]
            selected = paths[:2] if self.fail_upload else paths
            for path in map(Path, selected):
                self.remote[path.name] = path.read_bytes()
            state['assets'] = [{'name': name} for name in self.remote]
            if self.fail_upload:
                raise subprocess.CalledProcessError(1, ['gh', 'release', 'upload'])
        elif command == 'download':
            target = Path(args[args.index('--dir') + 1])
            for name, data in self.remote.items():
                (target / name).write_bytes(data)
            if self.corrupt_download:
                (target / 'SHA256SUMS').write_text('corrupt')
        elif command == 'edit':
            self.assertTrue(state['draft'])
            state['draft'] = False
        else:
            self.fail(f'Unexpected gh mutation: {args}')
        return ''

    def publish(self, dry_run=False):
        publisher.publish(self.tag, self.dist, self.notes, self.repo, dry_run)

    def writes(self):
        return [c for c in self.calls if c[:2] in (('release', 'create'), ('release', 'upload'), ('release', 'edit'))
                or (c[0] == 'api' and '--method' in c and c[c.index('--method') + 1] == 'POST')]

    def test_upload_failure_stays_draft_and_retry_reuses_it(self):
        self.fail_upload = True
        with self.assertRaises(subprocess.CalledProcessError):
            self.publish()
        self.assertTrue(self.states[0]['draft'])
        self.fail_upload = False
        self.publish()
        self.assertFalse(self.states[0]['draft'])
        self.assertEqual(1, len(self.create_requests))
        self.assertIn('--latest=true', self.writes()[-1])

    def test_new_draft_can_be_verified_when_the_tag_endpoint_returns_404(self):
        self.publish()
        self.assertFalse(self.states[0]['draft'])
        self.assertFalse(any('/tags/' in c[1] for c in self.calls if c[0] == 'api'))
        self.assertTrue(any(c[:2] == ('api', f'repos/{self.repo}/releases/1') for c in self.calls))

    def test_created_draft_is_used_even_when_the_release_list_never_shows_it(self):
        self.hide_created_release_from_list = True
        self.publish()
        self.assertFalse(self.states[0]['draft'])
        self.assertEqual(1, sum(c[:2] == ('api', f'repos/{self.repo}/releases?per_page=100') for c in self.calls))

    def test_create_request_keeps_multiline_notes_as_json_and_stays_draft(self):
        self.notes.write_text('Fix "quoted" text.\nKeep `$literal` and Unicode: 修复.\n')
        self.publish()
        self.assertEqual([{
            'tag_name': self.tag, 'target_commitish': 'main', 'draft': True,
            'name': self.tag,
            'body': self.notes.read_text() + f'\n---\n\nInstall or upgrade: see the [README](https://github.com/{self.repo}#readme). Every payload is listed in `SHA256SUMS`.\n',
        }], self.create_requests)

    def test_invalid_create_response_stops_before_uploading(self):
        for response in (None, {}, {'id': True, 'tag_name': self.tag, 'draft': True},
                         {'id': 0, 'tag_name': self.tag, 'draft': True},
                         {'id': 1, 'tag_name': 'v9.9.9', 'draft': True},
                         {'id': 1, 'tag_name': self.tag, 'draft': False}):
            with self.subTest(response=response):
                def invalid_response(*args):
                    if args[0] == 'api' and '--method' in args:
                        return json.dumps(response)
                    return self.gh(*args)
                with patch.object(publisher, 'gh', side_effect=invalid_response):
                    with self.assertRaisesRegex(ValueError, 'expected draft'):
                        self.publish()
                self.assertFalse(any(c[:2] == ('release', 'upload') for c in self.calls))

    def test_same_published_release_is_read_only_but_different_files_fail(self):
        self.populate(self.state(draft=False))
        self.publish()
        self.assertEqual([], self.writes())
        self.remote['SHA256SUMS'] = b'changed'
        with self.assertRaisesRegex(ValueError, 'differ'):
            self.publish()
        self.assertEqual([], self.writes())

    def test_incomplete_or_extra_remote_assets_fail(self):
        state = self.state(draft=False)
        self.populate(state)
        for assets in (state['assets'][:-1], state['assets'] + [{'name': 'extra.txt'}]):
            state['assets'] = assets
            with self.assertRaisesRegex(ValueError, 'exactly'):
                self.publish()
        self.assertEqual([], self.writes())

    def test_remote_corruption_does_not_publish_a_draft(self):
        self.corrupt_download = True
        with self.assertRaisesRegex(ValueError, 'differ'):
            self.publish()
        self.assertTrue(self.states[0]['draft'])
        self.assertFalse(any(c[:2] == ('release', 'edit') for c in self.calls))

    def test_newer_version_on_another_page_keeps_latest(self):
        self.state('v1.2.2', draft=False)
        self.state('v2.0.0', draft=False)
        self.publish()
        self.assertIn('--latest=false', self.writes()[-1])

    def test_lookup_failure_does_not_create_a_release(self):
        with patch.object(publisher, 'gh', side_effect=subprocess.CalledProcessError(1, ['gh', 'api'])):
            with self.assertRaises(subprocess.CalledProcessError):
                self.publish()
        self.assertEqual([], self.calls)

    def test_local_corruption_or_private_notes_fail_before_network(self):
        apk = self.dist / f'remote-agent-{self.tag}.apk'
        apk.write_bytes(b'corrupt')
        with self.assertRaisesRegex(ValueError, 'SHA256SUMS'):
            self.publish()
        self.sums()
        self.notes.write_text('Deploy at ra.' + 'work' + 'station.top')
        with self.assertRaisesRegex(ValueError, 'Not publishable'):
            self.publish()
        self.assertEqual([], self.calls)

    def test_daemon_metadata_must_match_checked_payloads(self):
        manifest = self.dist / 'latest.properties'
        base = manifest.read_text() + 'protocol=6\ndaemonUpdate=1\ndaemonState=1\ndaemonAutomatic=true\n'
        for target in ('darwin-arm64', 'darwin-amd64', 'linux-arm64', 'linux-amd64'):
            archive = self.dist / f'remote-agent-{self.tag}-{target}.tar.gz'
            base += f'daemon.{target}.sha256={publisher.sha256(archive)}\ndaemon.{target}.size={archive.stat().st_size}\n'
        manifest.write_text(base)
        publisher.validate(self.tag, self.dist, self.notes)
        manifest.write_text(base.replace('daemonState=1', 'daemonState=bad'))
        with self.assertRaises(ValueError):
            publisher.validate(self.tag, self.dist, self.notes)
        manifest.write_text(base + 'daemon.linux-amd64.size=1\n')
        with self.assertRaises(ValueError):
            publisher.validate(self.tag, self.dist, self.notes)

    def test_manifest_or_checksum_file_cannot_select_other_payloads(self):
        for change in ('apk=other.apk\n', 'sha256=wrong\n', 'version=1.2.4\n'):
            self.sums()
            with (self.dist / 'latest.properties').open('a') as f:
                f.write(change)
            with self.assertRaisesRegex(ValueError, 'Manifest'):
                self.publish()
        self.sums()
        with (self.dist / 'SHA256SUMS').open('a') as f:
            f.write('0' * 64 + '  ../other.apk\n')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            self.publish()
        self.assertEqual([], self.calls)

    def test_rehearsal_never_writes_even_if_release_is_missing(self):
        self.publish(dry_run=True)
        self.assertEqual([], self.writes())

    def test_unexpected_draft_asset_is_refused_without_overwriting_it(self):
        self.state()['assets'] = [{'name': 'private.txt'}]
        with self.assertRaisesRegex(ValueError, 'unexpected'):
            self.publish()
        self.assertEqual([], self.writes())


if __name__ == '__main__':
    unittest.main()
