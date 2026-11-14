import { afterEach, expect, test, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { Bucket, PathChanged, Recovery } from "./recovery";
import { SyntheticRemote } from "../test/remote-fixture";
import { ServerFrameSchema } from "../gen/remoteagent/v1/relay_pb";
import { CommandResultSchema } from "../gen/remoteagent/v1/remote_pb";
import { parseHeader, SecurityError } from "../device-security";
import type { Envelope } from "../gen/remoteagent/v1/envelope_pb";
import type { FrameStream } from "./path";

afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); });

test("default controllers share process credit across distinct targets", () => {
  let now=0;
  vi.spyOn(performance,"now").mockImplementation(() => now);
  const a=new Recovery(),b=new Recovery(),c=new Recovery(),d=new Recovery();
  expect([a.take(),a.take(),b.take(),b.take(),c.take()]).toEqual([true,true,true,true,false]);
  now=500;expect(c.take()).toBe(true);expect(d.take()).toBe(false);
  now=1000;expect(c.take()).toBe(true);expect(d.take()).toBe(false);
});

test("recovery preserves target/global credit and full jitter through notification storms", () => {
  let now = 0;
  const global = new Bucket(4, 2, () => now);
  const a = new Recovery(() => now, () => .5, global), b = new Recovery(() => now, () => .5, global);
  expect([a.take(), a.take(), a.take(), b.take(), b.take(), b.take()]).toEqual([true, true, false, true, true, false]);
  a.ready(); expect(a.take()).toBe(false);
  now = 1000; expect(a.take()).toBe(true);
  now += 1000;
  for (const cap of [1000, 2000, 4000, 8000, 16000, 30000, 30000]) {
    a.failed();
    for (let i = 0; i < 1000; i++) expect(a.delay()).toBe(cap / 2);
    now += cap / 2;
  }
});

test("only authenticated replacement wakes an HTTP waiter without treating cancellation as unsent", async () => {
  const r = new Recovery();
  let signal!: AbortSignal;
  const pending = r.follow(s => { signal = s; return new Promise(() => {}); });
  const result = pending.catch(e => e);
  r.ready();
  expect(await result).toBeInstanceOf(PathChanged);
  expect(signal.aborted).toBe(true);
});

test("duplicate network observations coalesce and changed observations cannot refill probe budgets", async () => {
  const remote = await SyntheticRemote.create();
  const runtime = remote as unknown as { connect(): Promise<void>; recovery: Recovery; networkGeneration: number };
  const connect = vi.spyOn(runtime, "connect").mockResolvedValue();
  try {
    vi.useFakeTimers();
    Object.defineProperty(remote, "recovery", { value: new Recovery(() => performance.now(), () => .5, new Bucket(4, 2)) });
    for (let i = 0; i < 1000; i++) remote.networkChanged("same-interface-new-address");
    await Promise.resolve();
    expect(runtime.networkGeneration).toBe(1); expect(connect).toHaveBeenCalledOnce();
    for (let i = 0; i < 1000; i++) remote.networkChanged(String(i));
    await Promise.resolve();
    expect(runtime.networkGeneration).toBe(2); expect(connect).toHaveBeenCalledTimes(2);
    remote.networkChanged("next-route"); await Promise.resolve();
    remote.networkChanged("latest-route"); await Promise.resolve();
    await vi.advanceTimersByTimeAsync(999); expect(connect).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1); expect(connect).toHaveBeenCalledTimes(3);
  } finally { vi.useRealTimers(); await remote.dispose(); }
});

test("network generation refreshes security before authorizing another delivery", async () => {
  const remote = await SyntheticRemote.create();
  vi.spyOn(remote as unknown as { connect(): Promise<void> }, "connect").mockResolvedValue();
  const sync = vi.spyOn(remote.security, "sync").mockRejectedValue(new SecurityError("clock"));
  const forward = vi.fn();
  (remote as unknown as { api: unknown }).api = { forward };
  try {
    remote.networkChanged("wake-after-clock-gap");
    await Promise.resolve();
    await expect(remote.machineCommand(remote.machine, { case: "getSettings", value: {} })).rejects.toMatchObject({ code: "clock" });
    expect(sync).toHaveBeenCalled(); expect(forward).not.toHaveBeenCalled();
  } finally { sync.mockRestore(); await remote.dispose(); }
});

