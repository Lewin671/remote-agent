package io.github.lewin671.remoteagent.core

/** Lease certificate attesting head state freshness, backed by Go `clientcore` `SecCall`. */
data class HeadLease(
    val domain: ByteArray,
    val head: ByteArray,
    val issuedAt: ULong,
    val notAfter: ULong,
    val signature: ByteArray,
) {
    fun signBytes(): ByteArray {
        require(
            domain.size == 32 &&
                domain.any { it != 0.toByte() } &&
                head.size == 32 &&
                head.any { it != 0.toByte() } &&
                issuedAt > 0uL &&
                notAfter > issuedAt,
        )
        return DeviceSecurity.Writer()
            .raw("ra3/lease\u0000".toByteArray())
            .raw(domain)
            .raw(head)
            .u64(issuedAt)
            .u64(notAfter)
            .bytes()
    }

    fun bytes(): ByteArray =
        SecCall.marshalLease(
            domain = domain,
            headHash = head,
            issuedAt = issuedAt.toLong(),
            notAfter = notAfter.toLong(),
            signature = signature,
        ).first

    fun verify(state: DeviceSecurity.State) {
        SecCall.verifyLease(bytes(), DeviceSecurity.stateBytes(state), 0L, false)
    }

    fun valid(state: DeviceSecurity.State, now: ULong) {
        SecCall.verifyLease(bytes(), DeviceSecurity.stateBytes(state), now.toLong(), true)
    }

    companion object {
        fun parse(b: ByteArray): HeadLease = SecCall.parseLease(b)

        fun sign(state: DeviceSecurity.State, signer: SigningKey, now: Long): HeadLease {
            val headBytes = DeviceSecurity.stateBytes(state)
            val (unsigned, signBytes) = SecCall.newLeaseUnsigned(headBytes, now)
            val sig = signer.sign(signBytes)
            return SecCall.marshalLease(
                domain = unsigned.domain.toByteArray(),
                headHash = unsigned.head.toByteArray(),
                issuedAt = unsigned.issuedAt,
                notAfter = unsigned.notAfter,
                signature = sig,
            ).second
        }
    }
}
