import * as w from './wire.mjs';
import {validateSDP} from './sdp.mjs';
export function inspectSDP(raw){const info=validateSDP(raw);return {raw,ufrag:w.utf8(info.ufrag),mid:w.utf8(info.mid??''),fingerprint:w.unhex(info.fingerprint.slice(8).replaceAll(':',''))}}
export const sdpBody=async raw=>w.pb(w.b(1,raw),w.b(2,await w.hash(raw)));
export async function readSDPBody(raw){const f=w.parse(raw,2);w.need(f,'bb');if(!w.equal(await w.hash(f.get(1).value),f.get(2).value))throw Error('SDP hash');return inspectSDP(f.get(1).value)}
export function candidateBody(list,index){return w.concat(...list.map(c=>{const raw=w.pb(w.b(1,c.original),w.b(2,c.ufrag),w.b(3,c.mid),w.u(4,c.index),w.u(5,c.generation));return w.concat(Uint8Array.of(10),w.vint(raw.length),raw)}),w.pb(w.u(2,index)))}
export function readCandidates(bytes){let at=0,list=[];
 const read=()=>{let n=0,shift=0,start=at;for(let i=0;i<5;i++){if(at===bytes.length)throw Error('short');const b=bytes[at++];n+=(b&127)*2**shift;if(!(b&128)){if(!w.equal(bytes.slice(start,at),w.vint(n)))throw Error('integer');return n}shift+=7}throw Error('length')};
 while(bytes[at]===10){at++;const len=read();if(len>1024||len>bytes.length-at||list.length>=16)throw Error('candidate bounds');const f=w.parse(bytes.slice(at,at+len),5);at+=len;w.need(f,'bbbuu');const v=n=>f.get(n).value;
  const original=w.text(v(1));if(!original.startsWith('candidate:')||/[\0\r\n]/.test(original))throw Error('candidate');const tokens=original.split(/\s+/);const u=tokens.indexOf('ufrag');if(u>=0&&tokens[u+1]!==w.text(v(2)))throw Error('ufrag');list.push({original:v(1),ufrag:v(2),mid:v(3),index:v(4),generation:v(5)});
 }
 const tail=w.parse(bytes.slice(at),1);if(tail.size!==1||!tail.has(2)||tail.get(2).bytes||tail.get(2).value>3n)throw Error('batch');return {list,index:Number(tail.get(2).value)};
}
export const endBody=(ufrag,count,batches)=>w.pb(w.b(1,ufrag),w.u(2,count),w.u(3,batches));
export class Signal {
 constructor(self,peer,connection,use){Object.assign(this,{self,peer,connection,use:BigInt(use),generation:1n,next:1n,bootReady:false,challengeHash:new Uint8Array(),localSDP:false,remoteSDP:false,dtls:false,confirmed:false,closed:false,retired:false,ended:false,localHash:new Uint8Array(),remoteHash:new Uint8Array(),localFingerprint:new Uint8Array(),remoteFingerprint:new Uint8Array(),remoteUfrag:new Uint8Array(),remoteMid:new Uint8Array(),candidates:0,batches:0,seen:new Map(),pending:new Map(),cacheUntil:new Map(),gapDeadline:0,sdkActions:0,duplicates:0,conflicts:0,rejected:0,unsupported:0,chain:Promise.resolve()})}
 offerer(){return w.hex(this.self.id)<w.hex(this.peer.id)}
 ready(){return !this.closed&&this.localSDP&&this.remoteSDP&&this.dtls&&this.confirmed}
 async setLocal(raw,offer,generation){if(BigInt(generation)!==this.generation||this.closed||!this.bootReady||offer!==this.offerer()||(offer&&this.localSDP)||(!offer&&!this.remoteSDP))throw Error('local role/state');const info=inspectSDP(raw);const digest=await w.hash(raw);if(BigInt(generation)!==this.generation)throw Error("stale SDK callback");this.localSDP=true;this.localHash=digest;this.localFingerprint=info.fingerprint}
 nextGeneration(gen){gen=BigInt(gen);if((this.closed&&!this.retired)||gen!==this.generation+1n||gen===0n)throw Error('generation');this.generation=gen;this.next=1n;this.closed=this.retired=false;this.localSDP=this.remoteSDP=this.dtls=this.confirmed=this.ended=false;this.pending.clear();this.gapDeadline=0;this.candidates=this.batches=0}
 receive(raw,now){const job=this.chain.then(async()=>{try{return await this.accept(await w.open(raw,this.peer,this.self,now),now)}catch(e){this.rejected++;throw e}});this.chain=job.catch(()=>{});return job}
 async accept(o,now){const c=o.context;if(!w.equal(c.connection,this.connection)||!w.equal(c.from,this.peer.id))throw Error('binding');const key=w.cacheKey(c);
  if(this.seen.has(key)){if(!w.equal(this.seen.get(key),o.hash)){this.conflicts++;throw Error('conflict')}this.duplicates++;return []}
  if(this.seen.size>=2048)throw Error('cache full');const remember=msg=>{const key=w.cacheKey(msg.context);this.seen.set(key,msg.hash);this.cacheUntil.set(key,Number(msg.context.deadline)+300000)};
  if(c.cls===2n){if(!this.bootReady&&o.type===4&&this.challengeHash.length){const f=w.parse(o.payload,5);if(f.size!==5||f.get(4)?.value!==1n||f.get(5)?.value!==1n||!w.equal(f.get(6)?.value??[],this.challengeHash)||f.get(7)?.value!==0n||f.get(8)?.value!==this.use)throw Error("challenge ACK");this.bootReady=true}remember(o);if(o.type!==4&&o.type!==10){this.unsupported++;return [{cls:2,type:10,payload:w.pb(w.u(1,c.cls),w.u(2,o.type),w.u(3,c.seq),w.b(4,o.hash)),generation:c.generation}]};return [{cls:2,type:o.type,payload:o.payload,generation:c.generation}]}
  if(o.type===1||o.type===2){if(w.text(c.domain)!=='bootstrap')throw Error('domain');const f=w.parse(o.payload,7);w.need(f,o.type===1?'bbb':'bbbb');if(!w.equal(f.get(1).value,c.connection)||!w.equal(f.get(2).value,c.senderInstance)||!w.equal(f.get(3).value,c.senderBoot)||(o.type===2&&!w.equal(f.get(4).value,this.self.boot)))throw Error('bootstrap binding');if(!this.localSDP&&!this.remoteSDP){this.peer={...this.peer,instance:c.senderInstance,boot:c.senderBoot}}remember(o);if(o.type===2)this.bootReady=true;return [{cls:1,type:o.type,payload:o.payload,generation:0n}]}
  if((this.closed&&!(this.retired&&c.generation===this.generation+1n))||!this.bootReady||c.use!==this.use||c.generation<this.generation||c.generation>this.generation+1n)throw Error('scope');
  if(c.generation===this.generation+1n){if(this.offerer())throw Error('generation role');this.nextGeneration(c.generation)}
  if(c.seq<this.next)throw Error('old seq');if(c.seq>this.next){if(this.pending.size>=32)throw Error('gap full');remember(o);this.pending.set(c.seq,o);if(!this.gapDeadline)this.gapDeadline=now+2500;return []}
  const actions=[];for(;;){remember(o);const action=await this.apply(o);this.next++;if(action.type)actions.push(action);if(!this.pending.has(this.next))break;o=this.pending.get(this.next);this.pending.delete(this.next)}if(!this.pending.size)this.gapDeadline=0;return actions;
 }
 async apply(o){let action={cls:1,type:o.type,payload:o.payload,generation:o.context.generation};
  switch(o.type){
   case 3:case 4:{if(o.type===3?(this.offerer()||this.remoteSDP):(!this.offerer()||!this.localSDP||this.remoteSDP))throw Error('SDP role');const info=await readSDPBody(o.payload);this.remoteSDP=true;this.remoteHash=await w.hash(info.raw);this.remoteFingerprint=info.fingerprint;this.remoteUfrag=info.ufrag;this.remoteMid=info.mid;this.sdkActions++;action.payload=info.raw;break}
   case 5:{if(!this.remoteSDP||this.ended)throw Error('candidate state');const {list,index}=readCandidates(o.payload);if(index!==this.batches||!list.length||this.batches===4||this.candidates+list.length>64)throw Error('candidate cap');for(const c of list)if(c.generation!==this.generation||!w.equal(c.ufrag,this.remoteUfrag)||!w.equal(c.mid,this.remoteMid)||c.index!==0n)throw Error('candidate binding');this.candidates+=list.length;this.batches++;this.sdkActions+=list.length;break}
   case 6:{if(!this.remoteSDP||this.ended)throw Error('end state');const f=w.parse(o.payload,3);w.need(f,'buu');if(!w.equal(f.get(1).value,this.remoteUfrag)||f.get(2).value!==BigInt(this.candidates)||f.get(3).value!==BigInt(this.batches))throw Error('end counts');this.sdkActions++;this.ended=true;break}
   case 7:if(!this.localSDP||!this.remoteSDP||!w.equal(o.payload,this.confirmBody()))throw Error('confirm');this.confirmed=true;break;
   case 8:if(!this.offerer())throw Error('restart role');break;
   case 9:this.closed=true;this.pending.clear();this.gapDeadline=0;break;
   default:this.unsupported++;action={cls:2,type:10,payload:w.pb(w.u(1,o.context.cls),w.u(2,o.type),w.u(3,o.context.seq),w.b(4,o.hash)),generation:o.context.generation};
  }return action;
 }
 confirmBody(){let oh=this.localHash,ah=this.remoteHash,of=this.localFingerprint,af=this.remoteFingerprint;if(!this.offerer())[oh,ah,of,af]=[ah,oh,af,of];return w.pb(w.b(1,oh),w.b(2,ah),w.b(3,of),w.b(4,af))}
 tick(now){for(const [key,until]of this.cacheUntil)if(now>until){this.cacheUntil.delete(key);this.seen.delete(key)}if(this.gapDeadline&&now>=this.gapDeadline){this.closed=this.retired=true;this.pending.clear();this.gapDeadline=0;return true}return false}
}
