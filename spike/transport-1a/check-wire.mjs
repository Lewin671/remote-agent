import fs from 'node:fs/promises';
import {fixture,open,unhex,hex} from './wire.mjs';
if(!process.env.REMOTE_AGENT_HOME?.startsWith('/tmp/'))throw Error('isolation');
const doc=JSON.parse(await fs.readFile(process.argv[2],'utf8'));
const from=await fixture(doc.sender,1),to=await fixture(doc.receiver,2);
for(const row of doc.rows){let valid,result;
 try {result=await open(unhex(row.packet_hex),from,to,doc.now_ms);valid=true;}catch {valid=false;}
 if(valid!==row.accept)throw Error(row.name);
 if(valid&&(result.type!==row.type||hex(result.payload)!==row.payload_hex))throw Error(row.name+' payload');
}
console.log(`JavaScript real-crypto vectors: ${doc.rows.length} passed`);
