import importlib.util
import io
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path

spec = importlib.util.spec_from_file_location('public_scan', Path(__file__).with_name('public_scan.py'))
public_scan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(public_scan)

# Assembled so that this file does not itself hold what the scan refuses.
HOST = 'ra.' + 'work' + 'station.top'


class PublicScanTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def tar(self, name, member, data, owner='root'):
        with tarfile.open(self.root / name, 'w:gz') as t:
            info = tarfile.TarInfo(member)
            info.size = len(data)
            info.uname = owner
            t.addfile(info, io.BytesIO(data))

    def terms(self):
        return [(Path(f).name, member, term) for f, member, term in public_scan.scan([self.root])]

    def test_clean_files_pass(self):
        (self.root / 'README.md').write_text('Deploy the relay at https://ra.example.com\nRA_SETUP_TOKEN=\n')
        self.tar('a.tar.gz', 'a/bin', b'\x00binary\x00github.com/Lewin671/remote-agent\x00')
        self.assertEqual([], self.terms())

    def test_finds_our_host_in_text_and_inside_archives(self):
        (self.root / 'guide.md').write_text(f'see https://{HOST}\n')
        self.tar('web.tar.gz', 'web/assets/index.js', f'const d="https://{HOST}"'.encode())
        with zipfile.ZipFile(self.root / 'app.apk', 'w', zipfile.ZIP_DEFLATED) as z:
            z.writestr('resources.arsc', HOST.encode('utf-16-le'))
        self.assertEqual({'guide.md', 'web.tar.gz', 'app.apk'}, {f for f, _, _ in self.terms()})
        self.assertIn(('app.apk', 'resources.arsc', 'work' + 'station'), self.terms())

    def test_finds_the_packing_user_in_archive_headers(self):
        self.tar('a.tar.gz', 'a/bin', b'clean', owner='work' + 'station')
        self.assertEqual([('a.tar.gz', '(unpacked)', 'work' + 'station')], self.terms())

    def test_finds_keys_and_filled_in_tokens(self):
        (self.root / 'key.pem').write_text('-----BEGIN ' + 'PRIVATE KEY-----\n')
        (self.root / '.env').write_text('RA_SETUP_TOKEN=' + 'abcdefgh12345678\n')
        self.assertEqual({'key.pem', '.env'}, {f for f, _, _ in self.terms()})

    def test_refuses_deployment_state_and_unsafe_paths_by_name(self):
        self.tar('a.tar.gz', 'server/.env', b'RELAY_DOMAIN=ra.example.com\n')
        self.tar('b.tar.gz', 'server/data/remote-agent.db', b'x')
        self.tar('c.tar.gz', '../outside', b'x')
        with zipfile.ZipFile(self.root / 'web.zip', 'w') as z:
            z.writestr('assets/index.js.map', '{}')
        with tarfile.open(self.root / 'd.tar.gz', 'w:gz') as t:
            link = tarfile.TarInfo('server/link')
            link.type = tarfile.SYMTYPE
            link.linkname = '/etc/passwd'
            t.addfile(link)
        self.assertEqual({
            ('a.tar.gz', 'server/.env', 'a file that is never published'),
            ('b.tar.gz', 'server/data/remote-agent.db', 'a file that is never published'),
            ('c.tar.gz', '../outside', 'a path outside the archive'),
            ('web.zip', 'assets/index.js.map', 'a file that is never published'),
            ('d.tar.gz', 'server/link', 'a link or special file'),
        }, set(self.terms()))

    def test_refuses_plain_files_by_name_and_links(self):
        (self.root / 'pages').mkdir()
        (self.root / 'pages' / '.env').write_text('RELAY_DOMAIN=ra.example.com\n')
        (self.root / 'pages' / 'account.json').write_text('{}')
        (self.root / 'pages' / 'env.example').write_text('RA_SETUP_TOKEN=\n')
        (self.root / 'pages' / '.dockerignore').write_text('*\n')
        (self.root / 'pages' / 'link').symlink_to('/etc/hosts')
        self.assertEqual({('.env', '', 'a file that is never published'), ('account.json', '', 'a file that is never published'), ('link', '', 'a link')}, set(self.terms()))
        self.assertEqual([(str(self.root / 'pages' / '.env'), '', 'a file that is never published')], public_scan.scan([self.root / 'pages' / '.env']))

    def release_dir(self, tag='v1.2.3'):
        dist = self.root / 'dist'
        dist.mkdir()
        def tar(name, files):
            with tarfile.open(dist / (name + '.tar.gz'), 'w:gz') as t:
                for f in files:
                    info = tarfile.TarInfo(f'{name}/{f}')
                    t.addfile(info, io.BytesIO(b''))
        for target in public_scan.TARGETS:
            tar(f'remote-agent-{tag}-{target}', ['remote-agent', 'remote-agent-service', 'THIRD_PARTY_NOTICES.txt'])
            if target.startswith('linux'):
                tar(f'remote-agent-server-{tag}-{target}', ['remote-agent-server', 'Dockerfile', '.dockerignore', 'compose.yaml', 'Caddyfile', 'env.example', 'THIRD_PARTY_NOTICES.txt'])
        tar(f'remote-agent-web-{tag}', sorted(public_scan.web_files()) + ['assets/index-Ab_1cD-9.js', 'assets/index-S8FXQorW.css'])
        for name in (f'remote-agent-{tag}.apk', 'SHA256SUMS', 'latest.properties'):
            (dist / name).write_text('x')
        return dist

    def test_a_complete_release_passes(self):
        self.assertEqual([], public_scan.release('v1.2.3', self.release_dir()))

    def test_retry_uses_tagged_web_inventory_with_current_privacy_scan(self):
        dist = self.release_dir()
        tagged_public = self.root / 'tagged-public'
        tagged_public.mkdir()
        (tagged_public / 'older.txt').write_text('public asset')
        name = 'remote-agent-web-v1.2.3'
        with tarfile.open(dist / (name + '.tar.gz'), 'w:gz') as t:
            for f in public_scan.WEB_BUILT | {'older.txt', 'assets/index-Ab_1cD-9.js'}:
                t.addfile(tarfile.TarInfo(f'{name}/{f}'), io.BytesIO(b''))
        self.assertNotEqual([], public_scan.release('v1.2.3', dist))
        self.assertEqual([], public_scan.release('v1.2.3', dist, tagged_public))
        (dist / 'latest.properties').write_text(HOST)
        self.assertTrue(public_scan.scan([dist]))

    def test_a_release_with_other_or_missing_files_is_refused(self):
        dist = self.release_dir()
        (dist / 'notes.txt').write_text('x')
        (dist / 'remote-agent-v1.2.3-linux-arm64.tar.gz').unlink()
        with tarfile.open(dist / 'remote-agent-server-v1.2.3-linux-amd64.tar.gz', 'w:gz') as t:
            for f in ('remote-agent-server', 'Dockerfile', '.dockerignore', 'compose.yaml', 'Caddyfile', 'env.example', 'THIRD_PARTY_NOTICES.txt', 'data/x'):
                t.addfile(tarfile.TarInfo(f'remote-agent-server-v1.2.3-linux-amd64/{f}'), io.BytesIO(b''))
        with tarfile.open(dist / 'remote-agent-web-v1.2.3.tar.gz', 'w:gz') as t:
            for f in sorted(public_scan.web_files() - {'sw.js'}) + ['report.html', 'assets/index-Ab_1cD-9.js', 'assets/deployment-backup.txt']:
                t.addfile(tarfile.TarInfo(f'remote-agent-web-v1.2.3/{f}'), io.BytesIO(b''))
        self.assertEqual({
            ('notes.txt', '', 'not a file of a release'),
            ('remote-agent-v1.2.3-linux-arm64.tar.gz', '', 'missing from the release'),
            ('remote-agent-server-v1.2.3-linux-amd64.tar.gz', 'data/x', 'not what this archive holds'),
            ('remote-agent-web-v1.2.3.tar.gz', 'report.html', 'not what this archive holds'),
            ('remote-agent-web-v1.2.3.tar.gz', 'sw.js', 'not what this archive holds'),
            ('remote-agent-web-v1.2.3.tar.gz', 'assets/deployment-backup.txt', 'not what this archive holds'),
        }, {(Path(f).name, m, why) for f, m, why in public_scan.release('v1.2.3', dist)})

    def test_the_public_repository_pages_are_clean(self):
        pages = Path(__file__).resolve().parents[1] / 'deploy' / 'releases-repo'
        self.assertEqual([], public_scan.scan([pages]))


if __name__ == '__main__':
    unittest.main()
