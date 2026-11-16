// @vitest-environment happy-dom
import { afterEach, expect, test, vi } from "vitest";
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

test.describe("status snapshot",()=>{
 const stub=()=>vi.stubGlobal("RTCPeerConnection",class{});
 afterEach(()=>{vi.unstubAllGlobals();localStorage.clear();});
 const route=(engine:DirectEngine)=>engine.routing("machine");

 test("a device without RTCPeerConnection reports unsupported, whatever else is set",()=>{
  const {engine}=readyEngine();vi.stubGlobal("RTCPeerConnection",undefined);
  expect(engine.status("machine")).toMatchObject({state:"unsupported",file:"relay",read:"relay"});
 });
 test("the local setting off is reported with its reason and nothing is claimed in use",()=>{
  const {engine}=readyEngine();stub();engine.setEnabled("machine",false);
  expect(engine.status("machine")).toMatchObject({state:"none",reason:"disabled",file:"relay",read:"relay"});
 });
 test("no session at all is plain none, with no invented reason",()=>{
  const {engine}=readyEngine();stub();Object.assign(engine,{peers:new Map()});
  expect(engine.status("machine").state).toBe("none");expect(engine.status("machine").reason).toBeUndefined();
 });
 test("a live session without a ready path is connecting; a ready path is connected",()=>{
  const {engine,peer}=readyEngine();stub();peer.paths.clear();
  expect(engine.status("machine").state).toBe("connecting");
  peer.paths.set(2,{ready:true});peer.paths.set(1,{ready:true});
  expect(engine.status("machine")).toMatchObject({state:"connected",file:"relay",read:"relay"});
 });
 test("per-purpose paths follow what routing really uses",()=>{
  const {engine,peer}=readyEngine();stub();peer.paths.set(1,{ready:true});
  route(engine).filePath="direct";
  expect(engine.status("machine")).toMatchObject({state:"connected",file:"direct",read:"relay"});
  // Reads go direct only after routing has promoted them and the read path is ready.
  Object.assign(route(engine),{readPreferred:"direct"});
  expect(engine.status("machine").read).toBe("direct");
  peer.paths.set(1,{ready:false});expect(engine.status("machine").read).toBe("relay");
 });
 test("numbers are present only when measured, and never as zero",()=>{
  const {engine}=readyEngine();stub();
  expect(engine.status("machine")).toMatchObject({fileMiBps:{},readMs:{}});
  route(engine).observe({kind:"bulk",file:"f"},"relay",4<<20,500);
  const got=engine.status("machine");expect(got.fileMiBps).toEqual({relay:8});expect(got.readMs).toEqual({});
  expect("direct" in got.fileMiBps).toBe(false);
 });
 test.each([
  ["peer_unsupported","peer_unsupported"],["no_response","no_response"],["no_traversal","no_traversal"],[undefined,"failed"],
 ] as const)("a round that ends without a path (%s) is reported as %s, then cleared by success",async(cause,reason)=>{
  vi.useFakeTimers();const {engine}=readyEngine();stub();
  type Peer={session:DirectSession;paths:Map<number,DirectChannel>};
  const state=engine as unknown as {create(p:w.Public,c:w.Bytes,d:number):Peer;peers:Map<string,Peer>};
  const self=await fixture("client",1),member=await fixture("machine",2),id=w.text(member.id),now=Date.now();
  Object.assign(engine,{self,codec:await fixtureCodec(),peers:new Map(),freshUntil:now+1000000});
  try {
   const p=state.create(member,new Uint8Array(16),now+10000);
   expect(engine.status(id).state).toBe("connecting");
   if(cause)p.session.failure=cause;
   p.session.close();
   expect(engine.status(id)).toMatchObject({state:"none",reason});
   // A later path coming up wipes the stale reason.
   const next=state.create(member,new Uint8Array(16).fill(1),now+10000);
   next.session.options.chosen();next.session.options.ready(2,{ready:true,close:vi.fn()} as unknown as DirectChannel);
   expect(engine.status(id).reason).toBeUndefined();
  } finally {engine.close();vi.useRealTimers();}
 });
});

