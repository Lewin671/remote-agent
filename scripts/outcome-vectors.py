#!/usr/bin/env python3
"""Deterministic protocol-15 evidence matrix; expected answers are table rows."""
import json
from pathlib import Path
path=Path(__file__).resolve().parents[1]/'testdata/transport/outcomes.json'
old=json.loads(path.read_text())
codes=sorted({v['code'] for v in old['vectors']} | {'ok','identity_conflict','busy'})
rows=[]
def row(name,source,answer,**e):
    rows.append((name,dict(source=source,**e),answer))
row('local-before','local','not_sent')
row('local-after','local','unknown',dispatched=True)
for reason in ['machine_offline','queue_full','quota','login','update','invalid_envelope']:
 row('server-'+reason,'server','not_sent',notForwarded=True,attemptBound=True,reason=reason)
row('server-unbound','server','unknown',notForwarded=True)
row('server-after-enqueue','server','unknown',attemptBound=True)
for reason in ['invalid_envelope','invalid_body','invalid_scope','protocol_mismatch','member_removed']:
 row('rejected-'+reason,'daemon','rejected',declared='rejected',reason=reason,signed=True)
row('rejected-transient','daemon','unknown',declared='rejected',reason='busy',signed=True)
row('rejected-contradiction','daemon','unknown',declared='rejected',reason='invalid_body',signed=True,admitted=True)
for available in [False,True]:
 row('executed-'+str(available),'daemon','executed',declared='executed',signed=True,resultAvailable=available)
row('signed-unknown','daemon','unknown',declared='unknown',signed=True)
for kind in ['business','identity']:
 row('conflict-'+kind,'daemon','conflict',declared='conflict',conflictKind=kind,signed=True)
row('conflict-missing-kind','daemon','unknown',declared='conflict',signed=True)
row('daemon-busy','daemon','not_sent',declared='not_sent',reason='busy',signed=True)
row('daemon-busy-contradiction','daemon','unknown',declared='not_sent',reason='busy',signed=True,admitted=True)
for bad in ['signature','request-hash','payload-hash','outer-category','outer-reason','unauthenticated-id']:
 row('invalid-'+bad,'daemon','unknown',declared='executed',reason=bad)
for kind in ['', 'future']:
 row('unknown-category-'+kind,'daemon','unknown',declared=kind,signed=True)
vectors=[]
for name,e,answer in rows:
 for code in codes:
  for prior in [False,True]:
   for readOnly in [False,True]:
    v=dict(name=name,source='',code=code,signed=False,prior=prior,readOnly=readOnly,declared='',reason='',conflictKind='',dispatched=False,notForwarded=False,attemptBound=False,admitted=False,resultAvailable=False)
    v.update(e)
    v['outcome']='unknown' if prior and answer=='not_sent' else answer
    vectors.append(v)
path.write_text('{\n  "protocol": 15,\n  "commands": '+json.dumps(old['commands'],ensure_ascii=False,indent=2)+',\n  "vectors": [\n'+',\n'.join('    '+json.dumps(v,separators=(',',':')) for v in vectors)+'\n  ],\n  \"downloads\": '+json.dumps(old['downloads'])+'\n}\n')
print(len(vectors))
