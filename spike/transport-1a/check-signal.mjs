import fs from 'node:fs/promises';
import {replay} from './signal-replay.mjs';
if(!process.env.REMOTE_AGENT_HOME?.startsWith('/tmp/'))throw Error('isolation');
console.log(JSON.stringify(await replay(await fs.readFile(process.argv[2],'utf8'))));
