import {CandidateBatcher,Sender} from './sender.mjs';
import fs from 'node:fs/promises';
import path from 'node:path';
import * as w from './wire.mjs';
import {Signal,sdpBody,candidateBody,endBody,inspectSDP} from './signal.mjs';
if(!process.env.REMOTE_AGENT_HOME?.startsWith('/tmp/'))throw Error('isolation');
const [sdpDir,out]=process.argv.slice(2),base=1700000000000;
const offer=await fs.readFile(path.join(sdpDir,'pion-offer.sdp')),answer=await fs.readFile(path.join(sdpDir,'pion-answer.sdp')),restart=await fs.readFile(path.join(sdpDir,'pion-restart.sdp'));
const fixtures=[{label:'c000000000000001',role:1,id:await w.fixture('c000000000000001',1)},{label:'m000000000000001',role:2,id:await w.fixture('m000000000000001',2)}].sort((a,b)=>w.hex(a.id.id)<w.hex(b.id.id)?-1:1);
const rows=[];let gates,conn;
function line(...fields){rows.push(fields.join('\t'))}
async function begin(name,use=1){conn=(await w.hash(w.utf8('signal/'+name))).slice(0,16);gates=fixtures.map((f,i)=>new Signal(f.id,fixtures[1-i].id,conn,use,base));line('CASE',name,...fixtures.flatMap(f=>[f.label,f.role]),use);}
async function rx(sender,type,payload,seq,gen=1,expect=true,opts={}){
 const receiver=1-sender,c=type===1&&opts.boot?w.probeContext(fixtures[sender].id,gates[sender].peer,conn,seq,base):w.context(fixtures[sender].id,gates[sender].peer,conn,opts.use??gates[sender].use,gen,seq,base,opts.cls??1,opts.boot??false);if(opts.boot)c.use=0n;if(opts.receiverBoot)c.receiverBoot=opts.receiverBoot;if(opts.deadline!==undefined)c.deadline=BigInt(base+opts.deadline);if(opts.context)Object.assign(c,opts.context);
 const packet=opts.packet??await w.seal(fixtures[sender].id,fixtures[receiver].id,c,type,payload);
 if(type===1&&expect&&!opts.packet){const sent=await w.open(packet,fixtures[sender].id,fixtures[receiver].id,base);await gates[sender].rememberProbe(sent);line('PROBE',sender,w.hex(packet))}
 let ok=true;try{await gates[receiver].receive(packet,base+(opts.at??0))}catch{ok=false}if(ok!==expect)throw Error(`generator transition ${type}`);
 line('RX',receiver,opts.at??0,expect?1:0,w.hex(packet));return packet;
}
async function local(which,raw,isOffer,gen=1,expected=true){let ok=true;try{await gates[which].setLocal(raw,isOffer,gen)}catch{ok=false}if(ok!==expected)throw Error('local');line('LOCAL',which,gen,isOffer?1:0,expected?1:0,w.hex(raw));}
function expect(which,key,value){const s=gates[which];const actual={sdk:s.sdkActions,duplicates:s.duplicates,conflicts:s.conflicts,unsupported:s.unsupported,pending:s.pending.size,ready:Number(s.ready()),closed:Number(s.closed),generation:Number(s.generation),next:Number(s.next),stale:s.staleChallenges,boot:Number(s.bootReady),gap_ms:s.gapDeadline?s.gapDeadline-base:0,peer_known:Number(s.peer.boot.length===32),dispatches:s.dispatches}[key];if(actual!==value)throw Error(`${key}: ${actual} != ${value}`);line('EXPECT',which,key,value)}
async function rememberChallenge(which,packet){const sent=await w.open(packet,fixtures[which].id,fixtures[1-which].id,base);await gates[which].rememberChallenge(sent);line('CHALLENGE',which,w.hex(packet));return sent}
function ackBody(sent,changes={}){const c=sent.context;return w.pb(w.u(4,changes.cls??c.cls),w.u(5,changes.seq??c.seq),w.b(6,changes.hash??sent.hash),w.u(7,changes.generation??c.generation),w.u(8,changes.use??c.use))}
async function boot({ack=true,challengeSeq=1,at=0,probeSender=0}={}){
 const a=probeSender,z=1-a;
 await rx(a,1,w.pb(w.b(1,conn),w.b(2,fixtures[a].id.instance),w.b(3,fixtures[a].id.boot)),1,0,true,{boot:true,at});
 await local(z,answer,false,1,false);
 const packet=await rx(z,2,w.pb(w.b(1,conn),w.b(2,fixtures[z].id.instance),w.b(3,fixtures[z].id.boot),w.b(4,gates[z].peer.boot),w.u(5,0),w.b(6,gates[z].receivedProbe.hash)),challengeSeq,0,true,{boot:true,at});
 const sent=await rememberChallenge(z,packet);
 if(ack)await rx(a,4,ackBody(sent),1,1,true,{cls:2,use:0,at});
 return sent;
}

