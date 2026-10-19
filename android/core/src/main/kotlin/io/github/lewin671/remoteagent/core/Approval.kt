package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.PairApproval

private val approvalContext = "remote-agent/v1/pair-approval\u0000".toByteArray()

/** Only the pairing flow supplies this; a root secret is not an admission. */
class Enrollment(
    val seed: ByteArray,
    val pairingId: String,
    val approval: ByteArray,
    val signature: ByteArray,
)

fun makePairApproval(
    account: Account,
    pairingId: String,
    candidate: ByteArray,
    kind: DeviceKind,
    expires: Long,
    replacesDeviceId: String = "",
): Pair<ByteArray, ByteArray> {
    val signer = SigningKey(account.deviceSeed)
    val acct = account.accountId.ifEmpty { deviceId(Keys.derive(account.root).account.publicKey) }
    val raw =
        PairApproval.newBuilder()
            .setAccountId(acct)
            .setPairingId(pairingId)
            .setApproverDeviceId(account.deviceId)
            .setApproverPublicKey(ByteString.copyFrom(signer.publicKey))
            .setCandidateDevicePublicKey(ByteString.copyFrom(candidate))
            .setCandidateKind(kind)
            .setReplacesDeviceId(replacesDeviceId)
            .setExpiresMs(expires)
            .setNonce(ByteString.copyFrom(randomBytes(16)))
            .build()
            .toByteArray()
    return raw to signer.sign(approvalContext + raw)
}

fun verifyPairApproval(enrollment: Enrollment, root: ByteArray, kind: DeviceKind, now: Long) {
    val a = PairApproval.parseFrom(enrollment.approval)
    val pub = SigningKey(enrollment.seed).publicKey
    require(
        enrollment.approval.size <= 1024 &&
            a.accountId == deviceId(Keys.derive(root).account.publicKey) &&
            a.pairingId == enrollment.pairingId &&
            a.candidateDevicePublicKey.toByteArray().contentEquals(pub) &&
            a.candidateKind == kind &&
            a.replacesDeviceId.isEmpty() &&
            a.nonce.size() == 16 &&
            a.expiresMs > now &&
            a.approverPublicKey.size() == 32 &&
            deviceId(a.approverPublicKey.toByteArray()) == a.approverDeviceId &&
            a.approverDeviceId != deviceId(pub) &&
            verifyEd25519(
                a.approverPublicKey.toByteArray(),
                approvalContext + enrollment.approval,
                enrollment.signature,
            ),
    ) {
        "配对批准与这台设备不匹配、已过期或签名无效"
    }
}