test("the engine's warm-up asks the owner for samples within the bound: 10 per path, one in flight per path, 4 a second, 5 seconds, only while the read PC is ready", async () => {
 vi.useFakeTimers();
 try {
  const { engine, peer } = readyEngine(); peer.paths.set(1, { ready: true });
  const calls: { at: number; path: string }[] = []; let inflight = { relay: 0, direct: 0 }, most = 0;
  engine.probe = async (_id, path) => {
   calls.push({ at: performance.now(), path }); inflight[path]++; most = Math.max(most, inflight[path]);
   await new Promise(r => setTimeout(r, path === "direct" ? 40 : 170)); inflight[path]--;
  };
  const start = performance.now();
  engine.routing("machine").arm();
  for (let i = 0; i < 50; i++) engine.routing("machine").arm(); // a burst of triggers is one warm-up
  await vi.advanceTimersByTimeAsync(20_000);
  const per = (p: string) => calls.filter(c => c.path === p).length;
  expect([per("direct"), per("relay")]).toEqual([10, 10]);
  expect(most).toBe(1);
  expect(Math.max(...calls.map(c => c.at)) - start).toBeLessThanOrEqual(5000);
  for (const c of calls) expect(calls.filter(d => d.at >= c.at && d.at < c.at + 1000).length).toBeLessThanOrEqual(4);
  expect(engine.status("machine").warming).toBe(false);
  // Read PC not ready: nothing is asked.
  const before = calls.length; peer.paths.set(1, { ready: false });
  engine.routing("machine").arm(); await vi.advanceTimersByTimeAsync(8000);
  expect(calls.length).toBe(before);
 } finally { vi.useRealTimers(); }
});
test("status says warming only while connected and measuring, and not for a computer with no read PC", async () => {
 vi.useFakeTimers(); vi.stubGlobal("RTCPeerConnection", class {});
 try {
  const { engine, peer } = readyEngine(); peer.paths.set(1, { ready: true });
  engine.probe = async () => undefined;
  expect(engine.status("machine").warming).toBe(false);
  engine.routing("machine").arm(); expect(engine.status("machine").warming).toBe(true);
  await vi.advanceTimersByTimeAsync(6000); expect(engine.status("machine").warming).toBe(false);
  peer.paths.set(1, { ready: false }); engine.routing("machine").arm(); expect(engine.status("machine").warming).toBe(false);
 } finally { vi.useRealTimers(); vi.unstubAllGlobals(); }
});

test("opening a screen with numbers triggers at most one bounded warm-up, and only when direct is connected and the window is thin", async () => {
 vi.useFakeTimers();
 try {
  const { engine, peer } = readyEngine(); peer.paths.set(1, { ready: true }); Object.assign(engine, { freshUntil: Date.now() + 1e9 });
  const calls: { at: number; path: string }[] = [];
  engine.probe = async (_id, path) => { calls.push({ at: performance.now(), path }); await new Promise(r => setTimeout(r, 30)); };
  const start = performance.now();
  for (let i = 0; i < 40; i++) engine.refresh("machine");   // many mounts at once: one warm-up
  await vi.advanceTimersByTimeAsync(15_000);
  const per = (p: string) => calls.filter(c => c.path === p).length;
  expect([per("direct"), per("relay")]).toEqual([10, 10]);
  expect(Math.max(...calls.map(c => c.at)) - start).toBeLessThanOrEqual(5000);
  for (const c of calls) expect(calls.filter(d => d.at >= c.at && d.at < c.at + 1000).length).toBeLessThanOrEqual(4);
  // Direct not connected: nothing.
  const before = calls.length; peer.paths.set(1, { ready: false });
  engine.refresh("machine"); await vi.advanceTimersByTimeAsync(8000); expect(calls.length).toBe(before);
  // A live window that already has 8 samples per path needs no warm-up.
  peer.paths.set(1, { ready: true }); const route = engine.routing("machine");
  for (let i = 0; i < 8; i++) { route.observe({ kind: "read" }, "relay", 0, 175); route.observe({ kind: "read" }, "direct", 0, 160); }
  expect(route.sparse()).toBe(false);
  engine.refresh("machine"); await vi.advanceTimersByTimeAsync(8000); expect(calls.length).toBe(before);
  // Once the window has aged out, opening again warms again, bounded the same way.
  await vi.advanceTimersByTimeAsync(61_000); expect(route.sparse()).toBe(true);
  engine.refresh("machine"); await vi.advanceTimersByTimeAsync(8000); expect(calls.length).toBe(before + 20); expect(engine.ready("machine", { kind: "read" })).toBe(true);
 } finally { vi.useRealTimers(); }
});

