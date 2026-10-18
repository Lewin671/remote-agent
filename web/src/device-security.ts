// Pinned-authority protocol. Root keys and relay registrations are deliberately
// absent: they are transport/legacy-history credentials, never current trust.
import {
  Aes256Gcm,
  CipherSuite,
  DhkemX25519HkdfSha256,
  HkdfSha256,
} from "@hpke/core";
import { concat, utf8, deviceID } from "./crypto";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  EnvelopeSchema,
  HeaderSchema,
  type Envelope,
} from "./gen/remoteagent/v1/envelope_pb";

type B = Uint8Array<ArrayBuffer>;
export const MAX_STATE = 64 << 10;
export const MAX_CIPHER = 4 << 20;
export const MAX_RPC = 1 << 20;
export class SecurityError extends Error {
  constructor(
    readonly code:
      | "invalid"
      | "untrusted"
      | "stale"
      | "conflict"
      | "pending"
      | "capacity"
      | "storage" = "invalid",
  ) {
    super(`security: ${code}`);
  }
}
function requireOK(
  ok: unknown,
  code: SecurityError["code"] = "invalid",
): asserts ok {
  if (!ok) throw new SecurityError(code);
}
const ZERO = new Uint8Array(32);
export function equal(a: Uint8Array, b: Uint8Array): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}
function hash32(b: Uint8Array): void {
  requireOK(b.length === 32);
}
function nonzero(b: Uint8Array): void {
  hash32(b);
  requireOK(!equal(b, ZERO));
}
const id = (s: string) => /^[0-9a-f]{32}$/.test(s);
const opaque = (s: string) => /^[A-Za-z0-9_:-]{1,128}$/.test(s);
export class Writer {
  private parts: B[] = [];
  raw(b: Uint8Array): this {
    this.parts.push(new Uint8Array(b));
    return this;
  }
  u32(n: number): this {
    requireOK(Number.isInteger(n) && n >= 0 && n <= 0xffffffff);
    const b = new Uint8Array(4);
    new DataView(b.buffer).setUint32(0, n);
    return this.raw(b);
  }
  u64(n: bigint): this {
    requireOK(n >= 0n && n <= 0xffffffffffffffffn);
    const b = new Uint8Array(8);
    new DataView(b.buffer).setBigUint64(0, n);
    return this.raw(b);
  }
  lp(b: Uint8Array): this {
    return this.u32(b.length).raw(b);
  }
  str(s: string): this {
    return this.lp(utf8(s));
  }
  bytes(): B {
    return concat(...this.parts);
  }
}
export class Reader {
  private at = 0;
  constructor(private readonly b: B) {}
  raw(n: number): B {
    requireOK(
      Number.isSafeInteger(n) && n >= 0 && n <= this.b.length - this.at,
    );
    const out = this.b.slice(this.at, this.at + n);
    this.at += n;
    return out;
  }
  u32(): number {
    return new DataView(this.raw(4).buffer).getUint32(0);
  }
  u64(): bigint {
    return new DataView(this.raw(8).buffer).getBigUint64(0);
  }
  lp(max: number): B {
    const n = this.u32();
    requireOK(n <= max);
    return this.raw(n);
  }
  str(): string {
    return new TextDecoder("utf-8", { fatal: true }).decode(this.lp(128));
  }
  done(): void {
    requireOK(this.at === this.b.length);
  }
}
export async function hash(label: string, ...parts: Uint8Array[]): Promise<B> {
  return new Uint8Array(
    await crypto.subtle.digest("SHA-256", concat(utf8(label), ...parts)),
  );
}
async function verify(pub: B, signature: B, message: B): Promise<void> {
  requireOK(pub.length === 32 && signature.length === 64, "untrusted");
  const key = await crypto.subtle.importKey("raw", pub, "Ed25519", false, [
    "verify",
  ]);
  requireOK(
    await crypto.subtle.verify("Ed25519", key, signature, message),
    "untrusted",
  );
}
async function sign(key: CryptoKey, message: B): Promise<B> {
  return new Uint8Array(await crypto.subtle.sign("Ed25519", key, message));
}
export interface Member {
  id: string;
  ed: B;
  x: B;
  kind: 1 | 2;
}
export interface Core {
  account: string;
  domain: B;
  authorityID: string;
  authorityEd: B;
  authorityX: B;
  parent: B;
  epoch: bigint;
  contentEpoch: bigint;
  members: Member[];
  removed: string[];
}
export interface Grant {
  recipient: string;
  enc: B;
  cipher: B;
}
export interface SecurityState {
  core: Core;
  grants: Grant[];
  signature: B;
}
export interface Pin {
  account: string;
  domain: B;
  genesis: B;
  authorityID: string;
  authorityEd: B;
  authorityX: B;
}
async function validateCore(c: Core): Promise<void> {
  requireOK(
    id(c.account) &&
      id(c.authorityID) &&
      c.epoch > 0n &&
      c.contentEpoch === c.epoch &&
      c.members.length > 0 &&
      c.members.length <= 128 &&
      c.removed.length <= 128,
  );
  nonzero(c.domain);
  nonzero(c.authorityEd);
  nonzero(c.authorityX);
  hash32(c.parent);
  for (let i = 0; i < c.members.length; i++) {
    const m = c.members[i];
    requireOK(
      id(m.id) &&
        m.id === (await deviceID(m.ed)) &&
        (m.kind === 1 || m.kind === 2),
    );
    hash32(m.ed);
    nonzero(m.x);
    if (i) requireOK(c.members[i - 1].id < m.id);
  }
  const a = c.members.find((m) => m.id === c.authorityID);
  requireOK(
    a?.kind === 1 && !equal(a.ed, c.authorityEd) && !equal(a.x, c.authorityX),
  );
  for (let i = 0; i < c.removed.length; i++)
    requireOK(
      id(c.removed[i]) &&
        (!i || c.removed[i - 1] < c.removed[i]) &&
        !c.members.some((m) => m.id === c.removed[i]),
    );
  if (c.epoch === 1n)
    requireOK(
      equal(c.parent, ZERO) && c.members.length === 1 && c.removed.length === 0,
    );
  else nonzero(c.parent);
}
export async function coreBytes(c: Core): Promise<B> {
  await validateCore(c);
  const w = new Writer()
    .u32(2)
    .str(c.account)
    .raw(c.domain)
    .str(c.authorityID)
    .raw(c.authorityEd)
    .raw(c.authorityX)
    .raw(c.parent)
    .u64(c.epoch)
    .u64(c.contentEpoch)
    .u32(c.members.length);
  for (const m of c.members) w.str(m.id).raw(m.ed).raw(m.x).u32(m.kind);
  w.u32(c.removed.length);
  for (const d of c.removed) w.str(d);
  return w.bytes();
}
export async function parseCore(b: B): Promise<Core> {
  requireOK(b.length <= MAX_STATE);
  const r = new Reader(b);
  requireOK(r.u32() === 2);
  const c: Core = {
    account: r.str(),
    domain: r.raw(32),
    authorityID: r.str(),
    authorityEd: r.raw(32),
    authorityX: r.raw(32),
    parent: r.raw(32),
    epoch: r.u64(),
    contentEpoch: r.u64(),
    members: [],
    removed: [],
  };
  let n = r.u32();
  requireOK(n <= 128);
  for (let i = 0; i < n; i++)
    c.members.push({
      id: r.str(),
      ed: r.raw(32),
      x: r.raw(32),
      kind: r.u32() as 1 | 2,
    });
  n = r.u32();
  requireOK(n <= 128);
  for (let i = 0; i < n; i++) c.removed.push(r.str());
  r.done();
  await validateCore(c);
  return c;
}
function grantBytes(s: SecurityState): B {
  requireOK(s.grants.length === s.core.members.length);
  const w = new Writer().u32(s.grants.length);
  for (let i = 0; i < s.grants.length; i++) {
    const g = s.grants[i];
    requireOK(g.recipient === s.core.members[i].id && g.cipher.length === 48);
    nonzero(g.enc);
    w.str(g.recipient).lp(g.enc).lp(g.cipher);
  }
  return w.bytes();
}
export async function stateHash(s: SecurityState): Promise<B> {
  const ch = await hash("ra2/core\0", await coreBytes(s.core));
  const gh = await hash("ra2/grants\0", grantBytes(s));
  return hash("ra2/state\0", ch, gh);
}
export async function stateBytes(s: SecurityState): Promise<B> {
  requireOK(s.signature.length === 64);
  const b = new Writer()
    .lp(await coreBytes(s.core))
    .raw(grantBytes(s))
    .lp(s.signature)
    .bytes();
  requireOK(b.length <= MAX_STATE);
  return b;
}
export async function parseState(b: B): Promise<SecurityState> {
  requireOK(b.length <= MAX_STATE);
  const r = new Reader(b);
  const core = await parseCore(r.lp(MAX_STATE));
  const n = r.u32();
  requireOK(n <= 128);
  const grants: Grant[] = [];
  for (let i = 0; i < n; i++)
    grants.push({ recipient: r.str(), enc: r.lp(32), cipher: r.lp(48) });
  const signature = r.lp(64);
  r.done();
  const s = { core, grants, signature };
  await stateBytes(s);
  return s;
}
export async function verifyState(p: Pin, s: SecurityState): Promise<void> {
  const c = s.core;
  requireOK(
    c.account === p.account &&
      equal(c.domain, p.domain) &&
      c.authorityID === p.authorityID &&
      equal(c.authorityEd, p.authorityEd) &&
      equal(c.authorityX, p.authorityX),
    "untrusted",
  );
  nonzero(p.genesis);
  const sh = await stateHash(s);
  if (c.epoch === 1n) requireOK(equal(sh, p.genesis), "untrusted");
  await verify(
    p.authorityEd,
    s.signature,
    new Writer()
      .raw(utf8("ra2/state-sig\0"))
      .str(p.account)
      .raw(p.domain)
      .u64(c.epoch)
      .raw(sh)
      .bytes(),
  );
}
export async function advance(
  p: Pin,
  old: SecurityState,
  next: SecurityState,
): Promise<void> {
  await verifyState(p, old);
  await verifyState(p, next);
  requireOK(
    next.core.epoch === old.core.epoch + 1n &&
      equal(next.core.parent, await stateHash(old)),
    "stale",
  );
  const removed: string[] = [];
  let added = 0;
  for (const m of old.core.members) {
    const n = next.core.members.find((n) => n.id === m.id);
    if (!n) removed.push(m.id);
    else requireOK(m.kind === n.kind && equal(m.ed, n.ed) && equal(m.x, n.x));
  }
  for (const m of next.core.members)
    if (!old.core.members.some((o) => o.id === m.id)) added++;
  requireOK(
    removed.join(",") === next.core.removed.join(",") &&
      added <= 1 &&
      !(added && removed.length) &&
      !!(added || removed.length),
  );
}
const suite = () =>
  new CipherSuite({
    kem: new DhkemX25519HkdfSha256(),
    kdf: new HkdfSha256(),
    aead: new Aes256Gcm(),
  });
