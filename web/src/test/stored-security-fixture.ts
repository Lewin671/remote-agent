import { create, toBinary } from "@bufbuild/protobuf";
import { inflateRawSync } from "node:zlib";
import { IDBFactory } from "fake-indexeddb";
import { vi } from "vitest";
import {
  V4_DB_NAME,
  V4_STORE_BUCKETS,
  V4_STORE_KEYS,
  V4_STORE_META,
} from "../core/security-store-v4";
import { concat, fromHex, utf8 } from "../crypto";
import { SecurityKeyKind, SecurityStoreBucket } from "../gen/remoteagent/core/v1/security_pb";
import { EnvelopeSchema, HeaderSchema, type Envelope } from "../gen/remoteagent/v1/envelope_pb";
import type { Event } from "../gen/remoteagent/v1/event_pb";
import { Purpose, SecurityError } from "../remote";
import vectors from "../../../internal/deviceauth/testdata/domain-v3.json";

type B = Uint8Array<ArrayBuffer>;

const toAB = (src: Uint8Array): B => {
  const out = new Uint8Array(src.byteLength);
  out.set(src);
  return out;
};
const u32be = (n: number): B => {
  const b = new Uint8Array(4);
  new DataView(b.buffer).setUint32(0, n, false);
  return b;
};
const u64be = (n: bigint): B => {
  const b = new Uint8Array(8);
  new DataView(b.buffer).setBigUint64(0, n, false);
  return b;
};
const u8byte = (v: boolean): B => new Uint8Array([v ? 1 : 0]);
const lpBytes = (b: Uint8Array): B => concat(u32be(b.length), toAB(b));
const lpStr = (s: string): B => lpBytes(utf8(s));

export function unb64(s: string): B {
  const pad = s.replace(/-/g, "+").replace(/_/g, "/");
  return new Uint8Array(Buffer.from(pad, "base64"));
}

export interface SecurityHeader {
  account: string;
  domain: Uint8Array;
  epoch: bigint;
  state: Uint8Array;
  contentEpoch: bigint;
  sender: string;
  kind: number;
  purpose: Purpose;
  machine: string;
  session: string;
  request: string;
  seq: bigint;
  deadline: bigint;
  compression: number;
}

export interface TestMember {
  id: string;
  edPublic: B;
  xPublic: B;
  kind: number;
}

export interface TestCore {
  account: string;
  domain: B;
  authorityID: string;
  recoveryEd: B;
  parent: B;
  epoch: bigint;
  contentEpoch: bigint;
  issuedAt: bigint;
  members: TestMember[];
  removed: string[];
}

export interface TestState {
  raw: B;
  coreBytes: B;
  grantBytes: B;
  core: TestCore;
  grants: { recipient: string; enc: B; cipher: B }[];
  prevSecret: B;
  signature: B;
}

export interface TestLease {
  raw: B;
  domain: B;
  head: B;
  issuedAt: bigint;
  notAfter: bigint;
  signature: B;
}

class TestReader {
  pos = 0;
  private readonly view: DataView;
  constructor(private readonly b: Uint8Array) {
    this.view = new DataView(b.buffer, b.byteOffset, b.byteLength);
  }
  u32(): number {
    if (this.pos + 4 > this.b.length) throw new SecurityError("invalid");
    const v = this.view.getUint32(this.pos, false);
    this.pos += 4;
    return v;
  }
  u64(): bigint {
    if (this.pos + 8 > this.b.length) throw new SecurityError("invalid");
    const v = this.view.getBigUint64(this.pos, false);
    this.pos += 8;
    return v;
  }
  raw(n: number): B {
    if (this.pos + n > this.b.length) throw new SecurityError("invalid");
    const out = toAB(this.b.subarray(this.pos, this.pos + n));
    this.pos += n;
    return out;
  }
  lp(): B {
    return this.raw(this.u32());
  }
  str(): string {
    return new TextDecoder("utf-8", { fatal: true }).decode(this.lp());
  }
  done(): void {
    if (this.pos !== this.b.length) throw new SecurityError("invalid");
  }
}

