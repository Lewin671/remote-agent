#!/usr/bin/env python3
"""Causal retry-budget counterexample; virtual time, not an SDK timing test.

Request/response bootstrap has five serial messages: Probe -> Challenge ->
Offer -> Answer -> the offerer's Confirm. The primary case assigns zero cost
to SDK, crypto, propagation and scheduling. A second, optimistic case starts
Challenge without waiting for Probe and overlaps Confirm with a 3-second
ICE/DTLS completion. Both miss the deadline. SDKs supply raw SDP sizes and
hashes, but this script does not run a PeerConnection.
"""
import argparse
from dataclasses import dataclass
import hashlib
import heapq
import json
import os
from pathlib import Path
import re


@dataclass(frozen=True)
class Frame:
    sender: str
    peer: int
    pc: int
    seq: int
    kind: str
    body: bytes


class Experiment:
    DEADLINE = 10000
    GAP = 2500
    RETRIES = (0, 500, 1500)
    CRITICAL = {'Probe','Challenge','Offer','Answer','Confirm'}

    def __init__(self, offer, answer, losses=2, relay_ms=3000,
                 proactive_challenge=False, ice_ms=0):
        self.offer, self.answer, self.losses = offer, answer, losses
        self.proactive_challenge,self.ice_ms=proactive_challenge,ice_ms
        self.events=[]; self.order=0; self.now=0; self.trace=[]
        self.acked=set(); self.cache={}; self.pending={}; self.expected={}; self.gaps={}
        self.outstanding={}; self.peak=0; self.buffer_peak=0
        self.initial={'client':0,'daemon':0};self.sends={'client':0,'daemon':0}
        self.body_bytes={'client':0,'daemon':0};self.acks={'client':0,'daemon':0}
        self.installed_offers=set();self.installed_answers=set();self.confirms={'client':set(),'daemon':set()}
        self.retired=0;self.ready=set();self.ice_ready=set();self.last_ready_ms=None
        self.event(relay_ms,self.start)

    def event(self,at,action):
        self.order+=1;heapq.heappush(self.events,(at,self.order,action))

    def log(self,event,frame=None,**fields):
        row={'ms':self.now,'event':event,**fields}
        # Store the full trace only for one representative peer/PC.
        if frame is not None:
            if frame.peer!=0 or frame.pc not in (-1,0):return
            row.update(sender=frame.sender,pc=frame.pc,seq=frame.seq,kind=frame.kind)
        self.trace.append(row)

    def send(self,frame):
        key=(frame.sender,frame.peer,frame.pc,frame.seq)
        self.initial[frame.sender]+=1
        lane=key[:3];self.outstanding.setdefault(lane,set()).add(key)
        self.peak=max(self.peak,len(self.outstanding[lane]));assert self.peak<=32
        digest=hashlib.sha256(frame.body).hexdigest()
        for attempt,offset in enumerate(self.RETRIES):
            self.event(self.now+offset,lambda f=frame,a=attempt,h=digest:self.attempt(f,a,h))

    def attempt(self,frame,attempt,digest):
        key=(frame.sender,frame.peer,frame.pc,frame.seq)
        if key in self.acked:return
        assert hashlib.sha256(frame.body).hexdigest()==digest
        self.sends[frame.sender]+=1;self.body_bytes[frame.sender]+=len(frame.body)
        self.log('send',frame,attempt=attempt)
        if frame.kind in self.CRITICAL and attempt<self.losses:
            self.log('drop',frame);return
        self.receive(frame)

    def receive(self,f):
        key=(f.sender,f.peer,f.pc,f.seq);lane=key[:3]
        # Assume valid identity, signature, epoch/head, lease, nonce and deadline.
        if key in self.cache:
            assert self.cache[key]==f.body
        else:
            self.cache[key]=f.body
            if f.pc==-1:self.process(f)
            else:
                expected=self.expected.setdefault(lane,1)
                if f.seq>expected:
                    pending=self.pending.setdefault(lane,{})
                    if not pending:
                        self.gaps[lane]=self.now+self.GAP
                        self.event(self.gaps[lane],lambda l=lane:self.gap(l))
                    pending[f.seq]=f;self.buffer_peak=max(self.buffer_peak,len(pending))
                    assert len(pending)<=32
                else:
                    assert f.seq==expected
                    self.process(f);self.expected[lane]+=1
                    pending=self.pending.setdefault(lane,{})
                    while self.expected[lane] in pending:
                        next_frame=pending.pop(self.expected[lane]);self.process(next_frame);self.expected[lane]+=1
                    if not pending:self.gaps.pop(lane,None)
        # Independent, healthy Control fake pipe. Authenticated receipt ACK;
        # acknowledging a buffered candidate does not install it in the SDK.
        self.acks['daemon' if f.sender=='client' else 'client']+=1
        self.acked.add(key);self.outstanding[lane].discard(key)

    def gap(self,lane):
        if self.gaps.get(lane)==self.now and self.pending.get(lane):
            self.retired+=1;self.log('gap_expired')

    def candidates(self,sender,peer,pc):
        # A bounded maximum: 64 synthetic, documentation-address candidates.
        # They are never submitted to an SDK or used as network destinations.
        sdp=self.offer if sender=='client' else self.answer
        ufrag=re.search(rb'a=ice-ufrag:([^\r]+)',sdp).group(1).decode()
        for batch in range(4):
            candidates=[f'candidate:{n+1} 1 UDP 2122260223 192.0.2.1 {5000+n} typ host ufrag {ufrag}' for n in range(batch*16,(batch+1)*16)]
            assert all(len(c.encode())<=1024 for c in candidates)
            body=json.dumps({'generation':1,'ufrag':ufrag,'candidates':candidates},separators=(',',':')).encode()
            self.send(Frame(sender,peer,pc,2+batch,'Candidate',body))
        self.send(Frame(sender,peer,pc,6,'EndCandidates',b'64,4'))

    def start(self):
        self.log('relay_ready')
        for peer in range(8):
            if self.proactive_challenge:
                # Optimistically assume the daemon already knows the client's
                # current nonce; remove the Probe dependency entirely.
                self.send(Frame('daemon',peer,-1,1,'Challenge',b'fresh-daemon-nonce'))
            else:self.send(Frame('client',peer,-1,1,'Probe',b'fresh-client-nonce'))

    def process(self,f):
        self.log('process',f)
        k=(f.peer,f.pc)
        if f.kind=='Probe':
            self.send(Frame('daemon',f.peer,-1,1,'Challenge',b'fresh-daemon-nonce'))
        elif f.kind=='Challenge':
            for pc in range(2):
                self.send(Frame('client',f.peer,pc,1,'Offer',self.offer))
                self.event(self.now+50,lambda p=f.peer,c=pc:self.candidates('client',p,c))
        elif f.kind=='Offer':
            assert k not in self.installed_offers;self.installed_offers.add(k)
            self.send(Frame('daemon',f.peer,f.pc,1,'Answer',self.answer))
            self.event(self.now+50,lambda p=f.peer,c=f.pc:self.answer_tail(p,c))
        elif f.kind=='Answer':
            assert k in self.installed_offers and k not in self.installed_answers;self.installed_answers.add(k)
            self.event(self.now+self.ice_ms,lambda k=k:self.ice_complete(k))
            self.send(Frame('client',f.peer,f.pc,7,'Confirm',self.confirm_body()))
        elif f.kind=='Confirm':
            assert k in self.installed_offers and k in self.installed_answers
            receiver='daemon' if f.sender=='client' else 'client'
            self.confirms[receiver].add(k)
            self.maybe_ready(k)
        elif f.kind=='Candidate':
            assert k in (self.installed_offers if f.sender=='client' else self.installed_answers)

    def confirm_body(self):
        return hashlib.sha256(self.offer).digest()+hashlib.sha256(self.answer).digest()

    def ice_complete(self,k):
        self.ice_ready.add(k);self.maybe_ready(k)

    def maybe_ready(self,k):
        if k in self.ice_ready and k in self.confirms['daemon'] and k in self.confirms['client']:
            self.ready.add(k);self.last_ready_ms=self.now

    def answer_tail(self,peer,pc):
        self.candidates('daemon',peer,pc)
        # The answerer knows both SDPs already; its Confirm may overlap Answer
        # retransmission. The offerer's Confirm cannot precede receiving Answer.
        self.send(Frame('daemon',peer,pc,7,'Confirm',self.confirm_body()))

    def run(self):
        while self.events:
            at,order,action=heapq.heappop(self.events)
            if at>=self.DEADLINE:break
            self.now=at;action()
        assert self.retired==0  # The lead's 2.5-second fix is respected.
        assert all(n<=128 for n in self.initial.values())
        assert all(n<=384 for n in self.sends.values())
        return dict(initial_frames=self.initial,transmitted_frames=self.sends,
                    payload_bytes_only=self.body_bytes,control_acks=self.acks,
                    sender_window_peak=self.peak,reorder_buffer_peak=self.buffer_peak,
                    retired_generations=self.retired,ready_pcs=len(self.ready),
                    all_ready_at_ms=self.last_ready_ms if len(self.ready)==16 else None,
                    offers_installed=len(self.installed_offers),answers_installed=len(self.installed_answers),
                    ice_ready_pcs=len(self.ice_ready),proactive_challenge=self.proactive_challenge,ice_ms=self.ice_ms,
                    total_deadline_ms=self.DEADLINE,trace=self.trace)