export async function newSecurityKey(): Promise<CryptoKeyPair> {
  return crypto.subtle.generateKey({ name: "X25519" }, false, [
    "deriveBits",
  ]) as Promise<CryptoKeyPair>;
}
export async function openGrant(
  p: Pin,
  s: SecurityState,
  recipient: string,
  xPrivate: CryptoKey,
): Promise<B> {
  await verifyState(p, s);
  const g = s.grants.find((g) => g.recipient === recipient);
  requireOK(g, "untrusted");
  const ctx = new Writer()
    .str(p.account)
    .raw(p.domain)
    .u64(s.core.contentEpoch)
    .raw(await hash("ra2/core\0", await coreBytes(s.core)))
    .str(recipient)
    .bytes();
  const rc = await suite().createRecipientContext({
    recipientKey: xPrivate,
    enc: g.enc,
    info: concat(utf8("ra2/grant-info\0"), ctx),
  });
  const secret = new Uint8Array(
    await rc.open(g.cipher, concat(utf8("ra2/grant-aad\0"), ctx)),
  );
  requireOK(secret.length === 32, "untrusted");
  return secret;
}

export enum Purpose {
  Event = 1,
  SessionMeta = 2,
  SessionKey = 3,
  MachineMeta = 4,
  Command = 5,
  Result = 6,
  TerminalOutput = 7,
  TerminalInput = 8,
  Notify = 10,
  Image = 11,
  Prefs = 12,
  DeviceMeta = 13,
  TerminalResize = 14,
  TerminalHistory = 15,
}
export interface SecurityHeader {
  account: string;
  domain: B;
  epoch: bigint;
  state: B;
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
  counter: bigint;
}
const mutable = (p: Purpose) =>
  [
    Purpose.SessionMeta,
    Purpose.MachineMeta,
    Purpose.DeviceMeta,
    Purpose.Prefs,
  ].includes(p);