export function parseTestState(raw: Uint8Array): TestState {
  const bytes = toAB(raw);
  const r = new TestReader(bytes);
  const coreBytes = r.lp();
  const cr = new TestReader(coreBytes);
  if (cr.u32() !== 3) throw new SecurityError("invalid");
  const core: TestCore = {
    account: cr.str(),
    domain: cr.raw(32),
    authorityID: cr.str(),
    recoveryEd: cr.raw(32),
    parent: cr.raw(32),
    epoch: cr.u64(),
    contentEpoch: cr.u64(),
    issuedAt: cr.u64(),
    members: [],
    removed: [],
  };
  const mc = cr.u32();
  for (let i = 0; i < mc; i++) {
    core.members.push({
      id: cr.str(),
      edPublic: cr.raw(32),
      xPublic: cr.raw(32),
      kind: cr.u32(),
    });
  }
  const rc = cr.u32();
  for (let i = 0; i < rc; i++) {
    core.removed.push(cr.str());
  }
  cr.done();

  const grantStart = r.pos;
  const gc = r.u32();
  const grants: { recipient: string; enc: B; cipher: B }[] = [];
  for (let i = 0; i < gc; i++) {
    grants.push({ recipient: r.str(), enc: r.lp(), cipher: r.lp() });
  }
  const grantBytes = toAB(bytes.subarray(grantStart, r.pos));
  const prevSecret = r.lp();
  const signature = r.lp();
  r.done();
  return { raw: bytes, coreBytes, grantBytes, core, grants, prevSecret, signature };
}

export function parseTestLease(raw: Uint8Array): TestLease {
  const bytes = toAB(raw);
  const r = new TestReader(bytes);
  const body = r.lp();
  const signature = r.lp();
  r.done();
  const prefix = utf8("ra3/lease\0");
  if (body.length < prefix.length || !body.subarray(0, prefix.length).every((v, i) => v === prefix[i])) {
    throw new SecurityError("invalid");
  }
  const br = new TestReader(body.subarray(prefix.length));
  const domain = br.raw(32);
  const head = br.raw(32);
  const issuedAt = br.u64();
  const notAfter = br.u64();
  br.done();
  return { raw: bytes, domain, head, issuedAt, notAfter, signature };
}

export async function stateHash(state: TestState): Promise<B> {
  const coreHash = new Uint8Array(
    await crypto.subtle.digest("SHA-256", concat(utf8("ra3/core\0"), state.coreBytes)),
  );
  return new Uint8Array(
    await crypto.subtle.digest(
      "SHA-256",
      concat(utf8("ra3/state\0"), coreHash, state.grantBytes, lpBytes(state.prevSecret)),
    ),
  );
}

export function encodeTestHeader(h: SecurityHeader): B {
  return concat(
    u32be(3),
    lpStr(h.account),
    toAB(h.domain),
    u64be(h.epoch),
    toAB(h.state),
    u64be(h.contentEpoch),
    lpStr(h.sender),
    u32be(h.kind),
    u32be(h.purpose),
    lpStr(h.machine),
    lpStr(h.session),
    lpStr(h.request),
    u64be(h.seq),
    u64be(h.deadline),
    u32be(h.compression),
  );
}

export function parseTestHeader(raw: Uint8Array): SecurityHeader {
  const r = new TestReader(raw);
  if (r.u32() !== 3) throw new SecurityError("invalid");
  const account = r.str();
  const domain = r.raw(32);
  const epoch = r.u64();
  const state = r.raw(32);
  const contentEpoch = r.u64();
  const sender = r.str();
  const kind = r.u32();
  const purpose = r.u32() as Purpose;
  const machine = r.str();
  const session = r.str();
  const request = r.str();
  const seq = r.u64();
  const deadline = r.u64();
  const compression = r.u32();
  r.done();
  return { account, domain, epoch, state, contentEpoch, sender, kind, purpose, machine, session, request, seq, deadline, compression };
}

export function unpackTestBody(h: SecurityHeader, raw: Uint8Array): B {
  if (h.compression === 0) return toAB(raw);
  if (h.compression === 1) {
    const small = [Purpose.SessionMeta, Purpose.MachineMeta, Purpose.Prefs, Purpose.DeviceMeta].includes(h.purpose);
    try {
      return toAB(inflateRawSync(raw, { maxOutputLength: small ? 65536 : 4 << 20 }));
    } catch {
      throw new SecurityError("invalid");
    }
  }
  throw new SecurityError("invalid");
}

