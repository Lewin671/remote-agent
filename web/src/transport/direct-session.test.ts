import { afterEach, expect, test, vi } from "vitest";
import { DirectSession } from "./direct-session";
import { DirectBudget, type DirectChannel } from "./direct-channel";
import type { Signal } from "./peer/signal";
import * as w from "./peer/wire";
import type { CandidateBatcher, Sender } from "./peer/sender";
import { fixture, fixtureCodec } from "../test/peer/fixture";
import { fakeRTC } from "../test/peer/fake-rtc";
import { RoundDiag, verdictOf } from "./direct-diag";

type Owner = { pcs: { gate: Signal; channel?: DirectChannel; batch: CandidateBatcher; ended: boolean }[]; makePC(i:number): boolean; resetPC(i:number): void; tick(): Promise<void>; queue(work:()=>Promise<void>):Promise<void>; chain: Promise<void> };
function deferred() { let resolve!:()=>void;const promise=new Promise<void>(r=>resolve=r);return {promise,resolve}; }
afterEach(()=>vi.useRealTimers());

test.each(["end", "candidate", "connected", "failed"])("queued old PC %s callback cannot affect its replacement", async event=>{
  vi.useFakeTimers();
  const sdk=fakeRTC(),closed=vi.fn(),self=await fixture("client",1),peer=await fixture("machine",2);
  const session=new DirectSession({self,peer,codec:await fixtureCodec(),connection:new Uint8Array(16),deadline:Date.now()+10000,budget:new DirectBudget(),stun:[],valid:()=>true,authorize:async()=>{},send:()=>true,chosen:()=>{},beforeOffer:()=>{},ready:()=>{},closed,frame:()=>{},capability:()=>{},factory:sdk.factory});
  const owner=session as unknown as Owner;
  try {
    expect(owner.makePC(0)).toBe(true);const old=sdk.pcs[0],blocked=deferred();
    void owner.queue(()=>blocked.promise);
    if(event==="end")old.candidate(null);
    else if(event==="candidate")old.candidate("old candidate");
    else old.state(event as RTCPeerConnectionState);
    closed.mockClear(); // A real failure before replacement may close the original PC.
    owner.pcs[0].gate.nextGeneration(2n);owner.resetPC(0);expect(owner.makePC(0)).toBe(true);
    const replacement=owner.pcs[0].channel!;
    // Bootstrap identities are already known; the test controls only SDK delivery.
    owner.pcs[0].gate.peer=peer;
    Object.assign(owner.pcs[0],{ufrag:new TextEncoder().encode("fixture-ufrag"),mid:new TextEncoder().encode("0")});
    blocked.resolve();await owner.chain;await owner.tick();
    sdk.pcs[1].candidate("candidate:1 1 udp 1 127.0.0.1 1234 typ host");await owner.chain;
    expect(replacement.signal.aborted).toBe(false);
    expect(owner.pcs[0].gate.generation).toBe(2n);
    expect(owner.pcs[0].gate.dtls).toBe(false);
    expect(owner.pcs[0].batch.pending).toHaveLength(1);
    expect(closed).not.toHaveBeenCalled();
    sdk.pcs[1].state("connected");await owner.chain;
    expect(owner.pcs[0].gate.dtls).toBe(true);
    sdk.pcs[1].channels[0].close();expect(closed).toHaveBeenCalledWith(1);
  } finally {session.close();}
});


test.each([2,3])("queued receipt %i from a replaced main leg cannot pause the new sender",async status=>{
 vi.useFakeTimers();const self=await fixture("client",1),peer=await fixture("machine",2);
 const session=new DirectSession({self,peer,codec:await fixtureCodec(),connection:new Uint8Array(16),deadline:Date.now()+10000,budget:new DirectBudget(),stun:[],valid:()=>true,authorize:async()=>{},send:()=>true,chosen:()=>{},beforeOffer:()=>{},ready:()=>{},closed:()=>{},frame:()=>{},capability:()=>{}});
 const owner=session as unknown as Owner & {boot:Sender};const id=new Uint8Array(16);
 try {
  await owner.boot.enqueue(w.pb(w.b(4,id)),Date.now(),Date.now()+10000,1);
  const blocked=deferred();void owner.queue(()=>blocked.promise);let generation=1;
  session.delivery(id,status,1000,()=>generation===1);generation=2;session.discovered();blocked.resolve();await owner.chain;
  expect(owner.boot.paused).toBe(false);expect([...owner.boot.pending.values()][0].notBefore).toBe(0);
  session.delivery(id,status,1000,()=>generation===2);await owner.chain;
  if(status===2)expect(owner.boot.paused).toBe(true);else expect([...owner.boot.pending.values()][0].notBefore).toBeGreaterThan(0);
 }finally{session.close();}
});

