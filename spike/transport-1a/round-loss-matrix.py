#!/usr/bin/env python3
"""Enumerate whole-round loss positions, including Control ACKs and retries.

Two peers x two PCs cover same/different-PC and same/different-peer positions.
Unrelated peers are symmetric in this zero-propagation model; an eight-peer
run also checks the maximum concurrent count. This is not an SDK latency test.
"""
import importlib.util
import itertools
import json
import os
from pathlib import Path
import sys

assert os.environ.get('REMOTE_AGENT_HOME','').startswith('/tmp/')
sys.dont_write_bytecode=True
spec=importlib.util.spec_from_file_location('handshake_budget',Path(__file__).with_name('handshake-budget.py'))
mod=importlib.util.module_from_spec(spec);sys.modules[spec.name]=mod;spec.loader.exec_module(mod)
root=Path(sys.argv[1]);offer=(root/'pion-offer.sdp').read_bytes();answer=(root/'pion-answer.sdp').read_bytes()
cases=0;max_ready=0;counts={0:0,1:0,2:0};worst=[]

def run(faults,peers=2,offerer="client",ack_delay_ms=0,delivery_delays=None):
    global cases,max_ready,worst
    # 3s relay + 1s discovery; ICE waits for both installed descriptions and
    # a candidate on each side, then consumes 3s. With <=2 total lost frames,
    # <=130 transmissions and <=3MiB+128KiB fit the initial 256/4MiB burst,
    # so no extra token wait is required. These are injected model budgets.
    e=mod.Experiment(offer,answer,losses=0,relay_ms=3000,discovery_ms=1000,ice_ms=3000,faults=set(faults),peers=peers,offerer=offerer,ack_delay_ms=ack_delay_ms,delivery_delays=delivery_delays)
    r=e.run();assert len(e.dropped)<=2
    assert r['ready_pcs']==2*peers and r['all_ready_at_ms']<=10000,(faults,r)
    assert r['active_timers']==r['pending_frames']==r['retired_generations']==0 and r['relay_available']
    cases+=1;counts[len(e.dropped)]+=1
    if r['all_ready_at_ms']>max_ready:max_ready=r['all_ready_at_ms'];worst=list(faults)
    return e,r

roles={};orders={}
for ack_delay_ms in [0,10]:
    order_start=cases
    for offerer in ['client','daemon']:
        before=cases;base,_=run([],offerer=offerer,ack_delay_ms=ack_delay_ms)
        for first in base.deliveries:
            one,_=run([first],offerer=offerer,ack_delay_ms=ack_delay_ms)
            position=one.deliveries.index(first)
            for second in one.deliveries[position+1:]:run([first,second],offerer=offerer,ack_delay_ms=ack_delay_ms)
        large,large_result=run(worst,peers=8,offerer=offerer,ack_delay_ms=ack_delay_ms)
        roles[f'{offerer}/ack-delay-{ack_delay_ms}']={'cases':cases-before,'large_ready':large_result['ready_pcs']}
    orders[str(ack_delay_ms)]=cases-order_start
# Exact review ordering: two Challenge ACK copies lost; the third at 5510 ms
# overtakes the last scheduled Offer retry point, 5500 ms. Admission must not
# depend on that late ACK. Explicit transport retries still require its binding.
faults={('Control','client',0,-1,1,0),('Control','client',0,-1,1,1)}
delays={('Control','client',0,-1,1,2):10}
review,r=run(faults,peers=1,delivery_delays=delays)
assert review.challenge_ack_at[0]==5510 and review.offer_installed_at[(0,0)]==4000
assert r['implicit_offer_proofs']==2 and r['bootstrap_gate_rejections']==0
legacy=mod.Experiment(offer,answer,losses=0,relay_ms=3000,discovery_ms=1000,ice_ms=3000,
                      peers=1,faults=faults,delivery_delays=delays,implicit_offer=False).run()
assert legacy['timed_out'] and legacy['retired_generations']>0
review_order={'dropped_frames':len(review.dropped),'challenge_ack_ms':review.challenge_ack_at[0],
              'last_scheduled_offer_retry_ms':5500,'offer_installed_ms':review.offer_installed_at[(0,0)],
              'ready_ms':r['all_ready_at_ms'],'legacy_gate_timed_out':legacy['timed_out']}
outside=mod.Experiment(offer,answer,losses=2,relay_ms=3000).run()
assert outside['timed_out'] and outside['owned_pcs']==outside['pending_frames']==outside['active_timers']==0
assert outside['relay_available'] and outside['next_retry_ms']==10500
# Full-jitter state and notifications: no notification may reset selected backoff.
class Recovery:
    def __init__(self):self.failures=0;self.retry_at=None;self.desired_generation=0
    def fail(self,now,random_fraction):
        assert self.retry_at is None and 0<=random_fraction<1
        cap=min(30000,1000*2**self.failures);self.failures+=1
        self.retry_at=now+int(cap*random_fraction)
        return cap
    def notify(self,generation):self.desired_generation=max(self.desired_generation,generation)
    def due(self,now):
        if self.retry_at is None or now<self.retry_at:return None
        self.retry_at=None;return self.desired_generation
recovery=Recovery()
for attempt in range(12):
    cap=recovery.fail(10000,0.625);selected=recovery.retry_at
    for generation in range(1000):recovery.notify(generation)
    assert recovery.retry_at==selected and 10000<=selected<=10000+cap
    assert recovery.due(selected-1) is None
    assert recovery.due(selected)==999 and recovery.due(selected) is None
print(json.dumps({'evidence':'virtual_time_model','cases':cases,'ordering_cases':orders,'review_order':review_order,'roles':roles,'loss_count_cases':counts,
    'maximum_ready_ms':max_ready,'worst_faults':worst,
    'eight_peer_initial_frames':large_result['initial_frames'],
    'eight_peer_ready':large_result['ready_pcs'],
    'outside_promise':{k:outside[k] for k in ['timed_out','owned_pcs','pending_frames','active_timers','relay_available','next_retry_ms']},
    'backoff':'full jitter 1/2/4/.../30 seconds; generation notifications preserve deadline'},indent=2))