export async function testRequestIdentity(h: SecurityHeader, unpacked: Uint8Array): Promise<{ key: B; payload: B }> {
  const keyInput = concat(
    utf8("ra4/request-key\0"),
    lpStr(h.account),
    toAB(h.domain),
    lpStr(h.sender),
    u32be(h.purpose),
    lpStr(h.machine),
    lpStr(h.session),
    lpStr(h.request),
    u64be(h.seq),
  );
  const key = new Uint8Array(await crypto.subtle.digest("SHA-256", keyInput));
  const payloadInput = concat(
    utf8("ra4/authorized-payload\0"),
    lpBytes(key),
    u32be(h.kind),
    u64be(h.deadline),
    lpBytes(unpacked),
  );
  const payload = new Uint8Array(await crypto.subtle.digest("SHA-256", payloadInput));
  return { key, payload };
}

export async function sealTestEnvelope(
  secret: Uint8Array,
  seed: Uint8Array | undefined,
  header: SecurityHeader,
  plain: Uint8Array,
  signer: CryptoKey,
): Promise<Envelope> {
  const headerBytes = encodeTestHeader(header);
  const secretKey = await crypto.subtle.importKey("raw", toAB(secret), "HKDF", false, ["deriveBits", "deriveKey"]);
  const purposeInfo = concat(
    utf8("ra3/purpose-key\0"),
    lpStr(header.account),
    toAB(header.domain),
    u64be(header.contentEpoch),
    u32be(header.purpose),
  );
  const purposeBits = await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt: new Uint8Array(0), info: purposeInfo },
    secretKey,
    256,
  );
  const purposeKey = await crypto.subtle.importKey("raw", purposeBits, "HKDF", false, ["deriveBits", "deriveKey"]);
  const needsSeed = [1, 7, 8, 11, 14, 15].includes(header.purpose);
  const salt = needsSeed && seed ? toAB(seed) : new Uint8Array(0);
  const contentInfo = concat(
    utf8("ra3/content-key\0"),
    lpStr(header.account),
    toAB(header.domain),
    u64be(header.epoch),
    toAB(header.state),
    u64be(header.contentEpoch),
    u32be(header.purpose),
    lpStr(header.sender),
    lpStr(header.machine),
    lpStr(header.session),
    lpStr(header.request),
    u64be(header.seq),
  );
  const encKey = await crypto.subtle.deriveKey(
    { name: "HKDF", hash: "SHA-256", salt, info: contentInfo },
    purposeKey,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt"],
  );
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const ct = new Uint8Array(
    await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce, additionalData: headerBytes }, encKey, toAB(plain)),
  );
  const commit = new Uint8Array(
    await crypto.subtle.digest("SHA-256", concat(utf8("ra3/ciphertext\0"), lpBytes(headerBytes), lpBytes(nonce), lpBytes(ct))),
  );
  const hdrDigest = new Uint8Array(await crypto.subtle.digest("SHA-256", headerBytes));
  const originMsg = concat(
    utf8("ra3/cipher-origin\0"),
    lpStr(header.account),
    toAB(header.domain),
    toAB(header.state),
    lpStr(header.sender),
    hdrDigest,
    commit,
  );
  const signature = new Uint8Array(await crypto.subtle.sign("Ed25519", signer, originMsg));
  return create(EnvelopeSchema, {
    header: toBinary(
      HeaderSchema,
      create(HeaderSchema, {
        version: 3,
        kind: header.kind,
        machineId: header.machine,
        sessionId: header.session,
        requestId: header.request,
        seq: header.seq,
        deadlineMs: header.deadline,
        sender: header.sender,
        compression: header.compression,
      }),
    ),
    securityHeader: headerBytes,
    nonce,
    ciphertext: ct,
    signature,
  });
}

export interface EncodeV4RecordParams {
  pin: { account: string; domain: Uint8Array; genesis: Uint8Array };
  memberID: string;
  member?: boolean;
  server?: string;
  accountPublicKey?: Uint8Array;
  rawStates: Uint8Array[];
  rawProofs?: (Uint8Array | undefined)[];
  rawLease?: Uint8Array;
  floorMs?: bigint;
  contentKeys?: { epoch: bigint; secret: Uint8Array }[];
  needsRecoveryRotation?: boolean;
  recoveryObjectsPending?: boolean;
}