test.each([
 ["the computer never answered the handshake","no_response",false,false],
 ["the handshake worked but STUN gave no address","no_traversal",true,false],
 ["the handshake worked and STUN answered","",true,true],
])("a round that ends at its deadline names a cause only when %s",async(_name,expected,boot,stun)=>{
 vi.useFakeTimers();
 const sdk=fakeRTC(),closed=vi.fn(),self=await fixture("client",1),peer=await fixture("machine",2);
 const session=new DirectSession({self,peer,codec:await fixtureCodec(),connection:new Uint8Array(16),deadline:Date.now()+10000,budget:new DirectBudget(),stun:[],valid:()=>true,authorize:async()=>{},send:()=>true,chosen:()=>{},beforeOffer:()=>{},ready:()=>{},closed,frame:()=>{},capability:()=>{},factory:sdk.factory});
 const owner=session as unknown as Owner;
 try {
  if(boot){owner.pcs[0].gate.bootReady=true;expect(owner.makePC(0)).toBe(true);}
  if(stun)(owner.pcs[0].channel as unknown as {stunReady:boolean}).stunReady=true;
  vi.setSystemTime(Date.now()+10001);await owner.tick();
  expect(closed).toHaveBeenCalled();expect(session.failure ?? "").toBe(expected);
 } finally {session.close();}
});

test.each([
 ["never answered the handshake","no_response","no_reply",false],
 ["answered but no description was exchanged (and STUN gave nothing)","no_traversal","no_sdp",true],
])("the diagnostics record of a round that ends at its deadline when the computer %s",async(_name,end,verdict,boot)=>{
 vi.useFakeTimers();
 const sdk=fakeRTC(),diag=new RoundDiag(1),self=await fixture("client",1),peer=await fixture("machine",2);
 const session=new DirectSession({self,peer,codec:await fixtureCodec(),connection:new Uint8Array(16),deadline:Date.now()+10000,budget:new DirectBudget(),stun:[],valid:()=>true,authorize:async()=>{},send:()=>true,chosen:()=>{},beforeOffer:()=>{},ready:()=>{},closed:()=>{},frame:()=>{},capability:()=>{},factory:sdk.factory,diag});
 const owner=session as unknown as Owner;
 try {
  if(boot){owner.pcs[0].gate.bootReady=true;expect(owner.makePC(0)).toBe(true);}
  await owner.tick();
  vi.setSystemTime(Date.now()+10001);await owner.tick();
  expect(diag.data.end).toBe(end);expect(verdictOf(diag.data)).toBe(verdict);
  expect(diag.data.pcs[0].marks.boot!==undefined).toBe(boot);
 } finally {session.close();}
});

test("closing a round that never timed out records that it was cancelled, and a delivery refusal is counted",async()=>{
 vi.useFakeTimers();
 const diag=new RoundDiag(1),self=await fixture("client",1),peer=await fixture("machine",2);
 const session=new DirectSession({self,peer,codec:await fixtureCodec(),connection:new Uint8Array(16),deadline:Date.now()+10000,budget:new DirectBudget(),stun:[],valid:()=>true,authorize:async()=>{},send:()=>true,chosen:()=>{},beforeOffer:()=>{},ready:()=>{},closed:()=>{},frame:()=>{},capability:()=>{},diag});
 const owner=session as unknown as Owner & {boot:Sender};const id=new Uint8Array(16);
 await owner.boot.enqueue(w.pb(w.b(4,id)),Date.now(),Date.now()+10000,1);
 session.delivery(id,3,1000,()=>true);session.delivery(id,2,0,()=>true);await owner.chain;
 expect(diag.data.signals).toEqual({unsupported:false,limited:1,offline:1});
 session.close();expect(diag.data.end).toBe("cancelled");
 session.close();expect(diag.data.end).toBe("cancelled");
});

test("Control 10 (Unsupported) for one-shot Control 15 is ignored without failing session, closing PCs, or retrying", async () => {
 vi.useFakeTimers();
 const sdk = fakeRTC(), closed = vi.fn(), sent: w.Bytes[] = [];
 const self = await fixture("client", 1), peer = await fixture("machine", 2), codec = await fixtureCodec();
 const session = new DirectSession({
  self, peer, codec, connection: new Uint8Array(16), deadline: Date.now() + 10_000,
  budget: new DirectBudget(), stun: [], valid: () => true, authorize: async () => {},
  send: (out) => { sent.push(out.raw); return true; },
  chosen: () => {}, beforeOffer: () => {}, ready: () => {}, closed, frame: () => {}, capability: () => {}, factory: sdk.factory,
 });
 const owner = session as unknown as Owner;
 try {
  expect(owner.makePC(0)).toBe(true);
  owner.pcs[0].gate.peer = peer;
  await session.sendControl(15, w.pb(w.b(1, w.utf8("sess-1"))));
  expect(sent).toHaveLength(1);

  const opened = await codec.open(sent[0], self, peer, Date.now());
  const ctx = w.context(peer, self, new Uint8Array(16), 0, 1, 1n, Date.now(), 2);
  const unsupBody = w.pb(w.u(1, 2), w.u(2, 15), w.u(3, opened.context.seq), w.b(4, opened.hash));
  const unsupRaw = await codec.seal(peer, self, ctx, 10, unsupBody);
  expect(session.receive(unsupRaw)).toBe(true);
  await owner.chain;

  // Advancing past retry intervals and ticking does not retransmit one-shot Control 15.
  vi.setSystemTime(Date.now() + 1_600);
  await owner.tick();
  expect(sent).toHaveLength(1);
  expect(session.failure).toBeUndefined();
  expect(owner.pcs[0].gate.closed).toBe(false);
  expect(closed).not.toHaveBeenCalled();
 } finally {
  session.close();
 }
});
