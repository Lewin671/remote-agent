import { create, toBinary } from "@bufbuild/protobuf";
import { IDBFactory } from "fake-indexeddb";
import { vi } from "vitest";
import { EnvelopeSchema, HeaderSchema, type Envelope } from "../gen/remoteagent/v1/envelope_pb";
import type { Event } from "../gen/remoteagent/v1/event_pb";
import vectors from "../../../internal/deviceauth/testdata/domain-v3.json";
import { concat, fromHex, utf8 } from "../crypto";
import { inflateBounded } from "../core/sec-call";
import {
  parseLease,
  parseRecoveryStatement,
  parseState,
  Purpose,
  SecurityError,
  stateHash,
  unb64,
  type SecurityHeader,
} from "../device-security";
import { initializeSecurity } from "../device-security-store";

type B = Uint8Array<ArrayBuffer>;
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
const lpBytes = (b: Uint8Array): B => concat(u32be(b.length), b);
const lpStr = (s: string): B => lpBytes(utf8(s));

class TestReader {
  private pos = 0;
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
    const out = this.b.slice(this.pos, this.pos + n);
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

export function encodeTestHeader(h: SecurityHeader): B {
  return concat(
    u32be(3),
    lpStr(h.account),
    h.domain,
    u64be(h.epoch),
    h.state,
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
  if (h.compression === 0) return new Uint8Array(raw);
  if (h.compression === 1) {
    const small = [Purpose.SessionMeta, Purpose.MachineMeta, Purpose.Prefs, Purpose.DeviceMeta].includes(h.purpose);
    return inflateBounded(raw, small ? 65536 : 4 << 20);
  }
  throw new SecurityError("invalid");
}

export async function testRequestIdentity(h: SecurityHeader, unpacked: Uint8Array): Promise<{ key: B; payload: B }> {
  const keyInput = concat(
    utf8("ra4/request-key\0"),
    lpStr(h.account),
    h.domain,
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
  const secretKey = await crypto.subtle.importKey("raw", new Uint8Array(secret), "HKDF", false, ["deriveBits", "deriveKey"]);
  const purposeInfo = concat(
    utf8("ra3/purpose-key\0"),
    lpStr(header.account),
    header.domain,
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
  const salt = needsSeed && seed ? new Uint8Array(seed) : new Uint8Array(0);
  const contentInfo = concat(
    utf8("ra3/content-key\0"),
    lpStr(header.account),
    header.domain,
    u64be(header.epoch),
    header.state,
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
    await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce, additionalData: headerBytes }, encKey, new Uint8Array(plain)),
  );
  const commit = new Uint8Array(
    await crypto.subtle.digest("SHA-256", concat(utf8("ra3/ciphertext\0"), lpBytes(headerBytes), lpBytes(nonce), lpBytes(ct))),
  );
  const hdrDigest = new Uint8Array(await crypto.subtle.digest("SHA-256", headerBytes));
  const originMsg = concat(
    utf8("ra3/cipher-origin\0"),
    lpStr(header.account),
    header.domain,
    header.state,
    lpStr(header.sender),
    hdrDigest,
    commit,
  );
  const signature = new Uint8Array(await crypto.subtle.sign("Ed25519", signer, originMsg));
  return create(EnvelopeSchema, {
    header: toBinary(HeaderSchema, create(HeaderSchema, {
      version: 3,
      kind: header.kind,
      machineId: header.machine,
      sessionId: header.session,
      requestId: header.request,
      seq: header.seq,
      deadlineMs: header.deadline,
      sender: header.sender,
      compression: header.compression,
    })),
    securityHeader: headerBytes,
    nonce,
    ciphertext: ct,
    signature,
  });
}

// Parse and import the immutable Go-generated fixture once per test file.
// Each test still installs it into a fresh database and controls its clock.
let fixture: ReturnType<typeof buildFixture> | undefined;
export function securityFixture() {
  return fixture ??= buildFixture();
}
async function buildFixture() {
  const [genesis, added, renewal, removed] = await Promise.all(
    [vectors.genesis, vectors.added, vectors.renewal, vectors.recovery]
      .map(v => parseState(unb64(v.state))),
  );
  const x = await crypto.subtle.importKey("pkcs8", concat(
    fromHex("302e020100300506032b656e04220420"), fromHex(vectors.phone_x),
  ), "X25519", false, ["deriveBits"]);
  const signingKey = (seed: string) => crypto.subtle.importKey("pkcs8", concat(
    fromHex("302e020100300506032b657004220420"), fromHex(seed),
  ), "Ed25519", false, ["sign"]);
  const [ed, machineEd] = await Promise.all([
    signingKey(vectors.phone_seed), signingKey(vectors.machine_seed),
  ]);
  const lease = parseLease(unb64(vectors.lease));
  const pin = { account: vectors.account, domain: fromHex(vectors.domain), genesis: fromHex(vectors.genesis.hash) };
  const memberID = genesis.core.authorityID;
  return { pin, states: [genesis, added], memberID, x, ed, machineEd, lease,
    removal: {
      states: [renewal, removed],
      proofs: [undefined, await parseRecoveryStatement(unb64(vectors.recovery_statement))],
      now: Number(removed.core.issuedAt),
    },
  };
}
export async function storedSecurity() {
  const f = await securityFixture();
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(Number(f.lease.issuedAt));
  await initializeSecurity(f.pin, f.states, [undefined, undefined], f.memberID, f.x, false, f.lease);
  return f;
}

/** Separate IDB connection/module callers model a second tab. */
export async function rawSecurity<T>(mode: IDBTransactionMode, op: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open("remote-agent-security-v3", 1);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
  return new Promise<T>((resolve, reject) => {
    const tx = db.transaction("security", mode);
    const req = op(tx.objectStore("security"));
    tx.oncomplete = () => { db.close(); resolve(req.result); };
    tx.onabort = tx.onerror = () => { db.close(); reject(tx.error); };
  });
}

export async function storedEvent(f: Awaited<ReturnType<typeof storedSecurity>>, seq = 1n, session = "synthetic-session", kind?: Event["kind"]) {
  const { EventSchema } = await import("../gen/remoteagent/v1/event_pb");
  const state = f.states.at(-1)!;
  const machine = state.core.members.find(m => m.kind === 1)!.id;
  const seed = new Uint8Array(32).fill(7);
  const envelope = await sealTestEnvelope(fromHex(vectors.content_secret_2), seed, {
    account: f.pin.account, domain: f.pin.domain, epoch: state.core.epoch,
    state: await stateHash(state), contentEpoch: state.core.contentEpoch, sender: machine,
    kind: 1, purpose: 1, machine, session, seq,
    request: "", deadline: 0n, compression: 0,
  }, new Uint8Array(toBinary(EventSchema, create(EventSchema, {
    sessionId: session, seq, kind: kind ?? { case: "agentMessage", value: { text: "synthetic event " + seq } },
  }))), f.machineEd);
  return { envelope, seed };
}
