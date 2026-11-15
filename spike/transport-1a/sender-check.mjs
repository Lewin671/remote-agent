import * as w from './wire.mjs';
import {Sender,CandidateBatcher} from './sender.mjs';
export async function senderCheck(rows) {
 const check=x=>{if(!x)throw Error('sender vector')},s=new Sender(10000),raws=rows.slice(0,33);
 for(const raw of raws.slice(0,32))check(await s.enqueue(raw,0,10000,1));check(!await s.enqueue(raws[32],0,10000,1));
 check((await s.tick(0)).length===32);check((await s.tick(499)).length===0);check((await s.tick(500)).length===32);check((await s.tick(1500)).length===32);check((await s.tick(2000)).length===0);
 for(const raw of raws.slice(0,32))check(s.ack(await w.hash(raw)));check(await s.enqueue(raws[32],2000,10000,1));check((await s.tick(2000)).length===1);
 s.limited(await w.hash(raws[32]),2000,9000);check((await s.tick(9999)).length===0);check((await s.tick(10000)).length===0&&s.timedOut&&s.pending.size===0);
 check(s.backoff(10000,.5)===10500);for(let n=1;n<=1000;n++)s.networkChanged(n);check(s.retryAt===10500);check(s.backoff(10500,.5)===11500);
 const p=new Sender(10000);await p.enqueue(raws[0],0,10000,1);check((await p.tick(0)).length===1);p.limited(await w.hash(raws[0]),0,800);check((await p.tick(500)).length===0);check((await p.tick(800)).length===1);check((await p.tick(1500)).length===1);p.limited(await w.hash(raws[0]),10001,5);check((await p.tick(10001)).length===0);
 const cb=new CandidateBatcher(2,10000,1);check(!cb.add(Uint8Array.of(1),1,0,1));for(let i=0;i<64;i++)check(cb.add(Uint8Array.of(i),2,0,1));check(!cb.add(Uint8Array.of(65),2,0,1));check(cb.flush(49,false,2,1).length===0);check(cb.flush(50,false,2,1).length===4);check(cb.flush(51,true,2,1).length===0);check(!cb.add(Uint8Array.of(1),2,52,1));
 return {sender_cases:7,window_peak:32,retry_points_ms:[0,500,1500],relay_retained:true};
}
export async function capacity(source) {
 const start=performance.now(),rows=source.trim().split('\n').map(x=>x.split('\t')),packets=[],identities=new Map();
 for(const p of rows){for(const i of [0,2])if(!identities.has(p[i]))identities.set(p[i],await w.fixture(p[i],Number(p[i+1])));const a=identities.get(p[0]),z=identities.get(p[2]),raw=w.unhex(p[4]),o=await w.open(raw,a,z,1700000000000);const encoded=await w.seal(a,z,o.context,o.type,o.payload);if(encoded.length!==raw.length)throw Error('length');await w.open(encoded,a,z,1700000000000);packets.push(encoded)}
 const metrics={initial_frames:packets.length,initial_bytes:packets.reduce((n,p)=>n+p.length,0),transmitted_frames:0,transmitted_bytes:0};
 for(let n=0;n<packets.length;n+=32){const s=new Sender(10000);for(const raw of packets.slice(n,n+32))await s.enqueue(raw,0,10000,1);for(const at of [0,500,1500])await s.tick(at);metrics.transmitted_frames+=s.frames;metrics.transmitted_bytes+=s.bytes;for(const raw of packets.slice(n,n+32))s.ack(await w.hash(raw));if(s.pending.size)throw Error('leak')}
 return {...metrics,...await senderCheck(packets),codec_wall_ms:performance.now()-start,evidence:'both directions; native real crypto; virtual retry clock; no SDK'};
}
