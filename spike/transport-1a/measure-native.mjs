import fs from 'node:fs/promises';
import {capacity} from './sender-check.mjs';
if(!process.env.REMOTE_AGENT_HOME?.startsWith('/tmp/'))throw Error('isolation');
console.log(JSON.stringify(await capacity(await fs.readFile(process.argv[2],'utf8')),null,2));
