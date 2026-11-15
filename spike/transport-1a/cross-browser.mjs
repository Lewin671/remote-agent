import fs from 'node:fs/promises';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {validateSDP} from './sdp.mjs';
if(!process.env.REMOTE_AGENT_HOME?.startsWith('/tmp/'))throw Error('isolation');
const [modulePath,configPath,outPath,corpusPath,tracePath]=process.argv.slice(2);
const {chromium,webkit}=await import(pathToFileURL(modulePath));const config=JSON.parse(await fs.readFile(configPath,'utf8'));const results=[];
for(const [engine,driver,options]of [['chrome',chromium,{channel:'chrome',headless:true}],['webkit',webkit,{headless:true}]]){
 const browser=await driver.launch(options);
 try{const page=await browser.newPage();
  if(corpusPath){const allowed=new Set(['wire.mjs','sender.mjs','sender-check.mjs','signal.mjs','sdp.mjs','signal-replay.mjs']);await page.route('**/lab/*.mjs',async route=>{const name=new URL(route.request().url()).pathname.split('/').pop();if(!allowed.has(name))return route.abort();await route.fulfill({contentType:'text/javascript',body:await fs.readFile(new URL(name,import.meta.url))})})}
  await page.goto(config.url);
  if(corpusPath){const corpus=await fs.readFile(corpusPath,'utf8'),trace=await fs.readFile(tracePath,'utf8');const codec=await page.evaluate(async ({url,corpus,trace})=>{const c=await import(url+'/lab/sender-check.mjs'),s=await import(url+'/lab/signal-replay.mjs');return {...await c.capacity(corpus),...await s.replay(trace)}},{url:config.url,corpus,trace});results.push({engine,mode:'native-webcrypto-and-signal',...codec})}
  for(const mode of ['client-offer','peer-offer','dcep','undeclared']){
   const result=await page.evaluate(async ({config,mode,id,validator})=>{
    const validate=(new Function(`return (${validator})`))(),encode=s=>new TextEncoder().encode(s),decode=b=>new TextDecoder('utf-8',{fatal:true}).decode(b);
    const b64=s=>btoa(String.fromCharCode(...encode(s))),unb64=s=>decode(Uint8Array.from(atob(s),c=>c.charCodeAt(0)));
    const call=async (operation,extra={},expectError=false)=>{const response=await fetch(config.url,{method:'POST',headers:{Authorization:'Bearer '+config.token,'Content-Type':'application/json'},body:JSON.stringify({id,operation,...extra})});if(expectError){if(response.ok)throw Error('bad candidate accepted');return true}if(!response.ok)throw Error('peer fixture '+operation);return response.json()};
    const started=performance.now(),pc=new RTCPeerConnection({iceServers:[]});let extra=0,business=0,rogue=0,closed=false,normalizedGetter=0;
    let collected=[],finishGather;pc.onicecandidate=e=>{if(e.candidate)collected.push(e.candidate.toJSON());else finishGather?.()};
    let waitExtra;pc.ondatachannel=e=>{extra++;e.channel.close();pc.close();closed=true;waitExtra?.()};
    const channels=[pc.createDataChannel('lab-0',{negotiated:true,id:0,ordered:true}),pc.createDataChannel('lab-1',{negotiated:true,id:1,ordered:false,maxRetransmits:0})];
    const timeout=p=>new Promise((resolve,reject)=>{const t=setTimeout(()=>reject(Error('SDK deadline')),10000);p.then(x=>{clearTimeout(t);resolve(x)},e=>{clearTimeout(t);reject(e)})});
    const opened=timeout(Promise.all(channels.map(dc=>new Promise(resolve=>dc.onopen=resolve))));
    const messages=new Map();channels.forEach(dc=>dc.onmessage=e=>{if(e.data==='forbidden-business')rogue++;if(dc.id===0)business++;messages.get(e.data)?.()});
    const echo=async (dc,value)=>{const promise=timeout(new Promise(resolve=>messages.set(value,resolve)));dc.send(value);await promise;messages.delete(value)};
    const probe=async gen=>{const value='probe-gen:'+gen+':'+crypto.randomUUID();return await new Promise((resolve,reject)=>{let timer;const end=()=>{clearInterval(timer);clearTimeout(deadline);messages.delete(value);resolve(true)};messages.set(value,end);const deadline=setTimeout(()=>{clearInterval(timer);messages.delete(value);resolve(false)},2500);const send=()=>{if(channels[1].readyState==='open')channels[1].send(value)};timer=setInterval(send,50);send()})};
    const gather=async description=>{collected=[];const promise=timeout(new Promise(resolve=>finishGather=resolve));await pc.setLocalDescription(description);await promise;return collected.slice()};
    const install=async (type,sdp)=>{const raw=encode(sdp),checked=validate(raw);if(!raw.every((b,i)=>encode(checked.raw)[i]===b)||encode(checked.raw).length!==raw.length)throw Error('application changed SDP');await pc.setRemoteDescription({type,sdp});if(pc.remoteDescription.sdp!==sdp)normalizedGetter++};
    const add=async list=>{for(const c of list)await pc.addIceCandidate(c);await pc.addIceCandidate(null)};
    let local,remote,localCandidates,gen=1;
    try{
     if(mode==='client-offer'){
      local=await pc.createOffer();validate(encode(local.sdp));localCandidates=await gather(local);
      remote=await call('answer-offer',{sdp:b64(local.sdp)});await install('answer',unb64(remote.sdp));
     }else{
      remote=await call('offer');await install('offer',unb64(remote.sdp));local=await pc.createAnswer();validate(encode(local.sdp));localCandidates=await gather(local);await call('install-answer',{sdp:b64(local.sdp),generation:gen});
     }
     const ufrag=validate(encode(local.sdp)).ufrag;
     const before=await call('stats');await call('candidates',{generation:gen+1,ufrag,candidates:localCandidates},true);await call('candidates',{generation:gen,ufrag:'wrong',candidates:localCandidates},true);const after=await call('stats');if(after.add_calls!==before.add_calls)throw Error('negative candidate called SDK');
     await call('candidates',{generation:gen,ufrag,candidates:localCandidates});await add(remote.candidates);await opened;await echo(channels[0],'cross-sdk-'+id);await echo(channels[1],'probe:'+id);
     const stats0=await call('stats');channels[1].send('business-on-probe');await echo(channels[0],'barrier-'+id);
     // This is an event-based bounded observation, not a polling loop.
     await new Promise(resolve=>setTimeout(resolve,100));const stats1=await call('stats');if(stats1.probe_business_rejected!==stats0.probe_business_rejected+1)throw Error('probe delivered business');
     let restarted=false,restartReadyMs=null,freshProbeWithin2500ms=null;
     if(mode==='peer-offer'){
      const restartStart=performance.now(),old=ufrag;remote=await call('restart-offer');gen=remote.generation;await install('offer',unb64(remote.sdp));local=await pc.createAnswer();localCandidates=await gather(local);const current=validate(encode(local.sdp)).ufrag;if(current===old)throw Error('restart credentials unchanged');await call('install-answer',{sdp:b64(local.sdp),generation:gen});await call('candidates',{generation:gen,ufrag:current,candidates:localCandidates});await add(remote.candidates);freshProbeWithin2500ms=await probe(gen);await echo(channels[0],'restart-'+id);restartReadyMs=performance.now()-restartStart;restarted=true;
     }
     if(mode==='dcep'){const observed=timeout(new Promise(resolve=>waitExtra=resolve));await call('rogue-dcep');await observed;if(extra!==1||!closed||rogue)throw Error('extra DCEP not rejected')}
     if(mode==='undeclared'){await call('rogue-negotiated');await echo(channels[0],'undeclared-barrier-'+id);await new Promise(resolve=>setTimeout(resolve,150));if(rogue||extra)throw Error('undeclared stream reached app')}
     return {mode,restart_ready_ms:restartReadyMs,fresh_probe_within_2500ms:freshProbeWithin2500ms,elapsed_ms:performance.now()-started,received:business,extra_dcep_rejected:extra,undeclared_business_received:rogue,restarted,sdk_getter_normalizations:normalizedGetter,original_passed_to_sdk:true,candidate_negative_before_sdk:true,probe_business_rejected:true};
    }finally{pc.close();await call('close')}
   },{config,mode,id:engine+'-'+mode,validator:validateSDP.toString()});results.push({engine,version:browser.version(),...result});console.log(JSON.stringify(results.at(-1)));
  }
 }finally{await browser.close()}
}
await fs.writeFile(outPath,JSON.stringify(results,null,2));