export function encodeV4Record(p: EncodeV4RecordParams): B {
  const parts: B[] = [
    utf8("RA4REC\0"),
    u32be(4),
    lpStr(p.pin.account),
    toAB(p.pin.domain),
    toAB(p.pin.genesis),
    lpStr(p.memberID),
    u8byte(p.member ?? true),
    lpStr(p.server ?? "https://ra.example.com"),
    lpBytes(p.accountPublicKey ?? new Uint8Array(0)),
    u32be(p.rawStates.length),
  ];
  for (let i = 0; i < p.rawStates.length; i++) {
    parts.push(lpBytes(p.rawStates[i]));
    parts.push(lpBytes(p.rawProofs?.[i] ?? new Uint8Array(0)));
  }
  parts.push(lpBytes(p.rawLease ?? new Uint8Array(0)));
  parts.push(u64be(p.floorMs ?? 0n));
  const cks = p.contentKeys ?? [];
  parts.push(u32be(cks.length));
  for (const ck of cks) {
    parts.push(u64be(ck.epoch));
    parts.push(toAB(ck.secret));
  }
  parts.push(u8byte(p.needsRecoveryRotation ?? false));
  parts.push(u8byte(p.recoveryObjectsPending ?? false));
  return concat(...parts);
}

export async function seedV4Database(
  idb: IDBFactory,
  params: {
    recordBlob: Uint8Array;
    edPrivate: CryptoKey;
    xPrivate: CryptoKey;
    edPublic: Uint8Array;
    xPublic: Uint8Array;
    revision?: string;
  },
): Promise<void> {
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const req = idb.open(V4_DB_NAME, 1);
    req.onupgradeneeded = () => {
      const d = req.result;
      if (!d.objectStoreNames.contains(V4_STORE_BUCKETS)) d.createObjectStore(V4_STORE_BUCKETS);
      if (!d.objectStoreNames.contains(V4_STORE_KEYS)) d.createObjectStore(V4_STORE_KEYS);
      if (!d.objectStoreNames.contains(V4_STORE_META)) d.createObjectStore(V4_STORE_META);
    };
    req.onerror = () => reject(req.error);
    req.onsuccess = () => resolve(req.result);
  });
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction([V4_STORE_BUCKETS, V4_STORE_KEYS], "readwrite");
    tx.objectStore(V4_STORE_BUCKETS).put(
      {
        bucket: SecurityStoreBucket.RECORD,
        key: "record",
        revision: params.revision ?? crypto.randomUUID(),
        data: toAB(params.recordBlob),
      },
      `${SecurityStoreBucket.RECORD}:record`,
    );
    tx.objectStore(V4_STORE_KEYS).put(
      { privateKey: params.edPrivate, publicKey: toAB(params.edPublic) },
      `${SecurityKeyKind.DEVICE_ED}:`,
    );
    tx.objectStore(V4_STORE_KEYS).put(
      { privateKey: params.xPrivate, publicKey: toAB(params.xPublic) },
      `${SecurityKeyKind.DEVICE_X}:`,
    );
    tx.oncomplete = () => {
      db.close();
      resolve();
    };
    tx.onabort = tx.onerror = () => {
      db.close();
      reject(tx.error);
    };
  });
}

