"""Conservative lexical ratchet, not a full language parser.

CJK literals everywhere and English text at known presentation/error sinks are
tracked by (file, literal hash, count), so line moves do not create debt and a
second copy of an old literal does. Comments and test/generated code are not UI.
Keep precise non-UI exceptions in i18n/lint-allowlist.json with a reason.
"""
from collections import Counter
import hashlib
import json
from pathlib import Path
import re
from i18n import load, method, read

SUFFIXES = {'.ts','.tsx','.js','.kt','.java','.swift','.go','.sh','.html','.xml','.plist','.webmanifest'}
STRINGS = re.compile(r'"(?:\\.|[^"\\])*"|\'(?:\\.|[^\'\\])*\'|`(?:\\.|[^`\\])*`', re.S)
CJK = re.compile('[\u3400-\u9fff]')
SINK = re.compile(r'(?:Text|Button|Btn|PillButton|TextLink|TextAction|LinkButton|GlassButton|Banner|Hint|PageNote|accessibilityLabel|accessibilityHint|contentDescription|setTitle|setText|Error|error|New|Errorf|Printf|Println|Sprintf|Fatalf|Fprintf|Label|NSLocalizedString)\s*\([\s\S]{0,100}$|(?:label|title|placeholder|message|text|description|note|action|aria-label|alt)\s*[:=]\s*[\s\S]{0,60}$|\b(?:echo|printf)\s+(?:-[a-z]+\s+)?$')


def sources(root):
    roots = ['web/src','web/public','web/index.html','android/app/src/main','android/core/src/main','android/terminal/src/main','ios/RemoteAgent','ios/RemoteAgentCore/Sources','cmd','internal','scripts/install.sh','scripts/migrate-security-v4','install.sh']
    for entry in roots:
        path = root/entry
        files = path.rglob('*') if path.is_dir() else [path]
        for p in files:
            name=p.relative_to(root).as_posix()
            if not p.is_file() or p.suffix not in SUFFIXES: continue
            if any(part in {'gen','RAProto','testdata','node_modules','build','assets'} for part in p.parts): continue
            if re.search(r'(?:\.test\.|_test\.|/Tests/|/test/|/androidTest/)',name): continue
            yield name,p


def strip_comments(text):
    # Preserve quoted strings while removing comments, including apostrophes in prose.
    token = re.compile(STRINGS.pattern + r'|/\*[\s\S]*?\*/|//[^\n]*|<!--[^>]*-->',re.S)
    return token.sub(lambda m: m[0] if m[0][0] in {'"',"'",'`'} else ' '*len(m[0]),text)


def findings(root):
    generated=set(read(root/'i18n/generated-files.json'))
    result=Counter()
    for name,path in sources(root):
        if name in generated: continue
        text=strip_comments(path.read_text())
        for match in STRINGS.finditer(text):
            literal=match[0][1:-1]
            if not literal.strip(): continue
            prefix=text[max(0,match.start()-160):match.start()]
            if re.search(r'\bt\(\s*$', prefix): continue  # key, checked by references()
            if CJK.search(literal) or (SINK.search(prefix) and re.search('[A-Za-z]{2,}',literal)):
                digest=hashlib.sha256(literal.encode()).hexdigest()[:20]
                result[name+'#'+digest]+=1
        # JSX/HTML visible text, including English. Ignore expressions/tags.
        if path.suffix in {'.tsx','.html','.xml','.plist'}:
            for match in re.finditer(r'>([^<>{}]+)<',text):
                literal=' '.join(match[1].split())
                if not re.search('[A-Za-z\u3400-\u9fff]',literal):continue
                digest=hashlib.sha256(literal.encode()).hexdigest()[:20]
                result[name+'#'+digest]+=1
        if path.suffix == '.webmanifest':
            for match in re.finditer(r'"(?:name|short_name|description)"\s*:\s*"([^"]+)"', text):
                literal=match[1]
                digest=hashlib.sha256(literal.encode()).hexdigest()[:20]
                result[name+'#'+digest]+=1
        if path.suffix == '.sh':
            for match in re.finditer(r'(?m)^\s*(?:echo|printf)\s+([^\n]+)', text):
                literal=match[1].strip()
                if literal.startswith(('\"', "'", '$')): continue
                if re.search('[A-Za-z\u3400-\u9fff]', literal):
                    digest=hashlib.sha256(literal.encode()).hexdigest()[:20]
                    result[name+'#'+digest]+=1
    return result


def references(root):
    _,messages=load(root)
    usage={k:set() for k in messages}
    names={method(k):k for k in messages}
    generated=set(read(root/'i18n/generated-files.json'))
    for name,path in sources(root):
        if name in generated:continue
        text=strip_comments(path.read_text())
        platform='web' if name.startswith('web/') else 'android' if name.startswith('android/') else 'ios' if name.startswith('ios/') else 'go'
        refs=[]
        if platform=='web':refs += re.findall(r'\bt\(\s*["\']([^"\']+)["\']',text)
        if platform in {'android','ios'}:
            for fn in re.findall(r'\b(?:UiMessages|L10n|L10nMessage)\.(\w+)\s*\(',text):
                if fn not in names: raise ValueError(f'{name}: undefined message function {fn}')
                refs.append(names[fn])
        if platform=='go':
            for fn in re.findall(r'\bi18n\.([A-Z]\w+)\s*\(',text):
                candidate=fn[0].lower()+fn[1:]
                if candidate in names: refs.append(names[candidate])
        for key in refs:
            if key not in messages: raise ValueError(f'{name}: undefined key {key}')
            if platform not in messages[key]['platforms']:raise ValueError(f'{name}: {key} not available on {platform}')
            usage[key].add(platform)
    # Static launch HTML and install metadata are generated from these inputs.
    for path in (root/'scripts/i18n_templates').glob('web-*'):
        for key in re.findall(r'\{\{([a-z][a-z0-9_.]+)\}\}', path.read_text()):
            if key not in messages: raise ValueError(f'{path.name}: undefined key {key}')
            if 'web' not in messages[key]['platforms']: raise ValueError(f'{path.name}: {key} not available on web')
            usage[key].add('web')
    for key in ['startup.ios_camera_usage', 'startup.ios_photo_library_add_usage']:
        if key in usage: usage[key].add('ios')
    orphan=[k for k,users in usage.items() if not users]
    if orphan:raise ValueError('orphan keys: '+', '.join(orphan))


def lint(root, update=False):
    references(root)
    actual=findings(root)
    allow=read(root/'i18n/lint-allowlist.json')
    for entry in allow:
        if set(entry)!={'path','literal','reason'} or not entry['reason'].strip():raise ValueError('allowlist requires exact path/literal/reason')
        digest=hashlib.sha256(entry['literal'].encode()).hexdigest()[:20]
        actual.pop(entry['path']+'#'+digest,None)
    path=root/'i18n/lint-baseline.json'
    if update:
        path.write_text(json.dumps(dict(sorted(actual.items())),indent=2)+'\n')
        return
    baseline=read(path)
    added={key:count-baseline.get(key,0) for key,count in actual.items() if count>baseline.get(key,0)}
    if added:raise ValueError('new hardcoded text (extract to i18n, or justify an exact allowlist entry):\n'+'\n'.join(f'{key}: +{count}' for key,count in sorted(added.items())))
