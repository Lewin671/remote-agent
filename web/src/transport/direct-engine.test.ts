// @vitest-environment happy-dom
import { expect, test, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { MachineCapabilitiesSchema } from "../gen/remoteagent/v1/capabilities_pb";
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
