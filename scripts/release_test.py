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
        for path in ('docs/plan.md', 'AGENTS.md', 'internal/a_test.go', 'web/src/a.test.ts', 'android/core/src/test/kotlin/Test.kt', 'android/app/src/androidTest/Test.kt', 'scripts/release_test.py'):
            self.write(path, 'test')
        self.commit()
        self.assertFalse(release.prepare('patch')['eligible'])

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
