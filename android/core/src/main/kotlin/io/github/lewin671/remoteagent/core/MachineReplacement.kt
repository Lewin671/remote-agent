package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.MachineReplacementProof
import io.github.lewin671.remoteagent.proto.PairApproval

/** Verified historical origins only; never a grant to send controls as an old device. */
fun machineReplacementOrigins(
    record: DeviceSecurity.Record,
    target: String,
    proofs: List<MachineReplacementProof>,
): Set<String> {
    fun requireProof(ok: Boolean) {
        if (!ok) throw DeviceSecurity.Failure("untrusted")
    }
    requireProof(proofs.size <= DeviceSecurity.MAX_PAIR_STATES)
    var cursor = target
    val origins = mutableSetOf<String>()
    for (proof in proofs.asReversed()) {
        val raw = proof.approval.toByteArray()
        val a = PairApproval.parseFrom(raw)
        requireProof(
            raw.size <= 1024 &&
                a.accountId == record.pin.account &&
                a.candidateKind == DeviceKind.DEVICE_KIND_MACHINE &&
                a.replacesDeviceId.isNotEmpty() &&
                deviceId(a.candidateDevicePublicKey.toByteArray()) == cursor &&
                a.nonce.size() == 16 &&
                a.expiresMs > 0 &&
                Regex("[0-9a-f]{32}").matches(a.pairingId) &&
                deviceId(a.approverPublicKey.toByteArray()) == a.approverDeviceId,
        )
        val first =
            record.chain.firstOrNull { it.core.member(cursor) != null }
                ?: throw DeviceSecurity.Failure("untrusted")
        requireProof(
            first.core
                .member(cursor)
                ?.ed
                ?.contentEquals(a.candidateDevicePublicKey.toByteArray()) == true &&
                first.core.member(cursor)?.kind == 1u &&
                first.core.authorityID == a.approverDeviceId &&
                first.core
                    .member(a.approverDeviceId)
                    ?.ed
                    ?.contentEquals(a.approverPublicKey.toByteArray()) == true &&
                a.replacesDeviceId in first.core.removed &&
                a.expiresMs.toULong() + DeviceSecurity.MAX_CLOCK_SKEW >= first.core.issuedAt,
        )
        requireProof(
            record.chain.any {
                it.core.epoch < first.core.epoch && it.core.member(a.replacesDeviceId)?.kind == 1u
            },
        )
        requireProof(
            verifyEd25519(
                a.approverPublicKey.toByteArray(),
                "remote-agent/v1/pair-approval\u0000".toByteArray() + raw,
                proof.signature.toByteArray(),
            ),
        )
        requireProof(origins.add(a.replacesDeviceId))
        cursor = a.replacesDeviceId
    }
    return origins
}