export const control = (p: Purpose) =>
  [
    Purpose.Command,
    Purpose.TerminalInput,
    Purpose.TerminalResize,
    Purpose.TerminalHistory,
  ].includes(p);
function validateHeader(h: SecurityHeader): void {
  requireOK(
    id(h.account) &&
      id(h.sender) &&
      h.epoch > 0n &&
      h.epoch === h.contentEpoch &&
      h.counter > 0n &&
      [0, 1].includes(h.compression),
  );
  nonzero(h.domain);
  nonzero(h.state);
  requireOK(!h.machine || id(h.machine));
  requireOK(!h.session || opaque(h.session));
  requireOK(!h.request || opaque(h.request));
  const kind = h.purpose === 14 || h.purpose === 15 ? 8 : Number(h.purpose);
  requireOK(h.kind === kind && kind > 0 && kind <= 13 && kind !== 9);
  switch (h.purpose) {
    case Purpose.Prefs:
      requireOK(
        h.request === "project-prefs" && !h.machine && !h.session && h.seq > 0n,
      );
      break;
    case Purpose.DeviceMeta:
      requireOK(id(h.request) && !h.machine && !h.session && h.seq > 0n);
      break;
    case Purpose.MachineMeta:
      requireOK(h.machine && !h.session && !h.request && h.seq > 0n);
      break;
    case Purpose.SessionMeta:
    case Purpose.SessionKey:
    case Purpose.Event:
      requireOK(h.machine && h.session && !h.request && h.seq > 0n);
      break;
    case Purpose.Command:
    case Purpose.Result:
      requireOK(h.machine && h.request.length >= 16);
      break;
    case Purpose.TerminalInput:
    case Purpose.TerminalResize:
    case Purpose.TerminalHistory:
    case Purpose.TerminalOutput:
      requireOK(h.machine && h.session && h.request.length >= 16 && h.seq > 0n);
      break;
    case Purpose.Image:
    case Purpose.Notify:
      requireOK(h.machine && h.session && h.request);
      break;
    default:
      throw new SecurityError();
  }
  if (control(h.purpose)) requireOK(h.deadline > 0n);
}
export function headerBytes(h: SecurityHeader): B {
  validateHeader(h);
  return new Writer()
    .u32(2)
    .str(h.account)
    .raw(h.domain)
    .u64(h.epoch)
    .raw(h.state)
    .u64(h.contentEpoch)
    .str(h.sender)
    .u32(h.kind)
    .u32(h.purpose)
    .str(h.machine)
    .str(h.session)
    .str(h.request)
    .u64(h.seq)
    .u64(h.deadline)
    .u32(h.compression)
    .u64(h.counter)
    .bytes();
}
export function parseHeader(b: B): SecurityHeader {
  requireOK(b.length <= 1024);
  const r = new Reader(b);
  requireOK(r.u32() === 2);
  const h = {
    account: r.str(),
    domain: r.raw(32),
    epoch: r.u64(),
    state: r.raw(32),
    contentEpoch: r.u64(),
    sender: r.str(),
    kind: r.u32(),
    purpose: r.u32() as Purpose,
    machine: r.str(),
    session: r.str(),
    request: r.str(),
    seq: r.u64(),
    deadline: r.u64(),
    compression: r.u32(),
    counter: r.u64(),
  };
  r.done();
  validateHeader(h);
  return h;
}
async function scope(h: SecurityHeader, s: SecurityState): Promise<Member> {
  validateHeader(h);
  requireOK(
    h.account === s.core.account &&
      equal(h.domain, s.core.domain) &&
      h.epoch === s.core.epoch &&
      h.contentEpoch === s.core.contentEpoch &&
      equal(h.state, await stateHash(s)),
    "stale",
  );
  const m = s.core.members.find((m) => m.id === h.sender);
  requireOK(m, "untrusted");
  if (h.machine)
    requireOK(
      s.core.members.some((m) => m.id === h.machine && m.kind === 1),
      "untrusted",
    );
  if (h.purpose === Purpose.DeviceMeta)
    requireOK(
      s.core.members.some((m) => m.id === h.request && m.kind === 2),
      "untrusted",
    );
  else if (h.purpose !== Purpose.Prefs && !control(h.purpose))
    requireOK(m.kind === 1 && m.id === h.machine, "untrusted");
  return m;
}
export interface FormedCipher {
  header: B;
  nonce: B;
  cipher: B;
  origin: B;
}
export interface SecureWire extends FormedCipher {
  authorization: B;
  authSignature: B;
}
export async function commitment(c: FormedCipher): Promise<B> {
  requireOK(
    c.header.length <= 1024 &&
      c.nonce.length === 12 &&
      c.cipher.length >= 16 &&
      c.cipher.length <= MAX_CIPHER,
  );
  parseHeader(c.header);
  return hash(
    "ra2/ciphertext\0",
    new Writer().lp(c.header).lp(c.nonce).lp(c.cipher).bytes(),
  );
}
function originBytes(h: SecurityHeader, hh: B, commit: B): B {
  return new Writer()
    .raw(utf8("ra2/cipher-origin\0"))
    .str(h.account)
    .raw(h.domain)
    .raw(h.state)
    .str(h.sender)
    .raw(hh)
    .raw(commit)
    .bytes();
}
export async function deriveContentKey(
  secret: B | CryptoKey,
  seed: B | undefined,
  h: SecurityHeader,
): Promise<CryptoKey> {
  requireOK(secret instanceof CryptoKey || secret.length === 32);
  validateHeader(h);
  let salt: B = new Uint8Array();
  if ([1, 7, 8, 11, 14, 15].includes(h.purpose)) {
    requireOK(seed?.length === 32);
    salt = seed;
  }
  const ctx = new Writer()
    .raw(utf8("ra2/content-key\0"))
    .str(h.account)
    .raw(h.domain)
    .u64(h.contentEpoch)
    .u32(h.purpose)
    .str(h.sender)
    .str(h.machine)
    .str(h.session)
    .str(h.request)
    .u64(h.seq)
    .u64(h.counter)
    .bytes();
  const root =
    secret instanceof CryptoKey
      ? secret
      : await crypto.subtle.importKey("raw", secret, "HKDF", false, [
          "deriveBits",
        ]);
  const purposeInfo = new Writer()
    .raw(utf8("ra2/purpose-key\0"))
    .str(h.account)
    .raw(h.domain)
    .u64(h.contentEpoch)
    .u32(h.purpose)
    .bytes();
  const child = new Uint8Array(
    await crypto.subtle.deriveBits(
      {
        name: "HKDF",
        hash: "SHA-256",
        salt: new Uint8Array(),
        info: purposeInfo,
      },
      root,
      256,
    ),
  );
  const k = await crypto.subtle.importKey("raw", child, "HKDF", false, [
    "deriveKey",
  ]);
  child.fill(0);
  return crypto.subtle.deriveKey(
    { name: "HKDF", hash: "SHA-256", salt, info: ctx },
    k,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt", "decrypt"],
  );
}
/** counter MUST be reserved in one durable storage transaction before calling. */
export async function formCipher(
  secret: B | CryptoKey,
  seed: B | undefined,
  h: SecurityHeader,
  plaintext: B,
  ed: CryptoKey,
): Promise<FormedCipher> {
  requireOK(plaintext.length <= MAX_CIPHER - 16);
  const header = headerBytes(h);
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const cipher = new Uint8Array(
    await crypto.subtle.encrypt(
      { name: "AES-GCM", iv: nonce, additionalData: header },
      await deriveContentKey(secret, seed, h),
      plaintext,
    ),
  );
  const c = { header, nonce, cipher, origin: new Uint8Array() };
  c.origin = await sign(
    ed,
    originBytes(h, await hash("", header), await commitment(c)),
  );
  return c;
}
export interface WriteAuth {
  header: SecurityHeader;
  writerKind: number;
  headerHash: B;
  commit: B;
  nonce: B;
}
export function parseAuthorization(b: B): WriteAuth {
  requireOK(b.length <= 1252);
  const r = new Reader(b);
  const hb = r.lp(1024);
  const header = parseHeader(hb);
  const out = {
    header,
    writerKind: r.u32(),
    headerHash: r.raw(32),
    commit: r.raw(32),
    nonce: r.raw(32),
  };
  r.done();
  requireOK([1, 2].includes(out.writerKind));
  nonzero(out.commit);
  nonzero(out.nonce);
  return out;
}
export async function verifyWire(
  p: Pin,
  s: SecurityState,
  w: SecureWire,
): Promise<SecurityHeader> {
  await verifyState(p, s);
  const h = parseHeader(w.header);
  const member = await scope(h, s);
  const hh = await hash("", w.header),
    commit = await commitment(w);
  await verify(member.ed, w.origin, originBytes(h, hh, commit));
  const auth = parseAuthorization(w.authorization);
  requireOK(
    equal(headerBytes(auth.header), w.header) &&
      auth.writerKind === member.kind &&
      equal(auth.headerHash, hh) &&
      equal(auth.commit, commit),
    "untrusted",
  );
  await verify(
    p.authorityEd,
    w.authSignature,
    concat(utf8("ra2/write-auth\0"), w.authorization),
  );
  return h;
}
/** The body decoder must additionally enforce the authenticated purpose and
 * owner. This primitive alone must never dispatch terminal or command actions. */
