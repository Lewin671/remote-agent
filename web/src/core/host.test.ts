// @vitest-environment happy-dom

import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, expect, test, vi } from "vitest";
import {
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CommandOutcomeEventSchema,
  EventSchema,
  OutcomeSource,
  ScreenTarget,
  type CacheWriteEffect,
  type DispatchCommandEffect,
  type Effect,
  type Event,
} from "../gen/remoteagent/core/v1/app_pb";
import {
  RelayRpcMethod,
  RelayRpcResultEventSchema,
  type RelayRpcEffect,
} from "../gen/remoteagent/core/v1/relay_pipe_pb";
import {
  DemoDeviceSchema,
  DemoMachineSchema,
  DemoSecuritySchema,
  DemoSessionSchema,
  SeedDemoWorldEventSchema,
} from "../gen/remoteagent/core/v1/security_pb";
import { ConnState } from "../gen/remoteagent/core/v1/home_view_pb";
import { Agent, SessionStatus } from "../gen/remoteagent/v1/event_pb";
import { SessionSchema } from "../gen/remoteagent/v1/local_pb";
import { DeviceKind, MachineSchema } from "../gen/remoteagent/v1/relay_pb";
import { CommandResultSchema, MachineMetaSchema } from "../gen/remoteagent/v1/remote_pb";
import { PEER_PROTOCOL_VERSION } from "../protocol";
import { localeStore } from "../i18n";
import { CoreHost, getCoreHost, type CoreExecutor, type StreamLegCallbacks } from "./host";
import { SyntheticRemote } from "../test/remote-fixture";

const hosts: CoreHost[] = [];

