// Pairing crypto, matching internal/e2e/pair.go and web/src/pair.ts: X25519
// keys, the initiator's commitment, the transcript, and the four-emoji SAS.
package io.github.lewin671.remoteagent.core

import org.bouncycastle.crypto.agreement.X25519Agreement
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters
import org.bouncycastle.crypto.params.X25519PublicKeyParameters
import java.nio.ByteBuffer

private const val PAIR_COMMIT_CONTEXT = "remote-agent/v1/pair-commit\u0000"
private const val PAIR_TRANSCRIPT_CONTEXT = "remote-agent/v1/pair\u0000"

/** One side's ephemeral X25519 key pair. */
class PairKey(privateKey: ByteArray = randomBytes(32)) {
    private val priv = X25519PrivateKeyParameters(privateKey.copyOf(), 0)
    val public: ByteArray = priv.generatePublicKey().encoded

    /** The shared secrets with the other side; rejects keys that give an all-zero result. */
    fun derive(otherPublic: ByteArray, transcript: ByteArray): PairSecrets {
        if (otherPublic.size != 32) throw IllegalArgumentException("配对密钥无效")
        val shared = ByteArray(32)
        val a = X25519Agreement()
        a.init(priv)
        try {
            a.calculateAgreement(X25519PublicKeyParameters(otherPublic, 0), shared, 0)
        } catch (e: IllegalStateException) {
            throw IllegalArgumentException("配对密钥无效")
        }
        if (shared.all { it == 0.toByte() }) throw IllegalArgumentException("配对密钥无效")
        val sas = hkdf(shared, transcript, "remote-agent/v1/pair-sas".toByteArray(), 4)
        val key = hkdf(shared, transcript, "remote-agent/v1/pair-key".toByteArray(), KEY_SIZE)
        return PairSecrets(sas, key)
    }
}

class PairSecrets(val sas: ByteArray, val key: ByteArray) {
    val emoji: List<String> get() = sas.map { SAS_EMOJI[it.toInt() and 0xff] }
}

object Pairing {
    fun commit(initiatorPublic: ByteArray): ByteArray = sha256(PAIR_COMMIT_CONTEXT.toByteArray(), initiatorPublic)

    fun transcript(pairingId: String, initiatorPublic: ByteArray, joinerPublic: ByteArray, joinerKind: Int): ByteArray {
        val parts = mutableListOf(PAIR_TRANSCRIPT_CONTEXT.toByteArray())
        for (p in listOf(pairingId.toByteArray(), initiatorPublic, joinerPublic)) {
            parts += u32(p.size)
            parts += p
        }
        parts += u32(joinerKind)
        return sha256(*parts.toTypedArray())
    }

    private fun u32(n: Int): ByteArray = ByteBuffer.allocate(4).putInt(n).array()
}

val SAS_EMOJI: List<String> = listOf(
        "🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼", "🐨", "🐯", "🦁", "🐮", "🐷", "🐸", "🐵", "🐔",
        "🐧", "🐦", "🐤", "🦆", "🦅", "🦉", "🦇", "🐺", "🐗", "🐴", "🦄", "🐝", "🐛", "🦋", "🐌", "🐞",
        "🐜", "🦗", "🕷", "🦂", "🐢", "🐍", "🦎", "🦖", "🦕", "🐙", "🦑", "🦐", "🦞", "🦀", "🐡", "🐠",
        "🐟", "🐬", "🐳", "🐋", "🦈", "🐊", "🐅", "🐆", "🦓", "🦍", "🦧", "🐘", "🦛", "🦏", "🐪", "🐫",
        "🦒", "🦘", "🐃", "🐂", "🐄", "🐎", "🐖", "🐏", "🐑", "🦙", "🐐", "🦌", "🐕", "🐩", "🦮", "🐈",
        "🐓", "🦃", "🦚", "🦜", "🦢", "🦩", "🕊", "🐇", "🦝", "🦨", "🦡", "🦦", "🦥", "🐁", "🐀", "🐿",
        "🦔", "🌵", "🎄", "🌲", "🌳", "🌴", "🌱", "🌿", "☘", "🍀", "🎍", "🎋", "🍃", "🍂", "🍁", "🍄",
        "🐚", "🌾", "💐", "🌷", "🌹", "🥀", "🌺", "🌸", "🌼", "🌻", "🌞", "🌝", "🌛", "🌜", "🌚", "🌕",
        "🌖", "🌗", "🌘", "🌑", "🌒", "🌓", "🌔", "🌙", "🌎", "🌍", "🌏", "🪐", "💫", "⭐", "🌟", "✨",
        "⚡", "☄", "💥", "🔥", "🌪", "🌈", "☀", "🌤", "⛅", "🌥", "☁", "🌦", "🌧", "⛈", "🌩", "🌨",
        "❄", "☃", "⛄", "🌬", "💨", "💧", "💦", "☔", "☂", "🌊", "🍏", "🍎", "🍐", "🍊", "🍋", "🍌",
        "🍉", "🍇", "🍓", "🫐", "🍈", "🍒", "🍑", "🥭", "🍍", "🥥", "🥝", "🍅", "🍆", "🥑", "🥦", "🥬",
        "🥒", "🌶", "🫑", "🌽", "🥕", "🧄", "🧅", "🥔", "🍠", "🥐", "🥯", "🍞", "🥖", "🥨", "🧀", "🥚",
        "🍳", "🧈", "🥞", "🧇", "🥓", "🥩", "🍗", "🍖", "🌭", "🍔", "🍟", "🍕", "🥪", "🥙", "🧆", "🌮",
        "🌯", "🥗", "🥘", "🥫", "🍝", "🍜", "🍲", "🍛", "🍣", "🍱", "🥟", "🦪", "🍤", "🍙", "🍚", "🍘",
        "🍥", "🥠", "🥮", "🍢", "🍡", "🍧", "🍨", "🍦", "🥧", "🧁", "🍰", "🎂", "🍮", "🍭", "🍬", "🍫",
)