export async function openWire(
  p: Pin,
  s: SecurityState,
  w: SecureWire,
  secret: B | CryptoKey,
  seed?: B,
): Promise<{ header: SecurityHeader; plaintext: B }> {
  const header = await verifyWire(p, s, w);
  const key = await deriveContentKey(secret, seed, header);
  const plaintext = new Uint8Array(
    await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: w.nonce, additionalData: w.header },
      key,
      w.cipher,
    ),
  );
  return { header, plaintext };
}
export function toEnvelope(w: SecureWire): Envelope {
  const h = parseHeader(w.header);
  return create(EnvelopeSchema, {
    header: toBinary(
      HeaderSchema,
      create(HeaderSchema, {
        version: 2,
        kind: h.kind,
        machineId: h.machine,
        sessionId: h.session,
        requestId: h.request,
        seq: h.seq,
        deadlineMs: h.deadline,
        sender: h.sender,
        compression: h.compression,
      }),
    ),
    nonce: w.nonce,
    ciphertext: w.cipher,
    signature: w.origin,
    securityHeader: w.header,
    authorization: w.authorization,
    authorizationSignature: w.authSignature,
  });
}
export function fromEnvelope(e: Envelope): SecureWire {
  const h = parseHeader(new Uint8Array(e.securityHeader));
  const p = fromBinary(HeaderSchema, e.header);
  requireOK(
    p.version === 2 &&
      p.kind === h.kind &&
      p.machineId === h.machine &&
      p.sessionId === h.session &&
      p.requestId === h.request &&
      p.seq === h.seq &&
      p.deadlineMs === h.deadline &&
      p.sender === h.sender &&
      p.compression === h.compression,
    "untrusted",
  );
  return {
    header: new Uint8Array(e.securityHeader),
    nonce: new Uint8Array(e.nonce),
    cipher: new Uint8Array(e.ciphertext),
    origin: new Uint8Array(e.signature),
    authorization: new Uint8Array(e.authorization),
    authSignature: new Uint8Array(e.authorizationSignature),
  };
}
export function wireBytes(w: SecureWire): B {
  requireOK(w.origin.length === 64 && w.authSignature.length === 64);
  parseAuthorization(w.authorization);
  return new Writer()
    .lp(w.header)
    .lp(w.nonce)
    .lp(w.cipher)
    .lp(w.origin)
    .lp(w.authorization)
    .lp(w.authSignature)
    .bytes();
}
export function parseWire(b: B): SecureWire {
  requireOK(b.length <= MAX_CIPHER + 4096);
  const r = new Reader(b);
  const w = {
    header: r.lp(1024),
    nonce: r.lp(12),
    cipher: r.lp(MAX_CIPHER),
    origin: r.lp(64),
    authorization: r.lp(1152),
    authSignature: r.lp(64),
  };
  r.done();
  wireBytes(w);
  return w;
}
export const isMutable = mutable;

