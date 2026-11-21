import { deviceID, hex, utf8 } from "../crypto";
import { equal, validLease } from "../device-security";
import type { SecurityRecord } from "../device-security-store";
import type { DirectSecuritySnapshotInput } from "../core/host";

const DIRECT_SELF_CHECK_PROBE = utf8("remote-agent/v1/direct-self-check\0");

export async function buildDirectSecuritySnapshot(params: {
  record: SecurityRecord;
  started: number;
  deviceId: string;
  devicePrivate: CryptoKey;
  nowMs?: number;
}): Promise<{ snapshot: DirectSecuritySnapshotInput; xPrivate: CryptoKey } | undefined> {
  const { record, started, deviceId, devicePrivate } = params;
  const nowMs = params.nowMs ?? Date.now();
  if (!record.member || record.memberID !== deviceId || nowMs >= started + 20_000 || record.states.length === 0) {
    return undefined;
  }
  const head = record.states[record.states.length - 1];
  const selfMember = head.core.members.find((m) => m.id === record.memberID);
  if (!selfMember || selfMember.ed.length !== 32 || selfMember.x.length !== 32) {
    return undefined;
  }
  try {
    await validLease(record.lease, head, BigInt(Math.max(nowMs, Number(record.floor))));
    if ((await deviceID(selfMember.ed)) !== deviceId) {
      return undefined;
    }
    const edKey = await crypto.subtle.importKey("raw", selfMember.ed, "Ed25519", false, ["verify"]);
    const sig = new Uint8Array(await crypto.subtle.sign("Ed25519", devicePrivate, DIRECT_SELF_CHECK_PROBE));
    const edOK = await crypto.subtle.verify("Ed25519", edKey, sig, DIRECT_SELF_CHECK_PROBE);
    if (!edOK) {
      return undefined;
    }
    const eph = (await crypto.subtle.generateKey({ name: "X25519" }, false, ["deriveBits"])) as CryptoKeyPair;
    const selfXKey = await crypto.subtle.importKey("raw", selfMember.x, { name: "X25519" }, false, []);
    const a = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: eph.publicKey }, record.xPrivate, 256));
    const b = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: selfXKey }, eph.privateKey, 256));
    if (!equal(a, b)) {
      return undefined;
    }
  } catch {
    return undefined;
  }
  const snapshot: DirectSecuritySnapshotInput = {
    member: true,
    account: record.pin.account,
    domain: new Uint8Array(record.pin.domain),
    head: hex(record.stateHash),
    headHash: new Uint8Array(record.stateHash),
    epoch: head.core.epoch,
    startedAtMs: BigInt(started),
    leaseNotAfterMs: record.lease?.notAfter ?? 0n,
    selfId: selfMember.id,
    selfEdPublic: new Uint8Array(selfMember.ed),
    selfXPublic: new Uint8Array(selfMember.x),
    members: head.core.members.map((m) => ({
      deviceId: m.id,
      edPublic: new Uint8Array(m.ed),
      xPublic: new Uint8Array(m.x),
      isSelf: m.id === record.memberID,
      kind: m.kind,
    })),
    rejected: false,
  };
  return { snapshot, xPrivate: record.xPrivate };
}
