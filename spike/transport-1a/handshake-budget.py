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
from dataclasses import dataclass, replace
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
    generation: int = 1
    use: int = 0
    receiver_nonce: bytes = b""
    recipient_unknown: bool = False
    probe_hash: bytes = b""
    deadline: int = 10000
    schema_valid: bool = True
    authorization_revision: int = 1


class Experiment:
    DEADLINE = 10000
    GAP = 2500
    RETRIES = (0, 500, 1500)
    CRITICAL = {'Probe','Challenge','Offer','Answer','Confirm'}

    def __init__(self, offer, answer, losses=2, relay_ms=3000,
                 proactive_challenge=False, ice_ms=0, faults=None, peers=8, discovery_ms=0, offerer="client", ack_delay_ms=0, delivery_delays=None, implicit_offer=True):
        self.offer, self.answer, self.losses = offer, answer, losses
        self.faults,self.peers=faults,peers
        self.ack_delay_ms=ack_delay_ms;self.delivery_delays=delivery_delays or {};self.implicit_offer=implicit_offer
        self.boot_ready={'client':set(),'daemon':set()};self.challenges={};self.probes={};self.received_probes={};self.learned_nonce={}
        self.implicit_proofs=[];self.gate_rejections=0;self.challenge_ack_at={};self.offer_installed_at={}
        self.retired_pcs=set()
        self.offerer=offerer;self.answerer="daemon" if offerer=="client" else "client";self.offer_started=set()
        self.deliveries=[];self.dropped=[];self.ack_ordinals={}
        self.proactive_challenge,self.ice_ms=proactive_challenge,ice_ms
        self.lease_until=10000;self.members_authorized=True;self.authorization_revision=1
        self.events=[]; self.order=0; self.now=0; self.trace=[]
        self.acked=set(); self.cache={}; self.pending={}; self.pending_at={}; self.expected={}; self.gaps={}
        self.outstanding={}; self.peak=0; self.buffer_peak=0
        self.initial={'client':0,'daemon':0};self.sends={'client':0,'daemon':0}
        self.body_bytes={'client':0,'daemon':0};self.acks={'client':0,'daemon':0}
        self.installed_offers=set();self.installed_answers=set();self.confirms={'client':set(),'daemon':set()}
        self.retired=0;self.ready=set();self.ice_ready=set();self.last_ready_ms=None
        self.ice_started=set();self.candidate_ready={"client":set(),"daemon":set()}
        self.event(relay_ms,lambda:self.log("relay_ready"))
        self.event(relay_ms+discovery_ms,self.start)

    def authorized(self,frame):
        return self.now<self.lease_until and self.members_authorized and frame.authorization_revision==self.authorization_revision

    @staticmethod
    def decode_body(name,raw,shape=False):
        import importlib.util
        spec=importlib.util.spec_from_file_location('model_schema',Path(__file__).with_name('model-schema.py'))
        codec=importlib.util.module_from_spec(spec);spec.loader.exec_module(codec)
        return codec.normalize(raw,name,shape=shape)

    def event(self,at,action):
        self.order+=1;heapq.heappush(self.events,(at,self.order,action))

    def log(self,event,frame=None,**fields):
        row={'ms':self.now,'event':event,**fields}
        # Store the full trace only for one representative peer/PC.
        if frame is not None:
            if frame.peer!=0 or frame.pc not in (-1,0):return
            row.update(sender=frame.sender,pc=frame.pc,seq=frame.seq,kind=frame.kind)
        self.trace.append(row)

    @staticmethod
    def nonce(side,peer):return f'current-{side}-{peer}'.encode()

    @staticmethod
    def ack_binding(frame):
        # Symbolic authenticated Context + frozen-frame identity, not crypto.
        identity=repr(frame).encode()
        return (1,frame.seq,hashlib.sha256(identity).digest(),frame.generation,frame.use)

    def send(self,frame):
        if not frame.schema_valid or not self.authorized(frame):return False
        receiver='daemon' if frame.sender=='client' else 'client'
        frame=replace(frame,generation=0 if frame.pc==-1 else frame.generation,use=0 if frame.pc==-1 else frame.pc+1,
                      recipient_unknown=frame.kind=='Probe',receiver_nonce=b'' if frame.kind=='Probe' else frame.receiver_nonce or self.learned_nonce.get((frame.sender,frame.peer),b'unknown'))
        if frame.kind=='Probe':self.probes[(frame.sender,frame.peer)]=frame
        if frame.kind=='Challenge':
            request=self.received_probes.get((frame.sender,frame.peer));frame=replace(frame,probe_hash=self.ack_binding(request)[2] if request else b'')
        if frame.kind=='Challenge':self.challenges[frame.peer]=frame
        key=(frame.sender,frame.peer,frame.pc,frame.seq)
        self.initial[frame.sender]+=1
        lane=key[:3];self.outstanding.setdefault(lane,set()).add(key)
        self.peak=max(self.peak,len(self.outstanding[lane]));assert self.peak<=32
        digest=hashlib.sha256(frame.body).hexdigest()
        for attempt,offset in enumerate(self.RETRIES):
            self.event(self.now+offset,lambda f=frame,a=attempt,h=digest:self.attempt(f,a,h))

    def attempt(self,frame,attempt,digest):
        if not frame.schema_valid or not self.authorized(frame):return False
        key=(frame.sender,frame.peer,frame.pc,frame.seq)
        if key in self.acked or self.now>=frame.deadline or (frame.pc>=0 and (frame.generation!=1 or frame.receiver_nonce!=self.learned_nonce.get((frame.sender,frame.peer)))):return
        assert hashlib.sha256(frame.body).hexdigest()==digest
        self.sends[frame.sender]+=1;self.body_bytes[frame.sender]+=len(frame.body)
        self.log('send',frame,attempt=attempt)
        delivery=('Signal',frame.sender,frame.peer,frame.pc,frame.seq,attempt)
        self.deliveries.append(delivery)
        if (self.faults is not None and delivery in self.faults) or (self.faults is None and frame.kind in self.CRITICAL and attempt<self.losses):
            self.dropped.append(delivery)
            self.log('drop',frame);return
        delay=self.delivery_delays.get(delivery,0)
        if delay:self.event(self.now+delay,lambda:self.receive(frame))
        else:self.receive(frame)

    def receive(self,f):
        if not f.schema_valid or not self.authorized(f):return False
        key=(f.sender,f.peer,f.pc,f.seq);lane=key[:3]
        # Crypto/identity/head/lease are premises; admission below is explicit.
        receiver='daemon' if f.sender=='client' else 'client';pc_key=(f.peer,f.pc)
        if self.now>=f.deadline:self.gate_rejections+=1;self.log('expired_delivery',f);return
        if f.recipient_unknown and (f.kind!='Probe' or f.receiver_nonce):self.gate_rejections+=1;return
        if f.kind=='Challenge':
            request=self.probes.get((receiver,f.peer))
            if not request or self.now>=request.deadline or f.probe_hash!=self.ack_binding(request)[2] or f.receiver_nonce!=self.nonce(receiver,f.peer):self.gate_rejections+=1;return
        if f.pc>=0:
            valid_context=not f.recipient_unknown and f.generation==1 and f.use==f.pc+1 and f.receiver_nonce==self.nonce(receiver,f.peer)
            if not valid_context or pc_key in self.retired_pcs:
                self.gate_rejections+=1;self.log('context_rejected',f);return
            if pc_key not in self.boot_ready[receiver]:
                if self.implicit_offer and f.kind=='Offer' and f.sender==self.offerer and f.seq==1:
                    self.boot_ready[receiver].add(pc_key);self.implicit_proofs.append((receiver,f.peer,f.pc,self.now));self.log('offer_nonce_proof',f)
                else:self.gate_rejections+=1;self.log('bootstrap_gate_rejected',f);return
        elif f.generation!=0 or f.use!=0:return
        if key in self.cache:
            assert self.cache[key]==f
        else:
            self.cache[key]=f
            if f.pc==-1:self.process(f)
            else:
                expected=self.expected.setdefault(lane,1)
                if f.seq>expected:
                    pending=self.pending.setdefault(lane,{})
                    pending[f.seq]=f;self.pending_at.setdefault(lane,{})[f.seq]=self.now
                    self.update_gap(lane)
                    self.buffer_peak=max(self.buffer_peak,len(pending))
                    assert len(pending)<=32
                else:
                    assert f.seq==expected
                    self.process(f);self.expected[lane]+=1
                    pending=self.pending.setdefault(lane,{})
                    while self.expected[lane] in pending:
                        next_frame=pending.pop(self.expected[lane]);self.pending_at[lane].pop(self.expected[lane])
                        if self.now>=next_frame.deadline or next_frame.generation!=1 or next_frame.receiver_nonce!=self.nonce(receiver,next_frame.peer):self.retired_pcs.add(pc_key);self.retired+=1;pending.clear();self.pending_at[lane].clear();self.log('stale_buffer',next_frame);return
                        self.process(next_frame);self.expected[lane]+=1
                    self.update_gap(lane)
        # Independent Control fake pipe; fault sets may also drop ACKs.
        # acknowledging a buffered candidate does not install it in the SDK.
        if f.kind=='Probe':return  # The correlated Challenge acknowledges Probe.
        ack_sender='daemon' if f.sender=='client' else 'client'
        self.acks[ack_sender]+=1
        ordinal=self.ack_ordinals.get(key,0);self.ack_ordinals[key]=ordinal+1
        delivery=('Control',ack_sender,f.peer,f.pc,f.seq,ordinal)
        self.deliveries.append(delivery)
        if self.faults is not None and delivery in self.faults:
            self.dropped.append(delivery);return
        delay=self.delivery_delays.get(delivery,self.ack_delay_ms)
        binding=self.ack_binding(f)
        if delay:self.event(self.now+delay,lambda:self.acknowledge(f,binding))
        else:self.acknowledge(f,binding)

    def acknowledge(self,f,binding):
        if not f.schema_valid or not self.authorized(f):return False
        receiver='daemon' if f.sender=='client' else 'client'
        if self.now>=f.deadline or (f.pc>=0 and ((f.peer,f.pc) in self.retired_pcs or f.generation!=1 or f.receiver_nonce!=self.nonce(receiver,f.peer))):return False
        if binding!=self.ack_binding(f):return False
        if f.kind=='Challenge':
            original=self.challenges.get(f.peer)
            if original is None or binding!=self.ack_binding(original):return False
            self.challenge_ack_at[f.peer]=self.now
            self.boot_ready['daemon'].update((f.peer,pc) for pc in range(2))
            self.log('challenge_ack',f,use=binding[4],generation=binding[3])
        key=(f.sender,f.peer,f.pc,f.seq);self.acked.add(key);self.outstanding[key[:3]].discard(key)
        if f.kind=='Challenge' and self.offerer=='daemon':self.begin_offers(f.peer)
        return True

    def update_gap(self,lane):
        arrivals=self.pending_at.get(lane,{})
        if not arrivals:self.gaps.pop(lane,None);return
        deadline=min(min(at+self.GAP,self.pending[lane][seq].deadline) for seq,at in arrivals.items())
        if self.gaps.get(lane)==deadline:return
        self.gaps[lane]=deadline
        self.event(deadline,lambda:self.gap(lane))

    def gap(self,lane):
        if self.gaps.get(lane)==self.now and self.pending.get(lane):
            self.retired+=1;self.retired_pcs.add((lane[1],lane[2]));self.log('gap_expired')

    def candidates(self,sender,peer,pc):
        # A bounded maximum: 64 synthetic, documentation-address candidates.
        # They are never submitted to an SDK or used as network destinations.
        sdp=self.offer if sender==self.offerer else self.answer
        ufrag=re.search(rb'a=ice-ufrag:([^\r]+)',sdp).group(1).decode()
        for batch in range(4):
            candidates=[f'candidate:{n+1} 1 UDP 2122260223 192.0.2.1 {5000+n} typ host ufrag {ufrag}' for n in range(batch*16,(batch+1)*16)]
            assert all(len(c.encode())<=1024 for c in candidates)
            body=json.dumps({'generation':1,'ufrag':ufrag,'candidates':candidates},separators=(',',':')).encode()
            self.send(Frame(sender,peer,pc,2+batch,'Candidate',body))
        self.send(Frame(sender,peer,pc,6,'EndCandidates',b'64,4'))

    def start(self):
        self.log('bootstrap_start')
        for peer in range(self.peers):
            if self.proactive_challenge:
                # Optimistically assume the daemon already knows the client's
                # current nonce; remove the Probe dependency entirely.
                self.learned_nonce[('daemon',peer)]=self.nonce('client',peer)
                self.send(Frame('daemon',peer,-1,1,'Challenge',self.nonce('daemon',peer)))
            else:self.send(Frame('client',peer,-1,1,'Probe',self.nonce('client',peer)))

    def process(self,f):
        if not f.schema_valid or not self.authorized(f):return False
        self.log('process',f)
        k=(f.peer,f.pc)
        if f.kind=='Probe':
            self.received_probes[('daemon',f.peer)]=f
            self.learned_nonce[('daemon',f.peer)]=f.body
            self.send(Frame('daemon',f.peer,-1,1,'Challenge',self.nonce('daemon',f.peer)))
        elif f.kind=='Challenge':
            request=self.probes[('client',f.peer)];key=(request.sender,request.peer,request.pc,request.seq);self.acked.add(key);self.outstanding[key[:3]].discard(key)
            assert f.receiver_nonce==self.nonce('client',f.peer)
            self.learned_nonce[('client',f.peer)]=f.body
            self.boot_ready['client'].update((f.peer,pc) for pc in range(2))
            if self.offerer=='client':self.begin_offers(f.peer)
        elif f.kind=='Offer':
            assert k not in self.installed_offers;self.installed_offers.add(k);self.offer_installed_at[k]=self.now
            self.send(Frame(self.answerer,f.peer,f.pc,1,'Answer',self.answer))
            self.event(self.now+50,lambda p=f.peer,c=f.pc:self.answer_tail(p,c))
        elif f.kind=='Answer':
            assert k in self.installed_offers and k not in self.installed_answers;self.installed_answers.add(k)
            self.maybe_start_ice(k)
            self.send(Frame(self.offerer,f.peer,f.pc,7,'Confirm',self.confirm_body()))
        elif f.kind=='Confirm':
            assert k in self.installed_offers and k in self.installed_answers
            receiver='daemon' if f.sender=='client' else 'client'
            self.confirms[receiver].add(k)
            self.maybe_ready(k)
        elif f.kind=='Candidate':
            assert k in (self.installed_offers if f.sender==self.offerer else self.installed_answers)
            self.candidate_ready[f.sender].add(k);self.maybe_start_ice(k)

    def begin_offers(self,peer):
        if peer in self.offer_started:return
        self.offer_started.add(peer)
        for pc in range(2):
            self.send(Frame(self.offerer,peer,pc,1,'Offer',self.offer))
            self.event(self.now+50,lambda p=peer,c=pc:self.candidates(self.offerer,p,c))

    def confirm_body(self):
        return hashlib.sha256(self.offer).digest()+hashlib.sha256(self.answer).digest()

    def maybe_start_ice(self,k):
        if k in self.ice_started or k not in self.installed_answers:return
        if any(k not in ready for ready in self.candidate_ready.values()):return
        self.ice_started.add(k)
        self.event(self.now+self.ice_ms,lambda:self.ice_complete(k))

    def ice_complete(self,k):
        self.ice_ready.add(k);self.maybe_ready(k)

    def maybe_ready(self,k):
        if k not in self.retired_pcs and k in self.ice_ready and k in self.confirms['daemon'] and k in self.confirms['client']:
            self.ready.add(k);self.last_ready_ms=self.now

    def answer_tail(self,peer,pc):
        self.candidates(self.answerer,peer,pc)
        # The answerer knows both SDPs already; its Confirm may overlap Answer
        # retransmission. The offerer's Confirm cannot precede receiving Answer.
        self.send(Frame(self.answerer,peer,pc,7,'Confirm',self.confirm_body()))

    def run(self):
        while self.events:
            at,order,action=heapq.heappop(self.events)
            if at>=self.DEADLINE:break
            self.now=at;action()
        assert all(n<=128 for n in self.initial.values())
        assert all(n<=384 for n in self.sends.values())
        timed_out=len(self.ready)!=2*self.peers
        pending_before=sum(len(q) for q in self.outstanding.values())
        self.events.clear();self.pending.clear();self.pending_at.clear();self.gaps.clear();self.outstanding.clear()
        return dict(timed_out=timed_out,relay_available=True,bootstrap_gate_rejections=self.gate_rejections,implicit_offer_proofs=len(self.implicit_proofs),
                    active_timers=0,pending_frames=0,owned_pcs=0 if timed_out else 2*self.peers,
                    tombstone_hashes=len(self.cache),unacked_at_deadline=pending_before,
                    next_retry_ms=self.DEADLINE+500 if timed_out else None,
                    initial_frames=self.initial,transmitted_frames=self.sends,
                    payload_bytes_only=self.body_bytes,control_acks=self.acks,
                    sender_window_peak=self.peak,reorder_buffer_peak=self.buffer_peak,
                    retired_generations=self.retired,ready_pcs=len(self.ready),
                    all_ready_at_ms=self.last_ready_ms if len(self.ready)==2*self.peers else None,
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
