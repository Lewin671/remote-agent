import { afterEach, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import { CommandSchema as RemoteCommandSchema } from "./gen/remoteagent/v1/remote_pb";
import type { Envelope } from "./gen/remoteagent/v1/envelope_pb";
import {
  GoSecurityFixture,
  MemorySecurityStorage,
  webMember,
  recordFromJSON,
} from "./test/security-fixture";
import { SyntheticRemote } from "./test/remote-fixture";
import { AuthoritySecurity } from "./authority-security";
import {
  extendRecord,
  verifyStoredSecurity,
  type SecurityRecord,
} from "./device-security-store";
import { validLease } from "./device-security";

afterEach(() => vi.useRealTimers());

it("seals a Web command that the actual Go receiver opens, and rejects a modified origin", async () => {
  const r = await SyntheticRemote.create();
  try {
    const cmd = create(RemoteCommandSchema, {
      protocolVersion: 10,
      kind: { case: "getSettings", value: {} },
    });
    const command = toBinary(RemoteCommandSchema, cmd);
    const { envelope } = await (
      r as unknown as {
        sealSecureMessage(
          h: Record<string, unknown>,
          schema: typeof RemoteCommandSchema,
          msg: typeof cmd,
        ): Promise<{ envelope: Envelope }>;
      }
    ).sealSecureMessage(
      {
        kind: 5,
        machineId: r.machine,
        sessionId: "",
        requestId: crypto.randomUUID(),
        seq: 0n,
        deadlineMs: BigInt(Date.now() + 60000),
        sender: r.account.deviceId,
      },
      RemoteCommandSchema,
      cmd,
    );
    const opened = await r.world.fixture.open(r.machine, envelope);
    expect(opened.header.sender).toBe(r.account.deviceId);
    expect(opened.plaintext).toEqual(command);
    const damaged = {
      ...envelope,
      signature: new Uint8Array(envelope.signature),
    };
    damaged.signature[0] ^= 1;
    await expect(r.world.fixture.open(r.machine, damaged)).rejects.toThrow();
  } finally {
    await r.dispose();
  }
}, 20000);

it("reads a Go historical event from the locally verified chain", async () => {
  const r = await SyntheticRemote.create();
  try {
    const seed = crypto.getRandomValues(new Uint8Array(32));
    await r.seedSession("synthetic-session", seed);
    const event = create(EventSchema, {
      sessionId: "synthetic-session",
      seq: 1n,
      kind: { case: "agentMessage", value: { text: "synthetic history" } },
    });
    const envelope = await r.world.fixture.seal(
      r.machine,
      { kind: 1, machineId: r.machine, sessionId: event.sessionId, seq: 1n },
      toBinary(EventSchema, event),
      seed,
    );
    const opened = await (
      r as unknown as {
        openSecureMessage(
          env: Envelope,
          want: Record<string, unknown>,
          schema: typeof EventSchema,
          history: boolean,
        ): Promise<{ msg: typeof event }>;
      }
    ).openSecureMessage(
      envelope,
      { kind: 1, machineId: r.machine, sessionId: event.sessionId, seq: 1n },
      EventSchema,
      true,
    );
    expect(opened.msg).toEqual(event);
    const damaged = {
      ...envelope,
      ciphertext: new Uint8Array(envelope.ciphertext),
    };
    damaged.ciphertext[0] ^= 1;
    await expect(
      (
        r as unknown as {
          openSecureMessage(
            env: Envelope,
            want: Record<string, unknown>,
            schema: typeof EventSchema,
            history: boolean,
          ): Promise<unknown>;
        }
      ).openSecureMessage(
        damaged,
        { kind: 1, machineId: r.machine, sessionId: event.sessionId, seq: 1n },
        EventSchema,
        true,
      ),
    ).rejects.toThrow();
  } finally {
    await r.dispose();
  }
}, 20000);

it("rejects a mixed cached content secret before trusting local security state", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { account, storage } = await webMember(f);
    const good = await storage.loadSecurity();
    const mixed = Object.create(storage) as MemorySecurityStorage;
    mixed.loadSecurity = async () => ({
      ...good,
      secret: crypto.getRandomValues(new Uint8Array(32)),
    });
    const sdk = new AuthoritySecurity(
      account.deviceId,
      account.devicePrivate,
      async () => {
        throw new Error("no state pull expected");
      },
      () => true,
      mixed,
    );
    await expect(sdk.verifiedRecord()).rejects.toMatchObject({ code: "storage" });
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("marks removed record as non-member and rejects a tampered middle state on storage verification", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const victim = await f.fixture.enroll(f.phone.json.member, 2);
    const other = await f.fixture.enroll(f.phone.json.member, 2);
    const { record } = await recordFromJSON(
      await f.fixture.record(victim.json.member),
    );
    await f.fixture.remove([victim.json.member]);
    const { states, proofs } = await f.fixture.chain(other.json.member);
    const removal = states.filter(
      (s) => s.core.epoch > record.states[record.states.length - 1].core.epoch,
    );
    const removed = await extendRecord(
      record,
      removal,
      removal.map(() => undefined),
    );
    expect(removed.member).toBe(false);
    await expect(verifyStoredSecurity(removed)).resolves.toBeTruthy();
    const tampered = structuredClone(removed) as SecurityRecord;
    tampered.states[1].signature[0] ^= 1;
    await expect(verifyStoredSecurity(tampered)).rejects.toThrow();
    expect(proofs.length).toBe(states.length);
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("persists observed lease expiry before a failed pull and blocks rolled-back clocks", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { account, storage, record } = await webMember(f);
    const lease = record.lease!;
    const fetchFailure = new Error("synthetic security pull failure");
    const atFetch: bigint[] = [];
    let failPull = false;
    const sdk = new AuthoritySecurity(
      account.deviceId,
      account.devicePrivate,
      async () => {
        if (!failPull)
          return { states: [], headEpoch: record.states.at(-1)!.core.epoch };
        atFetch.push((await storage.loadSecurity()).floor);
        throw fetchFailure;
      },
      () => true,
      storage,
    );
    vi.useFakeTimers();
    vi.setSystemTime(Number(lease.issuedAt));
    await sdk.sync();
    failPull = true;
    vi.setSystemTime(Number(lease.notAfter));
    await expect(sdk.sync()).rejects.toBe(fetchFailure);
    vi.setSystemTime(Number(lease.issuedAt));
    const reopened = new AuthoritySecurity(
      account.deviceId,
      account.devicePrivate,
      async () => {
        throw fetchFailure;
      },
      () => true,
      storage,
    );
    const rec1 = await sdk.verifiedRecord();
    const rec2 = await reopened.verifiedRecord();
    await expect.soft(validLease(rec1.lease, rec1.states.at(-1)!, rec1.floor)).rejects.toMatchObject({ code: "lease" });
    await expect.soft(validLease(rec2.lease, rec2.states.at(-1)!, rec2.floor)).rejects.toMatchObject({ code: "lease" });
    expect(atFetch).toEqual([lease.notAfter]);
    expect((await storage.loadSecurity()).floor).toBe(lease.notAfter);
    expect((await sdk.verifiedRecord()).floor).toBe(lease.notAfter);
  } finally {
    vi.useRealTimers();
    await f.fixture.close();
  }
}, 20000);