export enum Method {
  Authorize = 1,
  Catalog = 2,
  States = 3,
  HistoryKeys = 4,
  BeginPair = 5,
  ApprovePair = 6,
  CancelPair = 7,
  Delete = 8,
  GetCompletion = 9,
  GetFences = 10,
  AckFence = 11,
  CompletionStates = 12,
}
export interface Capsule {
  from: string;
  to: string;
  method: Method;
  enc: B;
  cipher: B;
}
export function capsuleBytes(c: Capsule): B {
  requireOK(
    id(c.from) &&
      id(c.to) &&
      c.method >= 1 &&
      c.method <= 12 &&
      c.enc.length === 32 &&
      c.cipher.length >= 16 &&
      c.cipher.length <= MAX_RPC,
  );
  return new Writer()
    .str(c.from)
    .str(c.to)
    .u32(c.method)
    .lp(c.enc)
    .lp(c.cipher)
    .bytes();
}
export function parseCapsule(b: B): Capsule {
  requireOK(b.length <= MAX_RPC + 512);
  const r = new Reader(b);
  const c = {
    from: r.str(),
    to: r.str(),
    method: r.u32(),
    enc: r.lp(32),
    cipher: r.lp(MAX_RPC),
  };
  r.done();
  capsuleBytes(c);
  return c;
}
function rpcContext(
  p: Pin,
  from: string,
  to: string,
  method: Method,
  response: boolean,
): B {
  return new Writer()
    .raw(utf8("ra2/rpc\0"))
    .str(p.account)
    .raw(p.domain)
    .str(p.authorityID)
    .str(from)
    .str(to)
    .u32(method)
    .u32(response ? 1 : 0)
    .bytes();
}
export function b64(b: Uint8Array): string {
  let s = "";
  for (const n of b) s += String.fromCharCode(n);
  return btoa(s);
}
export function unb64(s: string): B {
  requireOK(
    typeof s === "string" && s.length <= MAX_RPC * 2 && s.length % 4 === 0,
  );
  let bin: string;
  try {
    bin = atob(s);
  } catch {
    throw new SecurityError();
  }
  const out = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  requireOK(b64(out) === s);
  return out;
}

