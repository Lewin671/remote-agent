import {relations} from './schema-relations.js';
import bounds from './schema-bounds.js';
import rules from './schema-data.js';
// Independent of the crypto module so defaults can be validated before effects.
const vint=n=>{n=BigInt(n);const out=[];do{let v=Number(n&127n);n>>=7n;if(n)v|=128;out.push(v)}while(n);return Uint8Array.from(out)};
const concat=(...parts)=>{const r=new Uint8Array(parts.reduce((n,p)=>n+p.length,0));let at=0;for(const p of parts){r.set(p,at);at+=p.length}return r};
const equal=(a,b)=>a.length===b.length&&a.every((v,i)=>v===b[i]);
const cap=(name,n)=>name==='Candidates'&&n===1?16:name==='Restart'&&n===1?2:name==='Capabilities'?64:128;
const max=k=>k==='bool'?1n:k==='u32'?0xffffffffn:k==='i64'?0x7fffffffffffffffn:k.startsWith('e')?BigInt(k.slice(1)):0xffffffffffffffffn;
export function normalize(raw,name,depth=0,shape=false){
 const defs=rules[name],limit=name==='Candidate'?1024:name==='LogicalFrame'?1048576:65536;if(!defs||depth>8||raw.length>limit)throw Error('schema size');
 const spec=new Map(defs.map(r=>[r[0],r])),values=new Map();let at=0,last=0;
 const read=()=>{let v=0n,start=at;for(let i=0;i<10;i++){if(at===raw.length)throw Error('short integer');const b=raw[at++];if(i===9&&b>1)throw Error('overflow');v|=BigInt(b&127)<<BigInt(i*7);if(!(b&128)){if(!equal(raw.slice(start,at),vint(v)))throw Error('nonminimal');return v}}throw Error('integer')};
 while(at<raw.length){const tag=read(),n=Number(tag>>3n),wire=Number(tag&7n),r=spec.get(n);if(!r||n<last||(!r[2]&&values.has(n)))throw Error('schema field');last=n;let list=values.get(n)??[];const kind=r[1],numeric=kind!=='b'&&!kind.startsWith('m');
  if(numeric&&wire===0){const value=read();if(value>max(kind))throw Error('range');list.push({n,value,bytes:false})}
  else if(wire===2){const size=read();if(size>BigInt(raw.length-at))throw Error('length');const body=raw.slice(at,at+Number(size));at+=Number(size);
   if(numeric){if(!r[2])throw Error('wire type');let p=0;while(p<body.length){if(list.length>=cap(name,n))throw Error('count');let v=0n,start=p,done=false;for(let i=0;i<10;i++){if(p===body.length)throw Error('packed');const b=body[p++];if(i===9&&b>1)throw Error('packed overflow');v|=BigInt(b&127)<<BigInt(i*7);if(!(b&128)){done=true;break}}if(!done||!equal(body.slice(start,p),vint(v))||v>max(kind))throw Error('packed integer');list.push({n,value:v,bytes:false})}}
   else{if(kind.startsWith('m'))normalize(body,kind.slice(1),depth+1,shape);list.push({n,value:body,bytes:true})}
  }else throw Error('wire type');if(list.length>cap(name,n))throw Error('count');values.set(n,list);
 }
 if(shape&&!relations(name,values))throw Error('body relation');
 const out=[];for(const [n,kind,repeated]of defs){let list=values.get(n)??[];if(!list.length&&!repeated&&!kind.startsWith('m'))list=[{n,value:kind==='b'?new Uint8Array():0n,bytes:kind==='b'}];for(const f of list){if(shape&&bounds[name]?.[n]){const [lo,hi]=bounds[name][n].map(BigInt),v=f.bytes?BigInt(f.value.length):f.value;if(v<lo||v>hi)throw Error('body bounds')};out.push(concat(vint(BigInt(n)*8n+(f.bytes?2n:0n)),...(f.bytes?[vint(f.value.length),f.value]:[vint(f.value)])))} }return concat(...out);
}
export const signalSchemas={1:'Probe',2:'Challenge',3:'SDP',4:'SDP',5:'Candidates',6:'EndCandidates',7:'Confirm',8:'Restart',9:'Close'};
export const controlSchemas={1:'Capabilities',2:'AttachPath',3:'InputStatus',4:'Ack',5:'Replay',6:'Barrier',7:'StreamFailed',8:'ClosePath',9:'DirectPolicy',10:'Unsupported',11:'InputStatusReply',12:'BarrierReply',13:'PolicyReply'};
export const knownPayload=(cls,type,raw)=>{const name=(Number(cls)===2?controlSchemas:signalSchemas)[type];return name?normalize(raw,name,0,true):raw};
export function restart(raw){raw=normalize(raw,'Restart');let at=0;const read=()=>{let v=0n,shift=0n;for(;;){const b=raw[at++];v|=BigInt(b&127)<<shift;if(!(b&128))return Number(v);shift+=7n}};const targets=new Map();while(raw[at]===10){at++;const size=read(),f=normalize(raw.slice(at,at+size),'RestartPC');at+=size;let i=0;const integer=()=>{let n=0n,s=0n;for(;;){const b=f[i++];n|=BigInt(b&127)<<s;if(!(b&128))return n;s+=7n}};integer();const use=integer();integer();const gen=integer();if(use<1n||use>2n||targets.has(use))throw Error('Restart targets');targets.set(use,gen)}if(!targets.size)throw Error('Restart empty');return targets}
