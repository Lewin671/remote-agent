import { create, toBinary } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
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
    expect(
      (await local.openPrefs(o)).prefs.pinned.map((p: any) => p.path),
    ).toEqual(["/stored"]);
    await expect(
      local.openPrefs({
        ...o,
        envelope: { ...env, signature: new Uint8Array(64) },
      }),
    ).rejects.toThrow();
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
    (r as any).api = {
      listMachines: async () => ({
        machines: retained
          ? [{ id: r.machine, online: false, lastSeenMs: 0n, meta }]
          : [],
      }),
      listRemoteSessions: async () => ({ sessions: retained ? [row] : [] }),
    };
    await r.refresh();
    await r.world.fixture.remove([r.machine]);
    await r.refresh();
    expect((r as any).devicesStatus).toBe("ready");
    expect((r as any).trustedMembers.has(r.machine)).toBe(false);
    expect(r.machines.has(r.machine)).toBe(true);
    await r.refresh();
    expect(r.machines.get(r.machine)?.meta?.hostname).toBe("archive");
    expect(r.sessions.has("s")).toBe(true);
    retained = false;
    await r.refresh();
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
