/// <reference types="node" />
// Actual Go authority and protocol for synthetic Web SDK integration tests.
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
  importRoot,
  accountKey,
  deviceID,
  deriveKeys,
} from "../crypto";
import {
  DeviceSecurityClient,
  type SecurityStorage,
} from "../device-security-client";
import {
  advance,
  equal,
  openGrant,
  parseState,
  stateHash,
  stateBytes,
  verifyState,
  SecurityError,
  type Pin,
  type SecurityState,
  parseCapsule,
  capsuleBytes,
} from "../device-security";
import type {
  CachedEpoch,
  SecurityRecord,
  StoredRemoval,
} from "../device-security-store";
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

export class GoSecurityFixture {
  private readonly process: ChildProcessWithoutNullStreams;
  private serial: Promise<unknown> = Promise.resolve();
  private waiting?: { resolve: (r: Reply) => void; reject: (e: Error) => void };
  private buffer = "";
  private closing?: Promise<void>;
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
  async rpc(capsule: B): Promise<B> {
    return raw(await this.call("rpc", { Capsule: b64(capsule) }), "Capsule");
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
    const record = await decodeRecord(raw(result, "Record"));
    const machine = record.memberID;
    return {
      fixture: f,
      machine,
      rootRaw,
      root,
      legacyKeys: await deriveKeys(root),
      machineRecord: record,
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
      record: await decodeRecord(raw(reply, "Record")),
      signingKey: await edKey(seed),
    };
  }
}
async function edKey(seed: B) {
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
async function contentKey(secret: B) {
  return crypto.subtle.importKey("raw", secret, "HKDF", false, ["deriveBits"]);
}
async function decodeRecord(bytes: B): Promise<SecurityRecord> {
  let at = 0;
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const take = (count: number): B => {
    if (count < 0 || at + count > bytes.length)
      throw new Error("bad synthetic record");
    const b = bytes.slice(at, at + count);
    at += count;
    return b;
  };
  const u32 = () => {
    const n = view.getUint32(at);
    at += 4;
    return n;
  };
  const lp = () => take(u32());
  const str = () => new TextDecoder().decode(lp());
  if (u32() !== 2) throw new Error("wrong synthetic format");
  const pin: Pin = {
    account: str(),
    domain: take(32),
    genesis: take(32),
    authorityID: str(),
    authorityEd: take(32),
    authorityX: take(32),
  };
  const state = lp();
  const memberID = str();
  const x = take(32);
  const counter = view.getBigUint64(at);
  at += 8;
  if (at !== bytes.length) throw new Error("trailing synthetic record");
  const xPrivate = await crypto.subtle.importKey(
    "pkcs8",
    concat(
      new Uint8Array(Buffer.from("302e020100300506032b656e04220420", "hex")),
      x,
    ),
    "X25519",
    false,
    ["deriveBits"],
  );
  const parsed = await parseState(state);
  await verifyState(pin, parsed);
  const secret = await contentKey(
    await openGrant(pin, parsed, memberID, xPrivate),
  );
  return {
    version: 2,
    pin,
    state,
    memberID,
    xPrivate,
    counter,
    stateHash: await stateHash(parsed),
    secret,
  };
}

/** Atomic in-memory state for protocol tests. Production uses strict IndexedDB. */
export class MemorySecurityStorage implements SecurityStorage {
  private record: SecurityRecord;
  private history = new Map<bigint, CachedEpoch>();
  private removals = new Map<string, StoredRemoval>();
  constructor(record: SecurityRecord) {
    this.record = record;
  }
  async loadSecurity() {
    return this.record;
  }
  async reserveCounter(expected: SecurityRecord) {
    if (!equal(expected.stateHash, this.record.stateHash))
      throw new SecurityError("stale");
    if (this.record.counter === 0xffffffffffffffffn)
      throw new SecurityError("capacity");
    this.record = { ...this.record, counter: this.record.counter + 1n };
    return this.record;
  }
  async installSecurity(expected: SecurityRecord, next: SecurityState) {
    const before = await parseState(expected.state);
    await advance(expected.pin, before, next);
    const secret = await contentKey(
      await openGrant(expected.pin, next, expected.memberID, expected.xPrivate),
    );
    const bytes = await stateBytes(next);
    const hash = await stateHash(next);
    if (!equal(this.record.stateHash, expected.stateHash))
      throw new SecurityError("stale");
    this.history.set(before.core.epoch, {
      state: expected.state,
      key: expected.secret,
    });
    this.record = {
      ...expected,
      state: bytes,
      stateHash: hash,
      secret,
      counter: 0n,
    };
  }
  async readSecurityEpoch(epoch: bigint) {
    return this.history.get(epoch);
  }
  async cacheSecurityEpoch(state: SecurityState, secret: B) {
    await verifyState(this.record.pin, state);
    this.history.set(state.core.epoch, {
      state: await stateBytes(state),
      key: await contentKey(secret),
    });
  }
  async saveRemoval(op: StoredRemoval) {
    if (
      op.request.Requester !== this.record.memberID ||
      op.request.Account !== this.record.pin.account
    )
      throw new SecurityError("untrusted");
    this.removals.set(op.request.Operation, op);
  }
  async loadRemovals() {
    return [...this.removals.values()];
  }
}

export async function webMember(
  f: Awaited<ReturnType<typeof GoSecurityFixture.create>>,
  seed?: B,
) {
  const client = await f.fixture.enroll(f.machine, 2, seed);
  const storage = new MemorySecurityStorage(client.record);
  const account: StoredAccount = {
    server: "https://ra.example.com",
    root: f.root,
    rootRaw: f.rootRaw,
    deviceId: client.record.memberID,
    devicePrivate: client.signingKey,
  };
  const sdk = new DeviceSecurityClient(
    account.deviceId,
    account.devicePrivate,
    async (capsule) => parseCapsule(await f.fixture.rpc(capsuleBytes(capsule))),
    () => true,
    storage,
  );
  return { account, storage, sdk };
}
