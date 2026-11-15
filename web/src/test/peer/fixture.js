import {concat,unhex,utf8,hash,hex,codec} from "../../transport/peer/wire.js";
const subtle=globalThis.crypto.subtle;
const pkcs8=(oid,seed)=>concat(unhex(`302e020100300506032b65${oid}04220420`),seed);
const from64=s=>Uint8Array.from(atob(s.replaceAll('-','+').replaceAll('_','/')),c=>c.charCodeAt(0));
export async function fixture(id,role){
 const sign=await subtle.importKey('pkcs8',pkcs8('70',await hash(utf8('lab/sign/'+id))),{name:'Ed25519'},true,['sign']);
 const box=await subtle.importKey('pkcs8',pkcs8('6e',await hash(utf8('lab/box/'+id))),{name:'X25519'},true,['deriveBits']);
 const signPub=await subtle.importKey('raw',from64((await subtle.exportKey('jwk',sign)).x),{name:'Ed25519'},true,['verify']);
 const boxPub=await subtle.importKey('raw',from64((await subtle.exportKey('jwk',box)).x),{name:'X25519'},true,[]);
 return {id:utf8(hex((await hash(new Uint8Array(await subtle.exportKey('raw',signPub)))).slice(0,16))),role:BigInt(role),instance:(await hash(utf8('lab/instance/'+id))).slice(0,16),boot:await hash(utf8('lab/boot/'+id)),sign,box,signPub,boxPub};
}

export async function fixtureCodec(){return codec({account:"0123456789abcdef0123456789abcdef",accountDomain:await hash(utf8("lab/account-domain")),head:await hash(utf8("lab/member-head/1")),epoch:1n})}
