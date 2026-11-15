#!/usr/bin/env python3
"""Finite offline checks. SDK emulator/browser runs are separate, explicit steps."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time

p = argparse.ArgumentParser()
p.add_argument('--out', type=Path, required=True)
p.add_argument('--bc', type=Path, required=True)
p.add_argument('--go', default='go')
a = p.parse_args()
assert os.environ.get('REMOTE_AGENT_HOME', '').startswith('/tmp/')
assert os.environ.get('CGO_ENABLED') == '0'
assert str(a.out).startswith('/tmp/')
root = Path(__file__).resolve().parents[2]
lab = root / 'spike/transport-1a'
a.out.mkdir(parents=True, exist_ok=True)
checks = []

def run(name, command, output=None):
    assert shutil.disk_usage(a.out).free >= 15 * 1024**3, 'clean only owned artifacts before continuing'
    start = time.monotonic()
    result = subprocess.run(list(map(str, command)), cwd=root, capture_output=True, text=True, timeout=120)
    checks.append(dict(name=name, exit_code=result.returncode, seconds=round(time.monotonic()-start, 3), summary=(result.stdout+result.stderr)[-1500:]))
    (a.out/'verification.json').write_text(json.dumps(checks, indent=2)+'\n')
    if output:
        (a.out/output).write_text(result.stdout)
    if result.returncode:
        raise RuntimeError(name+': '+result.stderr[-1500:])
    print(name+': passed', flush=True)

run('browser module graph', ['node',lab/'check-browser-modules.mjs'])
run('draft schema tables', ['python3',lab/'schema-generate.py','--check'])
run('retry/window', ['python3', lab/'retry-gap.py'], 'retry.json')
run('model deferred-path audit', ['python3',lab/'audit-models.py',root/'testdata/transport/sdp'], 'model-audit.json')
run('causal retry model', ['python3',lab/'handshake-budget.py',root/'testdata/transport/sdp'], 'causal.json')
run('whole-round loss', ['python3', lab/'round-loss-matrix.py', root/'testdata/transport/sdp'], 'round-loss.json')
run('Go SDK and sender', [a.go, '-C', lab/'go', 'test', './...', '-count=1'])
run('Go SDP', [a.go, '-C', lab/'go', 'run', '.', 'vectors', root/'testdata/transport/sdp'])
run('JS SDP', ['node', lab/'check-vectors.mjs', root/'testdata/transport/sdp'])
run('JS crypto', ['node', lab/'check-wire.mjs', root/'testdata/transport/wire/vectors.json'])
run('JS shared trace', ['node',lab/'check-signal.mjs',root/'testdata/transport/signal/scenarios.tsv'])
run('Go signal', [a.go, '-C', lab/'go', 'run', './cmd/signal-vectors', root/'testdata/transport/signal/scenarios.tsv'])
run('Java compile', ['javac', '-cp', a.bc, '-d', a.out/'classes', *[lab/(n+'.java') for n in ['SchemaData','Schema','Wire','SDPCheck','Signal','SignalVectors','Sender','SenderVectors']]])
cp = str(a.out/'classes')+os.pathsep+str(a.bc)
for name, source in [('SDPCheck','sdp'), ('Wire','wire'), ('SignalVectors','signal')]:
    target = root/'testdata/transport'/source
    if source != 'sdp':
        target /= 'scenarios.tsv' if source == 'signal' else 'vectors.tsv'
    run('Java '+source, ['java', '-cp', cp, 'example.transportlab.'+name, target])
run('Swift signal compile', ['swiftc','-O','-parse-as-library','-module-cache-path',a.out/'swift-cache',*[lab/(n+'.swift') for n in ['SchemaData','Schema','Wire','SDPCheck','Sender','Signal','SignalVectors']],'-o',a.out/'signal'])
run('Swift signal', [a.out/'signal', root/'testdata/transport/signal/scenarios.tsv'])
# The Swift crypto CLI writes its independent signature beside the input.
shutil.copy(root/'testdata/transport/wire/vectors.tsv', a.out/'vectors.tsv')
run('Swift crypto compile', ['swiftc','-O','-parse-as-library','-D','WIRE_CLI','-module-cache-path',a.out/'swift-cache',lab/'SchemaData.swift',lab/'Schema.swift',lab/'Wire.swift','-o',a.out/'wire'])
run('Swift crypto', [a.out/'wire', a.out/'vectors.tsv'])
run('Go verifies Swift signature', [a.go,'-C',lab/'go','run','./cmd/verify-wire',a.out/'swift-sealed.bin'])
run('Swift SDP compile', ['swiftc','-O','-parse-as-library','-D','VECTOR_CLI','-module-cache-path',a.out/'swift-cache',lab/'SDPCheck.swift','-o',a.out/'sdp'])
run('Swift SDP', [a.out/'sdp',root/'testdata/transport/sdp'])
run('wire capacity generator', [a.go,'-C',lab/'go','run','./cmd/measure-wire',root/'testdata/transport/sdp',a.out/'capacity.tsv'], 'capacity-go.json')
run('JS sender and capacity', ['node',lab/'measure-native.mjs',a.out/'capacity.tsv'], 'capacity-js.json')
run('Java sender and capacity', ['java','-cp',cp,'example.transportlab.SenderVectors',a.out/'capacity.tsv'], 'capacity-java.json')
run('Swift sender compile', ['swiftc','-O','-parse-as-library','-module-cache-path',a.out/'swift-cache',*[lab/(n+'.swift') for n in ['SchemaData','Schema','Wire','Sender','SenderVectors']],'-o',a.out/'sender'])
run('Swift sender and capacity', [a.out/'sender',a.out/'capacity.tsv'], 'capacity-swift.json')
run('terminal fake paths', [a.go,'-C',lab/'terminal','run','.'], 'terminal.json')
assert json.loads((a.out/'terminal.json').read_text()) == json.loads((root/'testdata/transport/terminal/expected.json').read_text())
run('proto syntax', ['protoc','--proto_path='+str(lab/'wire'),'--descriptor_set_out='+str(a.out/'draft.pb'),lab/'wire/transport-draft.proto'])
for row in json.loads((root/'testdata/transport/sdp/manifest.json').read_text()):
    assert hashlib.sha256((root/'testdata/transport/sdp'/row['file']).read_bytes()).hexdigest()==row['sha256']
# The parity inventory must point to actual shared scenarios, not prose-only claims.
cases={row.split('\t')[1] for row in (root/'testdata/transport/signal/scenarios.tsv').read_text().splitlines() if row.split('\t')[0] in ['CASE','BATCH_CASE','SENDER_CASE']}
schema_rows=[r.split('\t') for r in (root/'testdata/transport/signal/scenarios.tsv').read_text().splitlines()]
assert {r[1] for r in schema_rows if r[0]=='SCHEMA'}==set(json.loads((lab/'wire/schema.json').read_text()))
assert {(int(r[1]),int(r[2])) for r in schema_rows if r[0]=='BODY'}=={(c,t) for c,n in [(1,9),(2,13)] for t in range(1,n+1)}
terminal=json.loads((root/'testdata/transport/terminal/expected.json').read_text())['scenarios']
for rule in json.loads((root/'testdata/transport/audit/parity.json').read_text()):
    assert all(rule.get(column) for column in ['d','e','f'])
    for vector in rule['vectors']:
        if '/' in vector: assert (root/'testdata/transport'/vector).exists()
        elif rule['implementations']=='terminal Go': assert vector in terminal
        else: assert vector in cases
run('diff whitespace', ['git','diff','--check'])
print('All finite offline checks passed; SDK runs remain separately recorded.')
