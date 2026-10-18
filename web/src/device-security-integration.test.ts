import { afterAll, afterEach, expect, it, vi } from "vitest";
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

it("reserves unique persisted-message counters before concurrent seals", async () => {
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
    const counters = messages.map((m) =>
      parseHeader(new Uint8Array(m.securityHeader)).counter.toString(),
    );
    expect(new Set(counters).size).toBe(messages.length);
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
