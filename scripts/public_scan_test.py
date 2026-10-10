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

    def test_the_public_repository_pages_are_clean(self):
        pages = Path(__file__).resolve().parents[1] / 'deploy' / 'releases-repo'
        self.assertEqual([], public_scan.scan([pages]))


if __name__ == '__main__':
    unittest.main()
