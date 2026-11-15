// Experimental wire only. Fixture keys are public test material, never accounts.
import {normalize} from './schema.mjs';
export const fields=(raw,name)=>parse(normalize(raw,name),64);
const subtle=globalThis.crypto.subtle;
const te=new TextEncoder();
export const utf8=s=>te.encode(s);
export const text=b=>new TextDecoder('utf-8',{fatal:true,ignoreBOM:true}).decode(b);
export const concat=(...xs)=>{const out=new Uint8Array(xs.reduce((n,x)=>n+x.length,0));let n=0;for(const x of xs){out.set(x,n);n+=x.length}return out};
export const equal=(a,b)=>a.length===b.length&&a.every((v,i)=>v===b[i]);
export const hex=b=>Array.from(b,b=>b.toString(16).padStart(2,'0')).join('');
export const unhex=s=>Uint8Array.from(s.match(/../g)??[],b=>parseInt(b,16));
export const hash=async b=>new Uint8Array(await subtle.digest('SHA-256',b));
export const num=n=>{const b=new Uint8Array(8);new DataView(b.buffer).setBigUint64(0,BigInt(n));return b};
export function lp(...xs){return concat(...xs.flatMap(x=>{const n=new Uint8Array(4);new DataView(n.buffer).setUint32(0,x.length);return[n,x]}))}
export function vint(n){n=BigInt(n);if(n<0n||n>0xffffffffffffffffn)throw Error('integer');const out=[];do{let b=Number(n&127n);n>>=7n;if(n)b|=128;out.push(b)}while(n);return Uint8Array.from(out)}
export const b=(n,value)=>({n,value,bytes:true});
export const u=(n,value)=>({n,value:BigInt(value),bytes:false});
export function pb(...fields){return concat(...fields.sort((a,b)=>a.n-b.n).map(f=>concat(vint(BigInt(f.n)*8n+(f.bytes?2n:0n)),...(f.bytes?[vint(f.value.length),f.value]:[vint(f.value)]))))}
export function parse(raw,max){
 if(raw.length>65536)throw Error('size');let at=0,last=0;const out=new Map();
 function read(){let n=0n,shift=0n,start=at;for(let i=0;i<10;i++){
  if(at===raw.length)throw Error('short');const c=raw[at++];if(i===9&&c>1)throw Error('overflow');n|=BigInt(c&127)<<shift;
  if(!(c&128)){if(!equal(raw.slice(start,at),vint(n)))throw Error('nonminimal');return n}shift+=7n;
 }throw Error('overflow')}
 while(at<raw.length){const tag=read(),n=Number(tag>>3n),wire=Number(tag&7n);if(n<=last||n>4095||out.size>=max)throw Error('fields');last=n;
  if(wire===0)out.set(n,u(n,read()));else if(wire===2){const len=read();if(len>BigInt(raw.length-at))throw Error('length');out.set(n,b(n,raw.slice(at,at+Number(len))));at+=Number(len)}else throw Error('wire');
 }return out;
}
export function need(f,schema){if(f.size!==schema.length)throw Error('schema');for(let i=0;i<schema.length;i++)if(!f.has(i+1)||f.get(i+1).bytes!==(schema[i]==='b'))throw Error('schema')}
const value=(f,n)=>f.get(n).value;
export const account=utf8('0123456789abcdef0123456789abcdef');
export const accountDomain=await hash(utf8('lab/account-domain'));
export const head=await hash(utf8('lab/member-head/1'));
const pkcs8=(oid,seed)=>concat(unhex(`302e020100300506032b65${oid}04220420`),seed);
const from64=s=>Uint8Array.from(atob(s.replaceAll('-','+').replaceAll('_','/')),c=>c.charCodeAt(0));
export async function fixture(id,role){
 const sign=await subtle.importKey('pkcs8',pkcs8('70',await hash(utf8('lab/sign/'+id))),{name:'Ed25519'},true,['sign']);
 const box=await subtle.importKey('pkcs8',pkcs8('6e',await hash(utf8('lab/box/'+id))),{name:'X25519'},true,['deriveBits']);
 const signPub=await subtle.importKey('raw',from64((await subtle.exportKey('jwk',sign)).x),{name:'Ed25519'},true,['verify']);
 const boxPub=await subtle.importKey('raw',from64((await subtle.exportKey('jwk',box)).x),{name:'X25519'},true,[]);
 return {id:utf8(hex((await hash(new Uint8Array(await subtle.exportKey('raw',signPub)))).slice(0,16))),role:BigInt(role),instance:(await hash(utf8('lab/instance/'+id))).slice(0,16),boot:await hash(utf8('lab/boot/'+id)),sign,box,signPub,boxPub};
}
export function context(from,to,connection,use,generation,seq,now,cls=1,boot=false){
 return {domain:utf8(boot?'bootstrap':cls===1?'signal':'control'),from:from.id,to:to.id,fromRole:from.role,toRole:to.role,machine:from.role===2n?from.id:to.id,
  senderInstance:from.instance,receiverInstance:to.instance,senderBoot:from.boot,receiverBoot:to.boot,connection,
  use:BigInt(use),generation:BigInt(generation),seq:BigInt(seq),issued:BigInt(now),deadline:BigInt(now)+10000n,cls:BigInt(cls),recipientUnknown:0n};
}
export function encodeContext(c){return pb(b(1,c.domain),b(2,account),b(3,c.from),b(4,c.to),u(5,c.fromRole),u(6,c.toRole),b(7,c.machine),u(8,16),u(9,1),b(10,head),b(11,c.senderInstance),b(12,c.receiverInstance),b(13,c.senderBoot),b(14,c.receiverBoot),b(15,c.connection),u(16,c.use),u(17,c.generation),u(18,c.seq),u(19,c.issued),u(20,c.deadline),u(21,c.cls),b(22,c.from),b(23,accountDomain),u(24,c.recipientUnknown))}
function decodeContext(raw){const f=fields(raw,'Context');need(f,'bbbbuubuubbbbbbuuuuuubbu');const v=n=>value(f,n);
 if(!equal(v(23),accountDomain)||!equal(v(2),account)||v(8)!==16n||v(9)!==1n||!equal(v(10),head)||!equal(v(22),v(3)))throw Error('scope');
 return {domain:v(1),from:v(3),to:v(4),fromRole:v(5),toRole:v(6),machine:v(7),senderInstance:v(11),receiverInstance:v(12),senderBoot:v(13),receiverBoot:v(14),connection:v(15),use:v(16),generation:v(17),seq:v(18),issued:v(19),deadline:v(20),cls:v(21),recipientUnknown:v(24)};
}
function wrapInfo(from,to){const sorted=[from,to].sort((a,b)=>{for(let i=0;i<Math.min(a.length,b.length);i++)if(a[i]!==b[i])return a[i]-b[i];return a.length-b.length});return lp(utf8('remote-agent/peer-context/v1'),account,accountDomain,...sorted,from,to)}
function bodyInfo(c){if(text(c.domain)==='bootstrap')return lp(utf8('remote-agent/peer-bootstrap/v1'),account,accountDomain,c.from,c.to);
 return lp(utf8('remote-agent/peer-body/v1'),account,accountDomain,c.from,c.to,c.senderInstance,c.receiverInstance,c.senderBoot,c.receiverBoot,c.connection,num(c.use),num(c.generation),num(c.cls));}
