import copy
import json
import os
import plistlib
import subprocess
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch
import i18n
import i18n_lint


class CompilerTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        shutil.copytree(i18n.ROOT / 'i18n', self.root / 'i18n')

    def mutate(self, change):
        path = self.root / 'i18n/messages/image.json'
        rows = i18n.read(path)
        # The checks below mutate a plural row; keep it first whatever order the source file has.
        rows.sort(key=lambda r: r['key'] != 'image.attach_failed')
        change(rows)
        path.write_text(i18n.dump(rows))

    def rejected(self, change, pattern):
        self.mutate(change)
        with self.assertRaisesRegex(ValueError, pattern): i18n.build(self.root)

    def test_missing_empty_duplicate_undefined_parameters(self):
        for change, pattern in [
            (lambda r: r[0].pop('en'), 'fields/translations'),
            (lambda r: r[0].update(en=''), 'selector differs'),
            (lambda r: r.append(copy.deepcopy(r[0])), 'duplicate key'),
            (lambda r: (r[0]['params'].update(detail={'type':'string','example':'Example'}), r[0]['en']['forms'].update(other='missing')), 'parameters/format'),
            (lambda r: r[0]['en']['forms'].update(other='{unknown}'), 'parameters/format'),
            (lambda r: r[0]['en']['forms'].update(other=' '), 'empty translation'),
            (lambda r: r[0]['en']['forms'].pop('one'), 'categories'),
            (lambda r: r[0]['params']['count'].update(type='string',example='x'), 'plural parameter'),
            (lambda r: r[0]['params']['count'].update(example='x'), 'bad example'),
            (lambda r: r[0].update(platforms=['watch']), 'platforms'),
        ]:
            with self.subTest(pattern=pattern):
                original=(self.root/'i18n/messages/image.json').read_text()
                self.rejected(change,pattern)
                (self.root/'i18n/messages/image.json').write_text(original)

    def test_plural_rich_slot_can_carry_the_visible_count(self):
        def change(rows):
            row = rows[0]
            row['params']['total'] = {'type': 'string', 'example': '2'}
            for lang in ('zh-CN', 'en'):
                row[lang]['forms'] = {form: text.replace('{count}', '{total}') for form, text in row[lang]['forms'].items()}
        self.mutate(change)
        output = i18n.build(self.root)
        self.assertIn('count: number; total: string', output['web/src/i18n/generated.ts'])
        self.assertIn('getQuantityString(R.plurals.image_attach_failed, count, count, total)', output['android/app/src/main/java/io/github/lewin671/remoteagent/ui/UiMessages.generated.kt'])
        self.rejected(lambda rows: rows[0]['en']['forms'].update(other='missing visible slot'), 'parameters/format')

    def test_launch_surfaces_are_generated_from_the_same_source(self):
        folder = self.root / 'scripts/i18n_templates'
        folder.mkdir(parents=True)
        for name in ('web-shell.html', 'web-manifest.json'):
            shutil.copy(i18n.ROOT / 'scripts/i18n_templates' / name, folder / name)
        output = i18n.build(self.root)
        self.assertIn('正在打开…', output['web/index.html'])
        self.assertIn('请启用 JavaScript 后重新加载。', output['web/index.html'])
        self.assertNotIn('{{', output['web/index.html'])
        self.assertIn('在任何设备上操作自己电脑上的 Codex', output['web/public/manifest.webmanifest'])
        self.assertIn('Opening…', output['web/src/i18n/startup.generated.ts'])
        (folder / 'web-shell.html').write_text('{{startup.unknown}}')
        with self.assertRaises(KeyError): i18n.build(self.root)

    def test_duplicate_json_fields(self):
        with self.assertRaisesRegex(ValueError,'duplicate JSON field'):
            json.loads('{"key":1,"key":2}',object_pairs_hook=i18n.pairs)

    def test_collision_and_select_branch_collisions(self):
        rows=i18n.read(self.root/'i18n/messages/common.json')
        row=copy.deepcopy(rows[0]); row['key']='common.back_thing'; rows.append(row)
        row=copy.deepcopy(row); row['key']='common.back.thing'; rows.append(row)
        (self.root/'i18n/messages/common.json').write_text(i18n.dump(rows))
        with self.assertRaisesRegex(ValueError,'collision'):i18n.build(self.root)

    def test_select_reordering_and_native_escaping(self):
        row=i18n.read(self.root/'i18n/messages/common.json')[0]
        row.update(key='common.fixture', params={'name':{'type':'string','example':'<a>'},'kind':{'type':'string','example':'photo'}})
        row['zh-CN']={'select':'kind','forms':{'photo':'{name} 的 "图" & 100%','other':'{name} 的文件'}}
        row['en']={'select':'kind','forms':{'photo':"100% & {name}'s photo",'other':'File: {name}'}}
        path=self.root/'i18n/messages/common.json'; path.write_text(i18n.dump([row]))
        out=i18n.build(self.root)
        android=out['android/app/src/main/res/values-en/i18n.xml']
        self.assertIn("100%% &amp; %2$s\\'s photo",android)
        self.assertIn('common_fixture_photo',android)
        swift=out['ios/RemoteAgentCore/Localization/Localizable.xcstrings']
        self.assertIn("100%% & %2$@'s photo",swift)
        row['params']['other']={'type':'integer','example':4}
        for lang in ('zh-CN','en'):
            for f in row[lang]['forms']: row[lang]['forms'][f]+=' {other}'
        self.assertIn('%3$lld',i18n.native(row['en']['forms']['photo'],row,True))

    def test_native_compilers_accept_escaping_and_reordered_plural_arguments(self):
        row=next(r for r in i18n.read(self.root/'i18n/messages/image.json') if r['key']=='image.attach_failed')
        row['params']['aName']={'type':'string','example':'A & B'}
        for lang in ('zh-CN','en'):
            for f in row[lang]['forms']:
                row[lang]['forms'][f]='{aName}: "100%" & '+row[lang]['forms'][f]+"'"
        (self.root/'i18n/messages/image.json').write_text(i18n.dump([row]))
        i18n.generate(self.root)
        if shutil.which('xcrun'):
            destination=self.root/'apple-compiled';destination.mkdir()
            subprocess.run(['xcrun','xcstringstool','compile',str(self.root/'ios/RemoteAgentCore/Localization/Localizable.xcstrings'),'--output-directory',str(destination)],check=True,capture_output=True)
            for tag in ('en','zh-Hans'):
                generated=self.root/f'ios/RemoteAgentCore/Sources/RACore/Resources/{tag}.lproj/Localizable.stringsdict'
                compiled=destination/f'{tag}.lproj/Localizable.stringsdict'
                self.assertEqual(plistlib.loads(generated.read_bytes()),plistlib.loads(compiled.read_bytes()))
        sdk=Path(os.environ.get('ANDROID_HOME',Path.home()/'Library/Android/sdk'))
        aapt=sorted((sdk/'build-tools').glob('*/aapt2'))
        if aapt:
            subprocess.run([str(aapt[-1]),'compile','--dir',str(self.root/'android/app/src/main/res'),'-o',str(self.root/'android-compiled.zip')],check=True,capture_output=True)

    def test_select_branch_resource_collision(self):
        path=self.root/'i18n/messages/common.json'
        row=i18n.read(path)[0]
        row.update(key='common.fixture',params={'kind':{'type':'string','example':'photo'}})
        row['zh-CN']={'select':'kind','forms':{'photo':'图','other':'文件'}}
        row['en']={'select':'kind','forms':{'photo':'Photo','other':'File'}}
        other=copy.deepcopy(row);other.update(key='common.fixture_photo',params={})
        other['zh-CN']='其他';other['en']='Other'
        path.write_text(i18n.dump([row,other]))
        with self.assertRaisesRegex(ValueError,'resource key collision'):i18n.build(self.root)

    def test_determinism_stale_and_removed_outputs(self):
        self.assertEqual(i18n.build(self.root),i18n.build(self.root))
        i18n.generate(self.root)
        i18n.generate(self.root,True)
        output=self.root/'web/src/i18n/generated.ts'; output.write_text(output.read_text()+'\n')
        with self.assertRaisesRegex(ValueError,'stale'):i18n.generate(self.root,True)
        self.assertTrue(output.read_text().endswith('\n\n'))  # --check never fixes files
        i18n.generate(self.root)
        manifest=self.root/'i18n/generated-files.json'
        old=self.root/'web/src/i18n/old.ts';old.write_text('old')
        manifest.write_text(i18n.dump(i18n.read(manifest)+['web/src/i18n/old.ts']))
        with self.assertRaisesRegex(ValueError,'stale'):i18n.generate(self.root,True)
        i18n.generate(self.root);self.assertFalse(old.exists())
        with patch.object(i18n,'build',side_effect=[{'a':'1'},{'a':'2'}]):
            with self.assertRaisesRegex(ValueError,'non-deterministic'):i18n.generate(self.root)

    def test_unreviewed_language_cannot_be_enabled(self):
        path=self.root/'i18n/locales.json';data=i18n.read(path);data['selectionEnabled']=True;path.write_text(i18n.dump(data))
        with self.assertRaisesRegex(ValueError,'not reviewed'):i18n.build(self.root)

    def test_lint_ratchet_and_references(self):
        i18n.generate(self.root)
        path=self.root/'web/src/pilot.tsx'
        messages=i18n.load(self.root)[1]
        path.write_text('\n'.join(f't("{k}")' for k,row in messages.items() if 'web' in row['platforms']))
        # Reference platform-only keys on a declared platform, too. Their
        # presence must not weaken the cross-platform misuse assertion below.
        native = {
            'ios': ('ios/RemoteAgent/Pilot.swift', 'L10n'),
            'android': ('android/core/src/main/Pilot.kt', 'UiMessages'),
            'go': ('internal/pilot/pilot.go', 'i18n'),
        }
        for platform, (name, receiver) in native.items():
            refs = []
            for key, row in messages.items():
                if 'web' in row['platforms'] or row['platforms'][0] != platform: continue
                fn = i18n.method(key)
                if platform == 'go': fn = fn[0].upper() + fn[1:]
                refs.append(f'{receiver}.{fn}()')
            fixture = self.root/name
            fixture.parent.mkdir(parents=True, exist_ok=True)
            fixture.write_text('\n'.join(refs))
        i18n_lint.lint(self.root,True);i18n_lint.lint(self.root)
        clean=path.read_text()
        for literal in ['<button>New English action</button>', '<button>\nNew English action\n</button>', 'Text(\n"New action")','Text("新文案")','const a = { label: "New action" };']:
            path.write_text(clean+'\n'+literal)
            with self.assertRaisesRegex(ValueError,'new hardcoded'):i18n_lint.lint(self.root)
        path.write_text(clean+'\nt("common.missing")')
        with self.assertRaisesRegex(ValueError,'undefined key'):i18n_lint.references(self.root)
        path.write_text(clean)
        source=self.root/'i18n/messages/common.json'
        rows=i18n.read(source);rows[0]['platforms']=['android','ios','go'];source.write_text(i18n.dump(rows))
        with self.assertRaisesRegex(ValueError,'not available on web'):i18n_lint.references(self.root)
        rows[0]['platforms']=['web','android','ios','go'];source.write_text(i18n.dump(rows))
        path.write_text('t("common.back")')
        with self.assertRaisesRegex(ValueError,'orphan keys'):i18n_lint.references(self.root)
        path.write_text(clean+'\nconst a="你好";')
        i18n_lint.lint(self.root,True)
        path.write_text(path.read_text()+'\nconst b="你好";')
        with self.assertRaisesRegex(ValueError,'new hardcoded'):i18n_lint.lint(self.root)

if __name__=='__main__':unittest.main()
