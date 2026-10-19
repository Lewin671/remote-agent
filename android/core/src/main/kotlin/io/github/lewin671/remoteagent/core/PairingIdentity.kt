package io.github.lewin671.remoteagent.core

/** Long-term candidate keys; ephemeral SAS keys deliberately stay in memory. */
class PairingIdentity(
    val server: String,
    val pairingId: String,
    val seed: ByteArray,
    val x: ByteArray,
) {
    init {
        require(seed.size == 32 && x.size == 32)
    }

    override fun toString() = "PairingIdentity"

    fun bytes(): ByteArray = operationMaterial { s ->
        s.writeInt(1)
        s.writeUTF(server)
        s.writeUTF(pairingId)
        s.blob(seed)
        s.blob(x)
    }

    companion object {
        fun parse(raw: ByteArray): PairingIdentity =
            readOperationMaterial(raw) { s ->
                PairingIdentity(s.readUTF(), s.readUTF(), s.blob(), s.blob())
            }
    }
}
