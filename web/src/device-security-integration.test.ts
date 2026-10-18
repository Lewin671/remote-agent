import { afterAll, expect, it } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { EventSchema } from "./gen/remoteagent/v1/event_pb";
import { CommandSchema as RemoteCommandSchema } from "./gen/remoteagent/v1/remote_pb";
import {
  GoSecurityFixture,
  MemorySecurityStorage,
  webMember,
  removeFixtureBuild,
} from "./test/security-fixture";
import { DeviceSecurityClient } from "./device-security-client";
import {
  Purpose,
  capsuleBytes,
  parseCapsule,
  parseHeader,
} from "./device-security";

afterAll(removeFixtureBuild);

it("authorizes a Web command that the actual Go crypto receiver opens, and rejects modified proof", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { sdk, account } = await webMember(f);
    const command = toBinary(
      RemoteCommandSchema,
      create(RemoteCommandSchema, {
        protocolVersion: 9,
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
      authorizationSignature: new Uint8Array(envelope.authorizationSignature),
    };
    damaged.authorizationSignature[0] ^= 1;
    await expect(f.fixture.open(f.machine, damaged)).rejects.toThrow();
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("reads an authenticated Go historical event offline only after caching its epoch key", async () => {
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
    const { account, storage } = await webMember(f);
    let offline = false;
    const exchange = async (capsule: Parameters<typeof capsuleBytes>[0]) => {
      if (offline) throw new Error("synthetic offline");
      return parseCapsule(await f.fixture.rpc(capsuleBytes(capsule)));
    };
    const sdk = new DeviceSecurityClient(
      account.deviceId,
      account.devicePrivate,
      exchange,
      () => true,
      storage,
    );
    expect(
      (await sdk.openMessage(envelope, EventSchema, true, seed)).msg,
    ).toEqual(event);
    offline = true;
    expect(
      (await sdk.openMessage(envelope, EventSchema, true, seed)).msg,
    ).toEqual(event);
    const empty = new MemorySecurityStorage(await storage.loadSecurity());
    const uncached = new DeviceSecurityClient(
      account.deviceId,
      account.devicePrivate,
      exchange,
      () => true,
      empty,
    );
    await expect(
      uncached.openMessage(envelope, EventSchema, true, seed),
    ).rejects.toThrow("synthetic offline");
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("reserves unique persisted-message counters before concurrent seals", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { sdk } = await webMember(f);
    const body = toBinary(
      RemoteCommandSchema,
      create(RemoteCommandSchema, {
        protocolVersion: 9,
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
    const counters = messages.map((m) =>
      parseHeader(new Uint8Array(m.securityHeader)).counter.toString(),
    );
    expect(new Set(counters).size).toBe(messages.length);
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("rejects a mixed cached content secret before any authorization or ciphertext publication", async () => {
  const f = await GoSecurityFixture.create();
  try {
    const { account, storage } = await webMember(f);
    const good = await storage.loadSecurity();
    const wrong = await crypto.subtle.importKey(
      "raw",
      crypto.getRandomValues(new Uint8Array(32)),
      "HKDF",
      false,
      ["deriveBits"],
    );
    let calls = 0;
    const exchange = async (c: Parameters<typeof capsuleBytes>[0]) => {
      calls++;
      return parseCapsule(await f.fixture.rpc(capsuleBytes(c)));
    };
    const mixed = Object.create(storage) as MemorySecurityStorage;
    mixed.loadSecurity = async () => ({ ...good, secret: wrong });
    const sdk = new DeviceSecurityClient(
      account.deviceId,
      account.devicePrivate,
      exchange,
      () => true,
      mixed,
    );
    const body = new Uint8Array(
      toBinary(
        RemoteCommandSchema,
        create(RemoteCommandSchema, {
          protocolVersion: 9,
          kind: { case: "getSettings", value: {} },
        }),
      ),
    );
    const header = {
      kind: 5,
      purpose: Purpose.Command,
      machine: f.machine,
      session: "",
      request: crypto.randomUUID(),
      seq: 0n,
      deadline: BigInt(Date.now() + 60000),
      compression: 0,
    };
    await expect(sdk.seal(header, body)).rejects.toThrow();
    expect(calls).toBe(0);
    const changed = Object.create(storage) as MemorySecurityStorage;
    changed.loadSecurity = () => storage.loadSecurity();
    changed.reserveCounter = async (expected) => ({
      ...(await storage.reserveCounter(expected)),
      secret: wrong,
    });
    const afterReservation = new DeviceSecurityClient(
      account.deviceId,
      account.devicePrivate,
      exchange,
      () => true,
      changed,
    );
    await expect(afterReservation.seal(header, body)).rejects.toThrow();
    expect(calls).toBe(0);
  } finally {
    await f.fixture.close();
  }
}, 20000);

it("keeps forged legacy delivery and cancel records read-only without settling a current outbox", async () => {
  const f = await GoSecurityFixture.create();
  const { Remote, newSlot } = await import("./remote");
  const { sealMessage, importSessionKey } = await import("./crypto");
  const { InputStatus } = await import("./gen/remoteagent/v1/event_pb");
  const { derive } = await import("./model");
  const { account, storage } = await webMember(f);
  const r = new (class extends Remote {
    constructor() {
      super(account, f.legacyKeys, undefined, storage);
    }
  })();
  try {
    const seed = crypto.getRandomValues(new Uint8Array(32));
    const key = await importSessionKey(seed);
    const sid = "synthetic-session";
    const session = {
      id: sid,
      machineId: f.machine,
      key,
      metaRev: 0n,
      lastSeq: 0n,
      updatedMs: 0n,
      endedMs: 0n,
    };
    r.sessions.set(sid, session);
    (
      r as unknown as { sessionSeeds: Map<string, Uint8Array> }
    ).sessionSeeds.set(sid, seed);
    (r as unknown as { api: object }).api = {
      getEvents: async () => ({ events: [], more: false }),
    };
    r.events(sid);
    (r as unknown as { outboxes: Map<string, unknown[]> }).outboxes.set(sid, [
      {
        sendId: "same-id",
        text: "local pending",
        status: "failed",
        unknown: true,
        ...newSlot(),
      },
    ]);
    const forged = [
      create(EventSchema, {
        seq: 1n,
        sessionId: sid,
        kind: {
          case: "inputState",
          value: { requestId: "same-id", status: InputStatus.CANCELED },
        },
      }),
      create(EventSchema, {
        seq: 2n,
        sessionId: sid,
        kind: {
          case: "userMessage",
          value: { requestId: "same-id", text: "forged delivery" },
        },
      }),
    ];
    const stored = await Promise.all(
      forged.map(async (event) => ({
        seq: event.seq,
        envelope: await sealMessage(
          key,
          { kind: 1, machineId: f.machine, sessionId: sid, seq: event.seq },
          EventSchema,
          event,
        ),
      })),
    );
    await (
      r as unknown as {
        ingest(
          s: typeof session,
          rows: typeof stored,
          fill: boolean,
          save: boolean,
        ): Promise<void>;
      }
    ).ingest(session, stored, false, false);
    expect(r.hasUnverifiedHistory(sid)).toBe(true);
    expect(r.outbox(sid)).toHaveLength(1);
    const current = r.currentDerived(sid, derive(r.events(sid)));
    expect(current.inputs.has("same-id")).toBe(false);
    expect(current.accepted.has("same-id")).toBe(false);
  } finally {
    r.stop();
    await f.fixture.close();
  }
}, 20000);