function aad(from,to,cls,id){return lp(utf8('remote-agent/peer-aad/v1'),account,accountDomain,num(16),pb(b(1,from),b(2,to),u(3,cls),b(4,id)))}
function sigInput(ad,nc,ec,nb,eb){return lp(utf8('remote-agent/peer-signature/v1'),ad,nc,ec,nb,eb)}
async function kdf(shared,info){const key=await subtle.importKey('raw',shared,'HKDF',false,['deriveBits']);return new Uint8Array(await subtle.deriveBits({name:'HKDF',hash:'SHA-256',salt:await hash(utf8('remote-agent/peer-hkdf/v1')),info},key,256))}
async function gcm(key,iv,additionalData,body,seal){const k=await subtle.importKey('raw',key,'AES-GCM',false,[seal?'encrypt':'decrypt']);return new Uint8Array(await subtle[seal?'encrypt':'decrypt']({name:'AES-GCM',iv,additionalData,tagLength:128},k,body))}
async function shared(from,to){return new Uint8Array(await subtle.deriveBits({name:'X25519',public:to.boxPub},from.box,256))}
const omitDefaults=raw=>pb(...[...parse(raw,64).values()].filter(f=>f.bytes?f.value.length:f.value!==0n));
export async function seal(from,to,c,type,payload,id=crypto.getRandomValues(new Uint8Array(16)),nc=crypto.getRandomValues(new Uint8Array(12)),nb=crypto.getRandomValues(new Uint8Array(12)),omit=false){
 const dh=await shared(from,to),ad=aad(from.id,to.id,c.cls,id);
 const ec=await gcm(await kdf(dh,wrapInfo(from.id,to.id)),nc,ad,omit?omitDefaults(encodeContext(c)):encodeContext(c),true);
 const eb=await gcm(await kdf(dh,bodyInfo(c)),nb,lp(ad,await hash(ec)),pb(b(1,omit?omitDefaults(pb(u(1,type),b(2,payload))):pb(u(1,type),b(2,payload)))),true);
 const sig=new Uint8Array(await subtle.sign('Ed25519',from.sign,sigInput(ad,nc,ec,nb,eb)));
 const packet=pb(b(1,from.id),b(2,to.id),u(3,c.cls),b(4,id),b(5,pb(b(1,nc),b(2,ec),b(3,nb),b(4,eb),b(5,sig))));
 if(packet.length>(c.cls===2n?16384:65536))throw Error('size');return packet;
}
export async function open(raw,from,to,now){return openShared(raw,from,to,now,await shared(to,from))}
export async function openSent(raw,from,to,now){const o=await openShared(raw,from,to,now,await shared(from,to));if(!equal(o.context.senderInstance,from.instance)||!equal(o.context.senderBoot,from.boot))throw Error('sent identity');return o}
async function openShared(raw,from,to,now,dh){
 if(raw.length>65536)throw Error('size');raw=raw.slice();now=BigInt(now);const f=fields(raw,'PeerFrameDelivery');need(f,'bbubb');const v=n=>value(f,n),cls=v(3),id=v(4);
 if(![1n,2n].includes(cls)||raw.length>(cls===2n?16384:65536)||id.length!==16||!equal(v(1),from.id)||!equal(v(2),to.id))throw Error('route');
 const p=fields(v(5),'SealedPeerPacket');need(p,'bbbbb');const [nc,ec,nb,eb,sig]=[1,2,3,4,5].map(n=>value(p,n)),ad=aad(from.id,to.id,cls,id);
 if(nc.length!==12||nb.length!==12||sig.length!==64||!await subtle.verify('Ed25519',from.signPub,sig,sigInput(ad,nc,ec,nb,eb)))throw Error('signature');
 const c=decodeContext(await gcm(await kdf(dh,wrapInfo(from.id,to.id)),nc,ad,ec,false));
 if(!equal(c.from,from.id)||!equal(c.to,to.id)||c.cls!==cls||c.fromRole!==from.role||c.toRole!==to.role||from.role===to.role||c.connection.length!==16||!c.seq||c.issued>=c.deadline||c.deadline-c.issued>30000n||now>=c.deadline||c.issued>now+300000n)throw Error('context');
 if(!equal(c.machine,from.role===2n?from.id:to.id))throw Error('machine');
 const boot=text(c.domain)==='bootstrap';
 if(c.senderInstance.length!==16||c.senderBoot.length!==32||c.fromRole<1n||c.fromRole>2n||c.toRole<1n||c.toRole>2n)throw Error('shape');
 if(c.recipientUnknown>1n||(c.recipientUnknown===1n&&(c.receiverInstance.length||c.receiverBoot.length))||(c.recipientUnknown===0n&&(c.receiverInstance.length!==16||c.receiverBoot.length!==32||!equal(c.receiverInstance,to.instance)||!equal(c.receiverBoot,to.boot))))throw Error('recipient binding');
 if(!boot&&(text(c.domain)!==(cls===1n?'signal':'control')))throw Error('domain');
 if(!boot&&(!equal(c.senderInstance,from.instance)||!equal(c.receiverInstance,to.instance)||!equal(c.senderBoot,from.boot)||!equal(c.receiverBoot,to.boot)||!c.generation||c.use>2n))throw Error('instance');
 const body=await gcm(await kdf(dh,bodyInfo(c)),nb,lp(ad,await hash(ec)),eb,false);const wrapper=fields(body,'PeerSignal');need(wrapper,'b');const typed=fields(value(wrapper,1),'TypedPayload');need(typed,'ub');const type=value(typed,1);
 if(!boot&&cls===1n&&c.use===0n&&![8n,9n].includes(type))throw Error('peer-scope type');
 if(type>0xffffffffn||(boot&&(cls!==1n||c.use!==0n||c.generation!==0n||![1n,2n].includes(type))))throw Error('type');
 if(c.recipientUnknown===1n&&(!boot||cls!==1n||type!==1n))throw Error('unknown recipient type');
 return {context:c,type:Number(type),payload:value(typed,2),frameID:id,hash:await hash(raw),raw:raw.slice()};
}
export function cacheKey(c){return [hex(c.from),hex(c.senderInstance),hex(c.connection),c.use,c.generation,c.cls,c.seq].join('/')}

