package io.github.lewin671.remoteagent.core

import com.google.protobuf.InvalidProtocolBufferException
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.PayloadKind
import java.io.ByteArrayOutputStream
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.zip.DataFormatException
import java.util.zip.Inflater

const val KEY_SIZE = 32
const val LOGIN_CONTEXT = "remote-agent/v1/login\u0000"

private val random = SecureRandom()

fun randomBytes(n: Int): ByteArray = ByteArray(n).also { random.nextBytes(it) }

fun ByteArray.hex(): String = joinToString("") { "%02x".format(it) }

fun String.unhex(): ByteArray {
    require(length % 2 == 0) { "odd hex length" }
    return ByteArray(length / 2) { i -> substring(2 * i, 2 * i + 2).toInt(16).toByte() }
}

fun sha256(vararg parts: ByteArray): ByteArray {
    val md = MessageDigest.getInstance("SHA-256")
    for (p in parts) md.update(p)
    return md.digest()
}

fun hmacSha256(key: ByteArray, message: ByteArray): ByteArray =
    javax.crypto.Mac.getInstance("HmacSHA256").apply {
        init(javax.crypto.spec.SecretKeySpec(key, "HmacSHA256"))
    }.doFinal(message)

/** HKDF-SHA256 with the given salt and info. */
fun hkdf(secret: ByteArray, salt: ByteArray?, info: ByteArray, size: Int): ByteArray =
    Corebind.hkdfSha256(secret, salt ?: ByteArray(0), info, size)

fun deriveScopeKey(key: ByteArray, purpose: String): ByteArray =
    hkdf(key, null, ("remote-agent/v1/$purpose").toByteArray(), KEY_SIZE)

fun loginMessage(challenge: ByteArray): ByteArray = LOGIN_CONTEXT.toByteArray() + challenge

object Secret {
    /** The server's origin; plain http only for local addresses. */
    fun checkServer(url: String, allowPlainHttp: Boolean = false): String {
        val u = try {
            java.net.URI(url.trim())
        } catch (e: java.net.URISyntaxException) {
            throw MessageArgumentException(UiMessages.errorsAndroidInvalidServerAddress().messageText())
        }
        val host = u.host ?: throw MessageArgumentException(UiMessages.errorsAndroidInvalidServerAddress().messageText())
        requireMessage(u.rawUserInfo == null) { UiMessages.errorsTheServerAddressCannotIncludeAUsernameOrPassword().messageText() }
        val local = host in setOf("localhost", "127.0.0.1", "[::1]", "::1") || (allowPlainHttp && host == "10.0.2.2")
        when (u.scheme) {
            "https" -> {}
            "http" -> if (!local) throw MessageArgumentException(UiMessages.errorsTheServerMustUseHttps().messageText())
            else -> throw MessageArgumentException(UiMessages.errorsAndroidInvalidServerAddress().messageText())
        }
        val port = if (u.port == -1) "" else ":${u.port}"
        return "${u.scheme}://$host$port"
    }
}

class OpenException(message: String) : Exception(message), MessageFailure {
    override var display: MessageText = message.messageText()
        private set
    constructor(message: MessageText) : this("invalid_envelope") { display = message }
}

/** Where an envelope must have been found; unset fields are not checked, except kind. */
data class Place(
    val kind: PayloadKind,
    val machineId: String = "",
    val sessionId: String = "",
    val seq: Long = 0,
    val requestId: String = "",
)

/** Maximum decompressed payload size, including current device-security history. */
const val MAX_UNPACKED = 64 shl 20

/**
 * Inflates raw DEFLATE. It stops at [max] bytes: a payload that
 * would unpack to more is refused before the rest of it is produced.
 */
internal fun unpack(packed: ByteArray, max: Int = MAX_UNPACKED): ByteArray {
    val bad = OpenException("envelope does not open")
    val inf = Inflater(true)
    try {
        inf.setInput(packed)
        val out = ByteArrayOutputStream(minOf(max, maxOf(packed.size * 4, 1024)))
        val buf = ByteArray(64 * 1024)
        while (!inf.finished()) {
            val n = inf.inflate(buf)
            if (n == 0 && (inf.needsInput() || inf.needsDictionary())) throw bad
            if (out.size() + n > max) throw bad
            out.write(buf, 0, n)
        }
        return out.toByteArray()
    } catch (e: DataFormatException) {
        throw bad
    } finally {
        inf.end()
    }
}

fun newRequestId(): String = randomBytes(16).hex()

val RETIRED_HISTORY_ERROR = UiMessages.errorsThisHistoryUsesAnOldEncryptionFormatAndCan().messageText()
class UnsupportedHistoryException : Exception("retired_history"), MessageFailure { override val display = RETIRED_HISTORY_ERROR }
internal fun rejectRetiredHistory(env: Envelope) {
    if (Header.parseFrom(env.header).version == 1) throw UnsupportedHistoryException()
}
