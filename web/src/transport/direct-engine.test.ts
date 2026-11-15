// @vitest-environment happy-dom
import { expect, test, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { CapabilitySnapshotSchema } from "../gen/remoteagent/v1/transport_pb";
import { MachineCapabilitiesSchema } from "../gen/remoteagent/v1/capabilities_pb";
import * as w from "./peer/wire";
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