function candidates(raw,gen=1,n=1,index=0,bad={}){const info=inspectSDP(raw);const list=Array.from({length:n},(_,i)=>({original:w.utf8(`candidate:${i+1} 1 UDP 2122260223 192.0.2.1 5000 typ host ufrag ${w.text(info.ufrag)}`),ufrag:info.ufrag,mid:info.mid,index:0,generation:gen,...bad}));return candidateBody(list,index)}
async function establish(){await boot();await local(0,offer,true);await rx(0,3,await sdpBody(offer),1);await local(1,answer,false);await rx(1,4,await sdpBody(answer),1);for(const sender of [0,1]){const raw=sender===0?offer:answer;await rx(sender,5,candidates(raw),2);await rx(sender,6,endBody(inspectSDP(raw).ufrag,1,1),3);}await rx(0,7,gates[0].confirmBody(),4);await rx(1,7,gates[1].confirmBody(),4);gates.forEach((g,i)=>{g.dtls=true;line('DTLS',i,1)});expect(0,'ready',1);expect(1,'ready',1);}

await begin('full-lifecycle');await establish();await rx(1,8,w.pb(w.b(1,w.pb(w.u(1,1),w.u(2,1))),w.u(2,2)),5);gates[0].nextGeneration(2);line('NEXT',0,2);
await local(0,restart,true,2);await rx(0,5,candidates(restart,2),2,2);expect(1,'pending',1);await local(1,answer,false,1,false);await rx(0,3,await sdpBody(restart),1,2);await local(1,answer,false,2);await rx(1,4,await sdpBody(answer),1,2);await rx(0,9,w.pb(w.u(1,1)),3,2);expect(1,'closed',1);

await begin('reorder-duplicate-conflict');await boot();await local(0,offer,true);const early=await rx(0,5,candidates(offer),2);expect(1,'sdk',0);expect(1,'pending',1);await rx(0,5,new Uint8Array(),2,1,true,{packet:early,at:500});expect(1,'duplicates',1);await rx(0,3,await sdpBody(offer),1,1,true,{at:1500});expect(1,'sdk',2);expect(1,'pending',0);await rx(0,5,candidates(offer,1,2),2,1,false);expect(1,'conflicts',1);expect(1,'sdk',2);

await begin('fixed-gap');await boot();await local(0,offer,true);const gap=await rx(0,5,candidates(offer),2,1,true,{at:50});await rx(0,5,new Uint8Array(),2,1,true,{packet:gap,at:2000});gates[1].tick(base+2549);line('TICK',1,2549);expect(1,'closed',0);gates[1].tick(base+2550);line('TICK',1,2550);expect(1,'closed',1);expect(1,'sdk',0);await rx(0,3,await sdpBody(restart),1,2,true,{at:2600});expect(1,'closed',0);expect(1,'generation',2);expect(1,'sdk',1);