export interface RPCWaiter {
  pin: Pin;
  sender: string;
  method: Method;
  nonce: B;
  requestHash: B;
}
export async function requestRPC(
  p: Pin,
  sender: string,
  ed: CryptoKey,
  method: Method,
  body: unknown,
): Promise<{ capsule: Capsule; waiter: RPCWaiter }> {
  requireOK(id(sender) && method >= 1 && method <= 12);
  const bodyBytes = utf8(JSON.stringify(body));
  requireOK(bodyBytes.length <= MAX_RPC - 1024);
  const nonce = crypto.getRandomValues(new Uint8Array(32));
  const ctx = rpcContext(p, sender, p.authorityID, method, false);
  const signed = new Writer()
    .raw(utf8("ra2/auth-request\0"))
    .raw(ctx)
    .raw(nonce)
    .lp(bodyBytes)
    .bytes();
  const signature = await sign(ed, signed);
  const requestHash = await hash("ra2/request-digest\0", signed, signature);
  const plain = utf8(
    JSON.stringify({
      Nonce: [...nonce],
      Body: b64(bodyBytes),
      Signature: b64(signature),
    }),
  );
  requireOK(plain.length <= MAX_RPC - 16);
  const pub = await crypto.subtle.importKey(
    "raw",
    p.authorityX,
    "X25519",
    true,
    [],
  );
  const sc = await suite().createSenderContext({
    recipientPublicKey: pub,
    info: concat(utf8("ra2/rpc-info\0"), ctx),
  });
  const cipher = new Uint8Array(
    await sc.seal(plain, concat(utf8("ra2/rpc-aad\0"), ctx)),
  );
  return {
    capsule: {
      from: sender,
      to: p.authorityID,
      method,
      enc: new Uint8Array(sc.enc),
      cipher,
    },
    waiter: { pin: p, sender, method, nonce, requestHash },
  };
}
export async function replyRPC<T>(
  w: RPCWaiter,
  c: Capsule,
  x: CryptoKey,
): Promise<T> {
  capsuleBytes(c);
  requireOK(
    c.from === w.pin.authorityID && c.to === w.sender && c.method === w.method,
    "untrusted",
  );
  const ctx = rpcContext(w.pin, c.from, c.to, c.method, true);
  const rc = await suite().createRecipientContext({
    recipientKey: x,
    enc: c.enc,
    info: concat(utf8("ra2/rpc-info\0"), ctx),
  });
  const plain = new Uint8Array(
    await rc.open(c.cipher, concat(utf8("ra2/rpc-aad\0"), ctx)),
  );
  requireOK(plain.length <= MAX_RPC);
  const r = safeJSON(plain);
  const nonce = jsonHash(r.Nonce),
    requestHash = jsonHash(r.RequestHash),
    body = r.Body ? unb64(r.Body) : new Uint8Array();
  requireOK(
    equal(nonce, w.nonce) &&
      equal(requestHash, w.requestHash) &&
      typeof r.Error === "string" &&
      r.Error.length <= 32,
    "untrusted",
  );
  const signed = new Writer()
    .raw(utf8("ra2/auth-response\0"))
    .raw(ctx)
    .raw(nonce)
    .raw(requestHash)
    .str(r.Error)
    .lp(body)
    .bytes();
  await verify(w.pin.authorityEd, unb64(r.Signature), signed);
  if (r.Error) {
    const code = [
      "stale",
      "conflict",
      "pending",
      "capacity",
      "storage",
    ].includes(r.Error)
      ? r.Error
      : "untrusted";
    throw new SecurityError(code);
  }
  return safeJSON(body) as T;
}
export function jsonHash(v: unknown): B {
  requireOK(
    Array.isArray(v) &&
      v.length === 32 &&
      v.every((n) => Number.isInteger(n) && n >= 0 && n <= 255),
  );
  return new Uint8Array(v);
}
// Go's JSON wrapper is opaque signed payload; all cryptographic state is still
// canonical binary. Epochs are capped by the authority, never lossy JS numbers.
export function pinFromJSON(p: {
  Account: string;
  Domain: number[];
  Genesis: number[];
  AuthorityID: string;
  AuthorityEd: number[];
  AuthorityX: number[];
}): Pin {
  return {
    account: p.Account,
    domain: jsonHash(p.Domain),
    genesis: jsonHash(p.Genesis),
    authorityID: p.AuthorityID,
    authorityEd: jsonHash(p.AuthorityEd),
    authorityX: jsonHash(p.AuthorityX),
  };
}
interface JSONState {
  Core: {
    Account: string;
    Domain: number[];
    AuthorityID: string;
    AuthorityEd: number[];
    AuthorityX: number[];
    Parent: number[];
    Epoch: number;
    ContentEpoch: number;
    Members: {
      ID: string;
      EdPublic: number[];
      XPublic: number[];
      Kind: 1 | 2;
    }[];
    Removed: string[] | null;
  };
  Grants: { Recipient: string; Enc: number[]; Cipher: string }[];
  Signature: string;
}
export async function stateFromJSON(s: JSONState): Promise<SecurityState> {
  const c = s.Core;
  requireOK(
    Number.isSafeInteger(c.Epoch) &&
      c.Epoch > 0 &&
      c.Epoch <= 32768 &&
      c.ContentEpoch === c.Epoch &&
      c.Members.length <= 128 &&
      s.Grants.length <= 128,
  );
  const out: SecurityState = {
    core: {
      account: c.Account,
      domain: jsonHash(c.Domain),
      authorityID: c.AuthorityID,
      authorityEd: jsonHash(c.AuthorityEd),
      authorityX: jsonHash(c.AuthorityX),
      parent: jsonHash(c.Parent),
      epoch: BigInt(c.Epoch),
      contentEpoch: BigInt(c.ContentEpoch),
      members: c.Members.map((m) => ({
        id: m.ID,
        ed: jsonHash(m.EdPublic),
        x: jsonHash(m.XPublic),
        kind: m.Kind,
      })),
      removed: c.Removed ?? [],
    },
    grants: s.Grants.map((g) => ({
      recipient: g.Recipient,
      enc: jsonHash(g.Enc),
      cipher: unb64(g.Cipher),
    })),
    signature: unb64(s.Signature),
  };
  await stateBytes(out);
  return out;
}
export function authRequestJSON(c: FormedCipher, commit: B): unknown {
  const h = parseHeader(c.header);
  const jsonCipher = {
    Header: b64(c.header),
    Nonce: b64(c.nonce),
    Cipher: b64(c.cipher),
    Origin: b64(c.origin),
  };
  return {
    Header: b64(c.header),
    Commit: [...commit],
    Origin: b64(c.origin),
    Cipher: mutable(h.purpose) ? jsonCipher : null,
  };
}
export async function attachAuthorization(
  p: Pin,
  s: SecurityState,
  c: FormedCipher,
  reply: { Authorization: string; Signature: string },
): Promise<SecureWire> {
  const wire = {
    ...c,
    authorization: unb64(reply.Authorization),
    authSignature: unb64(reply.Signature),
  };
  await verifyWire(p, s, wire);
  return wire;
}

