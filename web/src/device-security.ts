// Protocol-10 device security. The web client is an ordinary member: it
// verifies the phone-signed membership chain from the pinned genesis, seals
// and opens envelopes with its own device keys, and never acts as authority.
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
export const VERSION = 3;
export const MAX_STATE = 64 << 10;
export const MAX_CIPHER = 4 << 20;
export const MAX_STATES = 4096;
/** Pairing payload limits: 256 states / 800 KiB of chain material. */
export const MAX_PAIR_STATES = 256;
export const MAX_PAIR_CHAIN_BYTES = 800 << 10;
export const LEASE = 72 * 60 * 60 * 1000;
export const MAX_LEASE = 7 * 24 * 60 * 60 * 1000;
/** How far ahead of the local clock a state's IssuedAt may be. */
export const MAX_CLOCK_SKEW = 5 * 60 * 1000;
export class SecurityError extends Error {
  constructor(
    readonly code:
      | "invalid"
      | "untrusted"
      | "stale"
      | "unknown"
      | "conflict"
      | "pending"
      | "capacity"
      | "storage"
      | "unavailable"
      | "lease"
      | "clock" = "invalid",
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
const opaque = (s: string) => /^[A-Za-z0-9_.:-]{1,128}$/.test(s);
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
    return new TextDecoder("utf-8", { fatal: true }).decode(this.lp(MAX_STATE));
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
  recoveryEd: B;
  parent: B;
  epoch: bigint;
  contentEpoch: bigint;
  issuedAt: bigint;
  notAfter: bigint;
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
  prevSecret: B;
  signature: B;
}
export interface Pin {
  account: string;
  domain: B;
  genesis: B;
}
function validateMember(m: Member): void {
  requireOK(
    id(m.id) &&
      (m.kind === 1 || m.kind === 2) &&
      m.ed.length === 32 &&
      m.x.length === 32,
  );
  nonzero(m.x);
}
async function validateCore(c: Core): Promise<void> {
  requireOK(
    id(c.account) &&
      id(c.authorityID) &&
      c.epoch > 0n &&
      c.contentEpoch > 0n &&
      c.contentEpoch <= c.epoch &&
      c.issuedAt > 0n &&
      c.notAfter > c.issuedAt &&
      c.notAfter - c.issuedAt <= BigInt(MAX_LEASE) &&
      c.members.length > 0 &&
      c.members.length <= 128 &&
      c.removed.length <= 128,
  );
  nonzero(c.domain);
  nonzero(c.recoveryEd);
  hash32(c.parent);
  for (let i = 0; i < c.members.length; i++) {
    const m = c.members[i];
    validateMember(m);
    requireOK(m.id === (await deviceID(m.ed)));
    if (i) requireOK(c.members[i - 1].id < m.id);
  }
  const a = c.members.find((m) => m.id === c.authorityID);
  requireOK(a?.kind === 2);
  for (let i = 0; i < c.removed.length; i++) {
    const rid = c.removed[i];
    requireOK(
      id(rid) &&
        (!i || c.removed[i - 1] < rid) &&
        !c.members.some((m) => m.id === rid) &&
        rid !== c.authorityID,
    );
  }
  if (c.epoch === 1n)
    requireOK(
      equal(c.parent, ZERO) &&
        c.members.length === 1 &&
        c.removed.length === 0 &&
        c.contentEpoch === 1n,
    );
  else requireOK(!equal(c.parent, ZERO));
}
export async function coreBytes(c: Core): Promise<B> {
  await validateCore(c);
  const w = new Writer()
    .u32(VERSION)
    .str(c.account)
    .raw(c.domain)
    .str(c.authorityID)
    .raw(c.recoveryEd)
    .raw(c.parent)
    .u64(c.epoch)
    .u64(c.contentEpoch)
    .u64(c.issuedAt)
    .u64(c.notAfter)
    .u32(c.members.length);
  for (const m of c.members)
    w.str(m.id).raw(m.ed).raw(m.x).u32(m.kind);
  w.u32(c.removed.length);
  for (const r of c.removed) w.str(r);
  return w.bytes();
}
export async function parseCore(b: B): Promise<Core> {
  requireOK(b.length <= MAX_STATE);
  const r = new Reader(b);
  requireOK(r.u32() === VERSION);
  const c: Core = {
    account: r.str(),
    domain: r.raw(32),
    authorityID: r.str(),
    recoveryEd: r.raw(32),
    parent: r.raw(32),
    epoch: r.u64(),
    contentEpoch: r.u64(),
    issuedAt: r.u64(),
    notAfter: r.u64(),
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
    requireOK(
      g.recipient === s.core.members[i].id &&
        g.cipher.length === 48 &&
        g.enc.length === 32,
    );
    nonzero(g.enc);
    w.str(g.recipient).lp(g.enc).lp(g.cipher);
  }
  return w.bytes();
}
async function coreHash(s: SecurityState): Promise<B> {
  return hash("ra3/core\0", await coreBytes(s.core));
}
export async function stateHash(s: SecurityState): Promise<B> {
  const ch = await coreHash(s);
  const g = grantBytes(s);
  return hash(
    "ra3/state\0",
    ch,
    g,
    new Writer().lp(s.prevSecret).bytes(),
  );
}
async function stateSignBytes(s: SecurityState): Promise<B> {
  const c = s.core;
  return new Writer()
    .raw(utf8("ra3/state-sig\0"))
    .str(c.account)
    .raw(c.domain)
    .raw(c.parent)
    .u64(c.epoch)
    .u64(c.contentEpoch)
    .u64(c.issuedAt)
    .u64(c.notAfter)
    .str(c.authorityID)
    .raw(c.recoveryEd)
    .raw(await stateHash(s))
    .bytes();
}
export async function stateBytes(s: SecurityState): Promise<B> {
  requireOK(s.signature.length === 64);
  const b = new Writer()
    .lp(await coreBytes(s.core))
    .raw(grantBytes(s))
    .lp(s.prevSecret)
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
  const prevSecret = r.lp(MAX_STATE);
  const signature = r.lp(64);
  r.done();
  const s = { core, grants, prevSecret, signature };
  // ParseState must accept only the canonical encoding, as Go does.
  requireOK(equal(await stateBytes(s), b));
  return s;
}
export async function verifyStateSignature(s: SecurityState): Promise<void> {
  const m = s.core.members.find((m) => m.id === s.core.authorityID);
  requireOK(m?.kind === 2, "untrusted");
  await verify(m.ed, s.signature, await stateSignBytes(s));
}
export function stateAt(states: SecurityState[], epoch: bigint): SecurityState {
  requireOK(epoch >= 1n && epoch <= BigInt(states.length));
  return states[Number(epoch - 1n)];
}
export function pinOf(genesis: SecurityState): Promise<Pin> {
  return (async () => {
    requireOK(genesis.core.epoch === 1n);
    return {
      account: genesis.core.account,
      domain: genesis.core.domain,
      genesis: await stateHash(genesis),
    };
  })();
}
/** The pin only anchors account, domain and genesis; later transitions are
 * verified against each parent's authority member. */
export async function verifyPin(p: Pin, s: SecurityState): Promise<void> {
  requireOK(
    s.core.account === p.account && equal(s.core.domain, p.domain),
    "untrusted",
  );
  nonzero(p.genesis);
  if (s.core.epoch === 1n)
    requireOK(equal(await stateHash(s), p.genesis), "untrusted");
}
export async function verifyGenesis(p: Pin, s: SecurityState): Promise<void> {
  await verifyPin(p, s);
  requireOK(s.core.epoch === 1n, "untrusted");
  const m = s.core.members.find((m) => m.id === s.core.authorityID);
  requireOK(
    m?.kind === 2 &&
      s.core.members.length === 1 &&
      s.core.members[0].id === m.id,
    "untrusted",
  );
  await verifyStateSignature(s);
}
export interface RecoveryStatement {
  account: string;
  domain: B;
  parent: B;
  newAuthorityID: string;
  newRecoveryEd: B;
  newMember: Member;
  issuedAt: bigint;
  notAfter: bigint;
  stateHash: B;
  nonce: B;
  signature: B;
}
async function recoverySignBytes(r: RecoveryStatement): Promise<B> {
  const m = r.newMember;
  requireOK(
    id(r.account) &&
      r.newAuthorityID === m.id &&
      m.kind === 2 &&
      id(r.newAuthorityID) &&
      r.newRecoveryEd.length === 32 &&
      !equal(r.newRecoveryEd, ZERO) &&
      r.issuedAt > 0n &&
      r.notAfter > r.issuedAt &&
      r.stateHash.length === 32 &&
      r.nonce.length === 32,
  );
  validateMember(m);
  requireOK(m.id === (await deviceID(m.ed)));
  requireOK(!equal(r.domain, ZERO) && !equal(r.parent, ZERO));
  requireOK(!equal(r.stateHash, ZERO) && !equal(r.nonce, ZERO));
  return new Writer()
    .raw(utf8("ra3/recovery\0"))
    .str(r.account)
    .raw(r.domain)
    .raw(r.parent)
    .str(r.newAuthorityID)
    .raw(r.newRecoveryEd)
    .str(m.id)
    .raw(m.ed)
    .raw(m.x)
    .u32(m.kind)
    .u64(r.issuedAt)
    .u64(r.notAfter)
    .raw(r.stateHash)
    .raw(r.nonce)
    .bytes();
}
export async function recoveryBytes(r: RecoveryStatement): Promise<B> {
  requireOK(r.signature.length === 64);
  const b = new Writer()
    .lp(await recoverySignBytes(r))
    .lp(r.signature)
    .bytes();
  requireOK(b.length <= MAX_STATE);
  return b;
}
export async function parseRecoveryStatement(
  b: B,
): Promise<RecoveryStatement> {
  requireOK(b.length <= MAX_STATE);
  const r = new Reader(b);
  const body = r.lp(MAX_STATE);
  const signature = r.lp(64);
  r.done();
  requireOK(signature.length === 64);
  const label = utf8("ra3/recovery\0");
  requireOK(
    body.length >= label.length && equal(body.slice(0, label.length), label),
  );
  const d = new Reader(body.slice(label.length));
  const out: RecoveryStatement = {
    account: d.str(),
    domain: d.raw(32),
    parent: d.raw(32),
    newAuthorityID: d.str(),
    newRecoveryEd: d.raw(32),
    newMember: {
      id: d.str(),
      ed: d.raw(32),
      x: d.raw(32),
      kind: d.u32() as 1 | 2,
    },
    issuedAt: d.u64(),
    notAfter: d.u64(),
    stateHash: d.raw(32),
    nonce: d.raw(32),
    signature,
  };
  d.done();
  requireOK(equal(await recoverySignBytes(out), body));
  return out;
}
export async function verifyRecovery(
  r: RecoveryStatement,
  parent: SecurityState,
  next: SecurityState,
): Promise<void> {
  const parentHash = await stateHash(parent);
  const nextHash = await stateHash(next);
  const m = next.core.members.find((m) => m.id === next.core.authorityID);
  requireOK(
    r.account === parent.core.account &&
      equal(r.domain, parent.core.domain) &&
      equal(r.parent, parentHash) &&
      r.newAuthorityID === next.core.authorityID &&
      equal(r.newRecoveryEd, next.core.recoveryEd) &&
      m !== undefined &&
      equal(m.ed, r.newMember.ed) &&
      equal(m.x, r.newMember.x) &&
      m.kind === r.newMember.kind &&
      r.newMember.id === m.id &&
      r.issuedAt === next.core.issuedAt &&
      r.notAfter === next.core.notAfter &&
      equal(r.stateHash, nextHash),
    "untrusted",
  );
  await verify(parent.core.recoveryEd, r.signature, await recoverySignBytes(r));
}
function sameMembers(a: Member[], b: Member[]): boolean {
  return (
    a.length === b.length &&
    a.every(
      (m, i) =>
        m.id === b[i].id &&
        equal(m.ed, b[i].ed) &&
        equal(m.x, b[i].x) &&
        m.kind === b[i].kind,
    )
  );
}
function membershipDelta(parent: SecurityState, next: SecurityState): void {
  const newlyRemoved: string[] = [];
  let added = 0;
  let rotated = 0;
  for (const m of parent.core.members) {
    const n = next.core.members.find((n) => n.id === m.id);
    if (!n) newlyRemoved.push(m.id);
    else if (!equal(n.ed, m.ed) || n.kind !== m.kind)
      throw new SecurityError();
    else if (!equal(n.x, m.x)) rotated++;
  }
  for (const m of next.core.members)
    if (!parent.core.members.some((o) => o.id === m.id)) {
      if (parent.core.removed.includes(m.id)) throw new SecurityError();
      added++;
    }
  // Removed is the accumulated tombstone set, not only this state's
  // removals; a removed ID can never be re-added.
  const wantRemoved = [...parent.core.removed, ...newlyRemoved].sort();
  requireOK(
    wantRemoved.join(",") === next.core.removed.join(",") &&
      added <= 1 &&
      rotated <= 1 &&
      !(added && newlyRemoved.length) &&
      !(rotated && (added || newlyRemoved.length)) &&
      !(added === 0 && newlyRemoved.length === 0 && rotated === 0),
  );
}
function recoveryDelta(parent: SecurityState, next: SecurityState): void {
  const removed = [...parent.core.removed, parent.core.authorityID].sort();
  requireOK(removed.join(",") === next.core.removed.join(","));
  for (const m of parent.core.members) {
    if (m.id === parent.core.authorityID) continue;
    const n = next.core.members.find((n) => n.id === m.id);
    requireOK(
      n !== undefined && equal(n.ed, m.ed) && equal(n.x, m.x) && n.kind === m.kind,
    );
  }
  for (const m of next.core.members) {
    if (m.id === next.core.authorityID) continue;
    requireOK(parent.core.members.some((o) => o.id === m.id));
  }
}
/** Checks the signed transition from parent to next. A recovery transition
 * also needs the old phone's recovery statement. Content grants are not
 * opened here; the chain installer verifies PrevSecret with the new key. */
export async function verifyTransition(
  parent: SecurityState,
  next: SecurityState,
  recovery?: RecoveryStatement,
): Promise<void> {
  requireOK(
    equal(next.core.parent, await stateHash(parent)) &&
      next.core.epoch === parent.core.epoch + 1n &&
      next.core.account === parent.core.account &&
      equal(next.core.domain, parent.core.domain) &&
      next.core.issuedAt >= parent.core.issuedAt,
    "stale",
  );
  const authority = next.core.members.find(
    (m) => m.id === next.core.authorityID,
  );
  requireOK(authority?.kind === 2);
  if (recovery) {
    await verifyRecovery(recovery, parent, next);
    requireOK(
      next.core.contentEpoch === parent.core.contentEpoch + 1n &&
        next.prevSecret.length === 0,
    );
    requireOK(!next.core.members.some((m) => m.id === parent.core.authorityID));
    requireOK(next.core.removed.includes(parent.core.authorityID));
    await verifyStateSignature(next);
    recoveryDelta(parent, next);
    return;
  }
  requireOK(next.core.authorityID === parent.core.authorityID, "untrusted");
  const oldAuthority = parent.core.members.find(
    (m) => m.id === parent.core.authorityID,
  );
  requireOK(oldAuthority !== undefined);
  await verify(oldAuthority.ed, next.signature, await stateSignBytes(next));
  if (next.core.contentEpoch === parent.core.contentEpoch) {
    requireOK(
      next.core.removed.join(",") === parent.core.removed.join(",") &&
        sameMembers(next.core.members, parent.core.members) &&
        next.prevSecret.length === 0,
    );
  } else if (next.core.contentEpoch === parent.core.contentEpoch + 1n) {
    requireOK(next.prevSecret.length === 60);
    membershipDelta(parent, next);
  } else {
    throw new SecurityError();
  }
}
export async function verifyChain(
  p: Pin,
  states: SecurityState[],
  proofs: (RecoveryStatement | undefined)[] = [],
): Promise<void> {
  requireOK(states.length > 0 && states.length <= MAX_STATES);
  requireOK(proofs.length === 0 || proofs.length === states.length);
  await verifyGenesis(p, states[0]);
  const now = BigInt(Date.now());
  for (let i = 0; i < states.length; i++) {
    if (states[i].core.issuedAt > now + BigInt(MAX_CLOCK_SKEW))
      throw new SecurityError("clock");
    if (i > 0)
      await verifyTransition(states[i - 1], states[i], proofs[i]);
  }
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
function grantContext(c: Core, ch: B, recipient: string): B {
  return new Writer()
    .str(c.account)
    .raw(c.domain)
    .u64(c.contentEpoch)
    .raw(ch)
    .str(recipient)
    .bytes();
}
export async function openGrant(
  p: Pin,
  s: SecurityState,
  recipient: string,
  xPrivate: CryptoKey,
): Promise<B> {
  await verifyPin(p, s);
  await verifyStateSignature(s);
  const m = s.core.members.find((m) => m.id === recipient);
  requireOK(m, "untrusted");
  const g = s.grants.find((g) => g.recipient === recipient);
  requireOK(g, "untrusted");
  const ctx = grantContext(s.core, await coreHash(s), recipient);
  const rc = await suite().createRecipientContext({
    recipientKey: xPrivate,
    enc: g.enc,
    info: concat(utf8("ra3/grant-info\0"), ctx),
  });
  const secret = new Uint8Array(
    await rc.open(g.cipher, concat(utf8("ra3/grant-aad\0"), ctx)),
  );
  requireOK(secret.length === 32, "untrusted");
  return secret;
}
export async function openPrevSecret(newSecret: B, blob: B): Promise<B> {
  requireOK(newSecret.length === 32 && blob.length === 60);
  const key = await crypto.subtle.importKey("raw", newSecret, "AES-GCM", false, [
    "decrypt",
  ]);
  const out = new Uint8Array(
    await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: blob.slice(0, 12), additionalData: utf8("ra3/prev-secret\0") },
      key,
      blob.slice(12),
    ),
  );
  requireOK(out.length === 32, "untrusted");
  return out;
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
const sessionKey = (p: Purpose) =>
  [
    Purpose.Event,
    Purpose.Image,
    Purpose.TerminalOutput,
    Purpose.TerminalInput,
    Purpose.TerminalResize,
    Purpose.TerminalHistory,
  ].includes(p);
function validateHeader(h: SecurityHeader): void {
  requireOK(
    id(h.account) &&
      id(h.sender) &&
      h.epoch > 0n &&
      h.contentEpoch > 0n &&
      h.contentEpoch <= h.epoch &&
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
    .u32(VERSION)
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
  requireOK(r.u32() === VERSION);
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
  if (h.purpose === Purpose.DeviceMeta) {
    requireOK(h.sender === h.request, "untrusted");
    requireOK(
      s.core.members.some((m) => m.id === h.request && m.kind === 2),
      "untrusted",
    );
  } else if (h.purpose === Purpose.Prefs || control(h.purpose))
    requireOK(m.kind === 2, "untrusted");
  else requireOK(m.kind === 1 && m.id === h.machine, "untrusted");
  return m;
}
export interface FormedCipher {
  header: B;
  nonce: B;
  cipher: B;
  origin: B;
}
export type SecureWire = FormedCipher;
export async function commitment(c: FormedCipher): Promise<B> {
  requireOK(
    c.header.length <= 1024 &&
      c.nonce.length === 12 &&
      c.cipher.length >= 16 &&
      c.cipher.length <= MAX_CIPHER,
  );
  parseHeader(c.header);
  return hash(
    "ra3/ciphertext\0",
    new Writer().lp(c.header).lp(c.nonce).lp(c.cipher).bytes(),
  );
}
function originBytes(h: SecurityHeader, hh: B, commit: B): B {
  return new Writer()
    .raw(utf8("ra3/cipher-origin\0"))
    .str(h.account)
    .raw(h.domain)
    .raw(h.state)
    .str(h.sender)
    .raw(hh)
    .raw(commit)
    .bytes();
}
export async function deriveContentKey(
  secret: B,
  seed: B | undefined,
  h: SecurityHeader,
): Promise<CryptoKey> {
  requireOK(secret.length === 32);
  validateHeader(h);
  let salt: B = new Uint8Array();
  if (sessionKey(h.purpose)) {
    requireOK(seed?.length === 32);
    salt = seed;
  }
  const ctx = new Writer()
    .raw(utf8("ra3/content-key\0"))
    .str(h.account)
    .raw(h.domain)
    .u64(h.epoch)
    .raw(h.state)
    .u64(h.contentEpoch)
    .u32(h.purpose)
    .str(h.sender)
    .str(h.machine)
    .str(h.session)
    .str(h.request)
    .u64(h.seq)
    .u64(h.counter)
    .bytes();
  const root = await crypto.subtle.importKey("raw", secret, "HKDF", false, [
    "deriveBits",
  ]);
  const purposeInfo = new Writer()
    .raw(utf8("ra3/purpose-key\0"))
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
  secret: B,
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
export async function verifyWire(
  p: Pin,
  s: SecurityState,
  head: SecurityState,
  w: SecureWire,
  live: boolean,
  now: bigint,
): Promise<SecurityHeader> {
  await verifyPin(p, s);
  await verifyStateSignature(s);
  const h = parseHeader(w.header);
  const member = await scope(h, s);
  if (live) {
    requireOK(head.core.members.some((m) => m.id === h.sender), "untrusted");
    if (control(h.purpose)) {
      if (head.core.issuedAt > now + BigInt(MAX_CLOCK_SKEW))
        throw new SecurityError("clock");
      requireOK(
        head.core.notAfter >= now && head.core.contentEpoch === h.contentEpoch,
        "stale",
      );
    }
  }
  const hh = await hash("", w.header);
  const commit = await commitment(w);
  await verify(member.ed, w.origin, originBytes(h, hh, commit));
  return h;
}
/** The body decoder must additionally enforce the authenticated purpose and
 * owner. This primitive alone must never dispatch terminal or command actions. */
export async function openWire(
  p: Pin,
  s: SecurityState,
  head: SecurityState,
  w: SecureWire,
  secret: B,
  live: boolean,
  now: bigint,
  seed?: B,
): Promise<{ header: SecurityHeader; plaintext: B }> {
  const header = await verifyWire(p, s, head, w, live, now);
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
        version: VERSION,
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
  });
}
export function fromEnvelope(e: Envelope): SecureWire {
  requireOK(e.securityHeader.length > 0);
  const h = parseHeader(new Uint8Array(e.securityHeader));
  const p = fromBinary(HeaderSchema, e.header);
  requireOK(
    p.version === VERSION &&
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
  };
}
export function wireBytes(w: SecureWire): B {
  requireOK(w.origin.length === 64);
  return new Writer()
    .lp(w.header)
    .lp(w.nonce)
    .lp(w.cipher)
    .lp(w.origin)
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
  };
  r.done();
  wireBytes(w);
  return w;
}
export const isMutable = mutable;

