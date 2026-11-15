import {hash,hex,equal} from './wire.mjs';
export class Sender {
 constructor(deadline){Object.assign(this,{deadline,pending:new Map(),timedOut:false,frames:0,bytes:0,retryAt:0,networkGeneration:0,failures:0})}
 async enqueue(raw,now){if(this.timedOut||now>=this.deadline||this.pending.size>=32)return false;const digest=await hash(raw),key=hex(digest);if(this.pending.has(key))return true;this.pending.set(key,{raw:raw.slice(),hash:digest,first:now,notBefore:0,attempts:0});return true}
 async tick(now){if(now>=this.deadline){this.pending.clear();this.timedOut=true;return []}const out=[];for(const p of this.pending.values()){if(p.attempts===3)continue;const due=Math.max(p.first+[0,500,1500][p.attempts],p.notBefore);if(now>=due){if(!equal(await hash(p.raw),p.hash))throw Error('mutable frame');out.push(p.raw);p.attempts++;this.frames++;this.bytes+=p.raw.length}}return out}
 ack(digest){return this.pending.delete(hex(digest))}
 limited(digest,now,delay){const p=this.pending.get(hex(digest));if(p)p.notBefore=Math.max(p.notBefore,Math.min(this.deadline,now+delay))}
 backoff(now,fraction){const cap=Math.min(30000,1000*2**this.failures++);this.retryAt=now+Math.floor(cap*fraction);return this.retryAt}
 networkChanged(g){this.networkGeneration=Math.max(this.networkGeneration,g)}
}

export class CandidateBatcher {
 constructor(generation){Object.assign(this,{generation,due:0,pending:[],count:0,batches:0,ended:false})}
 add(raw,g,now){if(this.ended||g!==this.generation||this.count>=64||raw.length>1024)return false;if(!this.pending.length)this.due=now+50;this.pending.push(raw.slice());this.count++;return true}
 flush(now,end=false){if(end)this.ended=true;if(!end&&now<this.due)return [];const out=[];while(this.pending.length>=16||(end&&this.pending.length)){out.push(this.pending.splice(0,16));if(++this.batches>4)throw Error('batch cap')}this.due=this.pending.length?now+50:0;return out}
}
