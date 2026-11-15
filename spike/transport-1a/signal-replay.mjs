import {CandidateBatcher} from './sender.mjs';
import * as w from './wire.mjs';
import {Signal} from './signal.mjs';
export async function replay(source){let gates,batch,cases=0,assertions=0;const check=x=>{if(!x)throw Error('signal trace '+cases)};
 for(const row of source.trim().split('\n')){const p=row.split('\t'),n=Number(p[1]);switch(p[0]){
 case 'CASE':{const a=await w.fixture(p[2],Number(p[3])),b=await w.fixture(p[4],Number(p[5])),conn=(await w.hash(w.utf8('signal/'+p[1]))).slice(0,16);gates=[new Signal(a,b,conn,Number(p[6])),new Signal(b,a,conn,Number(p[6]))];cases++;break}
 case 'BATCH_CASE':batch=new CandidateBatcher(Number(p[2]));cases++;break;
 case 'BATCH_ADD':check(batch.add(w.unhex(p[3]),Number(p[2]),Number(p[1]))===(p[4]==='1'));assertions++;break;
 case 'BATCH_FLUSH':{const groups=batch.flush(Number(p[1]),p[2]==='1');check(groups.map(g=>g.length).join(',')===p[3]&&w.hex(w.concat(...groups.flat()))===p[4]);assertions++;break}
 case 'POLICY':{let ok=true;try{w.validatePolicyReply(await w.open(w.unhex(p[3]),gates[0].self,gates[1].self,1700000000000),await w.open(w.unhex(p[4]),gates[1].self,gates[0].self,1700000000000))}catch{ok=false}check(ok===(p[2]==='1'));assertions++;break}
 case 'CHALLENGE':gates[n].rememberChallenge(await w.open(w.unhex(p[2]),gates[n].self,gates[1-n].self,1700000000000));break;
 case 'RX':{let ok=true;try{await gates[n].receive(w.unhex(p[4]),1700000000000+Number(p[2]))}catch{ok=false}check(ok===(p[3]==='1'));assertions++;break}
 case 'LOCAL':{let ok=true;try{await gates[n].setLocal(w.unhex(p[5]),p[3]==='1',Number(p[2]))}catch{ok=false}check(ok===(p[4]==='1'));assertions++;break}
 case 'DTLS':gates[n].dtls=true;break;
 case 'NEXT':gates[n].nextGeneration(Number(p[2]));break;
 case 'TICK':gates[n].tick(1700000000000+Number(p[2]));break;
 case 'EXPECT':{const s=gates[n],actual={sdk:s.sdkActions,duplicates:s.duplicates,conflicts:s.conflicts,unsupported:s.unsupported,pending:s.pending.size,ready:Number(s.ready()),closed:Number(s.closed),generation:Number(s.generation),next:Number(s.next),stale:s.staleChallenges,boot:Number(s.bootReady),gap_ms:s.gapDeadline?s.gapDeadline-1700000000000:0};check(actual[p[2]]===Number(p[3]));assertions++;break}
 default:throw Error('trace operation');
 }}return {signal_scenarios:cases,assertions};
}
