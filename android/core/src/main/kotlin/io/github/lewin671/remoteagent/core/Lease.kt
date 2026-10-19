package io.github.lewin671.remoteagent.core

/** A signed lease renews availability without changing membership history. */
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
                notAfter > issuedAt &&
                notAfter - issuedAt <= DeviceSecurity.LEASE_U,
        )
        return DeviceSecurity.Writer()
            .raw("ra3/lease\u0000".toByteArray())
            .raw(domain)
            .raw(head)
            .u64(issuedAt)
            .u64(notAfter)
            .bytes()
    }

    fun bytes(): ByteArray {
        require(signature.size == 64)
        return DeviceSecurity.Writer().lp(signBytes()).lp(signature).bytes()
    }

    fun verify(state: DeviceSecurity.State) {
        val m =
            state.core.member(state.core.authorityID) ?: throw DeviceSecurity.Failure("untrusted")
        if (
            !domain.contentEquals(state.core.domain) ||
                !head.contentEquals(DeviceSecurity.stateHash(state)) ||
                issuedAt < state.core.issuedAt ||
                !verifyEd25519(m.ed, signBytes(), signature)
        )
            throw DeviceSecurity.Failure("untrusted")
    }

    fun valid(state: DeviceSecurity.State, now: ULong) {
        verify(state)
        if (issuedAt > now + DeviceSecurity.MAX_CLOCK_SKEW) throw DeviceSecurity.Failure("clock")
        if (now >= notAfter) throw DeviceSecurity.Failure("lease")
    }

    companion object {
        fun parse(b: ByteArray): HeadLease {
            require(b.size <= 256)
            val r = DeviceSecurity.Reader(b)
            val body = r.lp(128)
            val sig = r.lp(64)
            r.done()
            val d = DeviceSecurity.Reader(body)
            require(d.raw(10).contentEquals("ra3/lease\u0000".toByteArray()))
            val l = HeadLease(d.raw(32), d.raw(32), d.u64(), d.u64(), sig)
            d.done()
            require(l.bytes().contentEquals(b))
            return l
        }

        fun sign(state: DeviceSecurity.State, signer: SigningKey, now: Long): HeadLease {
            require(state.core.authorityID == deviceId(signer.publicKey))
            val l =
                HeadLease(
                    state.core.domain,
                    DeviceSecurity.stateHash(state),
                    now.toULong(),
                    (now + DeviceSecurity.LEASE).toULong(),
                    byteArrayOf(),
                )
            return l.copy(signature = signer.sign(l.signBytes()))
        }
    }
}
