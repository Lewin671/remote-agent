import { afterEach, expect, test, vi } from "vitest";
import { DirectSession } from "./direct-session";
import { DirectBudget, type DirectChannel } from "./direct-channel";
import type { Signal } from "./peer/signal";
import * as w from "./peer/wire";
import type { CandidateBatcher, Sender } from "./peer/sender";
import { fixture, fixtureCodec } from "../test/peer/fixture";
import { fakeRTC } from "../test/peer/fake-rtc";

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
