import { fromBinary } from "@bufbuild/protobuf";
import { PairApprovalSchema, DeviceKind } from "./gen/remoteagent/v1/relay_pb";
import type { MachineReplacementProof } from "./gen/remoteagent/v1/local_pb";
import type { SecurityRecord } from "./device-security-store";
import {
  SecurityError,
  equal,
  MAX_PAIR_STATES,
  MAX_CLOCK_SKEW,
} from "./device-security";
import { deviceID, concat, utf8 } from "./crypto";

// A historical-origin proof never authorizes live control as the retired ID.
export async function machineReplacementOrigins(
  record: SecurityRecord,
  target: string,
  proofs: MachineReplacementProof[],
) {
  const check = (ok: unknown) => {
    if (!ok) throw new SecurityError("untrusted");
  };
  check(proofs.length <= MAX_PAIR_STATES);
  const origins = new Set<string>();
  let cursor = target;
  for (const p of [...proofs].reverse()) {
    const a = fromBinary(PairApprovalSchema, p.approval);
    check(
      p.approval.length <= 1024 &&
        a.accountId === record.pin.account &&
        a.candidateKind === DeviceKind.MACHINE &&
        a.replacesDeviceId &&
        (await deviceID(a.candidateDevicePublicKey)) === cursor &&
        a.nonce.length === 16 &&
        /^[0-9a-f]{32}$/.test(a.pairingId) &&
        (await deviceID(a.approverPublicKey)) === a.approverDeviceId,
    );
    const first = record.states.find((s) =>
      s.core.members.some((m) => m.id === cursor),
    );
    check(first);
    const member = first!.core.members.find((m) => m.id === cursor),
      signer = first!.core.members.find((m) => m.id === a.approverDeviceId);
    check(
      member?.kind === 1 &&
        equal(member.ed, a.candidateDevicePublicKey) &&
        first!.core.authorityID === a.approverDeviceId &&
        signer &&
        equal(signer.ed, a.approverPublicKey) &&
        first!.core.removed.includes(a.replacesDeviceId) &&
        a.expiresMs > 0n &&
        a.expiresMs + BigInt(MAX_CLOCK_SKEW) >= first!.core.issuedAt,
    );
    check(
      record.states.some(
        (s) =>
          s.core.epoch < first!.core.epoch &&
          s.core.members.some(
            (m) => m.id === a.replacesDeviceId && m.kind === 1,
          ),
      ),
    );
    const key = await crypto.subtle.importKey(
      "raw",
      new Uint8Array(a.approverPublicKey),
      "Ed25519",
      false,
      ["verify"],
    );
    check(
      await crypto.subtle.verify(
        "Ed25519",
        key,
        new Uint8Array(p.signature),
        concat(
          utf8("remote-agent/v1/pair-approval\0"),
          new Uint8Array(p.approval),
        ),
      ),
    );
    check(!origins.has(a.replacesDeviceId));
    origins.add(a.replacesDeviceId);
    cursor = a.replacesDeviceId;
  }
  return origins;
}