test("a new network retires a stale security HTTP round and old replies cannot install state", async () => {
  const remote = await SyntheticRemote.create();
  const real = remote.world.fixture.fetcher(remote.member.account.deviceId);
  let release!: (value: unknown) => void, started!: () => void;
  const entered = new Promise<void>(resolve => { started = resolve; });
  let requests = 0;
  (remote as unknown as { api: unknown }).api = { getSecurityStates: async (q: { afterEpoch: bigint; limit: number }) => {
    if (++requests === 1) { started(); return new Promise(resolve => { release = resolve; }); }
    return real(q.afterEpoch,q.limit);
  } };
  const first = remote.security.sync();
  try {
    await entered;
    remote.security.networkChanged();
    const next = remote.security.sync();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const completed = await Promise.race([next.then(() => true), new Promise<boolean>(resolve => { timer=setTimeout(() => resolve(false),1000); })]);
    clearTimeout(timer);
    expect(completed, "obsolete metadata must not hold the new network").toBe(true);
    const record = await next;
    let callerTimer: ReturnType<typeof setTimeout> | undefined;
    const callerCompleted=await Promise.race([first.then(() => true),new Promise<boolean>(resolve => {callerTimer=setTimeout(() => resolve(false),1000);})]);
    clearTimeout(callerTimer);
    expect(callerCompleted,"the original sync caller should join the fresh round").toBe(true);
    release({ states: [], headEpoch: record.states.at(-1)!.core.epoch, recoveryPending: true });
    await new Promise(resolve => setTimeout(resolve,0));
    expect(remote.security.recoveryPending).toBe(false);
  } finally { release?.({ states: [], headEpoch: 0n }); await first.catch(() => undefined); await remote.dispose(); }
});

test("candidate Ready swaps atomically and old callbacks cannot close the replacement or terminal", async () => {
  const remote = await SyntheticRemote.create();
  const runtime = remote as unknown as { ws?: FrameStream; candidate?: FrameStream; generation: number; closed: boolean; openStream(g: number): void };
  const old: FrameStream = { ready: true, send: vi.fn(), close: vi.fn() };
  const fresh: FrameStream = { ready: true, send: vi.fn(), close: vi.fn() };
  vi.spyOn(remote.deviceLink, "openPathStream").mockReturnValueOnce(old).mockReturnValueOnce(fresh);
  const close = vi.fn();
  remote.deviceLink.openStream(close, vi.fn());
  vi.spyOn(remote, "refresh").mockResolvedValue();
  const frame = () => toBinary(ServerFrameSchema, create(ServerFrameSchema, { kind: { case: "ready", value: {} } })).buffer as ArrayBuffer;
  try {
    vi.useFakeTimers(); runtime.closed = false;
    runtime.openStream(runtime.generation); old.onFrame?.(frame());
    await Promise.resolve();
    runtime.openStream(runtime.generation);
    expect(runtime.ws).toBe(old); expect(old.close).not.toHaveBeenCalled();
    fresh.onFrame?.(frame()); await Promise.resolve();
    expect(runtime.ws).toBe(fresh); expect(old.close).toHaveBeenCalledOnce();
    old.onEnd?.(); expect(runtime.ws).toBe(fresh); expect(close).not.toHaveBeenCalled();
  } finally { vi.useRealTimers(); await remote.dispose(); }
});

test("a recovered relay retries the exact original command instead of waiting for stale HTTP", async () => {
  const remote = await SyntheticRemote.create();
  const bodies: { command: Envelope; attemptId: string }[] = [];
  let handed!: () => void;
  const first = new Promise<void>(resolve => { handed = resolve; });
  (remote as unknown as { api: unknown }).api = {
    forward: async (q: { command: Envelope; attemptId: string }) => {
      bodies.push(q);
      if (bodies.length === 1) { handed(); return new Promise(() => {}); }
      const h = parseHeader(new Uint8Array(q.command.securityHeader));
      return { result: await remote.seal({ kind: 6, requestId: h.request }, CommandResultSchema, create(CommandResultSchema)) };
    },
  };
  try {
    const call = remote.machineCommand(remote.machine, { case: "getSettings", value: {} });
    await first;
    (remote as unknown as { recovery: Recovery }).recovery.ready();
    await call;
    expect(bodies).toHaveLength(2);
    expect(bodies[1].command).toBe(bodies[0].command);
    expect(bodies[1].attemptId).not.toBe(bodies[0].attemptId);
  } finally { await remote.dispose(); }
});

test("a temporary daemon gap keeps the original live call pending within the recovery budget", async () => {
  const remote=await SyntheticRemote.create();
  const bodies:{command:Envelope;attemptId:string}[]=[];
  const runtime=remote as unknown as {begun:boolean;api:unknown;connect():Promise<void>};
  runtime.begun=true;
  vi.spyOn(runtime,"connect").mockResolvedValue();
  runtime.api={forward:async(q:{command:Envelope;attemptId:string})=>{
    bodies.push(q);
    if(bodies.length===1)throw new ConnectError("temporary path gap",Code.Unavailable);
    const h=parseHeader(new Uint8Array(q.command.securityHeader));
    return {result:await remote.seal({kind:6,requestId:h.request},CommandResultSchema,create(CommandResultSchema))};
  }};
  try {
    await expect(remote.machineCommand(remote.machine,{case:"getSettings",value:{}})).resolves.toBeDefined();
    expect(bodies).toHaveLength(2);expect(bodies[1].command).toBe(bodies[0].command);
  } finally {await remote.dispose();}
});