await begin('candidate-binding');await boot();await local(0,offer,true);await rx(0,3,await sdpBody(offer),1);await rx(0,5,candidates(offer,1,1,0,{ufrag:w.utf8('wrong')}),2,1,false);expect(1,'sdk',1);
await begin('candidate-old-generation');await establish();gates[0].nextGeneration(2);line('NEXT',0,2);await local(0,restart,true,2);await rx(0,3,await sdpBody(restart),1,2);await rx(0,5,candidates(offer,1),99,1,false);expect(1,'sdk',4);
await begin('candidate-cap');await boot();await local(0,offer,true);await rx(0,3,await sdpBody(offer),1);for(let i=0;i<4;i++)await rx(0,5,candidates(offer,1,16,i),2+i);await rx(0,5,candidates(offer,1,1,3),6,1,false);expect(1,'sdk',65);
await begin('glare-role');await boot();await local(1,offer,true,1,false);await rx(1,3,await sdpBody(offer),1,1,false);expect(0,'sdk',0);
await begin('bad-confirm');await boot();await local(0,offer,true);await rx(0,3,await sdpBody(offer),1);await local(1,answer,false);await rx(0,7,new Uint8Array(128),2,1,false);expect(1,'ready',0);
await begin('unknown-authenticated');await boot();const unknown=await rx(0,31337,Uint8Array.of(0,255),1);expect(1,'unsupported',1);expect(1,'sdk',0);await rx(0,31337,new Uint8Array(),1,1,true,{packet:unknown});expect(1,'duplicates',1);
await begin('tamper-before-state');await boot();const c=w.context(fixtures[0].id,fixtures[1].id,conn,1,1,1,base);const tamper=await w.seal(fixtures[0].id,fixtures[1].id,c,3,await sdpBody(offer));tamper[tamper.length-1]^=1;await rx(0,3,new Uint8Array(),1,1,false,{packet:tamper});expect(1,'sdk',0);expect(1,'next',1);
await begin('control-namespace');await establish();await rx(1,4,w.pb(w.u(4,1),w.u(5,4),w.b(6,new Uint8Array(32)),w.u(7,1),w.u(8,1)),1,1,true,{cls:2});expect(0,'sdk',3);await rx(1,999,w.utf8('future control'),2,1,true,{cls:2});expect(0,'unsupported',1);expect(0,'sdk',3);
await begin('buffer-limit');await boot();await local(0,offer,true);for(let i=2;i<=33;i++)await rx(0,31337,Uint8Array.of(i),i);await rx(0,31337,Uint8Array.of(34),34,1,false);expect(1,'pending',32);expect(1,'sdk',0);

await begin('probe-ack-is-not-challenge-proof');
const probePacket=await rx(0,1,w.pb(w.b(1,conn),w.b(2,fixtures[0].id.instance),w.b(3,fixtures[0].id.boot)),1,0,true,{boot:true});
const probeRecord=await w.open(probePacket,fixtures[0].id,fixtures[1].id,base);
await rx(1,4,ackBody(probeRecord),1,1,false,{cls:2,use:0});expect(0,'boot',0);expect(0,'sdk',0);await local(0,offer,true,1,false);
await rx(1,4,ackBody(probeRecord,{use:1}),2,1,false,{cls:2,use:0});expect(0,'boot',0);

