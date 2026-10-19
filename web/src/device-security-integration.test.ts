import { afterAll, afterEach, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import { CommandSchema as RemoteCommandSchema } from "./gen/remoteagent/v1/remote_pb";
import { ProjectPrefsSchema } from "./gen/remoteagent/v1/remote_pb";
import {
  GoSecurityFixture,
  MemorySecurityStorage,
  webMember,
  removeFixtureBuild,
  recordFromJSON,
} from "./test/security-fixture";
import { DeviceSecurityClient } from "./device-security-client";
import {
  extendRecord,
  verifyStoredSecurity,
  type SecurityRecord,
} from "./device-security-store";
import { Purpose, parseHeader } from "./device-security";

afterAll(removeFixtureBuild);
afterEach(() => vi.useRealTimers());

it("seals a Web command that the actual Go receiver opens, and rejects a modified origin", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { sdk, account } = await webMember(f);
    const command = toBinary(
      RemoteCommandSchema,
      create(RemoteCommandSchema, {
        protocolVersion: 10,
        kind: { case: "getSettings", value: {} },
      }),
    );
    const envelope = await sdk.seal(
      {
        kind: 5,
        purpose: Purpose.Command,
        machine: f.machine,
        session: "",
        request: crypto.randomUUID(),
        seq: 0n,
        deadline: BigInt(Date.now() + 60000),
        compression: 0,
      },
      command,
    );
    const opened = await f.fixture.open(f.machine, envelope);
    expect(opened.header.sender).toBe(account.deviceId);
    expect(opened.plaintext).toEqual(command);
    const damaged = {
      ...envelope,
      signature: new Uint8Array(envelope.signature),
    };
    damaged.signature[0] ^= 1;
    await expect(f.fixture.open(f.machine, damaged)).rejects.toThrow();
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("reads a Go historical event from the locally verified chain", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const seed = crypto.getRandomValues(new Uint8Array(32));
    const event = create(EventSchema, {
      sessionId: "synthetic-session",
      seq: 1n,
      kind: { case: "agentMessage", value: { text: "synthetic history" } },
    });
    const envelope = await f.fixture.seal(
      f.machine,
      { kind: 1, machineId: f.machine, sessionId: event.sessionId, seq: 1n },
      toBinary(EventSchema, event),
      seed,
    );
    const { sdk } = await webMember(f);
    expect(
      (await sdk.openMessage(envelope, EventSchema, true, seed)).msg,
    ).toEqual(event);
    const damaged = {
      ...envelope,
      ciphertext: new Uint8Array(envelope.ciphertext),
    };
    damaged.ciphertext[0] ^= 1;
    await expect(
      sdk.openMessage(damaged, EventSchema, true, seed),
    ).rejects.toThrow();
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("uses independent random nonces for concurrent seals", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { sdk } = await webMember(f);
    const body = toBinary(
      RemoteCommandSchema,
      create(RemoteCommandSchema, {
        protocolVersion: 10,
        kind: { case: "getSettings", value: {} },
      }),
    );
    const messages = await Promise.all(
      Array.from({ length: 16 }, () =>
        sdk.seal(
          {
            kind: 5,
            purpose: Purpose.Command,
            machine: f.machine,
            session: "",
            request: crypto.randomUUID(),
            seq: 0n,
            deadline: BigInt(Date.now() + 60000),
            compression: 0,
          },
          new Uint8Array(body),
        ),
      ),
    );
    const nonces = messages.map((m) => Array.from(m.nonce).join(","));
    expect(new Set(nonces).size).toBe(messages.length);
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("does not seal control while the local clock is behind the state", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { sdk } = await webMember(f);
    vi.useFakeTimers();
    vi.setSystemTime(1_600_000_000_000);
    await expect(
      sdk.seal(
        {
          kind: 5,
          purpose: Purpose.Command,
          machine: f.machine,
          session: "",
          request: crypto.randomUUID(),
          seq: 0n,
          deadline: BigInt(Date.now() + 60_000),
          compression: 0,
        },
        new Uint8Array(1),
      ),
    ).rejects.toMatchObject({ code: "clock" });
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("rejects a mixed cached content secret before any ciphertext publication", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { account, storage } = await webMember(f);
    const good = await storage.loadSecurity();
    const mixed = Object.create(storage) as MemorySecurityStorage;
    mixed.loadSecurity = async () => ({
      ...good,
      secret: crypto.getRandomValues(new Uint8Array(32)),
    });
    const sdk = new DeviceSecurityClient(
      account.deviceId,
      account.devicePrivate,
      async () => {
        throw new Error("no state pull expected");
      },
      () => true,
      mixed,
    );
    await expect(
      sdk.seal(
        {
          kind: 5,
          purpose: Purpose.Command,
          machine: f.machine,
          session: "",
          request: crypto.randomUUID(),
          seq: 0n,
          deadline: BigInt(Date.now() + 60000),
          compression: 0,
        },
        new Uint8Array(1),
      ),
    ).rejects.toThrow();
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("accepts an old-state live frame from a current member and refuses a removed sender", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const victim = await f.fixture.enroll(f.phone.json.member, 2);
    const seed = crypto.getRandomValues(new Uint8Array(32));
    const event = create(EventSchema, {
      sessionId: "s-live",
      seq: 1n,
      kind: {
        case: "agentMessage",
        value: { text: "retained history" },
      },
    });
    const eventEnvelope = await f.fixture.seal(
      f.machine,
      {
        kind: 1,
        machineId: f.machine,
        sessionId: event.sessionId,
        seq: 1n,
      },
      toBinary(EventSchema, event),
      seed,
    );
    const prefs = create(ProjectPrefsSchema, { schema: 1 });
    const prefsEnvelope = await f.fixture.seal(
      victim.json.member,
      { kind: 12, requestId: "project-prefs", seq: 1n },
      toBinary(ProjectPrefsSchema, prefs),
    );
    // Another member joins, so the prefs write names an old state while its
    // sender is still in the head.
    const other = await f.fixture.enroll(f.phone.json.member, 2);
    const { record } = await recordFromJSON(
      await f.fixture.record(victim.json.member),
    );
    const sdk = new DeviceSecurityClient(
      record.memberID,
      victim.signingKey,
      async () => {
        throw new Error("no state pull expected");
      },
      () => true,
      new MemorySecurityStorage(record),
    );
    await expect(
      sdk.openMessage(prefsEnvelope, ProjectPrefsSchema, false),
    ).resolves.toBeTruthy();

    // Removing the sender leaves a non-member record whose history still opens.
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
    // A tampered middle state no longer passes the load verifier.
    const tampered = structuredClone(removed) as SecurityRecord;
    tampered.states[1].signature[0] ^= 1;
    await expect(verifyStoredSecurity(tampered)).rejects.toThrow();

    const removedSdk = new DeviceSecurityClient(
      removed.memberID,
      victim.signingKey,
      async () => {
        throw new Error("no state pull expected");
      },
      () => true,
      new MemorySecurityStorage(removed),
    );
    await expect(
      removedSdk.openMessage(prefsEnvelope, ProjectPrefsSchema, false),
    ).rejects.toThrow();
    await expect(
      removedSdk.openMessage(eventEnvelope, EventSchema, true, seed),
    ).resolves.toBeTruthy();
    expect(proofs.length).toBe(states.length);
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("does not seal control after the lease expires", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { sdk } = await webMember(f);
    vi.useFakeTimers();
    vi.setSystemTime(Date.now() + 73 * 60 * 60 * 1000);
    await expect(
      sdk.seal(
        {
          kind: 5,
          purpose: Purpose.Command,
          machine: f.machine,
          session: "",
          request: crypto.randomUUID(),
          seq: 0n,
          deadline: BigInt(Date.now() + 60_000),
          compression: 0,
        },
        new Uint8Array(1),
      ),
    ).rejects.toMatchObject({ code: "lease" });
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("refreshes its sealing record when another tab installed the head and sync returns no states", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { sdk, storage } = await webMember(f);
    const header = {
      kind: 12,
      purpose: Purpose.Prefs,
      machine: "",
      session: "",
      request: "project-prefs",
      seq: 1n,
      deadline: 0n,
      compression: 0,
    };
    await f.fixture.enroll(f.phone.json.member, 2);
    await sdk.sync();
    await sdk.seal(header, new Uint8Array([1]));
    const old = await storage.loadSecurity();
    await f.fixture.enroll(f.phone.json.member, 2);
    const { states, proofs } = await f.fixture.chain(old.memberID);
    const newer = states.filter(
      (s) => s.core.epoch > old.states.at(-1)!.core.epoch,
    );
    await storage.installSecurity(
      old,
      newer,
      newer.map((s) => proofs[Number(s.core.epoch - 1n)]),
    );
    await sdk.sync();
    const env = await sdk.seal(header, new Uint8Array([1]));
    expect(parseHeader(new Uint8Array(env.securityHeader)).epoch).toBe(
      states.at(-1)!.core.epoch,
    );
  } finally {
    await f.fixture.close();
  }
}, 20000);