// Reply must pass open(); request is the frozen outgoing record. Owner authorizes policy.
export function validatePolicyReply(request,reply,now){
 const a=request.context,z=reply.context,q=fields(request.payload,'DirectPolicy'),r=fields(reply.payload,'PolicyReply');need(q,'buuu');need(r,'uuuubub');if(BigInt(now)>=a.deadline||BigInt(now)>=z.deadline||a.generation!==z.generation||a.use!==z.use||!equal(a.senderInstance,z.receiverInstance)||!equal(a.receiverInstance,z.senderInstance)||!equal(a.senderBoot,z.receiverBoot)||!equal(a.receiverBoot,z.senderBoot))throw Error('stale policy reply');const v=(f,n)=>f.get(n).value;
 if(a.cls!==2n||z.cls!==2n||request.type!==9||reply.type!==13||!equal(a.from,z.to)||!equal(a.to,z.from)||!equal(a.connection,z.connection))throw Error('policy binding');
 if(v(q,2)===0n||v(q,3)>1n||v(r,2)>1n||v(r,3)>1n||!(v(q,4)===1n&&v(q,1).length===0||v(q,4)===2n&&v(q,1).length===32))throw Error('policy fields');
 if(v(r,1)!==v(q,2)||v(r,4)!==a.seq||v(r,5).length!==32||!equal(v(r,5),request.hash)||v(r,6)!==v(q,4)||!equal(v(r,7),v(q,1))||(v(r,2)===1n&&v(r,3)!==v(q,3)))throw Error('policy reply');
}

export const member=peer=>({...peer,instance:new Uint8Array(),boot:new Uint8Array()});
export function probeContext(from,to,conn,seq,now){const c=context(from,member(to),conn,0,0,seq,now,1,true);c.recipientUnknown=1n;return c}
