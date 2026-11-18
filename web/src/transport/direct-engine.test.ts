// @vitest-environment happy-dom
import { afterEach, expect, test, vi } from "vitest";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { EnvelopeSchema, HeaderSchema, PayloadKind } from "../gen/remoteagent/v1/envelope_pb";
import { PeerFrameSchema } from "../gen/remoteagent/v1/relay_pb";
import { CapabilitiesSchema, CapabilitySnapshotSchema, DirectEventSubscriptionSchema } from "../gen/remoteagent/v1/transport_pb";
import { MachineCapabilitiesSchema } from "../gen/remoteagent/v1/capabilities_pb";
import * as w from "./peer/wire";
import { DirectSession } from "./direct-session";
import { DirectRouting } from "./direct-routing";
import type { DirectChannel } from "./direct-channel";
import { fixture, fixtureCodec } from "../test/peer/fixture";
import { fakeRTC } from "../test/peer/fake-rtc";
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
 const {engine,peer}=readyEngine();
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
 const snapshot=JSON.stringify(route,(_key,value)=>value instanceof Map?[...value]:value),failure=vi.spyOn(route,"hardFail");
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
 test("no session reports explicit starting or security_pending reason rather than blank none",()=>{
  const {engine}=readyEngine();stub();Object.assign(engine,{peers:new Map()});
  expect(engine.status("machine")).toMatchObject({state:"none",reason:"starting"});
  Object.assign(engine,{freshUntil:Date.now()-1});
  expect(engine.status("machine")).toMatchObject({state:"none",reason:"security_pending"});
 });
 test("a live session without a ready path is connecting; a ready path is connected and direct-first",()=>{
  const {engine,peer}=readyEngine();stub();peer.paths.clear();
  expect(engine.status("machine").state).toBe("connecting");
  peer.paths.set(2,{ready:true});peer.paths.set(1,{ready:true});
  expect(engine.status("machine")).toMatchObject({state:"connected",file:"direct",read:"direct"});
 });
 test("per-purpose paths become relay when slow fallback triggers or when the PC closes",()=>{
  const {engine,peer}=readyEngine();stub();peer.paths.set(1,{ready:true});
  expect(engine.status("machine")).toMatchObject({state:"connected",file:"direct",read:"direct"});
  route(engine).fail({kind:"bulk",file:"f"});
  expect(engine.status("machine")).toMatchObject({state:"connected",file:"relay",read:"direct",fileSlow:true});
  route(engine).fail({kind:"read"});
  expect(engine.status("machine")).toMatchObject({state:"connected",file:"relay",read:"relay",readSlow:true,fileSlow:true});
  peer.paths.set(1,{ready:false});
  expect(engine.status("machine").read).toBe("relay");
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
test("background closes all direct peers and foreground restores discovery after security sync", async () => {
 const { engine, peer } = readyEngine();
 Object.assign(engine, { sessions: new Map([["s1", peer]]), leg: { ready: true } });
 const discover = vi.spyOn(engine as unknown as { discover(): Promise<void> }, "discover").mockResolvedValue();
 const authorize = vi.spyOn(engine as unknown as { authorize(): Promise<void> }, "authorize").mockResolvedValue();
 peer.paths.set(1, { ready: true });
 expect(engine.ready("machine", { kind: "read" })).toBe(true);

 engine.setForeground(false);
 expect(peer.session.close).toHaveBeenCalledOnce();
 expect(engine.ready("machine", { kind: "read" })).toBe(false);
 expect(engine.ready("machine", { kind: "bulk", file: "f" })).toBe(false);

 engine.setForeground(true);
 expect(authorize).toHaveBeenCalledTimes(1);
 await vi.waitFor(() => expect(discover).toHaveBeenCalledTimes(1));
});

test("attemptInteractive returns a direct attempt only when use=1 is ready", () => {
 const { engine, peer } = readyEngine();
 expect(engine.attemptInteractive("machine", async () => {})).toBeUndefined();
 peer.paths.set(1, { ready: true });
 const route = engine.routing("machine");
 route.fail({ kind: "read" }); // even when read fell back to relay, interactive dual-send still uses ready direct PC!
 expect(engine.attemptInteractive("machine", async () => {})).toBeDefined();
 engine.setForeground(false);
 expect(engine.attemptInteractive("machine", async () => {})).toBeUndefined();
});

test("Router.dual picks first trusted result and ignores late arrivals", async () => {
 const { Router } = await import("./router");
 const router = new Router({ status: () => ({ ready: true, reachable: true, relayRequired: false, authorized: true, unlinked: false }), attempt: vi.fn(), openStream: vi.fn() } as never);
 let resolveRelay!: (r: { trusted: true; value: string }) => void;
 let resolveDirect!: (r: { trusted: true; value: string }) => void;
 const relaySignal = { aborted: false };

 const promise = router.dual<string>(
  () => new Promise((r) => { resolveDirect = r; }),
  (signal) => new Promise((r) => {
   signal.addEventListener("abort", () => { relaySignal.aborted = true; });
   resolveRelay = r;
  }),
 );

 resolveDirect({ trusted: true, value: "from-direct" });
 expect(await promise).toBe("from-direct");
 expect(relaySignal.aborted).toBe(true);
 // Resolving the slower leg late cannot change or reject the settled promise.
 resolveRelay({ trusted: true, value: "late-relay" });
 expect(await promise).toBe("from-direct");
});

test("「重新尝试」 starts one discovery at once, only when no round runs and the switch is on, and not twice within two seconds",()=>{
 vi.useFakeTimers();
 try {
  const {engine,peer}=readyEngine(),owner=engine as unknown as {discover():Promise<void>;retryAfter:Map<string,number>;peers:Map<string,unknown>};
  const discover=vi.spyOn(owner,"discover").mockResolvedValue();
  expect(engine.retryNow("machine")).toBe(false);          // a round is running
  owner.peers.delete("machine");owner.retryAfter.set("machine",Date.now()+30_000);
  expect(engine.retryNow("machine")).toBe(true);
  expect(discover).toHaveBeenCalledTimes(1);expect(owner.retryAfter.has("machine")).toBe(false);
  expect(engine.retryNow("machine")).toBe(false);          // too soon
  vi.advanceTimersByTime(2001);expect(engine.retryNow("machine")).toBe(true);
  vi.advanceTimersByTime(2001);engine.setEnabled("machine",false);expect(engine.retryNow("machine")).toBe(false);
  vi.advanceTimersByTime(2001);engine.setEnabled("machine",true);Object.assign(engine,{freshUntil:Date.now()-1});expect(engine.retryNow("machine")).toBe(false);
  void peer;
 } finally { vi.useRealTimers(); localStorage.clear(); }
});

test("a round refused for lack of slots is reported as such and does not add rounds",()=>{
 const {engine}=readyEngine(),owner=engine as unknown as {diag:{begin(id:string):unknown;blocked(id:string):void}};
 for(let i=0;i<5;i++)owner.diag.blocked("machine");
 expect(engine.diagnostics("machine").rounds).toEqual([]);expect(engine.diagnostics("machine").blockedAt).toBeTypeOf("number");
 owner.diag.begin("machine");expect(engine.diagnostics("machine").blockedAt).toBeUndefined();
 expect(engine.diagnostics("machine").rounds).toHaveLength(1);
});

test("background invalidates direct freshness and bumps syncRevision so resume requires fresh sync", () => {
  const { engine, peer } = readyEngine();
  peer.paths.set(1, { ready: true });
  const state = engine as unknown as { freshUntil: number; syncRevision: number };
  state.syncRevision = 1;
  expect(state.freshUntil).toBeGreaterThan(Date.now());

  engine.setForeground(false);
  expect(state.freshUntil).toBe(0);
  expect(state.syncRevision).toBe(2);

  const discover = vi.spyOn(engine as unknown as { discover(): Promise<void> }, "discover").mockResolvedValue();
  const authorize = vi.spyOn(engine as unknown as { authorize(): Promise<void> }, "authorize").mockRejectedValue(new Error("offline"));
  Object.assign(engine, { leg: { ready: true, send: vi.fn() } });
  engine.setForeground(true);
  expect(discover).not.toHaveBeenCalled();
  expect(authorize).toHaveBeenCalledTimes(1);
});

test("attach authorizes stale direct state before discovering peers after background/stop", async () => {
  const { engine } = readyEngine();
  engine.setForeground(false);
  engine.setForeground(true);
  const discover = vi.spyOn(engine as unknown as { discover(): Promise<void> }, "discover").mockResolvedValue();
  const authorize = vi.spyOn(engine as unknown as { authorize(): Promise<void> }, "authorize").mockImplementation(async () => {
    (engine as unknown as { freshUntil: number }).freshUntil = Date.now() + 20_000;
  });

  engine.attach({ ready: true, send: vi.fn() } as never);
  await vi.waitFor(() => {
    expect(authorize).toHaveBeenCalledTimes(1);
    expect(discover).toHaveBeenCalledTimes(1);
  });
});

test("install of new security state and setEnabled(true) when stale re-authorize before discovering", async () => {
  const { engine } = readyEngine();
  const rec = { pin: { account: "acct" }, stateHash: Uint8Array.from({ length: 32 }, (_, i) => i + 1), member: true };
  Object.assign(engine, { leg: { ready: true, send: vi.fn() }, record: { ...rec, stateHash: new Uint8Array(32) } });
  const discover = vi.spyOn(engine as unknown as { discover(): Promise<void> }, "discover").mockResolvedValue();
  const authorize = vi.spyOn(engine as unknown as { authorize(): Promise<void> }, "authorize").mockImplementation(async () => {
    (engine as unknown as { freshUntil: number }).freshUntil = Date.now() + 20_000;
  });

  engine.install(rec as never);
  await vi.waitFor(() => {
    expect(authorize).toHaveBeenCalledTimes(1);
    expect(discover).toHaveBeenCalledTimes(1);
  });

  (engine as unknown as { freshUntil: number }).freshUntil = 0;
  engine.setEnabled("machine", true);
  await vi.waitFor(() => {
    expect(authorize).toHaveBeenCalledTimes(2);
    expect(discover).toHaveBeenCalledTimes(2);
  });
});

test("proactive keepalive renews security before 20s window expires without interrupting established session", async () => {
  vi.useFakeTimers();
  try {
    vi.stubGlobal("RTCPeerConnection", class {});
    const { engine, peer } = readyEngine();
    peer.paths.set(1, { ready: true });
    let authCalls = 0;
    const authorize = vi.spyOn(engine as unknown as { authorize(): Promise<void> }, "authorize").mockImplementation(async () => {
      authCalls++;
      (engine as unknown as { freshUntil: number }).freshUntil = Date.now() + 20_000;
    });
    vi.spyOn(engine as unknown as { discover(): Promise<void> }, "discover").mockResolvedValue();

    // Seed remaining freshness inside the 7s proactive threshold and advance timer 2.5s.
    (engine as unknown as { freshUntil: number }).freshUntil = Date.now() + 6_500;
    engine.attach({ ready: true, send: vi.fn() } as never);
    await vi.advanceTimersByTimeAsync(2_500);

    expect(authCalls).toBeGreaterThanOrEqual(1);
    expect(authorize).toHaveBeenCalled();
    expect(engine.status("machine").state).toBe("connected");
    engine.close();
  } finally {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  }
});

test("Control 15 subscription respects capabilities/readiness and routes PayloadKind.EVENT only when peer matches", async () => {
  vi.useFakeTimers();
  const { engine, peer } = readyEngine();
  try {
  vi.spyOn(engine as unknown as { refreshAndDiscover(): void }, "refreshAndDiscover").mockImplementation(() => {});
  vi.spyOn(engine as unknown as { discover(): Promise<void> }, "discover").mockResolvedValue();
  const sendControl = vi.fn<(_type: number, _payload: w.Bytes) => Promise<void>>(async () => {});
  Object.assign(peer.session, { sendControl });
  Object.assign(peer, { snapshotSeq: 0n, downloadSeq: 0n });
  peer.paths.set(1, { ready: true } as unknown as DirectChannel);

  engine.setWatchedSessions(new Map([["machine", ["s1", "s2", "s3", "s4", "s5"]]]));
  // Before Control 1 advertises type 15, nothing is sent.
  expect(sendControl).not.toHaveBeenCalled();

  const capsPayload = toBinary(CapabilitiesSchema, create(CapabilitiesSchema, {
    controlTypes: [15],
    expiresAtMs: BigInt(Date.now() + 30_000),
    fileDownload: true,
  }));
  (engine as unknown as { capability(peer: unknown, opened: unknown): void }).capability(peer, {
    type: 1,
    payload: capsPayload,
    context: { seq: 1n },
  });
  await Promise.resolve();
  expect(sendControl).toHaveBeenCalledTimes(1);
  expect(sendControl.mock.calls[0][0]).toBe(15);
  const decodedSub = fromBinary(DirectEventSubscriptionSchema, sendControl.mock.calls[0][1]);
  expect(decodedSub.sessionIds).toEqual(["s1", "s2", "s3", "s4"]);

  // A -> B -> A and [] -> s1 -> [] switches send the full new set immediately without waiting for any Ack.
  engine.setWatchedSessions(new Map([["machine", ["s2"]]]));
  engine.setWatchedSessions(new Map([["machine", ["s1", "s2", "s3", "s4"]]]));
  engine.setWatchedSessions(new Map());
  expect(sendControl).toHaveBeenCalledTimes(4);
  expect(fromBinary(DirectEventSubscriptionSchema, sendControl.mock.calls[1][1]).sessionIds).toEqual(["s2"]);
  expect(fromBinary(DirectEventSubscriptionSchema, sendControl.mock.calls[2][1]).sessionIds).toEqual(["s1", "s2", "s3", "s4"]);
  expect(fromBinary(DirectEventSubscriptionSchema, sendControl.mock.calls[3][1]).sessionIds).toEqual([]);

  // Same watched set does not send; attach() and Capabilities refresh each resend current set.
  engine.setWatchedSessions(new Map([["machine", ["s1"]]]));
  expect(sendControl).toHaveBeenCalledTimes(5);
  engine.setWatchedSessions(new Map([["machine", ["s1"]]]));
  expect(sendControl).toHaveBeenCalledTimes(5);

  engine.attach({ ready: true, send: vi.fn() } as never);
  expect(sendControl).toHaveBeenCalledTimes(6);

  (engine as unknown as { capability(peer: unknown, opened: unknown): void }).capability(peer, {
    type: 1,
    payload: toBinary(CapabilitiesSchema, create(CapabilitiesSchema, {
      controlTypes: [15],
      expiresAtMs: BigInt(Date.now() + 30_000),
      fileDownload: true,
    })),
    context: { seq: 2n },
  });
  expect(sendControl).toHaveBeenCalledTimes(7);
  expect(fromBinary(DirectEventSubscriptionSchema, sendControl.mock.calls[6][1]).sessionIds).toEqual(["s1"]);

  // Routing: only use=1, PayloadKind.EVENT, header.machineId === peer.id && header.sender === peer.id passes.
  const received: Array<{ machineId: string; sessionId: string; seq: bigint }> = [];
  engine.setEventHandler((machineId, _env, header) => {
    received.push({ machineId, sessionId: header.sessionId, seq: header.seq });
  });
  const mkEnv = (kind: PayloadKind, machineId: string, sender: string, seq: bigint) => toBinary(EnvelopeSchema, create(EnvelopeSchema, {
    header: toBinary(HeaderSchema, create(HeaderSchema, { kind, machineId, sender, sessionId: "s1", seq })),
  }));
  const frameFn = (engine as unknown as { frame(peer: unknown, raw: Uint8Array, use: number): void }).frame.bind(engine);

  frameFn(peer, mkEnv(PayloadKind.EVENT, "wrong-machine", "machine", 1n), 1);
  frameFn(peer, mkEnv(PayloadKind.EVENT, "machine", "wrong-sender", 2n), 1);
  frameFn(peer, mkEnv(PayloadKind.EVENT, "machine", "machine", 3n), 2);
  frameFn(peer, mkEnv(PayloadKind.COMMAND_RESULT, "machine", "machine", 4n), 1);
  frameFn(peer, mkEnv(PayloadKind.EVENT, "machine", "machine", 5n), 1);

  expect(received).toEqual([{ machineId: "machine", sessionId: "s1", seq: 5n }]);
  } finally {
    engine.close();
    vi.useRealTimers();
  }
});

test("sends Control 15 once when DataChannel opens after Capabilities", async () => {
  vi.useFakeTimers();
  const sdk = fakeRTC();
  const self = await fixture("client", 1), peerPub = await fixture("machine", 2), codec = await fixtureCodec();
  const machineId = w.text(peerPub.id);
  vi.stubGlobal("RTCPeerConnection", function () {
    const pc = sdk.factory();
    return pc;
  });
  const engine = new DirectEngine({ server: "https://example.com", deviceId: "client", devicePrivate: {} as CryptoKey }, async () => {}, () => {});
  try {
    const control15Subs: string[][] = [];
    const pendingOpens: Promise<void>[] = [];
    const leg = {
      ready: true,
      send: vi.fn((raw: Uint8Array) => {
        const frame = fromBinary(PeerFrameSchema, raw);
        if (frame.kind.case !== "directedPeerFrame") return true;
        const d = frame.kind.value;
        const delivery = w.pb(
          w.b(1, self.id),
          w.b(2, new Uint8Array(d.toDevice)),
          w.u(3, d.frameClass),
          w.b(4, new Uint8Array(d.frameId)),
          w.b(5, new Uint8Array(d.sealed)),
        );
        pendingOpens.push(
          codec.openSent(delivery, self, peerPub, Date.now()).then((opened) => {
            if (opened.context.cls === 2n && opened.type === 15) {
              const sub = fromBinary(DirectEventSubscriptionSchema, opened.payload);
              control15Subs.push(sub.sessionIds);
            }
          }),
        );
        return true;
      }),
    };
    const peerRecord = { id: machineId, kind: 1, ed: new Uint8Array(32), x: new Uint8Array(32) };
    Object.assign(engine, {
      leg,
      self,
      codec,
      head: "head",
      freshUntil: Date.now() + 20_000,
      record: { pin: { account: "acct", domain: new Uint8Array(32) }, member: peerRecord, states: [{ core: { epoch: 1n, members: [peerRecord] } }] },
    });
    vi.spyOn(engine as unknown as { discover(): Promise<void> }, "discover").mockResolvedValue();
    engine.setWatchedSessions(new Map([[machineId, ["s-delayed"]]]));

    const conn = new Uint8Array(16).fill(7);
    const createdPeer = (engine as unknown as { create(p: w.Public, c: w.Bytes, d: number): unknown }).create(peerPub, conn, Date.now() + 10_000) as {
      paths: Map<number, DirectChannel>;
      session: DirectSession;
      snapshotSeq: bigint;
      downloadSeq: bigint;
    };
    // Build use=1 peer connection via DirectSession.makePC while dc0 stays "connecting".
    const sessionPriv = createdPeer.session as unknown as { makePC(i: number): boolean; pcs: [{ channel: DirectChannel; gate: { peer: w.Public } }]; chain: Promise<void> };
    sessionPriv.pcs[0].gate.peer = peerPub;
    expect(sessionPriv.makePC(0)).toBe(true);
    const channel = sessionPriv.pcs[0].channel;
    Object.assign(channel, { confirmed: true });
    createdPeer.paths.set(1, channel);
    expect(channel.ready).toBe(false);

    // Control 1 (Capabilities with type 15) arrives before dc0 opens.
    const capsPayload = toBinary(CapabilitiesSchema, create(CapabilitiesSchema, {
      controlTypes: [15],
      expiresAtMs: BigInt(Date.now() + 30_000),
      fileDownload: true,
    }));
    (engine as unknown as { capability(peer: unknown, opened: unknown): void }).capability(createdPeer, {
      type: 1,
      payload: capsPayload,
      context: { seq: 1n },
    });
    await sessionPriv.chain;
    await Promise.all(pendingOpens);
    expect(leg.send).toHaveBeenCalledTimes(0);
    expect(control15Subs).toEqual([]);

    // Now SCTP finishes and dc0 opens -> triggers Control 15 once without waiting for 55s refresh.
    const dc0 = sdk.pcs[0].channels[0];
    dc0.readyState = "open";
    dc0.onopen?.();
    await sessionPriv.chain;
    expect(leg.send).toHaveBeenCalledTimes(1);
    await Promise.all(pendingOpens);
    expect(control15Subs).toEqual([["s-delayed"]]);

    // Subsequent open/ready callback for the same channel does not duplicate Control 15.
    (createdPeer.session as unknown as { options: { ready(u: number, ch: DirectChannel): void; open?(u: number): void } }).options.ready(1, channel);
    (createdPeer.session as unknown as { options: { open?(u: number): void } }).options.open?.(1);
    await sessionPriv.chain;
    await Promise.all(pendingOpens);
    expect(leg.send).toHaveBeenCalledTimes(1);
  } finally {
    engine.close();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  }
});
