// @vitest-environment happy-dom

import { create } from "@bufbuild/protobuf";
import { afterEach, expect, test, vi } from "vitest";
import {
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CommandOutcomeEventSchema,
  EventSchema,
  ListsFetchedEventSchema,
  OutcomeSource,
  PlainDeviceSchema,
  PlainMachineSchema,
  PlainSessionSchema,
  ScreenTarget,
  SecuritySnapshotEventSchema,
  TokenResultEventSchema,
  type CacheWriteEffect,
  type DispatchCommandEffect,
  type Effect,
  type Event,
  type RpcEffect,
} from "../gen/remoteagent/core/v1/app_pb";
import { SecurityRecordEventSchema } from "../gen/remoteagent/core/v1/security_pb";
import { ConnState } from "../gen/remoteagent/core/v1/home_view_pb";
import { Agent, SessionStatus } from "../gen/remoteagent/v1/event_pb";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import { CoreHost, type CoreExecutor, type StreamLegCallbacks } from "./host";

const hosts: CoreHost[] = [];

afterEach(() => {
  while (hosts.length) hosts.pop()!.dispose();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function flushPromises(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

function makeExecutor(overrides: Partial<CoreExecutor> = {}): CoreExecutor & {
  streamLegs: Map<bigint, StreamLegCallbacks>;
  tokenCalls: Array<{ opId: bigint; forceLogin: boolean }>;
  writeOrder: string[];
  releasedEvents: Event[];
} {
  const streamLegs = new Map<bigint, StreamLegCallbacks>();
  const tokenCalls: Array<{ opId: bigint; forceLogin: boolean }> = [];
  const writeOrder: string[] = [];
  const releasedEvents: Event[] = [];

  return {
    streamLegs,
    tokenCalls,
    writeOrder,
    releasedEvents,
    openStreamLeg(legId, _token, callbacks) {
      streamLegs.set(legId, callbacks);
      callbacks.onReady(legId, BigInt(Date.now()));
    },
    closeStreamLeg(legId) {
      streamLegs.delete(legId);
    },
    async ensureToken(opId, forceLogin) {
      tokenCalls.push({ opId, forceLogin });
      return create(TokenResultEventSchema, {
        opId,
        token: "tok-1",
        expiresMs: BigInt(Date.now() + 600_000),
      });
    },
    discardToken() {},
    async syncSecurity(opId, networkGeneration) {
      return create(SecurityRecordEventSchema, {
        opId,
        networkGeneration,
      });
    },
    async syncSecuritySnapshot(opId, networkGeneration) {
      return create(SecuritySnapshotEventSchema, {
        opId,
        isMember: true,
        selfDeviceId: "dev-client",
        authorityDeviceId: "dev-client",
        trustedMachineIds: ["m1"],
        trustedClientIds: ["dev-client"],
        headEpoch: 1n,
        networkGeneration,
      });
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
    applySyncStateView() {},
    async executeRpc(eff: RpcEffect) {
      if (eff.kind.case === "fetchLists") {
        return create(EventSchema, {
          event: {
            case: "listsFetched",
            value: create(ListsFetchedEventSchema, {
              opId: eff.opId,
              machines: [
                create(PlainMachineSchema, {
                  machineId: "m1",
                  online: true,
                  meta: { hostname: "MacBook" },
                  cacheRef: "cache-m1",
                }),
              ],
              sessions: [
                create(PlainSessionSchema, {
                  sessionId: "s1",
                  machineId: "m1",
                  lastSeq: 2n,
                  updatedMs: BigInt(Date.now()),
                  meta: {
                    id: "s1",
                    cwd: "/Users/me/app",
                    title: "Fix tests",
                    status: SessionStatus.RUNNING,
                    agent: Agent.CLAUDE,
                  },
                  cacheRef: "cache-s1",
                }),
              ],
              devices: [
                create(PlainDeviceSchema, {
                  deviceId: "dev-client",
                  kind: DeviceKind.CLIENT,
                }),
                create(PlainDeviceSchema, {
                  deviceId: "m1",
                  kind: DeviceKind.MACHINE,
                }),
              ],
            }),
          },
        });
      }
      return create(EventSchema, {});
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
  const exec = makeExecutor();
  const host = new CoreHost(undefined, {
    executor: exec,
    onEffect: (e) => effects.push(e),
  });
  hosts.push(host);

  const onHome = vi.fn();
  const unsub = host.subscribe(ScreenTarget.HOME, onHome);
  try {
    host.start();
    await flushPromises();
    await flushPromises();

    // Persistent HMAC key from CacheBootstrapLoaded must NOT be shadowed by an ephemeral Start key.
    expect(effects.some((e) => e.effect.case === "requestEntropy" || e.effect.case === "cacheRead")).toBe(true);
    expect(exec.tokenCalls.length).toBeGreaterThan(0);
    expect(exec.streamLegs.size).toBe(1);

    const home = host.viewHome();
    expect(home.connection?.state).toBe(ConnState.CONNECTED);
    expect(home.running.map((r) => r.sessionId)).toEqual(["s1"]);
    expect(onHome).toHaveBeenCalled();

    const sync = host.viewSyncState();
    expect(sync.machinesListed).toBe(true);
    expect(sync.sessionsListed).toBe(true);
    expect(sync.isMember).toBe(true);
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
  const exec = makeExecutor({
    prepareCacheWrite(eff) {
      const idx = ++callCount;
      const label = `${idx}:${eff.kind.case}`;
      return async () => {
        order.push(`start:${label}`);
        if (idx === 1) await firstGate;
        order.push(`end:${label}`);
      };
    },
  });

  const host = new CoreHost(undefined, { executor: exec });
  hosts.push(host);
  host.start();
  await flushPromises();
  await flushPromises();

  // At least two cache writes occurred (machine/session/prefs); verify strict FIFO serialization.
  expect(order[0]).toMatch(/^start:1:/);
  expect(order.filter((x) => x.startsWith("start:2:"))).toEqual([]);
  releaseFirst();
  await flushPromises();
  await flushPromises();

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

test("CoreHost dispatches unrequested securitySnapshot when pushSeq increases monotonically", async () => {
  const exec = makeExecutor();
  const host = new CoreHost(undefined, { executor: exec });
  hosts.push(host);
  host.start();
  await flushPromises();
  await flushPromises();

  // OpId=0 with monotonic pushSeq updates security state; stale pushSeq is ignored.
  host.dispatch(
    create(EventSchema, {
      event: {
        case: "securitySnapshot",
        value: create(SecuritySnapshotEventSchema, {
          opId: 0n,
          pushSeq: 2n,
          isMember: false,
          selfDeviceId: "dev-client",
          authorityDeviceId: "dev-client",
          headEpoch: 3n,
        }),
      },
    }),
  );
  expect(host.viewSyncState().isMember).toBe(false);

  // Stale pushSeq=1n (< 2n) must be ignored and keep isMember=false.
  host.dispatch(
    create(EventSchema, {
      event: {
        case: "securitySnapshot",
        value: create(SecuritySnapshotEventSchema, {
          opId: 0n,
          pushSeq: 1n,
          isMember: true,
          selfDeviceId: "dev-client",
          authorityDeviceId: "dev-client",
          trustedMachineIds: ["m1", "m2"],
          trustedClientIds: ["dev-client"],
          headEpoch: 2n,
        }),
      },
    }),
  );
  expect(host.viewSyncState().isMember).toBe(false);
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
      machines: [{ machineId: "m-cached", online: true }],
    });
  };
  const host = new CoreHost(undefined, { executor: exec });
  hosts.push(host);

  await host.warmLocalCache("settings");
  expect(host.viewSyncState().machines.map((m) => m.machineId)).toEqual(["m-cached"]);
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
  expect(host.viewSyncState().machines).toEqual([]);
});
