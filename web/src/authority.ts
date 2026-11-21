// Pure signing coordinator delegating state/grant/recovery construction & validation to clientcore via SecCall.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { concat, deviceID, utf8 } from "./crypto";
import {
  SecBuildStateCallSchema,
  SecKemPairSchema,
  SecMarshalRecoveryStatementCallSchema,
  SecMarshalStateCallSchema,
  SecNewRecoveryStatementUnsignedCallSchema,
  type SecKemPair,
} from "./gen/remoteagent/core/v1/seccall_pb";
import {
  ed25519SeedPublic,
  ed25519SeedSign,
  secCall,
  x25519DeriveWithPrivateKey,
  x25519PrivateKeyFromSeed,
  x25519SeedPublicKey,
} from "./core/sec-call";
import {
  SecurityError,
  memberToProto,
  stateBytes,
  stateFromProto,
  verifyPublicSignature,
  verifyRecovery,
  recoveryBytes,
  type LeaseCertificate,
  type Member,
  type RecoveryStatement,
  type SecurityState,
} from "./device-security";

type B = Uint8Array<ArrayBuffer>;
/** Optional foreground guard; deterministic random inputs are for tests. */
export interface AuthorityRandomness {
  beforeSign?: () => void;
  grantEkm?: (member: Member) => B;
  prevNonce?: B;
  recoveryNonce?: B;
}
function check(ok: unknown): asserts ok {
  if (!ok) throw new SecurityError();
}
const sorted = (members: Member[]) => [...members].sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
const max = (...values: bigint[]) => values.reduce((a, b) => a > b ? a : b);
/** Pass this time into state builders; it also respects an installed lease. */
export function authoritySigningTime(head: SecurityState, now: bigint, floor = 0n, lease?: LeaseCertificate): bigint {
  return max(1n, now, head.core.issuedAt, floor, lease?.issuedAt ?? 0n);
}