type Round = { session: DirectSession; paths: Map<number, DirectChannel> };
async function rounds(random = () => 0.5) {
 vi.useFakeTimers();
 const engine = new DirectEngine({ server: "https://example.com", deviceId: "client", devicePrivate: {} as CryptoKey }, async () => {}, () => {}, random);
 const self = await fixture("client", 1), member = await fixture("machine", 2), id = w.text(member.id);
 Object.assign(engine, { self, codec: await fixtureCodec(), record: { member: true, states: [{ core: { members: [] } }] }, peers: new Map(), freshUntil: Date.now() + 1e9 });
 const state = engine as unknown as { create(p: w.Public, c: w.Bytes, d: number): Round; peers: Map<string, Round>; retryAfter: Map<string, number>; failureCount: Map<string, number> };
 let n = 0;
 const round = () => state.create(member, new Uint8Array(16).fill(++n), Date.now() + 10_000);
 return { engine, state, id, round, delayOf: () => state.retryAfter.get(id)! - Date.now() };
}
test("a failed round is retried after a full-jitter delay that doubles from 1 s to a 30 s ceiling, not after a fixed 5 s", async () => {
 const { engine, round, delayOf } = await rounds();
 try {
  const seen: number[] = [];
  for (let i = 0; i < 8; i++) { round().session.close(); seen.push(delayOf()); }
  expect(seen).toEqual([500, 1000, 2000, 4000, 8000, 15_000, 15_000, 15_000]);
 } finally { engine.close(); vi.useRealTimers(); }
});
test("the delay is uniform below the ceiling: a low draw retries at once, a high one waits nearly the whole ceiling", async () => {
 for (const [draw, expected] of [[0, 0], [0.999, 999]] as const) {
  const { engine, round, delayOf } = await rounds(() => draw);
  try { round().session.close(); expect(delayOf()).toBeCloseTo(expected, 0); } finally { engine.close(); vi.useRealTimers(); }
 }
});
test("a path that stayed up for 30 s starts the sequence again; one that dropped sooner does not", async () => {
 const { engine, state, id, round, delayOf } = await rounds();
 try {
  for (let i = 0; i < 3; i++) round().session.close();
  expect(state.failureCount.get(id)).toBe(3);
  for (const [lived, expected] of [[5_000, 4000], [31_000, 500]] as const) {
   const channel = { ready: true, close: vi.fn() };
   const p = round(); p.session.options.chosen(); Object.assign(p.session, { established: true }); p.session.options.ready(1, channel as unknown as DirectChannel);
   await vi.advanceTimersByTimeAsync(lived); channel.ready = false; p.session.close();
   expect(delayOf()).toBe(expected);
  }
 } finally { engine.close(); vi.useRealTimers(); }
});
test.each(["the main connection comes back", "the network generation changes", "the direct switch is turned on again"])("a retry waiting out its delay does not wait on when %s", async kind => {
 const { engine, state, id, round } = await rounds();
 try {
  for (let i = 0; i < 6; i++) round().session.close();
  expect(state.retryAfter.get(id)! - Date.now()).toBeGreaterThan(10_000);
  if (kind.startsWith("the main")) engine.attach({ ready: true, send: vi.fn(), close: vi.fn() } as never);
  else if (kind.startsWith("the network")) engine.invalidate();
  else { engine.setEnabled(id, false); state.retryAfter.set(id, Date.now() + 20_000); engine.setEnabled(id, true); }
  expect(state.retryAfter.has(id)).toBe(false);
  if (kind.startsWith("the network") || kind.startsWith("the direct")) expect(state.failureCount.has(id)).toBe(false);
 } finally { engine.close(); vi.useRealTimers(); localStorage.clear(); }
});
test("one end of a round that had a ready path counts once however many of its paths close, and redials after that same delay", async () => {
 const { engine, state, id, round } = await rounds();
 try {
  const p = round() as Round & { redialAt?: number }; p.session.options.chosen();
  for (const use of [1, 2]) p.paths.set(use, { ready: true } as DirectChannel);
  p.session.options.ready(1, p.paths.get(1)!); Object.assign(p.session, { established: true });
  p.paths.set(2, { ready: false } as DirectChannel); p.session.options.closed(2);       // one path closes, the other stays
  expect(p.redialAt! - Date.now()).toBe(500); expect(state.failureCount.get(id)).toBe(1);
  p.paths.set(1, { ready: false } as DirectChannel); p.session.options.closed(1);       // then the last
  expect(state.retryAfter.get(id)! - Date.now()).toBe(500); expect(state.failureCount.get(id)).toBe(1);
 } finally { engine.close(); vi.useRealTimers(); }
});
test("a read PC that comes up after the fallback's warm-up window opened is still measured in a whole window", async () => {
 const { engine, state, id, round } = await rounds(); vi.stubGlobal("RTCPeerConnection", class {});
 try {
  const hint = { kind: "read" } as const, route = engine.routing(id), calls = { relay: 0, direct: 0 };
  engine.probe = async (_id, path) => { calls[path]++; route.observe(hint, path, 0, path === "direct" ? 13 : 335); };
  const p = round(); p.session.options.chosen(); Object.assign(p.session, { established: true });
  route.fail();                                                  // the path dropped: a re-measure is armed for 5 s later
  await vi.advanceTimersByTimeAsync(7000);                       // the new PC is ready two seconds into that window
  p.session.options.ready(1, { ready: true, close: vi.fn() } as unknown as DirectChannel);
  expect(engine.status(id)).toMatchObject({ state: "connected", warming: true, compared: false });
  await vi.advanceTimersByTimeAsync(20_000);
  // A whole window: 5 s at 4 a second (the last slot falls on its closing edge), not the 3 s that were left of the old one.
  expect(calls.direct).toBe(10); expect(calls.relay).toBeGreaterThanOrEqual(9);
  expect(engine.status(id)).toMatchObject({ state: "connected", read: "direct", warming: false, compared: true });
  void state;
 } finally { engine.close(); vi.useRealTimers(); vi.unstubAllGlobals(); }
});
test("connected but not yet compared is measuring while a warm-up is waiting or running, and plain relay once it ended without enough samples", async () => {
 const { engine, id, round } = await rounds(); vi.stubGlobal("RTCPeerConnection", class {});
 try {
  engine.probe = async () => undefined;                           // a window in which no sample is ever recorded
  const p = round(); p.session.options.chosen(); Object.assign(p.session, { established: true }); engine.routing(id).fail();
  p.session.options.ready(1, { ready: true, close: vi.fn() } as unknown as DirectChannel);
  expect(engine.status(id)).toMatchObject({ state: "connected", read: "relay", warming: true, compared: false });   // waiting out the stability period
  await vi.advanceTimersByTimeAsync(5100);
  expect(engine.status(id).warming).toBe(true);                                                          // and running
  await vi.advanceTimersByTimeAsync(11_000);
  expect(engine.status(id)).toMatchObject({ state: "connected", read: "relay", warming: false, compared: false });
 } finally { engine.close(); vi.useRealTimers(); vi.unstubAllGlobals(); }
});
