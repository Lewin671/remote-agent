// Pure signing core. Callers must hold the foreground operation lock and
// durably save the exact result before publishing it (no RPCs in this module).
import { deviceID } from "./crypto";
import {
  SecurityError, hash, coreBytes, stateHash, stateBytes, stateSignBytes,
  pinOf, verifyGenesis, verifyTransition, sealGrant, sealPrevSecret,
  signRecoveryStatement, type Core, type Member, type SecurityState,
  type LeaseCertificate,
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
async function build(core: Core, secret: B, prevSecret: B, signer: CryptoKey, random?: AuthorityRandomness): Promise<SecurityState> {
  check(secret.length === 32);
  const ch = await hash("ra3/core\0", await coreBytes(core));
  const grants = [];
  for (const member of core.members)
    grants.push(await sealGrant(member, core, ch, secret, random?.grantEkm?.(member)));
  const state: SecurityState = { core, grants, prevSecret, signature: new Uint8Array() };
  const message = await stateSignBytes(state);
  random?.beforeSign?.();
  state.signature = new Uint8Array(await crypto.subtle.sign("Ed25519", signer, message));
  await stateBytes(state); // Capacity and canonical encoding before returning any result.
  return state;
}
async function successor(parent: SecurityState, now: bigint): Promise<Core> {
  return { ...parent.core, parent: await stateHash(parent), epoch: parent.core.epoch + 1n, issuedAt: max(1n, now, parent.core.issuedAt) };
}
async function genesis(account: string, domain: B, member: Member, recoveryEd: B, secret: B, signer: CryptoKey, now: bigint, random?: AuthorityRandomness) {
  check(member.kind === 2);
  const core: Core = { account, domain, authorityID: member.id, recoveryEd, parent: new Uint8Array(32), epoch: 1n, contentEpoch: 1n, issuedAt: max(1n, now), members: [member], removed: [] };
  const state = await build(core, secret, new Uint8Array(), signer, random);
  await verifyGenesis(await pinOf(state), state);
  return state;
}
async function membership(parent: SecurityState, members: Member[], removed: string[], oldSecret: B, newSecret: B, signer: CryptoKey, now: bigint, random?: AuthorityRandomness) {
  const core = { ...await successor(parent, now), contentEpoch: parent.core.contentEpoch + 1n, members: sorted(members), removed: [...removed].sort() };
  const state = await build(core, newSecret, await sealPrevSecret(newSecret, oldSecret, random?.prevNonce), signer, random);
  await verifyTransition(parent, state);
  return state;
}
async function pair(parent: SecurityState, candidate: Member, oldSecret: B, newSecret: B, signer: CryptoKey, now: bigint, random?: AuthorityRandomness) {
  check(!parent.core.members.some((m) => m.id === candidate.id) && !parent.core.removed.includes(candidate.id));
  return membership(parent, [...parent.core.members, candidate], parent.core.removed, oldSecret, newSecret, signer, now, random);
}
async function remove(parent: SecurityState, targets: string[], oldSecret: B, newSecret: B, signer: CryptoKey, now: bigint, random?: AuthorityRandomness) {
  check(targets.length > 0 && targets.every((id) => id !== parent.core.authorityID && parent.core.members.some((m) => m.id === id)));
  return membership(parent, parent.core.members.filter((m) => !targets.includes(m.id)), [...new Set([...parent.core.removed, ...targets])], oldSecret, newSecret, signer, now, random);
}
async function rotateX(parent: SecurityState, target: string, newXPublic: B, oldSecret: B, newSecret: B, signer: CryptoKey, now: bigint, random?: AuthorityRandomness) {
  check(newXPublic.length === 32 && parent.core.members.some((m) => m.id === target));
  return membership(parent, parent.core.members.map((m) => m.id === target ? { ...m, x: new Uint8Array(newXPublic) } : m), parent.core.removed, oldSecret, newSecret, signer, now, random);
}
async function veto(parent: SecurityState, newRecoveryEd: B, secret: B, signer: CryptoKey, now: bigint, random?: AuthorityRandomness) {
  const state = await build({ ...await successor(parent, now), recoveryEd: newRecoveryEd }, secret, new Uint8Array(), signer, random);
  await verifyTransition(parent, state);
  return state;
}
async function handoff(parent: SecurityState, incoming: string, recoveryEd: B, oldSecret: B, newSecret: B, signer: CryptoKey, now: bigint, random?: AuthorityRandomness) {
  check(incoming !== parent.core.authorityID && parent.core.members.some((m) => m.id === incoming && m.kind === 2));
  const core = { ...await successor(parent, now), contentEpoch: parent.core.contentEpoch + 1n, authorityID: incoming, recoveryEd, members: parent.core.members.filter((m) => m.id !== parent.core.authorityID), removed: [...parent.core.removed, parent.core.authorityID].sort() };
  const state = await build(core, newSecret, await sealPrevSecret(newSecret, oldSecret, random?.prevNonce), signer, random);
  await verifyTransition(parent, state);
  return state;
}
async function recovery(parent: SecurityState, newMember: Member, members: Member[], secret: B, newSigner: CryptoKey, oldRecoverySeed: B, newRecoveryEd: B, now: bigint, random?: AuthorityRandomness) {
  check(newMember.kind === 2 && !members.some((m) => m.id === parent.core.authorityID));
  const core = { ...await successor(parent, now), contentEpoch: parent.core.contentEpoch + 1n, authorityID: newMember.id, recoveryEd: newRecoveryEd, members: sorted(members), removed: [...new Set([...parent.core.removed, parent.core.authorityID])].sort() };
  const state = await build(core, secret, new Uint8Array(), newSigner, random);
  const statement = await signRecoveryStatement({ account: core.account, domain: core.domain, parent: core.parent, newAuthorityID: newMember.id, newRecoveryEd, newMember, issuedAt: core.issuedAt, stateHash: await stateHash(state), nonce: random?.recoveryNonce ?? crypto.getRandomValues(new Uint8Array(32)), signature: new Uint8Array() }, oldRecoverySeed, random?.beforeSign);
  await verifyTransition(parent, state, statement);
  return { state, statement };
}
async function memberWithXPublic(ed: B, x: B, kind: 1 | 2): Promise<Member> {
  check(ed.length === 32 && x.length === 32);
  return { id: await deviceID(ed), ed: new Uint8Array(ed), x: new Uint8Array(x), kind };
}
async function memberWithX(ed: B, xPrivate: B, kind: 1 | 2): Promise<Member> {
  check(ed.length === 32 && xPrivate.length === 32);
  const { x25519 } = await import("@noble/curves/ed25519.js");
  return memberWithXPublic(ed, new Uint8Array(x25519.getPublicKey(xPrivate)), kind);
}
async function newMember(ed: CryptoKey, x: CryptoKey, kind: 1 | 2): Promise<Member> {
  return memberWithXPublic(new Uint8Array(await crypto.subtle.exportKey("raw", ed)), new Uint8Array(await crypto.subtle.exportKey("raw", x)), kind);
}
async function newRecovery() {
  const seed = crypto.getRandomValues(new Uint8Array(32));
  const { ed25519 } = await import("@noble/curves/ed25519.js");
  return { seed, publicKey: new Uint8Array(ed25519.getPublicKey(seed)) };
}
function needsRenewal(lease: LeaseCertificate | undefined, now: bigint): boolean {
  if (!lease) return true;
  const at = max(1n, now);
  if (at < lease.issuedAt) return false;
  const remaining = max(0n, lease.notAfter - at);
  return remaining < 24n * 60n * 60n * 1000n && at - lease.issuedAt >= 12n * 60n * 60n * 1000n;
}
export const Authority = { genesis, membership, pair, remove, rotateX, veto, handoff, recovery, sorted, newMember, memberWithX, newRecovery, newSecret: () => crypto.getRandomValues(new Uint8Array(32)), needsRenewal };
