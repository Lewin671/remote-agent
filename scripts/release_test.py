import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('release', Path(__file__).with_name('release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.root_patch = patch.object(release, 'ROOT', self.root)
        self.file_patch = patch.object(release, 'VERSION', self.root / 'version.properties')
        self.root_patch.start()
        self.file_patch.start()
        self.git('init', '-q')
        self.git('config', 'user.name', 'Release test')
        self.git('config', 'user.email', 'test@example.invalid')
        self.write('version.properties', 'version=0.1.0\nandroidVersionCode=1\n')
        self.write('cmd/app/main.go', 'package main\n')
        self.commit()
        self.git('tag', 'v0.1.0')

    def tearDown(self):
        self.file_patch.stop()
        self.root_patch.stop()
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.root, text=True).strip()

    def write(self, path, text):
        p = self.root / path
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'Test commit')

    def test_docs_and_tests_do_not_release(self):
        for path in ('docs/plan.md', 'AGENTS.md', 'internal/a_test.go', 'android/core/src/test/kotlin/Test.kt', 'android/app/src/androidTest/Test.kt', 'scripts/release_test.py'):
            self.write(path, 'test')
        self.commit()
        self.assertFalse(release.prepare('patch')['eligible'])

    def test_web_tests_release_since_the_build_type_checks_them(self):
        self.assertTrue(release.product_change('web/src/a.test.ts'))

    def test_release_tooling_and_public_pages_do_not_release(self):
        for path in ('scripts/release.py', 'scripts/public_scan.py', 'scripts/publish-public.sh', 'scripts/dist-smoke.sh', '.github/workflows/release.yml',
                     'deploy/deploy.sh', 'install.sh', 'docs/assets/demo-light.svg', 'README.md'):
            self.assertFalse(release.product_change(path), path)

    def test_what_is_packed_or_decides_the_build_releases(self):
        for path in ('cmd/remote-agent/main.go', 'web/src/main.tsx', 'android/app/build.gradle.kts', 'go.mod', 'scripts/daemon.sh', 'scripts/build-go.sh',
                     'scripts/build-dist.sh', 'scripts/notices.py', 'scripts/android-ci.py', 'deploy/self-host/compose.yaml'):
            self.assertTrue(release.product_change(path), path)

    def test_product_change_bumps_shared_version_and_code(self):
        self.write('cmd/app/main.go', 'package main\n// fix\n')
        self.commit()
        self.assertEqual(release.prepare('patch')['tag'], 'v0.1.1')
        self.assertEqual(release.read_version(), ('0.1.1', 2))
        self.commit()
        self.git('tag', 'v0.1.1')
        self.assertEqual(release.validate('v0.1.1')['androidVersionCode'], 2)
        self.assertFalse(release.prepare('patch')['eligible'])

    def test_feature_bump_resets_patch(self):
        self.write('web/src/app.ts', '// feature')
        self.commit()
        self.assertEqual(release.prepare('minor')['version'], '0.2.0')

    def test_dirty_tree_and_tag_mismatch_rejected(self):
        self.write('cmd/app/main.go', '// uncommitted')
        with self.assertRaises(ValueError):
            release.prepare('patch')
        with self.assertRaises(ValueError):
            release.validate('v0.2.0')

    def test_manifest_is_one_line_per_field(self):
        self.write('internal/protocol/protocol.go', 'package protocol\n\nconst Version = 4\n')
        self.write('version.properties', 'version=0.1.1\nandroidVersionCode=2\n')
        self.commit()
        # A message of several lines, with what a properties file gives meaning to.
        self.git('tag', '-a', 'v0.1.1', '-m', '项目列表统一。\n路径 C:\\tmp = 不变\n协议版本不变 #1')
        apk = self.root / 'app.apk'
        apk.write_bytes(b'apk bytes')
        self.assertEqual(release.manifest('v0.1.1', apk), (
            'version=0.1.1\n'
            'versionCode=2\n'
            'protocol=4\n'
            'apk=remote-agent-v0.1.1.apk\n'
            'sha256=3b8c56b5c5f3b4a4f4c2a4a1a4b0d6d3e0a3d4a5c7b8e9f0a1b2c3d4e5f60718\n'
            'size=9\n'
            'notes=项目列表统一。\\n路径 C:\\\\tmp = 不变\\n协议版本不变 #1\n'
        ).replace('3b8c56b5c5f3b4a4f4c2a4a1a4b0d6d3e0a3d4a5c7b8e9f0a1b2c3d4e5f60718', __import__('hashlib').sha256(b'apk bytes').hexdigest()))

    def test_daemon_manifest_uses_checked_release_payloads(self):
        self.write('internal/protocol/protocol.go', 'package protocol\n\nconst Version = 6\n')
        self.write('internal/updater/policy.json', '{"state":1,"automatic":true}')
        self.write('version.properties', 'version=0.1.1\nandroidVersionCode=2\n')
        self.commit()
        self.git('tag', '-a', 'v0.1.1', '-m', 'Safe daemon updates')
        import hashlib
        dist = self.root / 'dist'
        dist.mkdir()
        apk = dist / 'remote-agent-v0.1.1.apk'
        apk.write_bytes(b'apk')
        payloads = [apk]
        for target in ('darwin-arm64', 'darwin-amd64', 'linux-arm64', 'linux-amd64'):
            archive = dist / f'remote-agent-v0.1.1-{target}.tar.gz'
            archive.write_bytes(target.encode())
            payloads.append(archive)
        sums = dist / 'SHA256SUMS'
        sums.write_text(''.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n' for p in payloads))
        manifest = dict(line.split('=', 1) for line in release.manifest('v0.1.1', apk).splitlines())
        self.assertEqual(manifest['daemonUpdate'], '1')
        self.assertEqual(manifest['daemonState'], '1')
        self.assertEqual(manifest['daemonAutomatic'], 'true')
        for p in payloads[1:]:
            target = p.name.removeprefix('remote-agent-v0.1.1-').removesuffix('.tar.gz')
            self.assertEqual(manifest[f'daemon.{target}.sha256'], hashlib.sha256(p.read_bytes()).hexdigest())
            self.assertEqual(manifest[f'daemon.{target}.size'], str(p.stat().st_size))
        payloads[-1].write_bytes(b'corrupt')
        with self.assertRaises(ValueError):
            release.manifest('v0.1.1', apk)

    def test_non_increasing_android_code_rejected(self):
        self.write('version.properties', 'version=0.1.1\nandroidVersionCode=1\n')
        self.commit()
        self.git('tag', 'v0.1.1')
        with self.assertRaises(ValueError):
            release.validate('v0.1.1')

    def test_existing_unmerged_tag_cannot_be_reused(self):
        self.git('tag', 'v0.1.1')
        self.write('cmd/app/main.go', '// fix')
        self.commit()
        with self.assertRaises(ValueError):
            release.prepare('patch')

    def test_invalid_and_overflow_versions_rejected(self):
        for text in ('version=01.2.3\nandroidVersionCode=1', 'version=0.2.0\nandroidVersionCode=2100000001'):
            with self.assertRaises(ValueError):
                release.read_version(text)
