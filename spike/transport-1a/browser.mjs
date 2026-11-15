import fs from 'node:fs/promises';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {validateSDP} from './sdp.mjs';

if (!process.env.REMOTE_AGENT_HOME?.startsWith('/tmp/')) throw Error('isolated state required');
const [modulePath, output] = process.argv.slice(2);
const {chromium, webkit} = await import(pathToFileURL(modulePath));
await fs.mkdir(output,{recursive:true});
for(const [name, type, options] of [
  ['chrome',chromium,{channel:'chrome',headless:true}],
  ['webkit',webkit,{headless:true}],
]) {
  const browser=await type.launch(options);
  try {
    const page=await browser.newPage();
    const result=await page.evaluate(async (validatorSource)=>{
      const validate=(new Function(`return (${validatorSource})`))();
      const check=sdp=>validate(new TextEncoder().encode(sdp));
      const started=performance.now(), peers=[], received=[], candidates=[[],[]];
      let unexpected=0;
      const timeout=p=>new Promise((resolve,reject)=>{
        const id=setTimeout(()=>reject(Error('SDK deadline')),10000);
        p.then(v=>{clearTimeout(id);resolve(v)},e=>{clearTimeout(id);reject(e)});
      });
      try {
        for(let side=0;side<2;side++) {
          const pc=new RTCPeerConnection({iceServers:[]});peers.push(pc);
          pc.ondatachannel=ev=>{unexpected++;ev.channel.close()};
          pc.onicecandidate=ev=>{if(ev.candidate)candidates[side].push(ev.candidate.toJSON())};
          const channels=[pc.createDataChannel('lab-0',{negotiated:true,id:0,ordered:true}),
                          pc.createDataChannel('lab-1',{negotiated:true,id:1,ordered:false,maxRetransmits:0})];
          received.push(channels);
        }
        const gathering=pc=>timeout(new Promise(resolve=>{pc.onicegatheringstatechange=()=>{if(pc.iceGatheringState==='complete')resolve()}}));
        const open=timeout(Promise.all(received.flat().map(dc=>new Promise(resolve=>dc.onopen=resolve))));
        const message=timeout(new Promise(resolve=>received[1][0].onmessage=e=>{if(e.data==='lab-message')resolve()}));
        const offer=await peers[0].createOffer();check(offer.sdp);
        const ga=gathering(peers[0]);await peers[0].setLocalDescription(offer);await ga;
        check(offer.sdp);await peers[1].setRemoteDescription(offer);
        const answer=await peers[1].createAnswer();check(answer.sdp);
        const gb=gathering(peers[1]);await peers[1].setLocalDescription(answer);await gb;
        check(answer.sdp);await peers[0].setRemoteDescription(answer);
        const exactBeforeCandidates=peers[0].remoteDescription.sdp===answer.sdp && peers[1].remoteDescription.sdp===offer.sdp;
        for(let side=0;side<2;side++) {
          const info=check(side===0?offer.sdp:answer.sdp);
          if(candidates[side].length>64)throw Error('candidate cap');
          for(const c of candidates[side]) {
            if(c.usernameFragment && c.usernameFragment!==info.ufrag)throw Error('candidate ufrag');
            await peers[1-side].addIceCandidate(c);
          }
          await peers[1-side].addIceCandidate(null);
        }
        await open;received[0][0].send('lab-message');await message;
        const restart=await peers[0].createOffer({iceRestart:true});check(restart.sdp);
        if(check(restart.sdp).ufrag===check(offer.sdp).ufrag)throw Error('restart ufrag');
        return {offer:offer.sdp,answer:answer.sdp,restart:restart.sdp,
          elapsed_ms:performance.now()-started,candidates:candidates.map(c=>c.length),
          dcep_callbacks:unexpected,message:true,negotiated_ids:[0,1],
          remote_description_matches_before_candidates:exactBeforeCandidates,
          validated_original_passed_to_sdk:true,
          max_message_size:peers[0].sctp.maxMessageSize,user_agent:navigator.userAgent};
      } finally { peers.forEach(pc=>pc.close()); }
    },validateSDP.toString());
    for(const kind of ['offer','answer','restart']) {
      await fs.writeFile(path.join(output,`${name}-${kind}.sdp`),result[kind]);delete result[kind];
    }
    result.version=browser.version();
    await fs.writeFile(path.join(output,`${name}.json`),JSON.stringify(result,null,2));
    console.log(JSON.stringify({name,...result}));
  } finally { await browser.close(); }
}
