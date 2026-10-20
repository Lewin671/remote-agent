// Signing only; the foreground pairing coordinator confirms SAS and durably
// stores the approval, successor and delivery request before any upload.
import { create, toBinary } from "@bufbuild/protobuf";
import { concat, deviceID, utf8 } from "./crypto";
import { SecurityError, verifyPublicSignature, type SecurityState } from "./device-security";
import { PairApprovalSchema, DeviceKind } from "./gen/remoteagent/v1/relay_pb";
type B = Uint8Array<ArrayBuffer>;
const id = (s: string) => /^[0-9a-f]{32}$/.test(s);
export async function makePairApproval(
  head: SecurityState,
  signer: CryptoKey,
  pairingId: string,
  candidate: B,
  kind: DeviceKind.CLIENT | DeviceKind.MACHINE,
  expires: bigint,
  replacesDeviceId = "",
  nonce = crypto.getRandomValues(new Uint8Array(16)),
  beforeSign?: () => void,
): Promise<{ approval: B; signature: B }> {
  const authority = head.core.members.find((m) => m.id === head.core.authorityID);
  if (!authority || authority.kind !== 2 || !id(head.core.account) ||
      !id(pairingId) || candidate.length !== 32 || nonce.length !== 16 ||
      expires <= 0n || expires > 0x7fffffffffffffffn ||
      (kind !== DeviceKind.CLIENT && kind !== DeviceKind.MACHINE) ||
      await deviceID(candidate) === authority.id ||
      (replacesDeviceId !== "" && (!id(replacesDeviceId) ||
        kind !== DeviceKind.MACHINE || replacesDeviceId === await deviceID(candidate) ||
        !head.core.removed.includes(replacesDeviceId)))) throw new SecurityError();
  const approval = toBinary(PairApprovalSchema, create(PairApprovalSchema, {
    accountId: head.core.account, pairingId, approverDeviceId: authority.id,
    approverPublicKey: authority.ed, candidateDevicePublicKey: candidate,
    candidateKind: kind, expiresMs: expires, nonce, replacesDeviceId,
  }));
  if (approval.length > 1024) throw new SecurityError("capacity");
  const message = concat(utf8("remote-agent/v1/pair-approval\0"), approval);
  beforeSign?.();
  const signature = new Uint8Array(await crypto.subtle.sign("Ed25519", signer, message));
  await verifyPublicSignature(authority.ed, signature, message);
  return { approval, signature };
}
