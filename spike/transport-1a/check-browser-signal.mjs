// Run the shared trace in real browser WebCrypto; serve only public lab modules.
import fs from 'node:fs/promises';
import http from 'node:http';
import {pathToFileURL} from 'node:url';
if(!process.env.REMOTE_AGENT_HOME?.startsWith('/tmp/'))throw Error('isolation');
const [modulePath,tracePath,outPath]=process.argv.slice(2);
const {chromium,webkit}=await import(pathToFileURL(modulePath));
const trace=await fs.readFile(tracePath,'utf8'),allowed=new Set(['wire.mjs','signal.mjs','sdp.mjs','sender.mjs','signal-replay.mjs']);
const server=http.createServer(async(req,res)=>{
 try{const name=req.url.slice(1);if(!name){res.setHeader('Content-Type','text/html');res.end('<!doctype html><title>Local signal lab</title>');return}
  if(!allowed.has(name)){res.writeHead(404);res.end();return}
  res.setHeader('Content-Type','text/javascript');res.end(await fs.readFile(new URL(name,import.meta.url)));
 }catch{res.writeHead(500);res.end()}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const url=`http://127.0.0.1:${server.address().port}`,results=[];
try{
 for(const [name,driver,options]of [['chrome',chromium,{channel:'chrome',headless:true}],['webkit',webkit,{headless:true}]]){
  const browser=await driver.launch(options);
  try{const page=await browser.newPage();await page.goto(url);const result=await page.evaluate(async trace=>{const {replay}=await import('/signal-replay.mjs');return replay(trace)},trace);results.push({runtime:name,version:browser.version(),...result});console.log(JSON.stringify(results.at(-1)))}finally{await browser.close()}
 }
 await fs.writeFile(outPath,JSON.stringify(results,null,2)+'\n');
}finally{await new Promise(resolve=>server.close(resolve))}
