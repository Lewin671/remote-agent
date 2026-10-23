import { execFileSync, spawnSync } from "node:child_process";
import { beforeAll, expect, inject, it } from "vitest";
import { ed25519, x25519 } from "@noble/curves/ed25519.js";
import { Authority, authoritySigningTime } from "./authority";
import { makePairApproval } from "./pair-approval";
import { RecoveryCode } from "./recovery";
import { concat, deviceID, fromHex } from "./crypto";
import {
  b64, unb64, leaseBytes, openGrant, openPrevSecret, parseLease,
  parseState, pinOf, recoveryBytes, sealGrant, sealPrevSecret, signLease,
  signRecoveryStatement, stateBytes, verifyChain, verifyTransition,
  parseRecoveryStatement, coreBytes, hash, type SecurityState,
} from "./device-security";
import { DeviceKind } from "./gen/remoteagent/v1/relay_pb";
import vectors from "./testdata/authority/vectors.json";
type B = Uint8Array<ArrayBuffer>;
const fill = (n: number, b: number): B => new Uint8Array(n).fill(b);
const pub = (b: number): B => new Uint8Array(ed25519.getPublicKey(fill(32, b)));
const xp = (b: number): B => new Uint8Array(x25519.getPublicKey(fill(32, b)));
const now = 1700000000000n;
const random = { grantEkm: () => fill(32, 0x71), prevNonce: fill(12, 0x72), recoveryNonce: fill(32, 0x73) };
const oracle = inject("authorityOracleBinary");
function edKey(b: number) {
  return crypto.subtle.importKey("pkcs8", concat(fromHex("302e020100300506032b657004220420"), fill(32, b)), "Ed25519", false, ["sign"]);
}
function xKey(b: number) {
  return crypto.subtle.importKey("pkcs8", concat(fromHex("302e020100300506032b656e04220420"), fill(32, b)), "X25519", false, ["deriveBits"]);
}
let states: Record<string, SecurityState>, signer: CryptoKey;
let authority: Awaited<ReturnType<typeof Authority.memberWithX>>;
let machine: typeof authority, client: typeof authority, incoming: typeof authority;
let output: typeof vectors, regenerated: typeof vectors, verified: string;
let tamperedStatus: number | null;
beforeAll(async () => {
  signer = await edKey(0x11);
  authority = await Authority.memberWithX(pub(0x11), fill(32, 0x21), 2);
  machine = await Authority.memberWithX(pub(0x12), fill(32, 0x22), 1);
  client = await Authority.memberWithX(pub(0x13), fill(32, 0x23), 2);
  incoming = await Authority.memberWithX(pub(0x14), fill(32, 0x24), 2);
  states = {};
  const s = (b: number) => fill(32, b);
  states.genesis = await Authority.genesis(await deviceID(pub(0x61)), s(0x51), authority, pub(0x31), s(0x41), signer, now, random);
  states.pairMachine = await Authority.pair(states.genesis, machine, s(0x41), s(0x42), signer, now + 1n, random);
  states.pairClient = await Authority.pair(states.pairMachine, client, s(0x42), s(0x43), signer, now + 2n, random);
  states.remove = await Authority.remove(states.pairClient, [machine.id], s(0x43), s(0x44), signer, now + 3n, random);
  states.rotateX = await Authority.rotateX(states.remove, client.id, xp(0x25), s(0x44), s(0x45), signer, 1n, random);
  states.veto = await Authority.veto(states.rotateX, pub(0x32), s(0x45), signer, 1n, random);
  states.handoff = await Authority.handoff(states.veto, client.id, pub(0x33), s(0x45), s(0x46), signer, now + 4n, random);
  const recovered = await Authority.recovery(states.veto, incoming, [{ ...client, x: xp(0x25) }, incoming], s(0x47), await edKey(0x14), s(0x32), pub(0x33), 1n, random);
  states.recovery = recovered.state;
  const lease = await signLease(states.veto, signer, 1n, now + 100n);
  const handoffLease = await signLease(states.handoff, await edKey(0x13), now + 4n);
  const approval = await makePairApproval(states.veto, signer, "0123456789abcdef0123456789abcdef", incoming.ed, DeviceKind.MACHINE, now + 300000n, machine.id, fill(16, 0x74));
  const normalApproval = await makePairApproval(states.veto, signer, "0123456789abcdef0123456789abcdef", incoming.ed, DeviceKind.CLIENT, now + 300000n, "", fill(16, 0x74));
  const serialized: Record<string, string> = {};
  for (const [name, state] of Object.entries(states)) serialized[name] = b64(await stateBytes(state));
  output = { States: serialized as typeof vectors.States, Recovery: b64(await recoveryBytes(recovered.statement)), Lease: b64(leaseBytes(lease)), HandoffLease: b64(leaseBytes(handoffLease)), Approval: b64(approval.approval), ApprovalSignature: b64(approval.signature), NormalApproval: b64(normalApproval.approval), NormalApprovalSignature: b64(normalApproval.signature), RecoveryCode: RecoveryCode.encode(s(0x31), (await pinOf(states.genesis)).genesis) };
  // Cross-language generation and verification are integration fixture work,
  // including subprocess scheduling; test deadlines cover only assertions.
  regenerated = JSON.parse(execFileSync(oracle, ["web"], { encoding: "utf8", timeout: 60000 }));
  const invoke = (v: typeof vectors) => execFileSync(oracle, ["web", "verify"], { input: JSON.stringify(v), encoding: "utf8", stdio: "pipe", timeout: 60000 });
  verified = invoke(output);
  const bad = structuredClone(output);
  const badLease = unb64(bad.Lease); badLease[badLease.length - 1] ^= 1; bad.Lease = b64(badLease);
  const rejected = spawnSync(oracle, ["web", "verify"], { input: JSON.stringify(bad), encoding: "utf8", timeout: 60000 });
  if (rejected.error) throw rejected.error;
  tamperedStatus = rejected.status;
}, 120000);
it("matches regenerated Go bytes for every state, lease, recovery statement and approval", () => {
  expect(regenerated).toEqual(vectors);
  expect(output).toEqual(vectors);
});
it("Go verifies WebCrypto signatures and opens all TS grants and previous secrets", () => {
  expect(verified).toContain("verified:");
  expect(tamperedStatus).toBe(1);
});
it("generic membership matches Go, sorts a copy and leaves the parent intact", async () => {
  const parent = await stateBytes(states.pairClient);
  const members = [client, authority];
  const state = await Authority.membership(states.pairClient, members, [machine.id], fill(32, 0x43), fill(32, 0x44), signer, now + 3n, random);
  expect(b64(await stateBytes(state))).toBe(vectors.States.remove);
  expect(members).toEqual([client, authority]);
  expect(await stateBytes(states.pairClient)).toEqual(parent);
});
it("preserves backward history and tombstones on handoff and cuts history on recovery", async () => {
  const pin = await pinOf(states.genesis);
  const secret = await openGrant(pin, states.handoff, client.id, await xKey(0x25));
  expect(secret).toEqual(fill(32, 0x46));
  expect(await openPrevSecret(secret, states.handoff.prevSecret)).toEqual(fill(32, 0x45));
  expect(states.handoff.core.removed).toContain(authority.id);
  expect(states.handoff.core.removed).toContain(machine.id);
  expect(states.recovery.prevSecret).toHaveLength(0);
  expect(states.veto.core.contentEpoch).toBe(states.rotateX.core.contentEpoch);
});
it("sealGrant and sealPrevSecret match Go fixed-randomness bytes", async () => {
  const g = states.genesis;
  const grant = await sealGrant(authority, g.core, await hash("ra3/core\0", await coreBytes(g.core)), fill(32, 0x41), fill(32, 0x71));
  expect(grant).toEqual((await parseState(unb64(vectors.States.genesis))).grants[0]);
  const padded = new Uint8Array(64); padded.set(authority.x, 16);
  const view = { ...authority, x: padded.subarray(16, 48) };
  expect(await sealGrant(view, g.core, await hash("ra3/core\0", await coreBytes(g.core)), fill(32, 0x41), fill(32, 0x71))).toEqual(grant);
  expect(await sealPrevSecret(fill(32, 0x42), fill(32, 0x41), fill(12, 0x72))).toEqual(states.pairMachine.prevSecret);
});
it("production randomness produces valid independent grants and chains", async () => {
  const g = await Authority.genesis(await deviceID(pub(0x61)), fill(32, 0x51), authority, pub(0x31), fill(32, 0x41), signer, now);
  const next = await Authority.pair(g, client, fill(32, 0x41), fill(32, 0x42), signer, now);
  const pin = await pinOf(g);
  await verifyChain(pin, [g, next]);
  expect(await openGrant(pin, next, client.id, await xKey(0x23))).toEqual(fill(32, 0x42));
  expect(next.grants[0].enc).not.toEqual(states.pairClient.grants[0].enc);
});
it("rejects Machine as genesis, handoff and recovery authority or recovery statement author", async () => {
  await expect(Authority.genesis(await deviceID(pub(0x61)), fill(32, 0x51), machine, pub(0x31), fill(32, 0x41), await edKey(0x12), now)).rejects.toThrow();
  await expect(Authority.handoff(states.pairMachine, machine.id, pub(0x32), fill(32, 0x42), fill(32, 0x43), signer, now)).rejects.toThrow();
  await expect(Authority.recovery(states.pairMachine, machine, [machine], fill(32, 0x43), await edKey(0x12), fill(32, 0x31), pub(0x32), now)).rejects.toThrow();
  const r = await parseRecoveryStatement(unb64(output.Recovery));
  await expect(signRecoveryStatement({ ...r, newMember: machine, newAuthorityID: machine.id }, fill(32, 0x32))).rejects.toThrow();
  const bad = { ...states.genesis, core: { ...states.genesis.core, authorityID: machine.id, members: [machine] } };
  await expect(signLease(bad, await edKey(0x12), now)).rejects.toThrow();
});
it("rejects wrong signers, illegal deltas, no-op rotations and retired identities", async () => {
  await expect(Authority.pair(states.genesis, client, fill(32, 0x41), fill(32, 0x42), await edKey(0x12), now)).rejects.toThrow();
  await expect(signLease(states.handoff, signer, now)).rejects.toThrow();
  await expect(Authority.remove(states.pairClient, [authority.id], fill(32, 0x43), fill(32, 0x44), signer, now)).rejects.toThrow();
  await expect(Authority.pair(states.remove, machine, fill(32, 0x44), fill(32, 0x45), signer, now)).rejects.toThrow();
  await expect(Authority.rotateX(states.remove, client.id, client.x, fill(32, 0x44), fill(32, 0x45), signer, now)).rejects.toThrow();
  await expect(Authority.veto(states.veto, pub(0x32), fill(32, 0x45), signer, now)).rejects.toThrow();
  await expect(Authority.membership(states.pairClient, [authority], [], fill(32, 0x43), fill(32, 0x44), signer, now)).rejects.toThrow();
  await expect(Authority.recovery(states.veto, incoming, [incoming], fill(32, 0x47), await edKey(0x14), fill(32, 0x32), pub(0x33), now)).rejects.toThrow();
  await expect(Authority.recovery(states.veto, incoming, [{ ...client, x: xp(0x25) }, incoming], fill(32, 0x47), await edKey(0x14), fill(32, 0x31), pub(0x33), now)).rejects.toThrow();
});
it("honors Android signing time floors and renewal thresholds", () => {
  const lease = parseLease(unb64(output.Lease));
  expect(authoritySigningTime(states.veto, 0n, now + 90n, lease)).toBe(now + 100n);
  expect(states.rotateX.core.issuedAt).toBe(states.remove.core.issuedAt);
  expect(states.recovery.core.issuedAt).toBe(states.veto.core.issuedAt);
  expect(Authority.needsRenewal(undefined, now)).toBe(true);
  expect(Authority.needsRenewal(lease, lease.issuedAt - 1n)).toBe(false);
  expect(Authority.needsRenewal(lease, lease.notAfter - 24n * 3600000n)).toBe(false);
  expect(Authority.needsRenewal(lease, lease.notAfter - 24n * 3600000n + 1n)).toBe(true);
  expect(Authority.needsRenewal({ ...lease, notAfter: lease.issuedAt + 100n }, lease.issuedAt + 12n * 3600000n - 1n)).toBe(false);
  expect(Authority.needsRenewal({ ...lease, notAfter: lease.issuedAt + 100n }, lease.issuedAt + 12n * 3600000n)).toBe(true);
});
it("matches Android recovery-code alphabet, lookalikes, prefix and padding", async () => {
  const pin = await pinOf(states.genesis);
  expect(RecoveryCode.encode(fill(32, 0x31), pin.genesis)).toBe(vectors.RecoveryCode);
  const input = vectors.RecoveryCode.toLowerCase().replaceAll("0", "o").replaceAll("1", "l").replaceAll("-", " \n");
  expect(RecoveryCode.decode(input)).toEqual({ seed: fill(32, 0x31), genesis: pin.genesis.slice(0, 10) });
  expect(RecoveryCode.decode("O".repeat(68))).toEqual({ seed: fill(32, 0), genesis: fill(10, 0) });
  for (const bad of ["", vectors.RecoveryCode + "0", vectors.RecoveryCode.slice(0, -1), vectors.RecoveryCode.slice(0, -1) + "1", "U".repeat(68)]) expect(RecoveryCode.decode(bad)).toBeUndefined();
});
it("rejects unauthorized replacement approvals and tampered signatures", async () => {
  const id = "0123456789abcdef0123456789abcdef";
  await expect(makePairApproval(states.veto, signer, id, incoming.ed, DeviceKind.CLIENT, now, machine.id)).rejects.toThrow();
  await expect(makePairApproval(states.veto, signer, "bad", incoming.ed, DeviceKind.CLIENT, now)).rejects.toThrow();
  await expect(makePairApproval(states.veto, await edKey(0x12), id, incoming.ed, DeviceKind.CLIENT, now)).rejects.toThrow();
  const altered = structuredClone(states.handoff); altered.signature[0] ^= 1;
  await expect(verifyTransition(states.veto, altered)).rejects.toThrow();
});
