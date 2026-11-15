import fs from 'node:fs/promises';
import {browserModules} from './browser-modules.mjs';
const seen=new Set();
async function visit(name){
 if(seen.has(name))return;
 if(!browserModules.has(name))throw Error('Unserved browser module: '+name);
 seen.add(name);const source=await fs.readFile(new URL(name,import.meta.url),'utf8');
 for(const match of source.matchAll(/(?:\bfrom\s*|\bimport\s*(?:\(\s*)?)["']\.\/([^"']+)["']/g))await visit(match[1]);
}
for(const entry of ['sender-check.mjs','signal-replay.mjs','sdp.mjs'])await visit(entry);
if(seen.size!==browserModules.size)throw Error('Unused allowlist entries');
console.log('Browser module graph: '+seen.size+' public modules');