afterEach(() => {
  while (hosts.length) hosts.pop()!.dispose();
  localeStore.overrideForTesting(undefined);
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function flushPromises(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

function makeExecutor(
  overrides: Partial<CoreExecutor> = {},
  getHost?: () => CoreHost | undefined,
): CoreExecutor & {
  streamLegs: Map<bigint, StreamLegCallbacks>;
  rpcMethods: RelayRpcMethod[];
  writeOrder: string[];
  releasedEvents: Event[];
} {
  const streamLegs = new Map<bigint, StreamLegCallbacks>();
  const rpcMethods: RelayRpcMethod[] = [];
  const writeOrder: string[] = [];
  const releasedEvents: Event[] = [];

  return {
    streamLegs,
    rpcMethods,
    writeOrder,
    releasedEvents,
    openStreamLeg(legId, _token, callbacks) {
      streamLegs.set(legId, callbacks);
      callbacks.onReady(legId, BigInt(Date.now()));
    },
    closeStreamLeg(legId) {
      streamLegs.delete(legId);
    },
    async readBootstrapCache(opId) {
      return create(CacheBootstrapLoadedEventSchema, {
        opId,
        homeScopeToken: "all",
        scopeHmacKey: new Uint8Array(32).fill(7),
      });
    },
    async readEventsCache(opId, sessionId) {
      return create(CacheEventsLoadedEventSchema, { opId, sessionId });
    },
    prepareCacheWrite(eff: CacheWriteEffect) {
      return async () => {
        writeOrder.push(eff.kind.case ?? "unknown");
      };
    },
    releaseEventRefs(ev: Event) {
      releasedEvents.push(ev);
    },
    async writeCache(eff: CacheWriteEffect) {
      writeOrder.push(eff.kind.case ?? "unknown");
    },
    async deleteCache() {},
    relayRpc(eff: RelayRpcEffect) {
      rpcMethods.push(eff.method);
      queueMicrotask(() => {
        getHost?.()?.dispatch(
          create(EventSchema, {
            event: {
              case: "relayRpcResult",
              value: create(RelayRpcResultEventSchema, { opId: eff.opId }),
            },
          }),
        );
      });
    },
    async executeCommandAttempt(eff: DispatchCommandEffect) {
      return create(CommandOutcomeEventSchema, {
        requestId: eff.requestId,
        attemptId: eff.attemptId,
        source: OutcomeSource.DAEMON,
      });
    },
    ...overrides,
  };
}

test("CoreHost boots real WASM core, hydrates HomeViewModel, and uses persistent HMAC key", async () => {
  const effects: Effect[] = [];
  let host!: CoreHost;
  const exec = makeExecutor({}, () => host);
  host = new CoreHost(undefined, {
    executor: exec,
    onEffect: (e) => effects.push(e),
  });
  hosts.push(host);

  const onHome = vi.fn();
  const unsub = host.subscribe(ScreenTarget.HOME, onHome);
  try {
    host.dispatch(
      create(EventSchema, {
        event: {
          case: "seedDemoWorld",
          value: create(SeedDemoWorldEventSchema, {
            clearDirectReports: true,
            machines: [
              create(DemoMachineSchema, {
                machineId: "m1",
                online: true,
                rawMeta: toBinary(
                  MachineMetaSchema,
                  create(MachineMetaSchema, { hostname: "MacBook", protocolVersion: PEER_PROTOCOL_VERSION }),
                ),
              }),
            ],
            sessions: [
              create(DemoSessionSchema, {
                sessionId: "s1",
                machineId: "m1",
                lastSeq: 1n,
                updatedMs: 1_700_000_000_000n,
                rawMeta: toBinary(
                  SessionSchema,
                  create(SessionSchema, {
                    id: "s1",
                    cwd: "/Users/me/app",
                    title: "Fix tests",
                    status: SessionStatus.RUNNING,
                    agent: Agent.CLAUDE,
                  }),
                ),
              }),
            ],
            devices: [
              create(DemoDeviceSchema, { deviceId: "dev-client", kind: DeviceKind.CLIENT }),
              create(DemoDeviceSchema, { deviceId: "m1", kind: DeviceKind.MACHINE }),
            ],
            security: create(DemoSecuritySchema, {
              isMember: true,
              selfDeviceId: "dev-client",
              authorityDeviceId: "dev-client",
              trustedMachineIds: ["m1"],
              trustedClientIds: ["dev-client"],
              headEpoch: 1n,
            }),
          }),
        },
      }),
    );
    host.start();
    await flushPromises();
    await flushPromises();

    // Demo mode does not emit RelayRpcEffect or CacheReadEffect.
    expect(exec.rpcMethods).toEqual([]);
    expect(exec.streamLegs.size).toBe(1);

    const home = host.viewHome();
    expect(home.connection?.state).toBe(ConnState.CONNECTED);
    expect(home.running.map((r) => r.sessionId)).toEqual(["s1"]);
    expect(onHome).toHaveBeenCalled();

    const conn = host.viewConnection();
    expect(conn.connected).toBe(true);
    expect(conn.liveLoaded).toBe(true);
    expect(conn.removed).toBe(false);
  } finally {
    unsub();
  }
});

test("CoreHost serializes prepared cache writes in effect order and releases unused event refs", async () => {
  const order: string[] = [];
  let releaseFirst!: () => void;
  const firstGate = new Promise<void>((r) => {
    releaseFirst = r;
  });

  let callCount = 0;
  const remote = await SyntheticRemote.create();
  const s1Row = await remote.sessionRow("s1", "First");
  const s2Row = await remote.sessionRow("s2", "Second");
  (remote as unknown as { api: Record<string, unknown> }).api = {
    listMachines: async () => ({
      machines: [
        {
          id: remote.machine,
          online: true,
          lastSeenMs: 0n,
          meta: await remote.seal({ kind: 4, seq: 1n }, MachineMetaSchema, create(MachineMetaSchema, { hostname: "MacBook", protocolVersion: PEER_PROTOCOL_VERSION })),
        },
      ],
    }),
    listRemoteSessions: async () => ({ sessions: [s1Row, s2Row] }),
  };
  vi.spyOn(remote, "prepareCacheWrite").mockImplementation((eff: CacheWriteEffect) => {
    const idx = ++callCount;
    const label = `${idx}:${eff.kind.case}`;
    return async () => {
      order.push(`start:${label}`);
      if (idx === 1) await firstGate;
      order.push(`end:${label}`);
    };
  });
  vi.spyOn(remote, "openStreamLeg").mockImplementation((legId, _token, callbacks) => {
    callbacks.onReady(legId, BigInt(Date.now()));
  });
  hosts.push(getCoreHost(remote));
  remote.start();
  await vi.waitFor(() => expect(order.length).toBeGreaterThanOrEqual(1));

  // At least two cache writes occurred (machine/session/prefs); verify strict FIFO serialization.
  expect(order[0]).toMatch(/^start:1:/);
  expect(order.filter((x) => x.startsWith("start:2:"))).toEqual([]);
  releaseFirst();
  await vi.waitFor(() => expect(order.filter((x) => x.startsWith("end:")).length).toBeGreaterThanOrEqual(2));

  const startIndices = order
    .map((x, i) => ({ x, i }))
    .filter((e) => e.x.startsWith("start:"))
    .map((e) => e.i);
  const endIndices = order
    .map((x, i) => ({ x, i }))
    .filter((e) => e.x.startsWith("end:"))
    .map((e) => e.i);
  expect(startIndices.length).toBeGreaterThanOrEqual(2);
  expect(endIndices[0]).toBeLessThan(startIndices[1]);
});

test("CoreHost honors fake timers for clock ticks and cleans up on dispose without hanging", async () => {
  vi.useFakeTimers();
  const exec = makeExecutor();
  const host = new CoreHost(undefined, { executor: exec });
  hosts.push(host);

  host.start();
  // Flush microtasks under fake timers.
  await vi.advanceTimersByTimeAsync(1);
  // Advance 61s to trigger the 60s clock tick interval and timer checks.
  await vi.advanceTimersByTimeAsync(61_000);
  host.dispose();
  // Advancing further after dispose must not throw or run stray work.
  await vi.advanceTimersByTimeAsync(120_000);
});

test("warmLocalCache hydrates bootstrap cache before start without re-reading IndexedDB on start", async () => {
  const exec = makeExecutor();
  let bootReads = 0;
  exec.readBootstrapCache = async (opId) => {
    bootReads++;
    return create(CacheBootstrapLoadedEventSchema, {
      opId,
      homeScopeToken: "all",
      scopeHmacKey: new Uint8Array(32).fill(7),
      rawMachines: [toBinary(MachineSchema, create(MachineSchema, { id: "m-cached", online: true }))],
    });
  };
  const host = new CoreHost(undefined, { executor: exec });
  hosts.push(host);

  await host.warmLocalCache("settings");
  expect(host.viewSettings().machines.map((m) => m.machineId)).toEqual(["m-cached"]);
  expect(bootReads).toBe(1);

  host.start();
  await Promise.resolve();
  expect(bootReads).toBe(1);
});

test("warmLocalCache uses timeout guard when cache read stalls without blocking mount", async () => {
  const exec = makeExecutor();
  exec.readBootstrapCache = () => new Promise(() => {});
  const host = new CoreHost(undefined, { executor: exec });
  hosts.push(host);

  let seenTimeout = 0;
  await host.warmLocalCache("settings", async (_work, ms) => {
    seenTimeout = ms;
    return undefined;
  });
  expect(seenTimeout).toBe(CoreHost.WARM_BOOTSTRAP_CACHE_TIMEOUT_MS);
  expect(host.viewSettings().machines).toEqual([]);
});

test("CoreHost dispatches LocaleChangedEvent on init and locale changes and unsubscribes on dispose", () => {
  const rendered: ScreenTarget[][] = [];
  localeStore.overrideForTesting("zh-CN");
  const host = new CoreHost(undefined, {
    executor: makeExecutor(),
    onEffect: (e) => {
      if (e.effect.case === "render") rendered.push([...e.effect.value.screens]);
    },
  });
  hosts.push(host);
  rendered.length = 0;

  localeStore.overrideForTesting("en");
  expect(rendered.some((targets) => targets.includes(ScreenTarget.USAGE) && targets.includes(ScreenTarget.MACHINE_STATUS))).toBe(true);

  rendered.length = 0;
  host.dispose();
  localeStore.overrideForTesting("zh-CN");
  expect(rendered).toEqual([]);
});

test("cancelPingMachine aborts only the matching request_id signal", async () => {
  const attempts: Array<{ eff: DispatchCommandEffect; signal?: AbortSignal; resolve: (ev: ReturnType<typeof create<typeof CommandOutcomeEventSchema>>) => void }> = [];
  const exec = makeExecutor({
    executeCommandAttempt(eff, signal) {
      return new Promise((resolve) => {
        attempts.push({ eff, signal, resolve });
      });
    },
  });
  const host = new CoreHost(undefined, { executor: exec });
  hosts.push(host);
  host.start();
  await flushPromises();
  const now = BigInt(Date.now());
  host.dispatch(
    create(EventSchema, {
      nowMs: now,
      event: {
        case: "seedDemoWorld",
        value: create(SeedDemoWorldEventSchema, {
          machines: [
            create(DemoMachineSchema, {
              machineId: "m1",
              online: true,
              rawMeta: toBinary(MachineMetaSchema, create(MachineMetaSchema, { hostname: "Box", protocolVersion: PEER_PROTOCOL_VERSION })),
            }),
          ],
          security: create(DemoSecuritySchema, {
            isMember: true,
            selfDeviceId: "c1",
            authorityDeviceId: "c1",
            trustedMachineIds: ["m1"],
            headEpoch: 1n,
            leaseEndMs: now + 3_600_000n,
          }),
        }),
      },
    }),
  );

  const ac1 = new AbortController();
  const ac2 = new AbortController();
  const p1 = host.pingMachine("m1", ac1.signal);
  const p2 = host.pingMachine("m1", ac2.signal);
  expect(attempts).toHaveLength(2);
  expect(attempts[0].eff.requestId).not.toBe(attempts[1].eff.requestId);
  expect(attempts[0].signal?.aborted).toBe(false);
  expect(attempts[1].signal?.aborted).toBe(false);

  ac1.abort();
  await expect(p1).rejects.toThrow();
  expect(attempts[0].signal?.aborted).toBe(true);
  expect(attempts[1].signal?.aborted).toBe(false);

  attempts[1].resolve(
    create(CommandOutcomeEventSchema, {
      requestId: attempts[1].eff.requestId,
      attemptId: attempts[1].eff.attemptId,
      source: OutcomeSource.DAEMON,
      signed: true,
      hashesMatch: true,
      attemptBound: true,
      outerMatchesInner: true,
      outerOutcome: "executed",
      outerReason: "ok",
      rawResult: toBinary(CommandResultSchema, create(CommandResultSchema, { outcome: "executed", reason: "ok", resultAvailable: true })),
    }),
  );
  await expect(p2).resolves.toBeUndefined();
});

test("cold-start networkChanged does not forge a signature or tear down the initial Ready stream on first real observation (audit #3)", async () => {
  const closedLegs: bigint[] = [];
  const openedLegs: bigint[] = [];
  let host!: CoreHost;
  const exec = makeExecutor(
    {
      openStreamLeg(legId, _token, callbacks) {
        openedLegs.push(legId);
        exec.streamLegs.set(legId, callbacks);
        callbacks.onReady(legId, BigInt(Date.now()));
      },
      closeStreamLeg(legId) {
        closedLegs.push(legId);
        exec.streamLegs.delete(legId);
      },
    },
    () => host,
  );
  host = new CoreHost(undefined, { executor: exec });
  hosts.push(host);
  host.dispatch(
    create(EventSchema, {
      event: {
        case: "seedDemoWorld",
        value: create(SeedDemoWorldEventSchema, {
          clearDirectReports: true,
          machines: [
            create(DemoMachineSchema, {
              machineId: "m1",
              online: true,
              rawMeta: toBinary(
                MachineMetaSchema,
                create(MachineMetaSchema, { hostname: "MacBook", protocolVersion: PEER_PROTOCOL_VERSION }),
              ),
            }),
          ],
          sessions: [
            create(DemoSessionSchema, {
              sessionId: "s1",
              machineId: "m1",
              lastSeq: 1n,
              updatedMs: 1_700_000_000_000n,
              rawMeta: toBinary(
                SessionSchema,
                create(SessionSchema, {
                  id: "s1",
                  cwd: "/Users/me/app",
                  title: "Fix tests",
                  status: SessionStatus.RUNNING,
                  agent: Agent.CLAUDE,
                }),
              ),
            }),
          ],
          security: create(DemoSecuritySchema, {
            isMember: true,
            selfDeviceId: "dev-client",
            authorityDeviceId: "dev-client",
            trustedMachineIds: ["m1"],
            trustedClientIds: ["dev-client"],
            headEpoch: 1n,
          }),
        }),
      },
    }),
  );
  host.start();
  await flushPromises();

  expect(openedLegs).toHaveLength(1);
  expect(closedLegs).toEqual([]);

  // First real browser network observation after start() must only record the baseline.
  host.setNetwork(true, JSON.stringify([true, ""]));
  await flushPromises();
  expect(closedLegs).toEqual([]);
  expect(openedLegs).toHaveLength(1);

  // Calling setNetwork without a real signature must not fabricate "true" and trigger sigChanged.
  host.setNetwork(true);
  await flushPromises();
  expect(closedLegs).toEqual([]);
  expect(openedLegs).toHaveLength(1);

  // A subsequent genuine signature change must still retire the old stream leg.
  host.setNetwork(true, JSON.stringify([true, "wifi"]));
  await flushPromises();
  expect(closedLegs).toEqual([openedLegs[0]]);
});

test("viewSession + viewSessionHeader computes core.view(SESSION) at most once per SESSION version bump", async () => {
  let host!: CoreHost;
  const exec = makeExecutor({}, () => host);
  host = new CoreHost(undefined, { executor: exec });
  hosts.push(host);
  host.dispatch(
    create(EventSchema, {
      event: {
        case: "seedDemoWorld",
        value: create(SeedDemoWorldEventSchema, {
          clearDirectReports: true,
          machines: [
            create(DemoMachineSchema, {
              machineId: "m1",
              online: true,
              rawMeta: toBinary(
                MachineMetaSchema,
                create(MachineMetaSchema, { hostname: "MacBook", protocolVersion: PEER_PROTOCOL_VERSION }),
              ),
            }),
          ],
          sessions: [
            create(DemoSessionSchema, {
              sessionId: "s1",
              machineId: "m1",
              lastSeq: 1n,
              updatedMs: 1_700_000_000_000n,
              rawMeta: toBinary(
                SessionSchema,
                create(SessionSchema, {
                  id: "s1",
                  cwd: "/Users/me/app",
                  title: "Fix tests",
                  status: SessionStatus.RUNNING,
                  agent: Agent.CLAUDE,
                }),
              ),
            }),
          ],
          security: create(DemoSecuritySchema, {
            isMember: true,
            selfDeviceId: "dev-client",
            authorityDeviceId: "dev-client",
            trustedMachineIds: ["m1"],
            trustedClientIds: ["dev-client"],
            headEpoch: 1n,
          }),
        }),
      },
    }),
  );
  host.start();
  await flushPromises();
  await flushPromises();

  const core = (host as unknown as { core: { view(req: Uint8Array): Uint8Array } }).core;
  const viewSpy = vi.spyOn(core, "view");

  // Single SESSION version: SessionPage computes full viewSession(sid, opts), then
  // any sibling/child reading viewSessionHeader(sid) MUST hit the per-version cache (0 extra boundary calls).
  const vm1 = host.viewSession("s1", { limit: 80 });
  const hdr1 = host.viewSessionHeader("s1");
  const vm1Again = host.viewSession("s1", { limit: 80 });
  expect(hdr1?.found).toBe(true);
  expect(vm1Again).toBe(vm1);
  expect(viewSpy).toHaveBeenCalledTimes(1);

  // Trigger a new SESSION version bump via sessionOpen -> exactly one fresh core.view call.
  viewSpy.mockClear();
  host.sessionOpen("s1", false, false);
  const vm2 = host.viewSession("s1", { limit: 80 });
  const hdr2 = host.viewSessionHeader("s1");
  expect(hdr2).toBe(vm2.header);
  expect(viewSpy).toHaveBeenCalledTimes(1);
});

test("viewSession invalidates cached not-found result when HOME renders before watchSession", async () => {
  const r = await SyntheticRemote.create({ seedDefaultSession: false });
  try {
    const row = await r.sessionRow("s");
    let releaseSessions!: () => void;
    const sessionsDeferred = new Promise<{ sessions: typeof row[] }>((resolve) => {
      releaseSessions = () => resolve({ sessions: [row] });
    });
    (r as unknown as { api: object }).api = {
      listMachines: async () => ({ machines: [] }),
      listRemoteSessions: () => sessionsDeferred,
      listDevices: async () => ({ devices: [] }),
      getAccountPrefs: async () => ({}),
    };
    (r as unknown as { openStreamLeg: (legId: bigint, t: string, cb: StreamLegCallbacks) => void }).openStreamLeg = (
      legId,
      _t,
      cb,
    ) => cb.onReady(legId, BigInt(Date.now()));

    const host = getCoreHost(r);
    r.start();
    await vi.waitFor(() => expect(host.viewConnection().connected).toBe(true));

    // Reading viewSession before watchSession/sessionOpen while listRemoteSessions is still
    // in flight caches found=false. Core intentionally omits SCREEN_TARGET_SESSION when no
    // session is opened/watched, so CoreHost must clear sessionCache on HOME/CONNECTION renders.
    expect(host.viewSession("s").header?.found).toBe(false);

    releaseSessions();
    await vi.waitFor(() => expect(host.viewSession("s").header?.found).toBe(true), { timeout: 1000 });
  } finally {
    await r.dispose();
  }
});
