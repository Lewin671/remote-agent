package io.github.lewin671.remoteagent.core

/**
 * Public pairing helpers left after the protocol-9 capsule RPC was removed. The descriptor binds
 * the immutable genesis anchor and the approver, not a mutable authority key; the SAS commits to it
 * before the candidate reveals its ephemeral key.
 */
object DeviceSecurityRPC {
    private fun check(ok: Boolean, code: String = "invalid") {
        if (!ok) throw DeviceSecurity.Failure(code)
    }

    private fun id(s: String) = s.matches(Regex("[0-9a-f]{32}"))

    private fun nonzero(b: ByteArray) = check(b.size == 32 && b.any { it != 0.toByte() })

    fun pairTrustDescriptor(p: DeviceSecurity.Pin, approver: String): ByteArray {
        check(id(p.account) && id(approver))
        nonzero(p.domain)
        nonzero(p.genesis)
        return DeviceSecurity.Writer()
            .raw("ra3/pair-trust\u0000".toByteArray())
            .str(p.account)
            .raw(p.domain)
            .raw(p.genesis)
            .str(approver)
            .bytes()
    }

    fun parsePairTrustDescriptor(raw: ByteArray): Pair<DeviceSecurity.Pin, String> {
        val label = "ra3/pair-trust\u0000".toByteArray()
        check(raw.size in label.size + 100..4096 && raw.copyOf(label.size).contentEquals(label))
        val r = DeviceSecurity.Reader(raw.copyOfRange(label.size, raw.size))
        val pin = DeviceSecurity.Pin(r.str(), r.raw(32), r.raw(32))
        val approver = r.str()
        r.done()
        check(id(pin.account) && id(approver))
        nonzero(pin.domain)
        nonzero(pin.genesis)
        return pin to approver
    }

    fun pairDomainCommit(ephemeral: ByteArray, trust: ByteArray): ByteArray {
        check(ephemeral.size == 32 && trust.isNotEmpty())
        return sha256(
            "ra3/pair-commit\u0000".toByteArray(),
            DeviceSecurity.Writer().lp(ephemeral).lp(trust).bytes(),
        )
    }
}
