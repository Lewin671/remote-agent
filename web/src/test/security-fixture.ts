/// <reference types="node" />
// Actual Go protocol and state chain for synthetic Web SDK integration tests.
// This module is imported only by tests, never by the application.
import {
  execFileSync,
  spawn,
  type ChildProcessWithoutNullStreams,
} from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { fromBinary, toBinary, create } from "@bufbuild/protobuf";
import {
  EnvelopeSchema,
  HeaderSchema,
  type Header,
  type Envelope,
} from "../gen/remoteagent/v1/envelope_pb";
import {
  concat,
  deviceID,
  importRoot,
  accountKey,
  deriveKeys,
} from "../crypto";
import {
  DeviceSecurityClient,
  type FetchStates,
  type SecurityStorage,
} from "../device-security-client";
import { buildRecord, type SecurityRecord } from "../device-security-store";
import {
  equal,
  parseLease,
  parseRecoveryStatement,
  parseState,
  recoveryBytes,
  stateBytes,
  SecurityError,
  type RecoveryStatement,
  type LeaseCertificate,
  verifyLease,
  type SecurityState,
} from "../device-security";
import type { StoredAccount } from "../storage";

type B = Uint8Array<ArrayBuffer>;
type Reply = Record<string, unknown>;
const b64 = (b: Uint8Array) => Buffer.from(b).toString("base64");
const raw = (r: Reply, key: string): B =>
  new Uint8Array(Buffer.from((r[key] as string | undefined) ?? "", "base64"));
let binary: string | undefined;
let buildDirectory: string | undefined;
function fixtureBinary(): string {
  if (binary) return binary;
  buildDirectory = mkdtempSync(join(tmpdir(), "ra-web-security-"));
  binary = join(buildDirectory, "fixture");
  execFileSync(
    "go",
    ["build", "-o", binary, "./internal/testutil/securityfixture"],
    {
      cwd: resolve(fileURLToPath(new URL("../../..", import.meta.url))),
      stdio: "pipe",
    },
  );
  return binary!;
}
export function removeFixtureBuild() {
  if (buildDirectory) rmSync(buildDirectory, { recursive: true, force: true });
  binary = undefined;
  buildDirectory = undefined;
}