// R2: no correct Offer may be exhausted while waiting for an overtaken ACK.
for(const use of [1,2]){
 await begin('offer-overtakes-bootstrap-ack-'+use,use);const sent=await boot({ack:false,at:4000});expect(1,'boot',0);
 await local(0,offer,true);const first=await rx(0,3,await sdpBody(offer),1,1,true,{at:4000});expect(1,'boot',1);expect(1,'sdk',1);
 await rx(0,3,new Uint8Array(),1,1,true,{packet:first,at:4500});await rx(0,3,new Uint8Array(),1,1,true,{packet:first,at:5500});expect(1,'sdk',1);
 await rx(0,4,ackBody(sent,{use:1}),2,1,false,{cls:2,use:0,at:5505});
 await rx(0,4,ackBody(sent),3,1,true,{cls:2,use:0,at:5510});expect(1,'boot',1);expect(1,'sdk',1);
 await local(1,answer,false);await rx(1,4,await sdpBody(answer),1,1,true,{at:5510});await rx(0,7,gates[0].confirmBody(),2,1,true,{at:5510});await rx(1,7,gates[1].confirmBody(),2,1,true,{at:5510});gates.forEach((g,i)=>{g.dtls=true;line('DTLS',i,1)});expect(0,'ready',1);expect(1,'ready',1);
}
await begin('bootstrap-ack-original-context');const original=await boot({ack:false,challengeSeq:7});
for(const [n,change]of [{use:1},{generation:1},{seq:1},{hash:new Uint8Array(32)},{cls:2}].entries()){await rx(0,4,ackBody(original,change),n+1,1,false,{cls:2,use:0});expect(1,'boot',0)}
await rx(0,4,ackBody(original),6,1,true,{cls:2,use:0});expect(1,'boot',1);
await begin('offer-wrong-current-nonce');await boot({ack:false});await local(0,offer,true);await rx(0,3,await sdpBody(offer),1,1,false,{receiverBoot:new Uint8Array(32)});expect(1,'boot',0);expect(1,'sdk',0);await rx(0,3,await sdpBody(offer),1);expect(1,'sdk',1);
await begin('offer-role-before-bootstrap-ack');await boot({ack:false,probeSender:1});await rx(1,3,await sdpBody(offer),1,1,false);expect(0,'boot',0);expect(0,'sdk',0);
const inlineCandidate=raw=>w.concat(raw,w.utf8('a=candidate:1 1 UDP 2122260223 192.0.2.1 5000 typ host ufrag '+w.text(inspectSDP(raw).ufrag)+'\r\n'));
await begin('inline-offer-before-ack');await boot({ack:false});await local(0,inlineCandidate(offer),true,1,false);await rx(0,3,await sdpBody(inlineCandidate(offer)),1,1,false);expect(1,'boot',0);expect(1,'sdk',0);
await begin('inline-answer');await boot();await local(0,offer,true);await rx(0,3,await sdpBody(offer),1);await rx(1,4,await sdpBody(inlineCandidate(answer)),1,1,false);expect(0,'sdk',0);
await begin('inline-restart');await establish();gates[0].nextGeneration(2);line('NEXT',0,2);await local(0,inlineCandidate(restart),true,2,false);await rx(0,3,await sdpBody(inlineCandidate(restart)),1,2,false);expect(1,'sdk',3);

// Accepted review cases: schema, independent gaps, binding, and trickled ICE.
await begin('stale-instance-challenge');const staleProbe=await rx(0,1,w.pb(w.b(1,conn),w.b(2,fixtures[0].id.instance),w.b(3,fixtures[0].id.boot)),1,0,true,{boot:true});
const challengeFields=[w.b(1,conn),w.b(2,fixtures[1].id.instance),w.b(3,fixtures[1].id.boot),w.b(4,fixtures[0].id.boot)];
const stale=await rx(1,2,w.pb(...challengeFields,w.u(5,1),w.b(6,await w.hash(staleProbe))),1,0,true,{boot:true});expect(0,'stale',1);expect(0,'boot',1);expect(0,'sdk',0);
await rx(1,2,new Uint8Array(),1,0,true,{boot:true,packet:stale,at:500});expect(0,'duplicates',1);expect(0,'stale',1);
await rx(1,2,w.pb(...challengeFields,w.u(5,2),w.b(6,await w.hash(staleProbe))),2,0,false,{boot:true});
await rx(1,2,w.pb(...challengeFields,w.b(5,Uint8Array.of(1)),w.b(6,await w.hash(staleProbe))),2,0,false,{boot:true});
await rx(1,2,w.pb(...challengeFields,w.u(5,0),w.b(6,await w.hash(staleProbe))),3,0,true,{boot:true});expect(0,'stale',1);

