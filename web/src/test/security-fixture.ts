import { inject } from "vitest";
/// <reference types="node" />
// Actual Go protocol and state chain for synthetic Web SDK integration tests.
// This module is imported only by tests, never by the application.
import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { fromBinary, toBinary, create } from "@bufbuild/protobuf";
import { IDBFactory } from "fake-indexeddb";
import { SecurityStoreV4 } from "../core/security-store-v4";
import { concat, hex } from "../crypto";
import { EnvelopeSchema, HeaderSchema, type Envelope, type Header } from "../gen/remoteagent/v1/envelope_pb";
import type { StoredAccount } from "../storage";
import {
  encodeV4Record,
  parseTestLease,
  parseTestState,
  seedV4Database,
  type TestLease,
  type TestState,
} from "./stored-security-fixture";

type B = Uint8Array<ArrayBuffer>;
type Reply = Record<string, unknown>;
const b64 = (b: Uint8Array) => Buffer.from(b).toString("base64");
const raw = (r: Reply, key: string): B =>
  new Uint8Array(Buffer.from((r[key] as string | undefined) ?? "", "base64"));

export interface FixtureRecordJSON {
  version: number;
  lease: string;
  account: string;
  domain: string;
  genesis: string;
  member: string;
  x: string;
  states: string[];
  proofs: (string | null)[];
}

function raw64(s: string): B {
  return new Uint8Array(Buffer.from(s, "base64"));
}

export async function xKey(seed: B): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "pkcs8",
    concat(new Uint8Array(Buffer.from("302e020100300506032b656e04220420", "hex")), seed),
    "X25519",
    false,
    ["deriveBits"],
  );
}

export async function edKey(seed: B): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "pkcs8",
    concat(new Uint8Array(Buffer.from("302e020100300506032b657004220420", "hex")), seed),
    "Ed25519",
    false,
    ["sign"],
  );
}

export interface DecodedFixtureRecord {
  recordBlob: B;
  pin: { account: string; domain: B; genesis: B };
  memberID: string;
  states: TestState[];
  rawStates: B[];
  rawProofs: (B | undefined)[];
  lease: TestLease;
  head: TestState;
  xPrivate: CryptoKey;
  edPublic: B;
  xPublic: B;
  server: string;
}

export async function recordFromJSON(
  r: FixtureRecordJSON,
  server = "https://ra.example.com",
): Promise<DecodedFixtureRecord> {
  if (r.version !== 3 || !r.states?.length || r.states.length !== r.proofs.length) {
    throw new Error("bad synthetic v3 record");
  }
  const rawStates = r.states.map(raw64);
  const rawProofs = r.proofs.map((p) => (p ? raw64(p) : undefined));
  const states = rawStates.map(parseTestState);
  const head = states.at(-1)!;
  const self = head.core.members.find((m) => m.id === r.member);
  if (!self) throw new Error(`member ${r.member} not in head state`);
  const lease = parseTestLease(raw64(r.lease));
  const pin = {
    account: r.account,
    domain: raw64(r.domain),
    genesis: raw64(r.genesis),
  };
  const xPrivate = await xKey(raw64(r.x));
  const recordBlob = encodeV4Record({
    pin,
    memberID: r.member,
    member: true,
    server,
    rawStates,
    rawProofs,
    rawLease: lease.raw,
    floorMs: lease.issuedAt,
  });
  return {
    recordBlob,
    pin,
    memberID: r.member,
    states,
    rawStates,
    rawProofs,
    lease,
    head,
    xPrivate,
    edPublic: self.edPublic,
    xPublic: self.xPublic,
    server,
  };
}

export async function createTestSecurityStore(
  r: FixtureRecordJSON,
  edSeed: B,
  server = "https://ra.example.com",
): Promise<{
  store: SecurityStoreV4;
  idb: IDBFactory;
  record: DecodedFixtureRecord;
  edPrivate: CryptoKey;
}> {
  const record = await recordFromJSON(r, server);
  const edPrivate = await edKey(edSeed);
  const idb = new IDBFactory();
  await seedV4Database(idb, {
    recordBlob: record.recordBlob,
    edPrivate,
    xPrivate: record.xPrivate,
    edPublic: record.edPublic,
    xPublic: record.xPublic,
  });
  const store = new SecurityStoreV4(() => idb);
  return { store, idb, record, edPrivate };
}

export class GoSecurityFixture {
  private readonly process: ChildProcessWithoutNullStreams;
  private serial: Promise<unknown> = Promise.resolve();
  private waiting?: { resolve: (r: Reply) => void; reject: (e: Error) => void };
  private buffer = "";
  private closing?: Promise<void>;
  phone!: { json: FixtureRecordJSON };
  machine!: string;
  machineRecord!: { json: FixtureRecordJSON };
  rootRaw!: B;

  private constructor() {
    this.process = spawn(inject("securityFixtureBinary"), [], { stdio: "pipe" });
    this.process.stdout.setEncoding("utf8");
    this.process.stdout.on("data", (chunk: string) => {
      this.buffer += chunk;
      const end = this.buffer.indexOf("\n");
      if (end < 0) return;
      const line = this.buffer.slice(0, end);
      this.buffer = this.buffer.slice(end + 1);
      const waiter = this.waiting;
      this.waiting = undefined;
      try {
        const reply = JSON.parse(line) as Reply;
        if (reply.Error) throw new Error(String(reply.Error));
        waiter?.resolve(reply);
      } catch (e) {
        waiter?.reject(e as Error);
      }
    });
    this.process.on("exit", () => {
      this.waiting?.reject(new Error("synthetic Go fixture exited"));
      this.waiting = undefined;
    });
  }

