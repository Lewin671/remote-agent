import { afterEach, expect, test, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { SyntheticRemote } from "../test/remote-fixture";
import { CommandResultSchema } from "../gen/remoteagent/v1/remote_pb";
import { DaemonSettingsSchema } from "../gen/remoteagent/v1/local_pb";
const remotes: SyntheticRemote[]=[];
afterEach(async()=>{for(const r of remotes.splice(0)) await r.dispose();vi.restoreAllMocks();});
async function remote(){const r=await SyntheticRemote.create();remotes.push(r);return r;}
function deferred<T>() {let resolve!:(v:T)=>void,reject!:(e:unknown)=>void;const promise=new Promise<T>((a,b)=>{resolve=a;reject=b});return {promise,resolve,reject};}

test("canceling a login waiter keeps the shared job tracked through retirement and successor settlement",async()=>{
 const r=await remote(), rt=r as any;
 const old=deferred<void>(), next=deferred<void>();const signals:AbortSignal[]=[];
 vi.spyOn(rt,"signIn").mockImplementation((...args:unknown[])=>{signals.push(args[0] as AbortSignal);return signals.length===1?old.promise:next.promise});
 const canceled=new AbortController();const first=rt.login(canceled.signal) as Promise<void>;
 canceled.abort();await expect(first).rejects.toBeDefined();
 const original=rt.loggingIn;expect(original).toBeDefined();
 const other=rt.login() as Promise<void>;expect(signals).toHaveLength(1);
 rt.retireNetworkIO();expect(signals[0].aborted).toBe(true);
 const latest=rt.login() as Promise<void>;const owner=rt.loggingIn;
 old.reject(new DOMException("retired","AbortError"));await Promise.resolve();await Promise.resolve();
 expect(rt.loggingIn).toBe(owner);expect(signals).toHaveLength(2);
 next.resolve();await Promise.all([other,latest]);expect(rt.loggingIn).toBeUndefined();
});

test("canceling a security waiter cannot clear the pull or mark its successful owner stale",async()=>{
 const r=await remote();const real=r.world.fixture.fetcher(r.member.account.deviceId);
 const started=deferred<void>(), release=deferred<void>();let calls=0;
 (r as any).api={getSecurityStates:async(q:{afterEpoch:bigint;limit:number})=>{calls++;started.resolve();await release.promise;return real(q.afterEpoch,q.limit);}};
 const canceled=new AbortController(), wasStale=r.security.recoveryStatusStale;
 const first=r.security.sync(canceled.signal);await started.promise;
 canceled.abort();await expect(first).rejects.toBeDefined();
 const owner=(r.security as any).syncJob;expect(owner).toBeDefined();expect(r.security.recoveryStatusStale).toBe(wasStale);
 const other=r.security.sync();await Promise.resolve();expect(calls).toBe(1);
 release.resolve();await other;expect(calls).toBe(1);expect((r.security as any).syncJob).toBeUndefined();
});

test.each([false,true])("a retired settings read cannot clear or overwrite its replacement (failure=%s)",async(failure)=>{
 const r=await remote();const old=deferred<any>(), next=deferred<any>();
 const send=vi.spyOn(r,"machineCommand").mockImplementationOnce(()=>old.promise).mockImplementationOnce(()=>next.promise);
 const first=r.settings(r.machine,true).catch(()=>undefined);
 const second=r.settings(r.machine,true);
 if(failure) {
  old.reject(new Error("old read failed"));await first;
  expect(r.settings(r.machine)).toBe(second);expect(send).toHaveBeenCalledTimes(2);
 }
 const result=create(CommandResultSchema,{kind:{case:"getSettings",value:{settings:create(DaemonSettingsSchema)}}});
 next.resolve(result);const settings=await second;
 if(!failure){old.resolve(create(CommandResultSchema,{kind:{case:"getSettings",value:{settings:create(DaemonSettingsSchema)}}}));await first;}
 expect(r.cachedSettings(r.machine)).toBe(settings);expect(r.settings(r.machine)).toBe(second);
});

test("a hidden main loop still retires the old network of an independent login",async()=>{
 const r=await remote(),rt=r as any,old=deferred<void>();let signal!:AbortSignal;
 const connect=vi.spyOn(rt,"connect").mockResolvedValue(undefined);
 vi.spyOn(rt,"signIn").mockImplementationOnce((...args:unknown[])=>{signal=args[0] as AbortSignal;return old.promise}).mockResolvedValue(undefined);
 const caller=rt.login() as Promise<void>;
 try {
  r.setForeground(false);r.networkChanged("new-hidden-route");await Promise.resolve();
  expect(signal.aborted).toBe(true);expect(connect).not.toHaveBeenCalled();
  old.reject(new DOMException("retired","AbortError"));await caller;
 } finally {r.stop();old.reject(new Error("fixture stopped"));await caller.catch(()=>undefined);}
});
