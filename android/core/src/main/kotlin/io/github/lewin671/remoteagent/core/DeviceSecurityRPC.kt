package io.github.lewin671.remoteagent.core

/** Pairing trust descriptor & domain commitment helpers delegated to Go `clientcore` `SecCall`. */
object DeviceSecurityRPC {
    fun pairTrustDescriptor(pin: DeviceSecurity.Pin, approver: String): ByteArray =
        SecCall.pairTrustDescriptor(pin, approver)

    fun parsePairTrustDescriptor(b: ByteArray): Pair<DeviceSecurity.Pin, String> =
        SecCall.parsePairTrustDescriptor(b)

    fun pairDomainCommit(initiatorPublic: ByteArray, trustDescriptor: ByteArray): ByteArray =
        SecCall.pairDomainCommit(initiatorPublic, trustDescriptor)
}