  call(op: string, fields: Reply = {}): Promise<Reply> {
    const reply = this.serial.then(
      () =>
        new Promise<Reply>((resolve, reject) => {
          this.waiting = { resolve, reject };
          this.process.stdin.write(JSON.stringify({ Op: op, ...fields }) + "\n");
        }),
    );
    this.serial = reply.catch(() => undefined);
    return reply;
  }

  close(): Promise<void> {
    return (this.closing ??= this.finishClose());
  }

  private async finishClose() {
    await this.serial;
    if (this.process.exitCode !== null) return;
    await new Promise<void>((resolve) => {
      const timer = setTimeout(() => this.process.kill("SIGKILL"), 3000);
      this.process.once("exit", () => {
        clearTimeout(timer);
        resolve();
      });
      this.process.stdin.end();
    });
  }

  async seal(
    member: string,
    header: Omit<Partial<Header>, "$typeName">,
    plain: B,
    seed?: B,
  ): Promise<Envelope> {
    const r = await this.call("seal", {
      Member: member,
      Header: b64(toBinary(HeaderSchema, create(HeaderSchema, header))),
      Plain: b64(plain),
      SessionSeed: b64(seed ?? new Uint8Array()),
    });
    return fromBinary(EnvelopeSchema, raw(r, "Envelope"));
  }

  async open(member: string, envelope: Envelope, seed?: B) {
    const r = await this.call("open", {
      Member: member,
      Envelope: b64(toBinary(EnvelopeSchema, envelope)),
      SessionSeed: b64(seed ?? new Uint8Array()),
    });
    return {
      header: fromBinary(HeaderSchema, raw(r, "Header")),
      plaintext: raw(r, "Plain"),
    };
  }

  async recover(seed = crypto.getRandomValues(new Uint8Array(32))): Promise<FixtureRecordJSON> {
    const reply = await this.call("recover", { Seed: b64(seed) });
    this.phone = { json: reply.Record as FixtureRecordJSON };
    return this.phone.json;
  }

  async record(member: string): Promise<FixtureRecordJSON> {
    return (await this.call("record", { Member: member })).Record as FixtureRecordJSON;
  }

  async chain(member: string): Promise<{ states: TestState[]; rawStates: B[]; rawProofs: (B | undefined)[] }> {
    const json = await this.record(member);
    const rawStates = json.states.map(raw64);
    const rawProofs = json.proofs.map((p) => (p ? raw64(p) : undefined));
    return { states: rawStates.map(parseTestState), rawStates, rawProofs };
  }

  async statesResponse(member: string, after: bigint, limit: number) {
    const json = await this.record(member);
    const rawStates = json.states.map(raw64);
    const rawProofs = json.proofs.map((p) => (p ? raw64(p) : undefined));
    const states = rawStates.map(parseTestState);
    const out: { state: Uint8Array; recovery: Uint8Array }[] = [];
    for (let i = 0; i < states.length && out.length < limit; i++) {
      if (states[i].core.epoch <= after) continue;
      out.push({
        state: rawStates[i],
        recovery: rawProofs[i] ?? new Uint8Array(),
      });
    }
    return {
      states: out,
      headEpoch: states[states.length - 1].core.epoch,
      lease: raw64(json.lease),
      recoveryPending: false,
      recoveryReadyMs: 0n,
    };
  }

  async enroll(approver: string, kind = 2, seed: B = crypto.getRandomValues(new Uint8Array(32))) {
    const reply = await this.call("enroll", {
      Approver: approver,
      Pair: crypto.randomUUID(),
      Kind: kind,
      Seed: b64(seed),
    });
    return {
      json: reply.Record as FixtureRecordJSON,
      seed,
      signingKey: await edKey(seed),
    };
  }

  /** Removes members as the synthetic authority and returns the new state. */
  async remove(targets: string[]): Promise<B> {
    const reply = await this.call("remove", { Targets: targets });
    return raw(reply, "State");
  }

  static async create(machineSeed?: B) {
    const f = new GoSecurityFixture();
    const rootRaw = crypto.getRandomValues(new Uint8Array(32));
    const accountDigest = new Uint8Array(await crypto.subtle.digest("SHA-256", rootRaw));
    const accountId = hex(accountDigest).slice(0, 32);
    const seed = machineSeed ?? crypto.getRandomValues(new Uint8Array(32));
    const result = await f.call("init", {
      Account: accountId,
      Seed: b64(seed),
    });
    f.phone = { json: result.Record as FixtureRecordJSON };
    const machine = await f.enroll(f.phone.json.member, 1);
    f.machine = machine.json.member;
    f.machineRecord = { json: machine.json };
    f.rootRaw = rootRaw;
    return {
      fixture: f,
      machine: f.machine,
      phone: f.phone,
      rootRaw,
      machineRecord: f.machineRecord,
    };
  }
}

export async function webMember(
  f: Awaited<ReturnType<typeof GoSecurityFixture.create>>,
  seed?: B,
) {
  const client = await f.fixture.enroll(f.machine, 2, seed);
  const { store, idb, record, edPrivate } = await createTestSecurityStore(
    client.json,
    client.seed,
    "https://ra.example.com",
  );
  const account: StoredAccount = {
    server: "https://ra.example.com",
    deviceId: record.memberID,
    accountId: record.pin.account,
  };
  return { account, store, idb, record, client, edPrivate };
}
