import { afterEach, expect, test, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { SyntheticRemote } from "../test/remote-fixture";
import { SecurityError } from "../device-security";
const remotes:SyntheticRemote[]=[];
afterEach(async()=>{vi.useRealTimers();for(const r of remotes.splice(0))await r.dispose();vi.restoreAllMocks();});
async function fixture(){const r=await SyntheticRemote.create();remotes.push(r);return r;}
async function drain(){await vi.advanceTimersByTimeAsync(0);}

test("a full successful sync releases the failed wake barrier without another hint",async()=>{
 const r=await fixture(), rt=r as any;
 vi.spyOn(rt,"connect").mockResolvedValue(undefined);
 const touch=vi.spyOn(r.security,"touch").mockRejectedValue(new SecurityError("clock"));
 r.networkChanged("wake");await Promise.resolve();await rt.wakeSecurity.catch(()=>undefined);
 rt.token="fixture";rt.tokenExpires=Date.now()+2*86_400_000;
 expect(await rt.preStream(rt.generation,()=>{},new AbortController().signal)).toBe(true);
 await expect(rt.ensureWakeSecurity()).resolves.toBeUndefined();
 expect(touch).toHaveBeenCalledTimes(1);
});

test("a failed local Touch can be retried safely by the next operation",async()=>{
 const r=await fixture(),rt=r as any;vi.spyOn(rt,"connect").mockResolvedValue(undefined);
 const touch=vi.spyOn(r.security,"touch").mockRejectedValueOnce(new SecurityError("storage")).mockResolvedValue(undefined);
 r.networkChanged("wake");await Promise.resolve();await rt.wakeSecurity.catch(()=>undefined);
 await expect(rt.ensureWakeSecurity()).resolves.toBeUndefined();expect(touch).toHaveBeenCalledTimes(2);
});

async function within<T>(promise:Promise<T>,ms=1000):Promise<{value?:T;error?:unknown;expired?:boolean}> {
 let timer:ReturnType<typeof setTimeout>|undefined;
 try{return await Promise.race([promise.then(value=>({value}),error=>({error})),new Promise<{expired:boolean}>(resolve=>{timer=setTimeout(()=>resolve({expired:true}),ms)})]);}
 finally{clearTimeout(timer);}
}

test("a security pull owns its timeout and a subsequent caller can start a fresh round",async()=>{
 const r=await fixture(), rt=r as any;vi.spyOn(rt,"roundBudget").mockReturnValue(100);
 const record=await r.security.verifiedRecord();let old:AbortSignal|undefined;
 vi.spyOn(r.security as any,"syncLoop").mockImplementationOnce((...args:unknown[])=>{old=args[0] as AbortSignal;return new Promise(()=>{});}).mockResolvedValue(record);
 const first=r.security.sync();
 try {const outcome=await within(first);expect(outcome.expired).not.toBe(true);expect(outcome.error).toMatchObject({code:Code.DeadlineExceeded});
  expect(old?.aborted).toBe(true);expect(r.security.recoveryStatusStale).toBe(true);
  await expect(r.security.sync()).resolves.toBeDefined();
 }finally{r.stop();await first.catch(()=>undefined);}
});

test("a login owner expires without silently retrying forever",async()=>{
 const r=await fixture(),rt=r as any;vi.spyOn(rt,"roundBudget").mockReturnValue(100);
 vi.spyOn(rt,"signIn").mockImplementationOnce(()=>new Promise(()=>{})).mockResolvedValue(undefined);
 const first=rt.login() as Promise<void>;
 try {const outcome=await within(first);expect(outcome.expired).not.toBe(true);expect(outcome.error).toBeInstanceOf(ConnectError);
  expect(rt.loggingIn).toBeUndefined();await expect(rt.login()).resolves.toBeUndefined();
 }finally{r.stop();await first.catch(()=>undefined);}
});

test("stop followed by start retires the paused round even before any Ready",async()=>{
 const r=await fixture(),rt=r as any;vi.useFakeTimers();vi.spyOn(rt,"connect").mockResolvedValue(undefined);
 const retirement=vi.spyOn(r.security,"networkChanged");r.start();await drain();expect(retirement).not.toHaveBeenCalled();
 r.stop();const before=retirement.mock.calls.length;rt.wakeSecurity=Promise.reject(new Error("obsolete"));rt.wakeSecurity.catch(()=>{});
 r.start();await drain();expect(retirement.mock.calls.length).toBeGreaterThan(before);expect(rt.networkGeneration).toBeGreaterThan(0);
});

test("a directory round times out and does not poison its successor",async()=>{
 const r=await fixture(),rt=r as any;vi.spyOn(rt,"roundBudget").mockReturnValue(500);
 let calls=0;let old:AbortSignal|undefined;
 rt.api={listDevices:async(_q:unknown,options:{signal:AbortSignal})=>{if(++calls===1){old=options.signal;return new Promise(()=>{});}return {devices:[]};}};
 const first=r.loadDevices(false);
 try {expect((await within(first,1500)).expired).not.toBe(true);expect(old?.aborted).toBe(true);expect(rt.directoryJob).toBeUndefined();
  await r.loadDevices(false);expect(calls).toBe(2);expect(r.devicesStatus).toBe("ready");
 }finally{r.stop();await first; }
});

test("a security gate renews the rejected token before retrying, without a main socket",async()=>{
 const r=await fixture(),rt=r as any;const real=r.world.fixture.fetcher(r.member.account.deviceId);let calls=0;
 rt.token="rejected";
 const login=vi.spyOn(rt,"login").mockImplementation(async()=>{rt.token="renewed"});
 rt.api={getSecurityStates:async(q:{afterEpoch:bigint;limit:number})=>{if(++calls===1)throw new ConnectError("rejected",Code.Unauthenticated);return real(q.afterEpoch,q.limit);}};
 await expect(r.security.sync()).resolves.toBeDefined();expect(login).toHaveBeenCalledOnce();expect(calls).toBe(2);
});

test("an old successful sync cannot release a newer failed wake barrier",async()=>{
 const r=await fixture(),rt=r as any;vi.spyOn(rt,"connect").mockResolvedValue(undefined);
 const record=await r.security.verifiedRecord();let release!:(v:typeof record)=>void,entered!:()=>void;
 const started=new Promise<void>(resolve=>{entered=resolve});
 vi.spyOn(r.security,"touch").mockRejectedValue(new SecurityError("clock"));
 vi.spyOn(r.security,"sync").mockImplementationOnce(()=>{entered();return new Promise(resolve=>{release=resolve})});
 r.networkChanged("first");await Promise.resolve();await rt.wakeSecurity.catch(()=>undefined);
 rt.token="fixture";rt.tokenExpires=Date.now()+2*86_400_000;
 const old=rt.preStream(rt.generation,()=>{},new AbortController().signal);await started;
 r.networkChanged("newer");await Promise.resolve();await rt.wakeSecurity.catch(()=>undefined);
 release(record);await old;
 await expect(rt.ensureWakeSecurity()).rejects.toMatchObject({code:"clock"});
});