function safeJSON(b: B): any {
  try {
    return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(b));
  } catch {
    throw new SecurityError();
  }
}

export interface PairContext {
  id: string;
  pin: Pin;
  approver: string;
  candidate: Member;
  expected: B;
  nonce: B;
  expires: bigint;
  signature: B;
}
export interface GoPairContext {
  ID: string;
  Account: string;
  Domain: number[];
  Genesis: number[];
  AuthorityID: string;
  AuthorityEd: number[];
  AuthorityX: number[];
  Approver: string;
  Candidate: { ID: string; EdPublic: number[]; XPublic: number[]; Kind: 1 | 2 };
  Expected: number[];
  Nonce: number[];
  Expires: number;
  Signature: string;
}
export function pairContextFromJSON(p: GoPairContext): PairContext {
  requireOK(Number.isSafeInteger(p.Expires) && p.Expires > 0);
  return {
    id: p.ID,
    pin: pinFromJSON(p),
    approver: p.Approver,
    candidate: {
      id: p.Candidate.ID,
      ed: jsonHash(p.Candidate.EdPublic),
      x: jsonHash(p.Candidate.XPublic),
      kind: p.Candidate.Kind,
    },
    expected: jsonHash(p.Expected),
    nonce: jsonHash(p.Nonce),
    expires: BigInt(p.Expires),
    signature: unb64(p.Signature),
  };
}
/** This descriptor is frozen in the initiator commitment BEFORE either party
 * knows DH. Late context nonce/head/expiry/signature never enter the SAS. */
