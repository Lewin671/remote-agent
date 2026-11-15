#!/usr/bin/env python3
"""Shared timing/scope premises exercised against both signaling event models."""
import importlib.util
import json
import os
from pathlib import Path
import sys
assert os.environ.get('REMOTE_AGENT_HOME','').startswith('/tmp/')
sys.dont_write_bytecode=True
def module(name,file):
    spec=importlib.util.spec_from_file_location(name,Path(__file__).with_name(file))
    value=importlib.util.module_from_spec(spec);sys.modules[name]=value;spec.loader.exec_module(value);return value
retry=module('audit_retry','retry-gap.py');handshake=module('audit_handshake','handshake-budget.py')
root=Path(sys.argv[1]);offer=(root/'pion-offer.sdp').read_bytes();answer=(root/'pion-answer.sdp').read_bytes()
results={}
# A queued callback must not invoke the observer after expiry/retirement.
for reason in ['expiry','generation']:
    e=retry.Experiment(0);e.expected[1]=1;e.buffer[1]={};f=retry.Frame(1,1,'Offer',b'opaque',100)
    e.now=100 if reason=='expiry' else 0
    if reason=='generation':e.current=2
    assert e.apply(f) is False and not e.sdk_calls
    results['retry-dispatch-'+reason]=True
# Isolate the gap after an actual cold Probe/Challenge; do not seed peer nonces.
class WithoutLocalOffer(handshake.Experiment):
    def begin_offers(self,peer):pass
for reason in ['expiry','identity','generation']:
    e=WithoutLocalOffer(offer,answer,losses=0,relay_ms=0,discovery_ms=0,ice_ms=0,peers=1,faults=set())
    def enqueue():
        f=handshake.Frame('client',0,0,2,'Candidate',b'opaque',deadline=100 if reason=='expiry' else 9000)
        e.send(f)
    e.event(10,enqueue)
    if reason=='identity':
        old_nonce=e.nonce
        def rebind():
            e.nonce=lambda side,peer:b'new-daemon' if side=='daemon' else old_nonce(side,peer)
            e.learned_nonce[('client',0)]=b'new-daemon'
        e.event(50,rebind)
    if reason=='generation':e.event(50,lambda:e.retired_pcs.add((0,0)))
    def fill():e.send(handshake.Frame('client',0,0,1,'Offer',offer))
    e.event(200,fill);e.run()
    assert ((0,0) in e.installed_offers)==(reason=='identity')
    assert not any(row.get('event')=='process' and row.get('kind')=='Candidate' for row in e.trace)
    results['handshake-deferred-'+reason]=True
f=review_frame=handshake.Frame('daemon',0,-1,1,'Challenge',b'opaque',generation=0,use=0,deadline=100)
e=handshake.Experiment(offer,answer,peers=1);e.now=100;assert not e.acknowledge(f,e.ack_binding(f));results['handshake-late-ack']=True
# Classes (d/e/f) are explicit premises at each model's decode/admit boundary.
trace=root.parent/'signal/scenarios.tsv'
for name,model in [('retry',retry),('handshake',handshake)]:
    count=0
    for row in trace.read_text().splitlines():
        p=row.split('\t')
        if p[0]=='BODY':
            names=['','Probe','Challenge','SDP','SDP','Candidates','EndCandidates','Confirm','Restart','Close'] if p[1]=='1' else ['','Capabilities','AttachPath','InputStatus','Ack','Replay','Barrier','StreamFailed','ClosePath','DirectPolicy','Unsupported','InputStatusReply','BarrierReply','PolicyReply']
            try:model.Experiment.decode_body(names[int(p[2])],bytes.fromhex(p[3]),True);ok=True
            except (ValueError,AssertionError,KeyError):ok=False
            assert ok==(p[4]=='1'),(name,names[int(p[2])]);count+=1
            continue
        if p[0]!='SCHEMA':continue
        try:got=model.Experiment.decode_body(p[1],bytes.fromhex(p[2])).hex()
        except (ValueError,AssertionError,KeyError):got='!'
        assert got==p[3],(name,p[1]);count+=1
    results[name+'-default-and-canonical-vectors']=count
    for reason in ['malformed','lease','membership','revision']:
        if name=='retry':
            e=model.Experiment(0);f=model.Frame(1,1,'Offer',b'opaque');e.expected[1]=1;e.buffer[1]={}
        else:
            e=model.Experiment(offer,answer,peers=1);f=model.Frame('client',0,-1,1,'Probe',b'opaque',generation=0,recipient_unknown=True)
        if reason=='malformed':f=__import__('dataclasses').replace(f,schema_valid=False)
        if reason=='lease':e.lease_until=e.now
        if reason=='membership':e.members_authorized=False
        if reason=='revision':e.authorization_revision+=1
        assert e.receive(f) is False
        if name=='retry':assert not e.seen and not e.sdk_calls;assert e.acknowledge(f) is False
        else:assert not e.cache and not e.learned_nonce;assert e.acknowledge(f,e.ack_binding(f)) is False
        results[name+'-admission-and-deferred-'+reason]=True
# Admit first, then invalidate the live view before queued reply/retry/ACK work.
for reason in ['lease','membership','revision','generation']:
    e=retry.Experiment(0);f=retry.Frame(1,1,'Offer',b'opaque');e.expected[1]=1;e.buffer[1]={};e.receive(f)
    admitted=len(e.sdk_calls);sent=e.ack_sends;e.now=50
    if reason=='lease':e.lease_until=50
    if reason=='membership':e.members_authorized=False
    if reason=='revision':e.authorization_revision=2
    if reason=='generation':e.current=2
    if reason=='generation':
        e.acknowledge(f);assert e.ack_sends==sent+1
    else:assert e.acknowledge(f) is False and e.ack_sends==sent
    assert len(e.sdk_calls)==admitted
    results['retry-admitted-then-revoked-'+reason]=True
    h=handshake.Experiment(offer,answer,losses=0,relay_ms=0,ice_ms=0,peers=1,faults=set())
    h.run();f=h.challenges[0];before=(len(h.acked),dict(h.initial),dict(h.sends));h.now=9500
    if reason=='lease':h.lease_until=h.now
    if reason=='membership':h.members_authorized=False
    if reason in ['revision','generation']:h.authorization_revision+=1
    assert h.acknowledge(f,h.ack_binding(f)) is False and h.send(f) is False
    assert before==(len(h.acked),dict(h.initial),dict(h.sends))
    results['handshake-admitted-then-revoked-'+reason]=True
# Retired-PC receipts settle the original hash only; no new SDK work.
h=handshake.Experiment(offer,answer,losses=0,relay_ms=0,ice_ms=0,peers=1,faults=set());h.run()
f=next(f for f in h.cache.values() if f.kind=='Offer');h.retired_pcs.add((f.peer,f.pc));h.now=100
assert h.receipt_allowed(f)
assert not h.receipt_allowed(__import__('dataclasses').replace(f,body=b'conflict'))
h.now=f.deadline;assert not h.receipt_allowed(f)
results['handshake-retired-hash-bound-receipt']=True
e=retry.Experiment(0);f=retry.Frame(1,1,'Offer',b'opaque');e.expected[1]=1;e.buffer[1]={};e.receive(f);e.current=2;e.now=1
assert e.receipt_allowed(f)
assert not e.receipt_allowed(__import__('dataclasses').replace(f,deadline=f.deadline+1))
assert not e.receipt_allowed(__import__('dataclasses').replace(f,kind='Close'))
results['retry-retired-full-frame-identity']=True
print(json.dumps({'evidence':'finite event models; crypto is checked by shared native vectors','checks':results},indent=2))
