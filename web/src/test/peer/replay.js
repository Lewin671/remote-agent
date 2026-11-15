import {normalize,knownPayload} from "../../transport/peer/schema.js";
import {CandidateBatcher,Sender} from "../../transport/peer/sender.js";
import * as w from "../../transport/peer/wire.js";
import {Signal} from "../../transport/peer/signal.js";
import {fixture,fixtureCodec} from "./fixture.js";
export async function replay(source){const codec=await fixtureCodec();let gates,batch,sender,queuedRaw,cases=0,assertions=0;const check=x=>{if(!x)throw Error('signal trace '+cases)};
 for(const row of source.trim().split('\n')){const p=row.split('\t'),n=Number(p[1]);switch(p[0]){
 case 'BODY':{let ok=true;try{knownPayload(Number(p[1]),Number(p[2]),w.unhex(p[3]))}catch{ok=false}check(ok===(p[4]==='1'));assertions++;break}
 case 'SCHEMA':{let result;try{result=w.hex(normalize(w.unhex(p[2]),p[1]))}catch{result='!'}check(result===p[3]);assertions++;break}
 case 'AUTH':gates[n].setAuthorization(1700000000000+Number(p[2]),p[3]==='1',Number(p[4]));break;
 case 'SEND_AUTH':sender.setAuthorization(Number(p[1]),p[2]==='1',Number(p[3]));break;
 case 'REPLY':check(await gates[n].replyAllowed(w.unhex(p[4]),1700000000000+Number(p[2]))===(p[3]==='1'));assertions++;break;
 case 'CASE':{const a=await fixture(p[2],Number(p[3])),b=await fixture(p[4],Number(p[5])),conn=(await w.hash(w.utf8('signal/'+p[1]))).slice(0,16);gates=[new Signal(codec,a,b,conn,Number(p[6]),1700000000000),new Signal(codec,b,a,conn,Number(p[6]),1700000000000)];cases++;break}
 case 'BATCH_CASE':batch=new CandidateBatcher(Number(p[2]),Number(p[3]),Number(p[4]));cases++;break;
 case 'BATCH_ADD':check(batch.add(w.unhex(p[3]),Number(p[2]),Number(p[1]),1)===(p[4]==='1'));assertions++;break;
 case 'BATCH_FLUSH':{const groups=batch.flush(Number(p[1]),p[2]==='1',2,1);check(groups.map(g=>g.length).join(',')===p[3]&&w.hex(w.concat(...groups.flat()))===p[4]);assertions++;break}
 case 'DISPATCH':check(await gates[n].dispatch(w.unhex(p[4]),1700000000000+Number(p[2]))===(p[3]==='1'));assertions++;break;
 case 'FORGET':gates[n].forgetPeer();break;
 case 'QUEUE':queuedRaw=w.unhex(p[2]);await codec.open(queuedRaw,gates[n].peer,gates[n].self,1700000000000);break;
 case 'DELIVER':{let ok=true;try{await gates[n].receive(queuedRaw,1700000000000+Number(p[2]))}catch{ok=false}check(ok===(p[3]==='1'));assertions++;break}
 case 'SENDER_CASE':sender=new Sender(Number(p[2]));cases++;break;
 case 'SEND_ADD':check(await sender.enqueue(w.unhex(p[4]),Number(p[1]),Number(p[2]),Number(p[3]))===(p[5]==='1'));assertions++;break;
 case 'SEND_TICK':check((await sender.tick(Number(p[1]))).length===Number(p[2]));assertions++;break;
 case 'SEND_REBIND':sender.rebind(Number(p[1]));break;
 case 'SEND_LIMIT':sender.limited(await w.hash(w.unhex(p[3])),Number(p[1]),Number(p[2]));break;
 case 'SEND_ADMIT':check(await sender.admit(w.unhex(p[2]),Number(p[1]))===(p[3]==='1'));assertions++;break;
 case 'SEND_PENDING':check(sender.pending.size===Number(p[1]));assertions++;break;
 case 'BATCH_BOUND_FLUSH':check(batch.flush(Number(p[1]),p[2]==='1',Number(p[3]),Number(p[4])).length===0&&batch.pending.length===0);assertions++;break;
 case 'POLICY':{let ok=true;try{w.validatePolicyReply(await codec.open(w.unhex(p[3]),gates[0].self,gates[1].self,1700000000000),await codec.open(w.unhex(p[4]),gates[1].self,gates[0].self,1700000000000),1700000000000+Number(p[5]))}catch{ok=false}check(ok===(p[2]==='1'));assertions++;break}
 case 'RECORD':{const which=Number(p[2]);gates[which].tick(1700000000000+Number(p[3]));const sent=await codec.open(w.unhex(p[4]),gates[which].self,gates[1-which].self,1700000000000);let ok=true;try{if(p[1]==='probe')await gates[which].rememberProbe(sent);else await gates[which].rememberChallenge(sent)}catch{ok=false}check(ok===(p[5]==='1'));assertions++;break}
 case 'PROBE':await gates[n].rememberProbe(await codec.open(w.unhex(p[2]),gates[n].self,gates[1-n].self,1700000000000));break;
 case 'CHALLENGE':await gates[n].rememberChallenge(await codec.open(w.unhex(p[2]),gates[n].self,gates[1-n].self,1700000000000));break;
 case 'RX':{let ok=true;try{await gates[n].receive(w.unhex(p[4]),1700000000000+Number(p[2]))}catch{ok=false}check(ok===(p[3]==='1'));assertions++;break}
 case 'LOCAL':{let ok=true;try{await gates[n].setLocal(w.unhex(p[5]),p[3]==='1',Number(p[2]))}catch{ok=false}check(ok===(p[4]==='1'));assertions++;break}
 case 'DTLS':gates[n].dtls=true;break;
 case 'NEXT':gates[n].nextGeneration(Number(p[2]));break;
 case 'TICK':gates[n].tick(1700000000000+Number(p[2]));break;
 case 'EXPECT':{const s=gates[n],actual={seen:s.seen.size,sdk:s.sdkActions,duplicates:s.duplicates,conflicts:s.conflicts,unsupported:s.unsupported,pending:s.pending.size,ready:Number(s.ready()),closed:Number(s.closed),generation:Number(s.generation),next:Number(s.next),stale:s.staleChallenges,boot:Number(s.bootReady),gap_ms:s.gapDeadline?s.gapDeadline-1700000000000:0,peer_known:Number(s.peer.boot.length===32),dispatches:s.dispatches};check(actual[p[2]]===Number(p[3]));assertions++;break}
 default:throw Error('trace operation');
 }}return {signal_scenarios:cases,assertions};
}
