import {hash,hex,equal} from './wire.mjs';
export class Sender {
 constructor(deadline){Object.assign(this,{deadline,pending:new Map(),timedOut:false,frames:0,bytes:0,retryAt:0,networkGeneration:0,failures:0,binding:1,clock:0,chain:Promise.resolve()})}
 enqueue(raw,now,expiry,binding){this.clock=Math.max(this.clock,now);const frozen=raw.slice();const job=this.chain.then(()=>this.enqueueNow(frozen,this.clock,expiry,binding));this.chain=job.catch(()=>{});return job}
 async enqueueNow(raw,now,expiry,binding){if(this.timedOut||binding!==this.binding||now>=expiry||now>=this.deadline||this.pending.size>=32)return false;const digest=await hash(raw),key=hex(digest);if(this.timedOut||this.clock>=expiry||this.clock>=this.deadline||binding!==this.binding||this.pending.size>=32)return false;if(this.pending.has(key))return true;this.pending.set(key,{raw:raw.slice(),hash:digest,first:now,notBefore:0,attempts:0,expiry:Math.min(expiry,this.deadline),binding});return true}
 tick(now){this.clock=Math.max(this.clock,now);const job=this.chain.then(()=>this.tickNow(this.clock));this.chain=job.catch(()=>{});return job}
 async tickNow(now){if(now>=this.deadline){this.pending.clear();this.timedOut=true;return []}const out=[];for(const [key,p] of this.pending){if(now>=p.expiry||p.binding!==this.binding){this.pending.delete(key);continue}if(p.attempts===3)continue;const due=Math.max(p.first+[0,500,1500][p.attempts],p.notBefore);if(now>=due){if(!equal(await hash(p.raw),p.hash))throw Error('mutable frame');out.push({key,p})}}const live=out.filter(({key,p})=>!this.timedOut&&this.pending.get(key)===p&&p.binding===this.binding&&this.clock<p.expiry&&this.clock<this.deadline);return live.map(({p})=>{p.attempts++;this.frames++;this.bytes+=p.raw.length;return p.raw.slice()})}
 async admit(raw,now){this.clock=Math.max(this.clock,now);const p=this.pending.get(hex(await hash(raw)));return !!p&&p.binding===this.binding&&this.clock<p.expiry&&this.clock<this.deadline&&!this.timedOut}
 ack(digest){return this.pending.delete(hex(digest))}
 limited(digest,now,delay){const p=this.pending.get(hex(digest));if(p)p.notBefore=Math.max(p.notBefore,Math.min(this.deadline,now+delay))}
 backoff(now,fraction){const cap=Math.min(30000,1000*2**this.failures++);this.retryAt=now+Math.floor(cap*fraction);return this.retryAt}
 rebind(binding){this.binding=binding;this.pending.clear()}
 networkChanged(g){this.networkGeneration=Math.max(this.networkGeneration,g)}
}

export class CandidateBatcher {
 constructor(generation,deadline,binding){Object.assign(this,{generation,deadline,binding,due:0,pending:[],count:0,batches:0,ended:false})}
 add(raw,g,now,binding){if(now>=this.deadline){this.ended=true;this.pending=[];this.due=0}if(binding!==this.binding||this.ended||g!==this.generation||this.count>=64||raw.length>1024)return false;if(!this.pending.length)this.due=now+50;this.pending.push(raw.slice());this.count++;return true}
 flush(now,end,g,binding){if(now>=this.deadline||g!==this.generation||binding!==this.binding){this.ended=true;this.pending=[];this.due=0;return []}if(end)this.ended=true;if(!end&&now<this.due)return [];const out=[];while(this.pending.length>=16||(end&&this.pending.length)){out.push(this.pending.splice(0,16));if(++this.batches>4)throw Error('batch cap')}this.due=this.pending.length?now+50:0;return out}
}
