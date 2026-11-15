import {CandidateBatcher} from './sender.mjs';
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
async function begin(name){conn=(await w.hash(w.utf8('signal/'+name))).slice(0,16);gates=fixtures.map((f,i)=>new Signal(f.id,fixtures[1-i].id,conn,1));line('CASE',name,...fixtures.flatMap(f=>[f.label,f.role]));}
async function rx(sender,type,payload,seq,gen=1,expect=true,opts={}){
 const receiver=1-sender,c=w.context(fixtures[sender].id,fixtures[receiver].id,conn,1,gen,seq,base,opts.cls??1,opts.boot??false);if(opts.boot)c.use=0n;
 const packet=opts.packet??await w.seal(fixtures[sender].id,fixtures[receiver].id,c,type,payload);
 let ok=true;try{await gates[receiver].receive(packet,base+(opts.at??0))}catch{ok=false}if(ok!==expect)throw Error(`generator transition ${type}`);
 line('RX',receiver,opts.at??0,expect?1:0,w.hex(packet));return packet;
}
async function local(which,raw,isOffer,gen=1,expected=true){let ok=true;try{await gates[which].setLocal(raw,isOffer,gen)}catch{ok=false}if(ok!==expected)throw Error('local');line('LOCAL',which,gen,isOffer?1:0,expected?1:0,w.hex(raw));}
function expect(which,key,value){const s=gates[which];const actual={sdk:s.sdkActions,duplicates:s.duplicates,conflicts:s.conflicts,unsupported:s.unsupported,pending:s.pending.size,ready:Number(s.ready()),closed:Number(s.closed),generation:Number(s.generation),next:Number(s.next),stale:s.staleChallenges,boot:Number(s.bootReady),gap_ms:s.gapDeadline?s.gapDeadline-base:0}[key];if(actual!==value)throw Error(`${key}: ${actual} != ${value}`);line('EXPECT',which,key,value)}
async function boot(){
 await rx(0,1,w.pb(w.b(1,conn),w.b(2,fixtures[0].id.instance),w.b(3,fixtures[0].id.boot)),1,0,true,{boot:true});
 // A replayed Probe only elicits a bounded Challenge, never creates a PC.
 await local(1,answer,false,1,false);
 const challenge=await rx(1,2,w.pb(w.b(1,conn),w.b(2,fixtures[1].id.instance),w.b(3,fixtures[1].id.boot),w.b(4,fixtures[0].id.boot)),1,0,true,{boot:true});
 gates[1].challengeHash=await w.hash(challenge);line('CHALLENGE',1,w.hex(gates[1].challengeHash));
 await rx(0,4,w.pb(w.u(4,1),w.u(5,1),w.b(6,gates[1].challengeHash),w.u(7,0),w.u(8,1)),1,1,true,{cls:2});
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
await begin('control-namespace');await establish();await rx(1,4,w.pb(w.u(4,1),w.u(5,4),w.b(6,new Uint8Array(32))),1,1,true,{cls:2});expect(0,'sdk',3);await rx(1,999,w.utf8('future control'),2,1,true,{cls:2});expect(0,'unsupported',1);expect(0,'sdk',3);
await begin('buffer-limit');await boot();await local(0,offer,true);for(let i=2;i<=33;i++)await rx(0,31337,Uint8Array.of(i),i);await rx(0,31337,Uint8Array.of(34),34,1,false);expect(1,'pending',32);expect(1,'sdk',0);

// Accepted review cases: schema, independent gaps, binding, and trickled ICE.
await begin('stale-instance-challenge');
const challengeFields=[w.b(1,conn),w.b(2,fixtures[1].id.instance),w.b(3,fixtures[1].id.boot),w.b(4,fixtures[0].id.boot)];
const stale=await rx(1,2,w.pb(...challengeFields,w.u(5,1)),1,0,true,{boot:true});expect(0,'stale',1);expect(0,'boot',1);expect(0,'sdk',0);
await rx(1,2,new Uint8Array(),1,0,true,{boot:true,packet:stale,at:500});expect(0,'duplicates',1);expect(0,'stale',1);
await rx(1,2,w.pb(...challengeFields,w.u(5,2)),2,0,false,{boot:true});
await rx(1,2,w.pb(...challengeFields,w.b(5,Uint8Array.of(1))),2,0,false,{boot:true});
await rx(1,2,w.pb(...challengeFields,w.u(5,0)),3,0,true,{boot:true});expect(0,'stale',1);

async function gapFrame(seq,at,packet){return rx(0,31337,Uint8Array.of(seq),seq,1,true,{at,packet})}
function tick(which,at){gates[which].tick(base+at);line('TICK',which,at)}
await begin('successive-gaps-recover');await boot();await gapFrame(2,0);const later=await gapFrame(4,2000);await gapFrame(1,2100);expect(1,'gap_ms',4500);tick(1,2500);expect(1,'closed',0);await gapFrame(4,3000,later);expect(1,'gap_ms',4500);await gapFrame(3,4400);expect(1,'next',5);expect(1,'gap_ms',0);expect(1,'closed',0);
await begin('successive-gaps-expire');await boot();await gapFrame(2,0);const expires=await gapFrame(4,2000);await gapFrame(1,2100);await gapFrame(4,4000,expires);expect(1,'gap_ms',4500);tick(1,4499);expect(1,'closed',0);tick(1,4500);expect(1,'closed',1);await rx(0,31337,Uint8Array.of(3),3,1,false,{at:4501});
await begin('split-gap-keeps-first-observation');await boot();await gapFrame(4,0);await gapFrame(2,2000);await gapFrame(1,2100);expect(1,'gap_ms',2500);tick(1,2499);expect(1,'closed',0);tick(1,2500);expect(1,'closed',1);

await begin('policy-reply-request-and-scope');
const a=fixtures[0].id,z=fixtures[1].id,empty=new Uint8Array();
const policy=async (scope,peer,seq)=>w.seal(a,z,w.context(a,z,conn,0,1,seq,base,2),9,w.pb(w.b(1,peer),w.u(2,7),w.u(3,0),w.u(4,scope)));
const machineRequest=await policy(1,empty,9),peerRequest=await policy(2,a.id,10);
async function policyCase(name,request,fields,expected,connection=conn){
 const reply=await w.seal(z,a,w.context(z,a,connection,0,1,400,base,2),13,w.pb(...fields));let accepted=true;
 try{w.validatePolicyReply(await w.open(request,a,z,base),await w.open(reply,z,a,base))}catch{accepted=false}
 if(accepted!==expected)throw Error(name);line('POLICY',name,Number(expected),w.hex(request),w.hex(reply));
}
const replyFields=async (request,seq,scope,peer)=>[w.u(1,7),w.u(2,1),w.u(3,0),w.u(4,seq),w.b(5,await w.hash(request)),w.u(6,scope),w.b(7,peer)];
const machineReply=await replyFields(machineRequest,9,1,empty),peerReply=await replyFields(peerRequest,10,2,a.id);
await policyCase('machine',machineRequest,machineReply,true);await policyCase('peer',peerRequest,peerReply,true);
await policyCase('cross-scope-same-revision',machineRequest,peerReply,false);
for(const [name,index,value]of [['wrong-request-seq',3,w.u(4,10)],['wrong-request-hash',4,w.b(5,await w.hash(peerRequest))],['wrong-scope',5,w.u(6,2)],['wrong-peer',6,w.b(7,z.id)]]){const fields=machineReply.slice();fields[index]=value;await policyCase(name,machineRequest,fields,false)}
const wrongPeer=peerReply.slice();wrongPeer[6]=w.b(7,z.id);await policyCase('other-peer',peerRequest,wrongPeer,false);
await policyCase('missing-request-binding',machineRequest,machineReply.slice(0,3),false);
await policyCase('other-connection',machineRequest,machineReply,false,(await w.hash(w.utf8('other-connection'))).slice(0,16));

for(const count of [5,63,64]){
 const batch=new CandidateBatcher(2);line('BATCH_CASE','trickle-'+count,2);let waiting=[];
 function add(at,gen,raw,ok){if(batch.add(raw,gen,at)!==ok)throw Error('batch add');line('BATCH_ADD',at,gen,w.hex(raw),Number(ok))}
 function flush(at,end,sizes,data){const groups=batch.flush(at,end);if(groups.map(g=>g.length).join(',')!==sizes.join(',')||w.hex(w.concat(...groups.flat()))!==w.hex(Uint8Array.from(data)))throw Error('batch flush');line('BATCH_FLUSH',at,Number(end),sizes.join(','),w.hex(Uint8Array.from(data)))}
 add(0,1,Uint8Array.of(99),false);
 for(let n=0;n<count;n++){add(n*100,2,Uint8Array.of(n),true);waiting.push(n);const full=waiting.length===16;flush(n*100+50,false,full?[16]:[],full?waiting:[]);if(full)waiting=[]}
 flush(count*100,true,waiting.length?[waiting.length]:[],waiting);flush(count*100+1,true,[],[]);add(count*100+2,2,Uint8Array.of(99),false);
}
await fs.mkdir(out,{recursive:true});await fs.writeFile(path.join(out,'scenarios.tsv'),rows.join('\n')+'\n');console.log(`Generated and ran ${rows.filter(r=>r.startsWith('CASE\t')||r.startsWith('BATCH_CASE\t')).length} shared scenarios with real sealed frames`);