export function pairTrustDescriptor(p: Pin, approver: string): B {
  requireOK(id(p.account) && id(p.authorityID) && id(approver));
  nonzero(p.domain);
  nonzero(p.genesis);
  nonzero(p.authorityEd);
  nonzero(p.authorityX);
  return new Writer()
    .raw(utf8("ra2/pair-trust\0"))
    .str(p.account)
    .raw(p.domain)
    .raw(p.genesis)
    .str(p.authorityID)
    .raw(p.authorityEd)
    .raw(p.authorityX)
    .str(approver)
    .bytes();
}
export async function pairDomainCommit(ephemeral: B, trust: B): Promise<B> {
  requireOK(ephemeral.length === 32);
  return hash(
    "ra2/pair-commit\0",
    new Writer().lp(ephemeral).lp(trust).bytes(),
  );
}
export async function pairBinding(p: PairContext): Promise<B> {
  requireOK(
    opaque(p.id) &&
      p.id.length >= 16 &&
      id(p.approver) &&
      p.expires > 0n &&
      id(p.candidate.id) &&
      p.candidate.id === (await deviceID(p.candidate.ed)) &&
      [1, 2].includes(p.candidate.kind),
  );
  nonzero(p.expected);
  nonzero(p.nonce);
  nonzero(p.candidate.x);
  pairTrustDescriptor(p.pin, p.approver);
  return new Writer()
    .raw(utf8("ra2/pair-context\0"))
    .str(p.id)
    .str(p.pin.account)
    .raw(p.pin.domain)
    .raw(p.pin.genesis)
    .str(p.pin.authorityID)
    .raw(p.pin.authorityEd)
    .raw(p.pin.authorityX)
    .str(p.approver)
    .str(p.candidate.id)
    .raw(p.candidate.ed)
    .raw(p.candidate.x)
    .u32(p.candidate.kind)
    .raw(p.expected)
    .raw(p.nonce)
    .u64(p.expires)
    .bytes();
}
export async function verifyPairContext(
  p: PairContext,
  pin: Pin,
): Promise<void> {
  requireOK(
    equal(
      pairTrustDescriptor(p.pin, p.approver),
      pairTrustDescriptor(pin, p.approver),
    ),
    "untrusted",
  );
  await verify(pin.authorityEd, p.signature, await pairBinding(p));
}
export async function signPairContext(
  p: PairContext,
  ed: CryptoKey,
  action: "approve" | "accept" | "cancel",
): Promise<B> {
  return sign(ed, concat(utf8(`ra2/pair-${action}\0`), await pairBinding(p)));
}

export async function verifyPublicSignature(
  pub: B,
  signature: B,
  message: B,
): Promise<void> {
  return verify(pub, signature, message);
}
