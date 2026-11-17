import { create, toBinary } from "@bufbuild/protobuf";
import { expect, it, vi } from "vitest";
import {
  ProjectPrefsSchema,
  MachineMetaSchema,
  DeviceMetaSchema,
} from "./gen/remoteagent/v1/remote_pb";
import { SyntheticRemote } from "./test/remote-fixture";
import { SecurityError } from "./device-security";
it("reads stored prefs from a removed writer and rejects tampered provenance", async () => {
  const r = await SyntheticRemote.create();
  try {
    const writer = await r.addClient();
    const env = await r.world.fixture.seal(
      writer.account.deviceId,
      { kind: 12, requestId: "project-prefs", seq: 1n },
      toBinary(
        ProjectPrefsSchema,
        create(ProjectPrefsSchema, {
          schema: 1,
          pinned: [{ path: "/stored" }],
        }),
      ),
    );
    await r.world.fixture.remove([writer.account.deviceId]);
    const local = r as any;
    await local.security.sync();
    const o = { revision: 1n, envelope: env };
    const opened = await local.unpackAccountPrefs(o);
    expect(opened.broken).toBe(false);
    expect(opened.prefs.pinned.map((p: any) => p.path)).toEqual(["/stored"]);
    const tampered = await local.unpackAccountPrefs({
      ...o,
      envelope: { ...env, signature: new Uint8Array(64) },
    });
    expect(tampered.broken).toBe(true);
  } finally {
    await r.dispose();
  }
}, 20000);
it("keeps returned archives across consecutive refreshes and removes only a deleted archive", async () => {
  const r = await SyntheticRemote.create();
  try {
    const row = await r.sessionRow("s");
    const meta = await r.seal(
      { kind: 4, seq: 1n },
      MachineMetaSchema,
      create(MachineMetaSchema, { hostname: "archive" }),
    );
    let retained = true;
    let streamLeg: { legId: bigint; callbacks: { onReady: (id: bigint, ms: bigint) => void; onClosed: (id: bigint, err: { policyViolation: boolean; errorCode: string; error: string; unresolvedHost: boolean }) => void } } | undefined;
    let fetches = 0;
    const listMachines = async () => {
      fetches++;
      return {
        machines: retained
          ? [{ id: r.machine, online: false, lastSeenMs: 0n, meta }]
          : [],
      };
    };
    (r as any).token = "tok";
    (r as any).tokenExpires = Date.now() + 7 * 86_400_000;
    (r as any).api = {
      listMachines,
      listRemoteSessions: async () => ({ sessions: retained ? [row] : [] }),
      listDevices: async () => ({ devices: [] }),
      getAccountPrefs: async () => ({}),
    };
    (r as any).openStreamLeg = (legId: bigint, _token: string, callbacks: NonNullable<typeof streamLeg>["callbacks"]) => {
      streamLeg = { legId, callbacks };
      queueMicrotask(() => callbacks.onReady(legId, BigInt(Date.now())));
    };
    const refresh = async () => {
      const target = fetches + 1;
      if (!streamLeg) {
        r.start();
      } else {
        streamLeg.callbacks.onClosed(streamLeg.legId, { policyViolation: false, errorCode: "unavailable", error: "refresh", unresolvedHost: false });
        (r as any).reconnectNow?.();
      }
      await new Promise<void>((resolve) => {
        const timer = setInterval(() => {
          if (fetches >= target) {
            clearInterval(timer);
            setTimeout(resolve, 20);
          }
        }, 5);
      });
    };
    await refresh();
    await vi.waitFor(() => expect((r as any).coreSyncDepth).toBe(0));
    await r.world.fixture.remove([r.machine]);
    await vi.waitFor(async () => {
      const snap = await (r as any).syncSecurity(0n, 0n, false);
      const { getCoreHost } = await import("./core/host");
      const { EventSchema } = await import("./gen/remoteagent/core/v1/app_pb");
      getCoreHost(r).dispatch(create(EventSchema, { event: { case: "securitySnapshot", value: snap } }));
      expect((r as any).trustedMembers.has(r.machine)).toBe(false);
    });
    await refresh();
    expect((r as any).devicesStatus).toBe("ready");
    expect((r as any).trustedMembers.has(r.machine)).toBe(false);
    expect(r.machines.has(r.machine)).toBe(true);
    await refresh();
    expect(r.machines.get(r.machine)?.meta?.hostname).toBe("archive");
    expect(r.sessions.has("s")).toBe(true);
    retained = false;
    await refresh();
    expect(r.machines.has(r.machine)).toBe(false);
    expect(r.sessions.has("s")).toBe(false);
  } finally {
    await r.dispose();
  }
}, 20000);

it("retries automatic device metadata after a lease-paused save on an unchanged head", async () => {
  const r = await SyntheticRemote.create();
  try {
    const local = r as any;
    local.closed = false;
    await r.loadDevices(false);
    let calls = 0;
    local.changeDeviceMeta = async () => {
      if (++calls === 1) throw new SecurityError("lease");
    };
    await local.describeSelf();
    expect(calls).toBe(1);
    await local.syncSecurity();
    expect(calls).toBe(2);
  } finally {
    await r.dispose();
  }
}, 20000);

it("verifies stored device metadata using the state that signed it after that device is removed", async () => {
  const r = await SyntheticRemote.create();
  try {
    const writer = await r.addClient();
    const meta = await r.world.fixture.seal(
      writer.account.deviceId,
      { kind: 13, requestId: writer.account.deviceId, seq: 1n },
      toBinary(
        DeviceMetaSchema,
        create(DeviceMetaSchema, { name: "historical client" }),
      ),
    );
    await r.world.fixture.remove([writer.account.deviceId]);
    const local = r as any;
    await local.security.sync();
    const opened = await local.openSecureMessage(
      meta,
      { kind: 13, requestId: writer.account.deviceId },
      DeviceMetaSchema,
    );
    expect(opened.msg.name).toBe("historical client");
  } finally {
    await r.dispose();
  }
}, 20000);