async function hmacSha256(key: B, msg: B): Promise<B> {
  const k = await crypto.subtle.importKey("raw", key, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return new Uint8Array(await crypto.subtle.sign("HMAC", k, msg));
}

// RFC 9180 DHKEM(X25519, HKDF-SHA256) DeriveKeyPair for deterministic test vectors.
async function dhkemX25519DeriveSk(ikm: B): Promise<B> {
  const suiteId = new Uint8Array([0x4b, 0x45, 0x4d, 0x00, 0x20]);
  const hpkeV1 = utf8("HPKE-v1");
  const dkpPrk = await hmacSha256(new Uint8Array(32), concat(hpkeV1, suiteId, utf8("dkp_prk"), ikm));
  return hmacSha256(dkpPrk, concat(new Uint8Array([0x00, 0x20]), hpkeV1, suiteId, utf8("sk"), new Uint8Array([0x01])));
}

async function encapsulateMembers(members: Member[], random?: AuthorityRandomness): Promise<SecKemPair[]> {
  const uniq = new Map<string, Member>();
  for (const m of members) {
    if (!uniq.has(m.id)) uniq.set(m.id, m);
  }
  return Promise.all(
    [...uniq.values()].map(async (member) => {
      const ekm = random?.grantEkm?.(member);
      let enc: B;
      let dh: B;
      if (ekm) {
        const sk = await dhkemX25519DeriveSk(ekm);
        const priv = await x25519PrivateKeyFromSeed(sk, false);
        enc = await x25519SeedPublicKey(sk);
        dh = await x25519DeriveWithPrivateKey(priv, member.x);
      } else {
        const eph = (await crypto.subtle.generateKey({ name: "X25519" }, true, ["deriveBits"])) as CryptoKeyPair;
        enc = new Uint8Array(await crypto.subtle.exportKey("raw", eph.publicKey));
        dh = await x25519DeriveWithPrivateKey(eph.privateKey, member.x);
      }
      return create(SecKemPairSchema, { memberId: member.id, enc, dh });
    }),
  );
}

async function buildAndSignState(
  call: MessageInitShape<typeof SecBuildStateCallSchema>,
  kemMembers: Member[],
  signer: CryptoKey,
  expectedSignerEd: B,
  random?: AuthorityRandomness,
): Promise<SecurityState> {
  const kems = await encapsulateMembers(kemMembers, random);
  const built = secCall({
    case: "buildState",
    value: create(SecBuildStateCallSchema, { ...call, kems }),
  });
  const unsigned = built.unsignedState!;
  random?.beforeSign?.();
  const signature = new Uint8Array(
    await crypto.subtle.sign("Ed25519", signer, new Uint8Array(unsigned.signingBytes)),
  );
  await verifyPublicSignature(expectedSignerEd, signature, new Uint8Array(unsigned.signingBytes));
  const marshaled = secCall({
    case: "marshalState",
    value: create(SecMarshalStateCallSchema, {
      unsignedCore: unsigned.unsignedCore,
      grants: unsigned.grants,
      prevSecret: unsigned.prevSecret,
      signature,
    }),
  });
  return stateFromProto(marshaled.parsedState!, new Uint8Array(marshaled.data));
}

async function genesis(
  account: string,
  domain: B,
  member: Member,
  recoveryEd: B,
  secret: B,
  signer: CryptoKey,
  now: bigint,
  random?: AuthorityRandomness,
) {
  return buildAndSignState(
    {
      kind: 1,
      account,
      domain: new Uint8Array(domain),
      member: memberToProto(member),
      recoveryEd: new Uint8Array(recoveryEd),
      secret: new Uint8Array(secret),
      nowMs: max(1n, now),
    },
    [member],
    signer,
    member.ed,
    random,
  );
}

async function membership(
  parent: SecurityState,
  members: Member[],
  removed: string[],
  oldSecret: B,
  newSecret: B,
  signer: CryptoKey,
  now: bigint,
  random?: AuthorityRandomness,
) {
  const auth = parent.core.members.find((m) => m.id === parent.core.authorityID);
  check(auth);
  const nextMembers = sorted(members);
  return buildAndSignState(
    {
      kind: 2,
      parentState: await stateBytes(parent),
      members: nextMembers.map(memberToProto),
      removed: [...removed].sort(),
      oldSecret: new Uint8Array(oldSecret),
      secret: new Uint8Array(newSecret),
      nowMs: max(1n, now, parent.core.issuedAt),
      prevSecretNonce: random?.prevNonce ?? crypto.getRandomValues(new Uint8Array(12)),
    },
    nextMembers,
    signer,
    auth.ed,
    random,
  );
}

async function pair(
  parent: SecurityState,
  candidate: Member,
  oldSecret: B,
  newSecret: B,
  signer: CryptoKey,
  now: bigint,
  random?: AuthorityRandomness,
) {
  const auth = parent.core.members.find((m) => m.id === parent.core.authorityID);
  check(auth);
  return buildAndSignState(
    {
      kind: 3,
      parentState: await stateBytes(parent),
      member: memberToProto(candidate),
      oldSecret: new Uint8Array(oldSecret),
      secret: new Uint8Array(newSecret),
      nowMs: max(1n, now, parent.core.issuedAt),
      prevSecretNonce: random?.prevNonce ?? crypto.getRandomValues(new Uint8Array(12)),
    },
    [...parent.core.members, candidate],
    signer,
    auth.ed,
    random,
  );
}

async function remove(
  parent: SecurityState,
  targets: string[],
  oldSecret: B,
  newSecret: B,
  signer: CryptoKey,
  now: bigint,
  random?: AuthorityRandomness,
) {
  check(targets.length > 0 && targets.every((id) => id !== parent.core.authorityID && parent.core.members.some((m) => m.id === id)));
  return membership(
    parent,
    parent.core.members.filter((m) => !targets.includes(m.id)),
    [...new Set([...parent.core.removed, ...targets])],
    oldSecret,
    newSecret,
    signer,
    now,
    random,
  );
}

async function rotateX(
  parent: SecurityState,
  target: string,
  newXPublic: B,
  oldSecret: B,
  newSecret: B,
  signer: CryptoKey,
  now: bigint,
  random?: AuthorityRandomness,
) {
  check(newXPublic.length === 32 && parent.core.members.some((m) => m.id === target));
  return membership(
    parent,
    parent.core.members.map((m) => m.id === target ? { ...m, x: new Uint8Array(newXPublic) } : m),
    parent.core.removed,
    oldSecret,
    newSecret,
    signer,
    now,
    random,
  );
}

async function veto(
  parent: SecurityState,
  newRecoveryEd: B,
  secret: B,
  signer: CryptoKey,
  now: bigint,
  random?: AuthorityRandomness,
) {
  const auth = parent.core.members.find((m) => m.id === parent.core.authorityID);
  check(auth);
  return buildAndSignState(
    {
      kind: 6,
      parentState: await stateBytes(parent),
      secret: new Uint8Array(secret),
      recoveryEd: new Uint8Array(newRecoveryEd),
      nowMs: max(1n, now, parent.core.issuedAt),
    },
    parent.core.members,
    signer,
    auth.ed,
    random,
  );
}

async function handoff(
  parent: SecurityState,
  incoming: string,
  recoveryEd: B,
  oldSecret: B,
  newSecret: B,
  signer: CryptoKey,
  now: bigint,
  random?: AuthorityRandomness,
) {
  const auth = parent.core.members.find((m) => m.id === parent.core.authorityID);
  check(auth);
  return buildAndSignState(
    {
      kind: 4,
      parentState: await stateBytes(parent),
      incomingId: incoming,
      recoveryEd: new Uint8Array(recoveryEd),
      oldSecret: new Uint8Array(oldSecret),
      secret: new Uint8Array(newSecret),
      nowMs: max(1n, now, parent.core.issuedAt),
      prevSecretNonce: random?.prevNonce ?? crypto.getRandomValues(new Uint8Array(12)),
    },
    parent.core.members,
    signer,
    auth.ed,
    random,
  );
}

async function recovery(
  parent: SecurityState,
  newMember: Member,
  members: Member[],
  secret: B,
  newSigner: CryptoKey,
  oldRecoverySeed: B,
  newRecoveryEd: B,
  now: bigint,
  random?: AuthorityRandomness,
): Promise<{ state: SecurityState; statement: RecoveryStatement }> {
  check(oldRecoverySeed.length === 32);
  const nextMembers = sorted(members);
  const parentRaw = await stateBytes(parent);
  const state = await buildAndSignState(
    {
      kind: 5,
      parentState: parentRaw,
      member: memberToProto(newMember),
      members: nextMembers.map(memberToProto),
      secret: new Uint8Array(secret),
      recoveryEd: new Uint8Array(newRecoveryEd),
      nowMs: max(1n, now, parent.core.issuedAt),
    },
    [...nextMembers, newMember],
    newSigner,
    newMember.ed,
    random,
  );
  const nextRaw = await stateBytes(state);
  const unsignedStmt = secCall({
    case: "newRecoveryStatementUnsigned",
    value: create(SecNewRecoveryStatementUnsignedCallSchema, {
      parentState: parentRaw,
      nextState: nextRaw,
      nonce: random?.recoveryNonce ?? crypto.getRandomValues(new Uint8Array(32)),
    }),
  });
  const pr = unsignedStmt.parsedRecovery!;
  random?.beforeSign?.();
  const stmtSig = ed25519SeedSign(oldRecoverySeed, new Uint8Array(unsignedStmt.data));
  const marshaled = secCall({
    case: "marshalRecoveryStatement",
    value: create(SecMarshalRecoveryStatementCallSchema, {
      account: pr.account,
      domain: new Uint8Array(pr.domain),
      parent: new Uint8Array(pr.parent),
      newAuthorityId: pr.newAuthorityId,
      newRecoveryEd: new Uint8Array(pr.newRecoveryEd),
      newMember: pr.newMember,
      issuedAt: pr.issuedAt,
      stateHash: new Uint8Array(pr.stateHash),
      nonce: new Uint8Array(pr.nonce),
      signature: stmtSig,
    }),
  });
  const mp = marshaled.parsedRecovery!;
  const statement: RecoveryStatement = {
    account: mp.account,
    domain: new Uint8Array(mp.domain),
    parent: new Uint8Array(mp.parent),
    newAuthorityID: mp.newAuthorityId,
    newRecoveryEd: new Uint8Array(mp.newRecoveryEd),
    newMember: {
      id: mp.newMember!.id,
      ed: new Uint8Array(mp.newMember!.edPublic),
      x: new Uint8Array(mp.newMember!.xPublic),
      kind: mp.newMember!.kind as 1 | 2,
    },
    issuedAt: mp.issuedAt,
    stateHash: new Uint8Array(mp.stateHash),
    nonce: new Uint8Array(mp.nonce),
    signature: new Uint8Array(mp.signature),
  };
  await recoveryBytes(statement);
  await verifyRecovery(statement, parent, state);
  return { state, statement };
}

async function memberWithXPublic(ed: B, x: B, kind: 1 | 2): Promise<Member> {
  check(ed.length === 32 && x.length === 32);
  return { id: await deviceID(ed), ed: new Uint8Array(ed), x: new Uint8Array(x), kind };
}
async function memberWithX(ed: B, xPrivate: B, kind: 1 | 2): Promise<Member> {
  check(ed.length === 32 && xPrivate.length === 32);
  return memberWithXPublic(ed, await x25519SeedPublicKey(xPrivate), kind);
}
async function newMember(ed: CryptoKey, x: CryptoKey, kind: 1 | 2): Promise<Member> {
  return memberWithXPublic(new Uint8Array(await crypto.subtle.exportKey("raw", ed)), new Uint8Array(await crypto.subtle.exportKey("raw", x)), kind);
}
async function newRecovery() {
  const seed = crypto.getRandomValues(new Uint8Array(32));
  return { seed, publicKey: ed25519SeedPublic(seed) };
}
function needsRenewal(lease: LeaseCertificate | undefined, now: bigint): boolean {
  if (!lease) return true;
  const at = max(1n, now);
  if (at < lease.issuedAt) return false;
  const remaining = max(0n, lease.notAfter - at);
  return remaining < 24n * 60n * 60n * 1000n && at - lease.issuedAt >= 12n * 60n * 60n * 1000n;
}
export const Authority = { genesis, membership, pair, remove, rotateX, veto, handoff, recovery, sorted, newMember, memberWithX, newRecovery, newSecret: () => crypto.getRandomValues(new Uint8Array(32)), needsRenewal };
