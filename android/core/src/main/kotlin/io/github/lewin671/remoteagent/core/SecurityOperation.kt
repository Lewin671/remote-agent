package io.github.lewin671.remoteagent.core

/**
 * One durable publication slot shared by every security flow. Secret code seeds are never stored
 * here; local material contains only completion keys.
 */
class SecurityOperation(
    val kind: String,
    val parent: ByteArray,
    val signed: ByteArray,
    val local: ByteArray = byteArrayOf(),
    val delivery: ByteArray = byteArrayOf(),
    val phase: Phase = Phase.PREPARED,
    val codeSaved: Boolean = true,
) {
    enum class Phase {
        PREPARED,
        PUBLISHED,
        DELIVERED,
    }

    override fun toString() = "SecurityOperation($kind,$phase)"

    fun bytes(): ByteArray =
        DeviceSecurity.Writer()
            .u32(1u)
            .str(kind)
            .lp(parent)
            .lp(signed)
            .lp(local)
            .lp(delivery)
            .u32(phase.ordinal.toUInt())
            .u32(if (codeSaved) 1u else 0u)
            .bytes()

    fun advanced(phase: Phase) =
        SecurityOperation(kind, parent, signed, local, delivery, phase, codeSaved)

    companion object {
        fun parse(b: ByteArray): SecurityOperation {
            val r = DeviceSecurity.Reader(b)
            require(r.u32() == 1u)
            val kind = r.str()
            val parent = r.lp(32)
            require(parent.isEmpty() || parent.size == 32)
            val op =
                SecurityOperation(
                    kind,
                    parent,
                    r.lp(16 shl 20),
                    r.lp(16 shl 20),
                    r.lp(1 shl 20),
                    Phase.entries[r.u32().toInt()],
                    r.u32() == 1u,
                )
            r.done()
            return op
        }
    }
}
