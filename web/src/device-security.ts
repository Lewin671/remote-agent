// Thin S5 Web facade delegating device security parsing, chain/lease/recovery
// verification, HPKE grant opening, and pair trust helpers to Go clientcore via SecCall.
import { create } from "@bufbuild/protobuf";
import { concat, utf8 } from "./crypto";
import {
  SecGrantChallengeCallSchema,
  SecGrantSchema,
  SecMarshalLeaseCallSchema,
  SecMarshalRecoveryStatementCallSchema,
  SecMemberSchema,
  SecNewLeaseUnsignedCallSchema,
  SecOpenGrantWithDHCallSchema,
  SecOpenPrevSecretCallSchema,
  SecPairDomainCommitCallSchema,
  SecPairTrustDescriptorCallSchema,
  SecVerifyChainCallSchema,
  SecVerifyLeaseCallSchema,
  SecVerifyRecoveryStatementCallSchema,
  type SecParsedLease,
  type SecParsedRecoveryStatement,
  type SecParsedState,
} from "./gen/remoteagent/core/v1/seccall_pb";
import {
  makeSecPin,
  secCall,
  SecurityError,
  x25519DeriveWithPrivateKey,
} from "./core/sec-call";

export { SecurityError };

type B = Uint8Array<ArrayBuffer>;
export const VERSION = 3;
export const MAX_STATE = 64 << 10;
export const MAX_CIPHER = 4 << 20;
export const MAX_STATES = 4096;
/** Pairing payload limits: 256 states / 800 KiB of chain material. */
export const MAX_PAIR_STATES = 256;
export const MAX_PAIR_CHAIN_BYTES = 800 << 10;
export const LEASE = 72 * 60 * 60 * 1000;
/** How far ahead of the local clock a state's IssuedAt may be. */
export const MAX_CLOCK_SKEW = 5 * 60 * 1000;

function requireOK(
  ok: unknown,
  code: SecurityError["code"] = "invalid",
): asserts ok {
  if (!ok) throw new SecurityError(code);
}
export function equal(a: Uint8Array, b: Uint8Array): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}
const u32be = (n: number): B => {
  requireOK(Number.isInteger(n) && n >= 0 && n <= 0xffffffff);
  const b = new Uint8Array(4);
  new DataView(b.buffer).setUint32(0, n);
  return b;
};
const u64be = (n: bigint): B => {
  requireOK(n >= 0n && n <= 0xffffffffffffffffn);
  const b = new Uint8Array(8);
  new DataView(b.buffer).setBigUint64(0, n);
  return b;
};
const lp = (b: Uint8Array): B => concat(u32be(b.length), b);
const str = (s: string): B => lp(utf8(s));