export function b64(b: Uint8Array): string {
  let s = "";
  for (const n of b) s += String.fromCharCode(n);
  return btoa(s);
}
export function unb64(s: string): B {
  requireOK(typeof s === "string" && s.length % 4 === 0);
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
export function jsonHash(v: unknown): B {
  requireOK(
    Array.isArray(v) &&
      v.length === 32 &&
      v.every((n) => Number.isInteger(n) && n >= 0 && n <= 255),
  );
  return new Uint8Array(v);
}

export interface PairTrust {
  pin: Pin;
  approver: string;
}
export function pairTrustDescriptor(pin: Pin, approver: string): B {
  requireOK(
    id(pin.account) && id(approver) && pin.genesis.length === 32,
  );
  nonzero(pin.domain);
  nonzero(pin.genesis);
  return new Writer()
    .raw(utf8("ra3/pair-trust\0"))
    .str(pin.account)
    .raw(pin.domain)
    .raw(pin.genesis)
    .str(approver)
    .bytes();
}
export function parsePairTrustDescriptor(b: B): PairTrust {
  const label = utf8("ra3/pair-trust\0");
  requireOK(b.length > label.length && equal(b.slice(0, label.length), label));
  const r = new Reader(b.slice(label.length));
  const pin = {
    account: r.str(),
    domain: r.raw(32),
    genesis: r.raw(32),
  };
  const approver = r.str();
  r.done();
  requireOK(id(pin.account) && id(approver));
  nonzero(pin.domain);
  nonzero(pin.genesis);
  return { pin, approver };
}
export async function pairDomainCommit(
  ephemeral: B,
  trust: B,
): Promise<B> {
  requireOK(ephemeral.length === 32);
  return hash(
    "ra3/pair-commit\0",
    new Writer().lp(ephemeral).lp(trust).bytes(),
  );
}
export async function verifyPublicSignature(
  pub: B,
  signature: B,
  message: B,
): Promise<void> {
  return verify(pub, signature, message);
}
