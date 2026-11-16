// @vitest-environment happy-dom
import { expect, test, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { CapabilitySnapshotSchema } from "../gen/remoteagent/v1/transport_pb";
import { MachineCapabilitiesSchema } from "../gen/remoteagent/v1/capabilities_pb";
import * as w from "./peer/wire";
import { DirectSession } from "./direct-session";
import { DirectRouting } from "./direct-routing";
import type { DirectChannel } from "./direct-channel";
import { fixture, fixtureCodec } from "../test/peer/fixture";
import { DirectEngine } from "./direct-engine";

function readyEngine() {
 const engine=new DirectEngine({server:"https://example.com",deviceId:"client",devicePrivate:{} as CryptoKey},async()=>{},()=>{});
 const peer={id:"machine",head:"head",live:true,session:{active:true,close:vi.fn()},paths:new Map([[2,{ready:true}]]),capabilities:create(MachineCapabilitiesSchema,{sharedFiles:true,fileUpload:true,imageUpload:false,maxChunkBytes:786432}),snapshotUntil:Date.now()+60000,downloadUntil:Date.now()+60000};
 Object.assign(engine,{self:{},codec:{},record:{member:true},freshUntil:Date.now()+20000,head:"head",peers:new Map([["machine",peer]])});
 return {engine,peer};
}
test("direct upload requires the current full snapshot and its exact upload limits",()=>{
 const {engine,peer}=readyEngine();const hint={kind:"bulk",file:"transfer",upload:"file",bytes:786432} as const;
 expect(engine.ready("machine",hint)).toBe(true);
 expect(engine.ready("machine",{...hint,bytes:786433})).toBe(false);
 expect(engine.ready("machine",{...hint,upload:"image"})).toBe(false);
 peer.snapshotUntil=Date.now()-1;expect(engine.ready("machine",hint)).toBe(false);
 expect(engine.ready("machine",{kind:"bulk",file:"download"})).toBe(true);
 peer.snapshotUntil=Date.now()+60000;peer.capabilities.fileUpload=false;expect(engine.ready("machine",hint)).toBe(false);
});
test("a live data channel cannot bypass the freshness, instance or local policy gate",()=>{
 const {engine,peer}=readyEngine(),hint={kind:"bulk",file:"download"} as const;
 peer.live=false;expect(engine.ready("machine",hint)).toBe(false);peer.live=true;
 peer.head="old";expect(engine.ready("machine",hint)).toBe(false);peer.head="head";
 Object.assign(engine,{freshUntil:Date.now()-1});expect(engine.ready("machine",hint)).toBe(false);
 Object.assign(engine,{freshUntil:Date.now()+20000});engine.setEnabled("machine",false);expect(engine.enabled("machine")).toBe(false);expect(engine.ready("machine",hint)).toBe(false);
 const reload=new DirectEngine({server:"https://example.com",deviceId:"client",devicePrivate:{} as CryptoKey},async()=>{},()=>{});expect(reload.enabled("machine")).toBe(false);
 localStorage.clear();
});

test("an unverified peer cannot occupy another peer's authentication queue share",async()=>{
 const {engine}=readyEngine(),self="1".repeat(32),peer="2".repeat(32),leg={ready:true,send:vi.fn(),close:vi.fn()};
 Object.assign(engine,{leg,self:{id:w.utf8(self)},record:{member:true,states:[{core:{members:[{id:peer,kind:1,ed:new Uint8Array(32),x:new Uint8Array(32)}]}}]}});
 const raw=w.pb(w.b(1,w.utf8(peer)),w.b(2,w.utf8(self)),w.u(3,1),w.b(4,new Uint8Array(16)),w.b(5,new Uint8Array([1])));
 for(let i=0;i<64;i++)engine.receive(raw,leg);
 expect((engine as unknown as {incoming:number}).incoming).toBe(8);
 await (engine as unknown as {chain:Promise<void>}).chain;
 expect((engine as unknown as {incoming:number}).incoming).toBe(0);
});

test("a delayed snapshot cannot overwrite a newer capability decision",()=>{
 const {engine,peer}=readyEngine();Object.assign(peer,{snapshotSeq:0n,downloadSeq:0n});
 const receive=(seq:bigint,fileUpload:boolean)=>{
  const payload=toBinary(CapabilitySnapshotSchema,create(CapabilitySnapshotSchema,{capabilityId:new Uint8Array(16),capabilities:create(MachineCapabilitiesSchema,{fileUpload,maxChunkBytes:786432}),expiresAtMs:BigInt(Date.now()+60000)}));
  (engine as unknown as {capability(peer:unknown,opened:unknown):void}).capability(peer,{type:14,payload,context:{seq}});
 };
 receive(2n,false);receive(1n,true);expect(engine.capabilities("machine")?.fileUpload).toBe(false);
 receive(3n,true);expect(engine.capabilities("machine")?.fileUpload).toBe(true);
});

test("a healthy read PC cannot make a closed bulk PC display as direct",()=>{
 const {engine,peer}=readyEngine();engine.routing("machine").filePath="direct";
 peer.paths.set(1,{ready:true});peer.paths.set(2,{ready:false});expect(engine.filePath("machine")).toBe("relay");
});


test("retired session expiry preserves the current peer, backoff and measured routes",async()=>{
 vi.useFakeTimers();const {engine}=readyEngine();
 type Peer={session:DirectSession;paths:Map<number,DirectChannel>};
 const state=engine as unknown as {create(peer:w.Public,connection:w.Bytes,deadline:number):Peer;peers:Map<string,Peer>;routes:Map<string,DirectRouting>;retryAfter:Map<string,number>};
 const self=await fixture("client",1),peer=await fixture("machine",2),id=w.text(peer.id),now=Date.now();
 Object.assign(engine,{self,codec:await fixtureCodec(),peers:new Map(),freshUntil:now+1000000});
 const old=state.create(peer,new Uint8Array(16),now+10000);old.session.retire();
 const current=state.create(peer,new Uint8Array(16).fill(1),now+10000);current.session.established=true;
 const channel={ready:true} as DirectChannel;current.paths.set(2,channel);
 let clock=0;const route=new DirectRouting(()=>clock);state.routes.set(id,route);
 for(let i=0;i<100;i++){clock=i*60;route.observe({kind:"read"},"relay",0,150);route.observe({kind:"read"},"direct",0,20);}
 for(let i=0;i<2;i++){route.observe({kind:"bulk",file:"file"},"relay",4<<20,100);route.observe({kind:"bulk",file:"file"},"direct",4<<20,90);}
 const snapshot=JSON.stringify(route,(_key,value)=>value instanceof Map?[...value]:value),failure=vi.spyOn(route,"fail");
 state.retryAfter.set(id,123);const backoff=[...state.retryAfter];
 try {
  vi.setSystemTime(old.session.retainUntil+1);await vi.advanceTimersByTimeAsync(25);
  expect(state.peers.get(id)).toBe(current);expect([...state.retryAfter]).toEqual(backoff);
  expect(failure).not.toHaveBeenCalled();expect(JSON.stringify(route,(_key,value)=>value instanceof Map?[...value]:value)).toBe(snapshot);
  Object.assign(channel,{ready:false});current.session.close();expect(failure).toHaveBeenCalledOnce();expect(state.peers.has(id)).toBe(false);
 } finally {engine.close();vi.useRealTimers();}
});

test.each(["session", "PC", "invalidation"])("late request measurements and failures cannot alter a newer %s",kind=>{
 const {engine,peer}=readyEngine(),hint={kind:"bulk",file:"file"} as const;
 const tracking=engine.track("machine",hint),route=engine.routing("machine"),failed=vi.spyOn(route,"fail"),observed=vi.spyOn(route,"observe");
 if(kind==="session") (engine as unknown as {peers:Map<string,unknown>}).peers.set("machine",{...peer});
 else if(kind==="PC")peer.paths.set(2,{ready:true});
 else engine.invalidate();
 failed.mockClear();observed.mockClear();tracking.failed();tracking.observe(true,4<<20,10);
 expect(failed).not.toHaveBeenCalled();expect(observed).not.toHaveBeenCalled();
 const current=engine.track("machine",hint);current.failed();expect(failed).toHaveBeenCalledOnce();
});

test("a pending discovery cannot replace an already chosen active session",async()=>{
 vi.useFakeTimers();const {engine}=readyEngine(),self=await fixture("client",1),peer=await fixture("machine",2),id=w.text(peer.id);
 type Peer={session:DirectSession;paths:Map<number,DirectChannel>};
 const state=engine as unknown as {create(p:w.Public,c:w.Bytes,d:number):Peer;peers:Map<string,Peer>};
 Object.assign(engine,{self,codec:await fixtureCodec(),peers:new Map()});
 try {
  const active=state.create(peer,new Uint8Array(16),Date.now()+10000);active.session.options.chosen();
  const pending=state.create(peer,new Uint8Array(16).fill(1),Date.now()+10000);
  expect(state.peers.get(id)).toBe(active);
  pending.session.options.beforeOffer();expect(state.peers.get(id)).toBe(pending);expect(active.session.active).toBe(false);
  active.session.options.chosen();expect(state.peers.get(id)).toBe(pending);expect(pending.session.active).toBe(true);
 }finally{engine.close();vi.useRealTimers();}
});

test.each(["main leg", "lifetime"])("a delayed discovery import cannot act after its %s is replaced",async kind=>{
 const {engine}=readyEngine(),peer=await fixture("machine",2),id=w.text(peer.id);
 const leg={ready:true,send:vi.fn(),close:vi.fn()};
 let complete!:(peer:w.Public)=>void;const imported=new Promise<w.Public>(resolve=>complete=resolve);
 const create=vi.fn();Object.assign(engine,{leg,record:{member:true,states:[{core:{members:[{id,kind:1}]} }]},public:()=>imported,create});
 const pending=(engine as unknown as {discover():Promise<void>}).discover();
 if(kind==="main leg")Object.assign(engine,{leg:{...leg}});
 else {engine.invalidate();Object.assign(engine,{freshUntil:Date.now()+20000});}
 complete(peer);await pending;expect(create).not.toHaveBeenCalled();
});
