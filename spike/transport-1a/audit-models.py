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
print(json.dumps({'evidence':'finite event models; crypto is checked by shared native vectors','checks':results},indent=2))
