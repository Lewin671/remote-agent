package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.corebind.Corebind

/** Ephemeral X25519 key pair for QR pairing, backed by Go `corebind` + `SecCall`. */
class PairKey(privateKey: ByteArray = randomBytes(KEY_SIZE)) {
    private val priv = privateKey.copyOf().also { require(it.size == KEY_SIZE) }
    val public: ByteArray = Corebind.x25519PublicKey(priv)

    /** Shared secrets bound to the pairing transcript via Go `clientcore` `SecCall`. */
    fun derive(otherPublic: ByteArray, transcript: ByteArray): PairSecrets {
        require(otherPublic.size == KEY_SIZE) { "invalid peer public key" }
        val shared = try {
            Corebind.x25519Shared(priv, otherPublic)
        } catch (t: Throwable) {
            throw IllegalArgumentException(t.message ?: "invalid peer public key", t)
        }
        val (key, sas, emoji) = SecCall.derivePairSecrets(shared, transcript)
        return PairSecrets(sas, key, emoji)
    }
}

class PairSecrets(val sas: ByteArray, val key: ByteArray, val emoji: List<String>)

/** Transcript and emoji derivation for pairing, backed by Go `SecCall`. */
object Pairing {
    fun domainTranscript(
        id: String,
        initiator: ByteArray,
        joiner: ByteArray,
        device: ByteArray,
        deviceX: ByteArray,
        kind: Int,
        trustDescriptor: ByteArray,
    ): ByteArray = SecCall.pairDomainTranscript(id, initiator, joiner, device, deviceX, kind, trustDescriptor)

    fun identityTranscript(
        id: String,
        initiator: ByteArray,
        joiner: ByteArray,
        device: ByteArray,
        kind: Int,
    ): ByteArray = SecCall.pairIdentityTranscript(id, initiator, joiner, device, kind)

    fun transcript(
        id: String,
        initiator: ByteArray,
        joiner: ByteArray,
        kind: Int,
    ): ByteArray = SecCall.pairTranscript(id, initiator, joiner, kind)
}