def main():
    assert os.environ.get('REMOTE_AGENT_HOME','').startswith('/tmp/')
    p=argparse.ArgumentParser();p.add_argument('sdp',type=Path);args=p.parse_args();results={}
    for sdk in ['pion','chrome','webkit','android','ios']:
        offer=(args.sdp/(sdk+'-offer.sdp')).read_bytes();answer=(args.sdp/(sdk+'-answer.sdp')).read_bytes()
        results[sdk]={}
        for losses in [0,1,2]:
            result=Experiment(offer,answer,losses).run()
            assert result['ready_pcs']==(0 if losses==2 else 16)
            results[sdk][str(losses)]=result
        optimistic=Experiment(offer,answer,2,proactive_challenge=True,ice_ms=3000).run()
        assert optimistic['ready_pcs']==0 and optimistic['ice_ready_pcs']==0
        results[sdk]['proactive_challenge_within_ice_budget']=optimistic
    print(json.dumps(dict(evidence='causal_virtual_model_not_sdk_latency',
        causal_chain=['Probe','Challenge','Offer','Answer','offerer Confirm'],
        relay_ms=3000,retry_points_ms=[0,500,1500],gap_ms=2500,sender_window=32,
        favorable_lower_bound_ms=3000+5*1500,
        optimistic_bound_ms=3000+3*1500+max(1500,3000),
        note='Primary case: all non-retry costs except relay are zero. Optimistic proactive bootstrap: ICE/DTLS is 3000 ms. Payload byte counts exclude framing/crypto.',
        results=results),indent=2))

if __name__=='__main__':main()