async function rawV4Store<T>(
  storeName: string,
  mode: IDBTransactionMode,
  op: (s: IDBObjectStore) => IDBRequest<T>,
  idb: IDBFactory = indexedDB,
): Promise<T> {
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const req = idb.open(V4_DB_NAME, 1);
    req.onupgradeneeded = () => {
      const d = req.result;
      if (!d.objectStoreNames.contains(V4_STORE_BUCKETS)) d.createObjectStore(V4_STORE_BUCKETS);
      if (!d.objectStoreNames.contains(V4_STORE_KEYS)) d.createObjectStore(V4_STORE_KEYS);
      if (!d.objectStoreNames.contains(V4_STORE_META)) d.createObjectStore(V4_STORE_META);
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
  return new Promise<T>((resolve, reject) => {
    const tx = db.transaction(storeName, mode);
    const req = op(tx.objectStore(storeName));
    tx.oncomplete = () => {
      db.close();
      resolve(req.result);
    };
    tx.onabort = tx.onerror = () => {
      db.close();
      reject(tx.error);
    };
  });
}

export const rawV4Meta = <T>(
  mode: IDBTransactionMode,
  op: (s: IDBObjectStore) => IDBRequest<T>,
  idb?: IDBFactory,
) => rawV4Store<T>(V4_STORE_META, mode, op, idb);

export const rawV4Buckets = <T>(
  mode: IDBTransactionMode,
  op: (s: IDBObjectStore) => IDBRequest<T>,
  idb?: IDBFactory,
) => rawV4Store<T>(V4_STORE_BUCKETS, mode, op, idb);

export const rawV4Keys = <T>(
  mode: IDBTransactionMode,
  op: (s: IDBObjectStore) => IDBRequest<T>,
  idb?: IDBFactory,
) => rawV4Store<T>(V4_STORE_KEYS, mode, op, idb);

// Parse the immutable Go-generated fixture once per test file.
let fixture: ReturnType<typeof buildFixture> | undefined;
export function securityFixture() {
  return (fixture ??= buildFixture());
}

async function buildFixture() {
  const [genesis, added] = [vectors.genesis, vectors.added].map((v) => parseTestState(unb64(v.state)));
  const x = await crypto.subtle.importKey(
    "pkcs8",
    concat(fromHex("302e020100300506032b656e04220420"), fromHex(vectors.phone_x)),
    "X25519",
    false,
    ["deriveBits"],
  );
  const signingKey = (seed: string) =>
    crypto.subtle.importKey(
      "pkcs8",
      concat(fromHex("302e020100300506032b657004220420"), fromHex(seed)),
      "Ed25519",
      false,
      ["sign"],
    );
  const [ed, machineEd] = await Promise.all([signingKey(vectors.phone_seed), signingKey(vectors.machine_seed)]);
  const lease = parseTestLease(unb64(vectors.lease));
  const pin = { account: vectors.account, domain: fromHex(vectors.domain), genesis: fromHex(vectors.genesis.hash) };
  const memberID = genesis.core.authorityID;
  const selfMember = added.core.members.find((m) => m.id === memberID)!;
  return {
    pin,
    states: [genesis, added],
    rawStates: [genesis.raw, added.raw],
    memberID,
    edPublic: selfMember.edPublic,
    xPublic: selfMember.xPublic,
    x,
    ed,
    machineEd,
    lease,
    secret: fromHex(vectors.content_secret_2),
  };
}

export async function storedSecurity() {
  const f = await securityFixture();
  const idb = new IDBFactory();
  vi.stubGlobal("indexedDB", idb);
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(Number(f.lease.issuedAt));
  const recordBlob = encodeV4Record({
    pin: f.pin,
    memberID: f.memberID,
    rawStates: f.rawStates,
    rawLease: f.lease.raw,
    floorMs: f.lease.issuedAt,
    contentKeys: [{ epoch: 2n, secret: f.secret }],
  });
  await seedV4Database(idb, {
    recordBlob,
    edPrivate: f.ed,
    xPrivate: f.x,
    edPublic: f.edPublic,
    xPublic: f.xPublic,
  });
  return { ...f, idb, recordBlob };
}

export async function storedEvent(
  f: Awaited<ReturnType<typeof storedSecurity>>,
  seq = 1n,
  session = "synthetic-session",
  kind?: Event["kind"],
) {
  const { EventSchema } = await import("../gen/remoteagent/v1/event_pb");
  const state = f.states.at(-1)!;
  const machine = state.core.members.find((m) => m.kind === 1)!.id;
  const seed = new Uint8Array(32).fill(7);
  const envelope = await sealTestEnvelope(
    f.secret,
    seed,
    {
      account: f.pin.account,
      domain: f.pin.domain,
      epoch: state.core.epoch,
      state: await stateHash(state),
      contentEpoch: state.core.contentEpoch,
      sender: machine,
      kind: 1,
      purpose: 1,
      machine,
      session,
      seq,
      request: "",
      deadline: 0n,
      compression: 0,
    },
    new Uint8Array(
      toBinary(
        EventSchema,
        create(EventSchema, {
          sessionId: session,
          seq,
          kind: kind ?? { case: "agentMessage", value: { text: "synthetic event " + seq } },
        }),
      ),
    ),
    f.machineEd,
  );
  return { envelope, seed };
}