async function gapFrame(seq,at,packet){return rx(0,31337,Uint8Array.of(seq),seq,1,true,{at,packet})}
function tick(which,at){gates[which].tick(base+at);line('TICK',which,at)}
await begin('successive-gaps-recover');await boot();await gapFrame(2,0);const later=await gapFrame(4,2000);await gapFrame(1,2100);expect(1,'gap_ms',4500);tick(1,2500);expect(1,'closed',0);await gapFrame(4,3000,later);expect(1,'gap_ms',4500);await gapFrame(3,4400);expect(1,'next',5);expect(1,'gap_ms',0);expect(1,'closed',0);
await begin('successive-gaps-expire');await boot();await gapFrame(2,0);const expires=await gapFrame(4,2000);await gapFrame(1,2100);await gapFrame(4,4000,expires);expect(1,'gap_ms',4500);tick(1,4499);expect(1,'closed',0);tick(1,4500);expect(1,'closed',1);await rx(0,31337,Uint8Array.of(3),3,1,false,{at:4501});
await begin('split-gap-keeps-first-observation');await boot();await gapFrame(4,0);await gapFrame(2,2000);await gapFrame(1,2100);expect(1,'gap_ms',2500);tick(1,2499);expect(1,'closed',0);tick(1,2500);expect(1,'closed',1);

await begin('policy-reply-request-and-scope');await boot();
const a=fixtures[0].id,z=fixtures[1].id,empty=new Uint8Array();
const policy=async (scope,peer,seq)=>w.seal(a,z,w.context(a,gates[0].peer,conn,0,1,seq,base,2),9,w.pb(w.b(1,peer),w.u(2,7),w.u(3,0),w.u(4,scope)));
const machineRequest=await policy(1,empty,9),peerRequest=await policy(2,a.id,10);
async function policyCase(name,request,fields,expected,connection=conn,at=0){
 const reply=await w.seal(z,a,w.context(z,gates[1].peer,connection,0,1,400,base,2),13,w.pb(...fields));let accepted=true;
 try{w.validatePolicyReply(await w.open(request,a,z,base),await w.open(reply,z,a,base),base+at)}catch{accepted=false}
 if(accepted!==expected)throw Error(name);line('POLICY',name,Number(expected),w.hex(request),w.hex(reply),at);
}
const replyFields=async (request,seq,scope,peer)=>[w.u(1,7),w.u(2,1),w.u(3,0),w.u(4,seq),w.b(5,await w.hash(request)),w.u(6,scope),w.b(7,peer)];
const machineReply=await replyFields(machineRequest,9,1,empty),peerReply=await replyFields(peerRequest,10,2,a.id);
await policyCase('machine',machineRequest,machineReply,true);await policyCase('late-policy-reply',machineRequest,machineReply,false,conn,10000);await policyCase('peer',peerRequest,peerReply,true);
await policyCase('cross-scope-same-revision',machineRequest,peerReply,false);
for(const [name,index,value]of [['wrong-request-seq',3,w.u(4,10)],['wrong-request-hash',4,w.b(5,await w.hash(peerRequest))],['wrong-scope',5,w.u(6,2)],['wrong-peer',6,w.b(7,z.id)]]){const fields=machineReply.slice();fields[index]=value;await policyCase(name,machineRequest,fields,false)}
const wrongPeer=peerReply.slice();wrongPeer[6]=w.b(7,z.id);await policyCase('other-peer',peerRequest,wrongPeer,false);
await policyCase('missing-request-binding',machineRequest,machineReply.slice(0,3),false);
await policyCase('other-connection',machineRequest,machineReply,false,(await w.hash(w.utf8('other-connection'))).slice(0,16));

