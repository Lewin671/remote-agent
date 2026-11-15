import fs from 'node:fs/promises';
import path from 'node:path';
import {validateSDP} from './sdp.mjs';
if(!process.env.REMOTE_AGENT_HOME?.startsWith('/tmp/'))throw Error('isolated state required');
const root=process.argv[2];let count=0;
for(const row of (await fs.readFile(path.join(root,'vectors.tsv'),'utf8')).trim().split('\n')) {
  const [file,expected]=row.split('\t');let valid;
  try {validateSDP(await fs.readFile(path.join(root,file)));valid=true;}catch{valid=false;}
  if(valid!==(expected==='1'))throw Error(file);count++;
}
console.log(`JavaScript SDP vectors: ${count} passed`);