async function verifyEd(pub: B, signature: B, message: B): Promise<void> {
  requireOK(pub.length === 32 && signature.length === 64, "untrusted");
  const key = await crypto.subtle.importKey("raw", pub, "Ed25519", false, ["verify"]);
  requireOK(await crypto.subtle.verify("Ed25519", key, signature, message), "untrusted");
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
export interface RecoveryStatement {
  account: string;
  domain: B;
  parent: B;
  newAuthorityID: string;
  newRecoveryEd: B;
  newMember: Member;
  issuedAt: bigint;
  stateHash: B;
  nonce: B;
  signature: B;
}
export interface LeaseCertificate {
  domain: B;
  head: B;
  issuedAt: bigint;
  notAfter: bigint;
  signature: B;
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
}
export interface PairTrust {
  pin: Pin;
  approver: string;
}

const stateInfo = new WeakMap<SecurityState, {
  raw?: B;
  parsed?: SecParsedState;
}>();

function immutableObject<T extends object>(source: T): T {
  const result = (Array.isArray(source) ? [] : {}) as T;
  for (const [name, value] of Object.entries(source)) {
    if (value instanceof Uint8Array) {
      const bytes = new Uint8Array(value);
      Object.defineProperty(result, name, { enumerable: true, get: () => bytes.slice() });
    } else {
      Object.defineProperty(result, name, {
        enumerable: true,
        value: value && typeof value === "object" ? immutableObject(value) : value,
      });
    }
  }
  return Object.freeze(result);
}

export function immutableState(s: SecurityState): SecurityState {
  if (stateInfo.has(s)) return s;
  const snapshot = immutableObject(s);
  stateInfo.set(snapshot, {});
  return snapshot;
}

const recoveryRawCache = new WeakMap<RecoveryStatement, B>();
const leaseRawCache = new WeakMap<LeaseCertificate, B>();

export function stateFromProto(ps: SecParsedState, _raw?: B): SecurityState {
  const s: SecurityState = {
    core: {
      account: ps.account,
      domain: new Uint8Array(ps.domain),
      authorityID: ps.authorityId,
      recoveryEd: new Uint8Array(ps.recoveryEd),
      parent: new Uint8Array(ps.parent),
      epoch: ps.epoch,
      contentEpoch: ps.contentEpoch,
      issuedAt: ps.issuedAt,
      members: ps.members.map((m) => ({
        id: m.id,
        ed: new Uint8Array(m.edPublic),
        x: new Uint8Array(m.xPublic),
        kind: m.kind as 1 | 2,
      })),
      removed: [...ps.removed],
    },
    grants: ps.grants.map((g) => ({
      recipient: g.recipient,
      enc: new Uint8Array(g.enc),
      cipher: new Uint8Array(g.cipher),
    })),
    prevSecret: new Uint8Array(ps.prevSecret),
    signature: new Uint8Array(ps.signature),
  };
  return s;
}

export function memberToProto(m: Member) {
  return create(SecMemberSchema, {
    id: m.id,
    edPublic: new Uint8Array(m.ed),
    xPublic: new Uint8Array(m.x),
    kind: m.kind,
  });
}

export function grantToProto(g: Grant) {
  return create(SecGrantSchema, {
    recipient: g.recipient,
    enc: new Uint8Array(g.enc),
    cipher: new Uint8Array(g.cipher),
  });
}

function leaseFromProto(pl: SecParsedLease, raw?: B): LeaseCertificate {
  const l: LeaseCertificate = {
    domain: new Uint8Array(pl.domain),
    head: new Uint8Array(pl.head),
    issuedAt: pl.issuedAt,
    notAfter: pl.notAfter,
    signature: new Uint8Array(pl.signature),
  };
  if (raw) leaseRawCache.set(l, new Uint8Array(raw));
  return l;
}

function recoveryFromProto(pr: SecParsedRecoveryStatement, raw?: B): RecoveryStatement {
  const r: RecoveryStatement = {
    account: pr.account,
    domain: new Uint8Array(pr.domain),
    parent: new Uint8Array(pr.parent),
    newAuthorityID: pr.newAuthorityId,
    newRecoveryEd: new Uint8Array(pr.newRecoveryEd),
    newMember: {
      id: pr.newMember?.id ?? "",
      ed: new Uint8Array(pr.newMember?.edPublic ?? []),
      x: new Uint8Array(pr.newMember?.xPublic ?? []),
      kind: (pr.newMember?.kind ?? 1) as 1 | 2,
    },
    issuedAt: pr.issuedAt,
    stateHash: new Uint8Array(pr.stateHash),
    nonce: new Uint8Array(pr.nonce),
    signature: new Uint8Array(pr.signature),
  };
  if (raw) recoveryRawCache.set(r, new Uint8Array(raw));
  return r;
}

// Structural wire packing for IndexedDB SecurityState objects loaded without an in-memory cache;
// Go parseState / verifyChain validates every invariant on receipt.
function encodeStateStructural(s: SecurityState): B {
  const cached = stateInfo.get(s);
  if (cached?.raw) return new Uint8Array(cached.raw);
  const c = s.core;
  const coreParts: B[] = [
    u32be(VERSION),
    str(c.account),
    c.domain,
    str(c.authorityID),
    c.recoveryEd,
    c.parent,
    u64be(c.epoch),
    u64be(c.contentEpoch),
    u64be(c.issuedAt),
    u32be(c.members.length),
    ...c.members.flatMap((m) => [str(m.id), m.ed, m.x, u32be(m.kind)]),
    u32be(c.removed.length),
    ...c.removed.map(str),
  ];
  const raw = concat(
    lp(concat(...coreParts)),
    u32be(s.grants.length),
    ...s.grants.flatMap((g) => [str(g.recipient), lp(g.enc), lp(g.cipher)]),
    lp(s.prevSecret),
    lp(s.signature),
  );
  requireOK(raw.length <= MAX_STATE, "capacity");
  return raw;
}

function ensureParsedState(s: SecurityState): { raw: B; parsed: SecParsedState } {
  const cached = stateInfo.get(s);
  if (cached?.raw && cached?.parsed) {
    return { raw: new Uint8Array(cached.raw), parsed: cached.parsed };
  }
  const raw = encodeStateStructural(s);
  const parsed = secCall({ case: "parseState", value: raw }).parsedState!;
  const meta = { raw, parsed };
  if (stateInfo.has(s)) stateInfo.set(s, meta);
  return meta;
}

export async function stateBytes(s: SecurityState): Promise<B> {
  return ensureParsedState(s).raw;
}

export async function parseState(b: B): Promise<SecurityState> {
  const resp = secCall({ case: "parseState", value: new Uint8Array(b) });
  return stateFromProto(resp.parsedState!, b);
}

export async function stateHash(s: SecurityState): Promise<B> {
  return new Uint8Array(ensureParsedState(s).parsed.stateHash);
}

export function stateAt(states: SecurityState[], epoch: bigint): SecurityState {
  requireOK(epoch > 0n && epoch <= BigInt(states.length), "stale");
  const s = states[Number(epoch - 1n)];
  requireOK(s.core.epoch === epoch, "stale");
  return s;
}

export async function pinOf(genesis: SecurityState): Promise<Pin> {
  requireOK(genesis.core.epoch === 1n);
  return {
    account: genesis.core.account,
    domain: genesis.core.domain,
    genesis: await stateHash(genesis),
  };
}

export async function recoveryBytes(r: RecoveryStatement): Promise<B> {
  const cached = recoveryRawCache.get(r);
  if (cached) return new Uint8Array(cached);
  const resp = secCall({
    case: "marshalRecoveryStatement",
    value: create(SecMarshalRecoveryStatementCallSchema, {
      account: r.account,
      domain: new Uint8Array(r.domain),
      parent: new Uint8Array(r.parent),
      newAuthorityId: r.newAuthorityID,
      newRecoveryEd: new Uint8Array(r.newRecoveryEd),
      newMember: memberToProto(r.newMember),
      issuedAt: r.issuedAt,
      stateHash: new Uint8Array(r.stateHash),
      nonce: new Uint8Array(r.nonce),
      signature: new Uint8Array(r.signature),
    }),
  });
  const raw = new Uint8Array(resp.data);
  recoveryRawCache.set(r, raw);
  return raw;
}

export async function parseRecoveryStatement(b: B): Promise<RecoveryStatement> {
  const resp = secCall({ case: "parseRecoveryStatement", value: new Uint8Array(b) });
  return recoveryFromProto(resp.parsedRecovery!, b);
}

export async function verifyRecovery(
  r: RecoveryStatement,
  parent: SecurityState,
  next: SecurityState,
): Promise<void> {
  secCall({
    case: "verifyRecoveryStatement",
    value: create(SecVerifyRecoveryStatementCallSchema, {
      statement: await recoveryBytes(r),
      parentState: await stateBytes(parent),
      nextState: await stateBytes(next),
    }),
  });
}

export async function verifyChain(
  p: Pin,
  states: SecurityState[],
  proofs: (RecoveryStatement | undefined)[] = states.map(() => undefined),
): Promise<void> {
  requireOK(states.length > 0 && states.length <= MAX_STATES && proofs.length === states.length);
  const rawStates = await Promise.all(states.map(stateBytes));
  const rawProofs = await Promise.all(
    proofs.map((pr) => (pr ? recoveryBytes(pr) : Promise.resolve(new Uint8Array()))),
  );
  secCall({
    case: "verifyChain",
    value: create(SecVerifyChainCallSchema, {
      pin: makeSecPin(p.account, p.domain, p.genesis),
      states: rawStates,
      recoveryProofs: rawProofs,
    }),
  });
}

export async function newSecurityKey(): Promise<CryptoKeyPair> {
  return crypto.subtle.generateKey({ name: "X25519" }, false, ["deriveBits"]) as Promise<CryptoKeyPair>;
}

export async function openGrant(
  p: Pin,
  s: SecurityState,
  recipient: string,
  xPrivate: CryptoKey,
): Promise<B> {
  const raw = await stateBytes(s);
  const pin = makeSecPin(p.account, p.domain, p.genesis);
  const chal = secCall({
    case: "grantChallenge",
    value: create(SecGrantChallengeCallSchema, { pin, state: raw, memberId: recipient }),
  });
  const dh = await x25519DeriveWithPrivateKey(xPrivate, chal.data);
  const resp = secCall({
    case: "openGrantWithDh",
    value: create(SecOpenGrantWithDHCallSchema, { pin, state: raw, memberId: recipient, dh }),
  });
  return new Uint8Array(resp.data);
}

export async function openPrevSecret(newSecret: B, blob: B): Promise<B> {
  const resp = secCall({
    case: "openPrevSecret",
    value: create(SecOpenPrevSecretCallSchema, {
      newSecret: new Uint8Array(newSecret),
      blob: new Uint8Array(blob),
    }),
  });
  return new Uint8Array(resp.data);
}

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

export function pairTrustDescriptor(pin: Pin, approver: string): B {
  const resp = secCall({
    case: "pairTrustDescriptor",
    value: create(SecPairTrustDescriptorCallSchema, { pin: makeSecPin(pin.account, pin.domain, pin.genesis), approver }),
  });
  return new Uint8Array(resp.data);
}

export function parsePairTrustDescriptor(b: B): PairTrust {
  const resp = secCall({ case: "parsePairTrustDescriptor", value: new Uint8Array(b) });
  const pin = resp.pin!;
  return {
    pin: {
      account: pin.account,
      domain: new Uint8Array(pin.domain),
      genesis: new Uint8Array(pin.genesis),
    },
    approver: resp.text,
  };
}

export async function pairDomainCommit(ephemeral: B, trust: B): Promise<B> {
  const resp = secCall({
    case: "pairDomainCommit",
    value: create(SecPairDomainCommitCallSchema, { initiator: new Uint8Array(ephemeral), trustDescriptor: new Uint8Array(trust) }),
  });
  return new Uint8Array(resp.data);
}

export async function verifyPublicSignature(
  pub: B,
  signature: B,
  message: B,
): Promise<void> {
  return verifyEd(pub, signature, message);
}

export function leaseBytes(l: LeaseCertificate): B {
  const cached = leaseRawCache.get(l);
  if (cached) return new Uint8Array(cached);
  const resp = secCall({
    case: "marshalLease",
    value: create(SecMarshalLeaseCallSchema, {
      domain: new Uint8Array(l.domain),
      head: new Uint8Array(l.head),
      issuedAt: l.issuedAt,
      notAfter: l.notAfter,
      signature: new Uint8Array(l.signature),
    }),
  });
  const raw = new Uint8Array(resp.data);
  leaseRawCache.set(l, raw);
  return raw;
}

export function parseLease(b: B): LeaseCertificate {
  const resp = secCall({ case: "parseLease", value: new Uint8Array(b) });
  return leaseFromProto(resp.parsedLease!, b);
}

export async function verifyLease(l: LeaseCertificate, head: SecurityState): Promise<void> {
  secCall({
    case: "verifyLease",
    value: create(SecVerifyLeaseCallSchema, { headState: await stateBytes(head), lease: leaseBytes(l), checkWindow: false, nowMs: 0n }),
  });
}

export async function signLease(
  head: SecurityState,
  signer: CryptoKey,
  now: bigint,
  floor = 0n,
  beforeSign?: () => void,
): Promise<LeaseCertificate> {
  const effNow = [0n, now, head.core.issuedAt, floor].reduce((a, b) => (a > b ? a : b));
  const unsigned = secCall({
    case: "newLeaseUnsigned",
    value: create(SecNewLeaseUnsignedCallSchema, { headState: await stateBytes(head), nowMs: effNow }),
  });
  const pl = unsigned.parsedLease!;
  beforeSign?.();
  const sig = new Uint8Array(await crypto.subtle.sign("Ed25519", signer, new Uint8Array(pl.signingBytes)));
  const marshaled = secCall({
    case: "marshalLease",
    value: create(SecMarshalLeaseCallSchema, {
      domain: new Uint8Array(pl.domain),
      head: new Uint8Array(pl.head),
      issuedAt: pl.issuedAt,
      notAfter: pl.notAfter,
      signature: sig,
    }),
  });
  const lease = leaseFromProto(marshaled.parsedLease!, new Uint8Array(marshaled.data));
  await verifyLease(lease, head);
  return lease;
}

export async function validLease(
  l: LeaseCertificate | undefined,
  head: SecurityState,
  now: bigint,
): Promise<void> {
  requireOK(l, "lease");
  requireOK(equal(l.head, await stateHash(head)), "lease");
  try {
    secCall({
      case: "verifyLease",
      value: create(SecVerifyLeaseCallSchema, { headState: await stateBytes(head), lease: leaseBytes(l), checkWindow: true, nowMs: now }),
    });
  } catch (e) {
    if (e instanceof SecurityError && e.code === "stale") throw new SecurityError("lease");
    throw e;
  }
}