interface FixtureRecordJSON {
  version: number;
  lease:string;
  account: string;
  domain: string;
  genesis: string;
  member: string;
  x: string;
  states: string[];
  proofs: (string | null)[];
}
async function chainFromJSON(r: FixtureRecordJSON): Promise<{
  states: SecurityState[];
  proofs: (RecoveryStatement | undefined)[];
}> {
  if (
    r.version !== 3 ||
    !r.states?.length ||
    r.states.length !== r.proofs.length
  )
    throw new Error("bad synthetic v3 record");
  const states: SecurityState[] = [];
  const proofs: (RecoveryStatement | undefined)[] = [];
  for (let i = 0; i < r.states.length; i++) {
    states.push(await parseState(raw64(r.states[i])));
    const proof = r.proofs[i];
    proofs.push(proof ? await parseRecoveryStatement(raw64(proof)) : undefined);
  }
  return { states, proofs };
}
function raw64(s: string): B {
  return new Uint8Array(Buffer.from(s, "base64"));
}
async function xKey(seed: B): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "pkcs8",
    concat(
      new Uint8Array(Buffer.from("302e020100300506032b656e04220420", "hex")),
      seed,
    ),
    "X25519",
    false,
    ["deriveBits"],
  );
}
async function edKey(seed: B): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "pkcs8",
    concat(
      new Uint8Array(Buffer.from("302e020100300506032b657004220420", "hex")),
      seed,
    ),
    "Ed25519",
    false,
    ["sign"],
  );
}
export async function recordFromJSON(r: FixtureRecordJSON): Promise<{
  record: SecurityRecord;
  states: SecurityState[];
  proofs: (RecoveryStatement | undefined)[];
}> {
  const { states, proofs } = await chainFromJSON(r);
  const pin = {
    account: r.account,
    domain: raw64(r.domain),
    genesis: raw64(r.genesis),
  };
  const record = await buildRecord(
    pin,
    states,
    proofs,
    r.member,
    await xKey(raw64(r.x)),
  );
  record.lease=parseLease(raw64(r.lease));
  return { record, states, proofs };
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
  root!: CryptoKey;
  legacyKeys!: Awaited<ReturnType<typeof deriveKeys>>;
  private constructor() {
    this.process = spawn(fixtureBinary(), [], { stdio: "pipe" });
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
          this.process.stdin.write(
            JSON.stringify({ Op: op, ...fields }) + "\n",
          );
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
  async recover(
    seed = crypto.getRandomValues(new Uint8Array(32)),
  ): Promise<FixtureRecordJSON> {
    const reply = await this.call("recover", { Seed: b64(seed) });
    this.phone = { json: reply.Record as FixtureRecordJSON };
    return this.phone.json;
  }
  async record(member: string): Promise<FixtureRecordJSON> {
    return (await this.call("record", { Member: member }))
      .Record as FixtureRecordJSON;
  }
  async chain(member: string): Promise<{
    states: SecurityState[];
    proofs: (RecoveryStatement | undefined)[];
  }> {
    return chainFromJSON(await this.record(member));
  }
  fetcher(member: string): FetchStates {
    return async (after, limit) => {
      const json=await this.record(member);
      const { states, proofs } = await chainFromJSON(json);
      const out: { state: Uint8Array; recovery: Uint8Array }[] = [];
      for (let i = 0; i < states.length && out.length < limit; i++) {
        if (states[i].core.epoch <= after) continue;
        out.push({
          state: await stateBytes(states[i]),
          recovery: proofs[i]
            ? await recoveryBytes(proofs[i]!)
            : new Uint8Array(),
        });
      }
      return {
        states: out,
        headEpoch: states[states.length - 1].core.epoch,
        lease: raw64(json.lease),
      };
    };
  }
  async enroll(
    approver: string,
    kind = 2,
    seed: B = crypto.getRandomValues(new Uint8Array(32)),
  ) {
    const reply = await this.call("enroll", {
      Approver: approver,
      Pair: crypto.randomUUID(),
      Kind: kind,
      Seed: b64(seed),
    });
    return {
      json: reply.Record as FixtureRecordJSON,
      signingKey: await edKey(seed),
    };
  }
  /** Removes members as the synthetic authority and returns the new state. */
  async remove(targets: string[]): Promise<B> {
    const reply = await this.call("remove", { Targets: targets });
    return raw(reply, "State");
  }
  /** The fixture's chain and record for a still-known member. */
  async recordAndChain(member: string) {
    return recordFromJSON(await this.record(member));
  }
  static async create(machineSeed?: B) {
    const f = new GoSecurityFixture();
    const rootRaw = crypto.getRandomValues(new Uint8Array(32));
    const root = await importRoot(rootRaw);
    const account = await accountKey(root);
    const seed = machineSeed ?? crypto.getRandomValues(new Uint8Array(32));
    const result = await f.call("init", {
      Account: await deviceID(new Uint8Array(account.publicKey)),
      Seed: b64(seed),
    });
    f.phone = { json: result.Record as FixtureRecordJSON };
    // The first device is the phone (a Client). Tests that need a real
    // Machine enroll one; the phone stays the authority.
    const machine = await f.enroll(f.phone.json.member, 1);
    f.machine = machine.json.member;
    f.machineRecord = { json: machine.json };
    f.rootRaw = rootRaw;
    f.root = root;
    f.legacyKeys = await deriveKeys(root);
    return {
      fixture: f,
      machine: f.machine,
      phone: f.phone,
      rootRaw,
      root,
      legacyKeys: f.legacyKeys,
      machineRecord: f.machineRecord,
    };
  }
}

/** Atomic in-memory state for protocol tests. Production uses strict IndexedDB. */
export class MemorySecurityStorage implements SecurityStorage {
  private record: SecurityRecord;
  constructor(record: SecurityRecord) {
    this.record = record;
  }
  async loadSecurity() {
    return this.record;
  }
  async installSecurity(
    expected: SecurityRecord,
    states: SecurityState[],
    proofs: (RecoveryStatement | undefined)[],
    lease?:LeaseCertificate,
  ) {
    if (!equal(expected.stateHash, this.record.stateHash))
      throw new SecurityError("stale");
    const { extendRecord } = await import("../device-security-store");
    this.record = { ...await extendRecord(this.record, states, proofs) };
    if(lease){await verifyLease(lease,this.record.states[this.record.states.length-1]);this.record.lease=lease;}
  }
  async touchSecurity(now: bigint) {
    if (now > this.record.floor) this.record = { ...this.record, floor: now };
  }
}

export async function webMember(
  f: Awaited<ReturnType<typeof GoSecurityFixture.create>>,
  seed?: B,
) {
  const client = await f.fixture.enroll(f.machine, 2, seed);
  const { record } = await recordFromJSON(client.json);
  const storage = new MemorySecurityStorage(record);
  const account: StoredAccount = {
    server: "https://ra.example.com",
    deviceId: record.memberID,
    devicePrivate: client.signingKey,
  };
  const sdk = new DeviceSecurityClient(
    account.deviceId,
    account.devicePrivate,
    f.fixture.fetcher(record.memberID),
    () => true,
    storage,
  );
  return { account, storage, sdk, record, client };
}