for(const count of [5,63,64]){
 const batch=new CandidateBatcher(2,10000,1);line('BATCH_CASE','trickle-'+count,2,10000,1);let waiting=[];
 function add(at,gen,raw,ok){if(batch.add(raw,gen,at,1)!==ok)throw Error('batch add');line('BATCH_ADD',at,gen,w.hex(raw),Number(ok))}
 function flush(at,end,sizes,data){const groups=batch.flush(at,end,2,1);if(groups.map(g=>g.length).join(',')!==sizes.join(',')||w.hex(w.concat(...groups.flat()))!==w.hex(Uint8Array.from(data)))throw Error('batch flush');line('BATCH_FLUSH',at,Number(end),sizes.join(','),w.hex(Uint8Array.from(data)))}
 add(0,1,Uint8Array.of(99),false);
 for(let n=0;n<count;n++){add(n*100,2,Uint8Array.of(n),true);waiting.push(n);const full=waiting.length===16;flush(n*100+50,false,full?[16]:[],full?waiting:[]);if(full)waiting=[]}
 flush(count*100,true,waiting.length?[waiting.length]:[],waiting);flush(count*100+1,true,[],[]);add(count*100+2,2,Uint8Array.of(99),false);
}

// Systematic audit: all paths share named operations and expected observables.
await begin('audit-cold-no-preloaded-process');expect(0,'peer_known',0);expect(1,'peer_known',0);await boot();expect(0,'peer_known',1);expect(1,'peer_known',1);await local(0,offer,true);await rx(0,3,await sdpBody(offer),1);expect(1,'sdk',1);
await begin('audit-challenge-probe-correlation');expect(0,'peer_known',0);const cp=await rx(0,1,w.pb(w.b(1,conn),w.b(2,fixtures[0].id.instance),w.b(3,fixtures[0].id.boot)),1,0,true,{boot:true});
const cbFields=[w.b(1,conn),w.b(2,fixtures[1].id.instance),w.b(3,fixtures[1].id.boot),w.b(4,fixtures[0].id.boot),w.u(5,0)];
await rx(1,2,w.pb(...cbFields,w.b(6,new Uint8Array(32))),1,0,false,{boot:true});expect(0,'peer_known',0);expect(0,'boot',0);
await rx(1,2,w.pb(...cbFields,w.b(6,await w.hash(cp))),2,0,true,{boot:true});expect(0,'peer_known',1);
await begin('audit-unsolicited-challenge');await rx(1,2,w.pb(w.b(1,conn),w.b(2,fixtures[1].id.instance),w.b(3,fixtures[1].id.boot),w.b(4,fixtures[0].id.boot),w.u(5,0),w.b(6,new Uint8Array(32))),1,0,false,{boot:true,context:{receiverInstance:fixtures[0].id.instance,receiverBoot:fixtures[0].id.boot}});expect(0,'boot',0);expect(0,'peer_known',0);
await begin('audit-probe-correlation-expiry');const expProbe=await rx(0,1,w.pb(w.b(1,conn),w.b(2,fixtures[0].id.instance),w.b(3,fixtures[0].id.boot)),1,0,true,{boot:true,deadline:100});await rx(1,2,w.pb(w.b(1,conn),w.b(2,fixtures[1].id.instance),w.b(3,fixtures[1].id.boot),w.b(4,fixtures[0].id.boot),w.u(5,0),w.b(6,await w.hash(expProbe))),1,0,false,{boot:true,at:100});expect(0,'peer_known',0);
for(const type of [2,3,4,5,6,7,8,9]){await begin('audit-unknown-recipient-type-'+type);await boot();await rx(0,type,new Uint8Array(),1,type===2?0:1,false,{boot:type===2,context:{recipientUnknown:1n,receiverInstance:new Uint8Array(),receiverBoot:new Uint8Array()}});expect(1,'sdk',0)}
await begin('audit-buffer-deadline-drain');await boot();await local(0,offer,true);await rx(0,5,candidates(offer),2,1,true,{deadline:100});expect(1,'gap_ms',100);await rx(0,3,await sdpBody(offer),1,1,false,{at:200});expect(1,'sdk',0);expect(1,'next',1);expect(1,'closed',1);
await begin('audit-buffer-deadline-equality');await boot();await local(0,offer,true);await rx(0,5,candidates(offer),2,1,true,{deadline:100});await rx(0,3,await sdpBody(offer),1,1,false,{at:100});expect(1,'sdk',0);
await begin('audit-buffer-before-deadline');await boot();await local(0,offer,true);await rx(0,5,candidates(offer),2,1,true,{deadline:100});await rx(0,3,await sdpBody(offer),1,1,true,{at:99});expect(1,'sdk',2);
async function dispatch(which,packet,at,expected){const got=await gates[which].dispatch(packet,base+at);if(got!==expected)throw Error('dispatch');line('DISPATCH',which,at,Number(expected),w.hex(packet))}
function forget(which){gates[which].forgetPeer();line('FORGET',which)}
for(const reason of ['expiry','generation','identity','close','duplicate']){await begin('audit-sdk-dispatch-'+reason);await boot();await local(0,offer,true);const p=await rx(0,3,await sdpBody(offer),1,1,true,{deadline:100});
 if(reason==='generation'){gates[1].nextGeneration(2);line('NEXT',1,2)}if(reason==='identity')forget(1);if(reason==='close')await rx(0,9,w.pb(w.u(1,1)),2);
 if(reason==='duplicate'){await dispatch(1,p,1,true);await dispatch(1,p,2,false);expect(1,'dispatches',1)}else{await dispatch(1,p,reason==='expiry'?100:1,false);expect(1,'dispatches',0)}
}
for(const reason of ['expiry','generation','identity']){await begin('audit-transport-queue-'+reason);await boot();await local(0,offer,true);const c=w.context(fixtures[0].id,gates[0].peer,conn,1,1,1,base);c.deadline=BigInt(base+100);const raw=await w.seal(fixtures[0].id,gates[0].peer,c,3,await sdpBody(offer));await w.open(raw,gates[1].peer,gates[1].self,base);line('QUEUE',1,w.hex(raw));
 if(reason==='generation'){gates[1].nextGeneration(2);line('NEXT',1,2)}if(reason==='identity')forget(1);let ok=true;try{await gates[1].receive(raw,base+200)}catch{ok=false}if(ok)throw Error('late queue');line('DELIVER',1,200,0);expect(1,'sdk',0);
}
await begin('audit-round-deadline-local-callback');await boot();tick(0,10000);await local(0,offer,true,1,false);expect(0,'closed',1);
const uf=w.text(inspectSDP(offer).ufrag),candidatePrefix='candidate:1 1 UDP 2122260223 192.0.2.1 5000 typ host';
for(const [name,text]of [['ufrag-duplicate',candidatePrefix+' ufrag '+uf+' ufrag wrong'],['ufrag-duplicate-same',candidatePrefix+' ufrag '+uf+' ufrag '+uf],['ufrag-case',candidatePrefix+' UFRAG '+uf],['ufrag-missing-value',candidatePrefix+' ufrag'],['candidate-tab',candidatePrefix+'\tufrag '+uf],['candidate-nel',candidatePrefix+'\u0085ufrag '+uf],['candidate-bom','\ufeff'+candidatePrefix]]){await begin('audit-'+name);await boot();await local(0,offer,true);await rx(0,3,await sdpBody(offer),1);await rx(0,5,candidates(offer,1,1,0,{original:w.utf8(text)}),2,1,false);expect(1,'sdk',1)}
// Primitives are driven with original expiry and an owner binding revision.
for(const mode of ['expiry','identity','generation','limited']){let sender=new Sender(10000);line('SENDER_CASE','audit-send-'+mode,10000);const raw=Uint8Array.of(1);if(!await sender.enqueue(raw,0,100,1))throw Error('enqueue');line('SEND_ADD',0,100,1,w.hex(raw),1);if((await sender.tick(0)).length!==1)throw Error('send first');line('SEND_TICK',0,1);
 if(mode==='identity'||mode==='generation'){sender.rebind(2);line('SEND_REBIND',2)}if(mode==='limited'){sender.limited(await w.hash(raw),0,500);line('SEND_LIMIT',0,500,w.hex(raw))}
 if((await sender.tick(100)).length!==0||sender.pending.size)throw Error('stale sender');line('SEND_TICK',100,0);line('SEND_PENDING',0);
}
for(const mode of ['expiry','identity','generation']){const b=new CandidateBatcher(2,100,1);line('BATCH_CASE','audit-batch-'+mode,2,100,1);if(!b.add(Uint8Array.of(1),2,0,1))throw Error('batch add');line('BATCH_ADD',0,2,'01',1);const at=mode==='expiry'?100:50,g=mode==='generation'?3:2,binding=mode==='identity'?2:1;const groups=b.flush(at,true,g,binding);if(groups.length||b.pending.length)throw Error('stale batch');line('BATCH_BOUND_FLUSH',at,1,g,binding);}

for(const mode of ['expiry','binding']){const sender=new Sender(10000),raw=Uint8Array.of(7);line('SENDER_CASE','audit-transport-write-'+mode,10000);await sender.enqueue(raw,0,100,1);line('SEND_ADD',0,100,1,'07',1);await sender.tick(0);line('SEND_TICK',0,1);if(mode==='binding'){sender.rebind(2);line('SEND_REBIND',2)};if(await sender.admit(raw,mode==='expiry'?100:1))throw Error('stale network write');line('SEND_ADMIT',mode==='expiry'?100:1,'07',0)}

async function recordAt(kind,which,packet,at,expected){gates[which].tick(base+at);const sent=await w.open(packet,fixtures[which].id,fixtures[1-which].id,base);let ok=true;try{if(kind==='probe')await gates[which].rememberProbe(sent);else await gates[which].rememberChallenge(sent)}catch{ok=false}if(ok!==expected)throw Error('record expiry');line('RECORD',kind,which,at,w.hex(packet),Number(expected))}
{await begin('audit-outgoing-probe-record-expiry');let c=w.probeContext(fixtures[0].id,gates[0].peer,conn,1,base);c.deadline=BigInt(base+100);let packet=await w.seal(fixtures[0].id,gates[0].peer,c,1,w.pb(w.b(1,conn),w.b(2,fixtures[0].id.instance),w.b(3,fixtures[0].id.boot)));await recordAt('probe',0,packet,100,false);expect(0,'boot',0);
await begin('audit-outgoing-challenge-record-expiry');const rp=await rx(0,1,w.pb(w.b(1,conn),w.b(2,fixtures[0].id.instance),w.b(3,fixtures[0].id.boot)),1,0,true,{boot:true,deadline:100});c=w.context(fixtures[1].id,gates[1].peer,conn,0,0,1,base,1,true);packet=await w.seal(fixtures[1].id,gates[1].peer,c,2,w.pb(w.b(1,conn),w.b(2,fixtures[1].id.instance),w.b(3,fixtures[1].id.boot),w.b(4,gates[1].peer.boot),w.u(5,0),w.b(6,await w.hash(rp))));await recordAt('challenge',1,packet,100,false);}
for(const reason of ['generation','identity','close']){await begin('audit-buffer-owner-'+reason);await boot();await local(0,offer,true);const old=await rx(0,5,candidates(offer),2);if(reason==='generation'){gates[1].nextGeneration(2);line('NEXT',1,2)}else if(reason==='identity')forget(1);else{await rx(0,9,w.pb(w.u(1,1)),1)}expect(1,'pending',0);expect(1,'sdk',0);await rx(0,3,await sdpBody(offer),1,1,false,{at:1});expect(1,'sdk',0)}
await fs.mkdir(out,{recursive:true});await fs.writeFile(path.join(out,'scenarios.tsv'),rows.join('\n')+'\n');console.log(`Generated and ran ${rows.filter(r=>r.startsWith('CASE\t')||r.startsWith('BATCH_CASE\t')||r.startsWith('SENDER_CASE\t')).length} shared scenarios with real sealed frames`);
