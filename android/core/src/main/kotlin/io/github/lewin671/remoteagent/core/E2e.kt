// End-to-end encryption, matching internal/e2e in Go and web/src/crypto.ts:
// HKDF-SHA256 key derivation from the account root key, AES-256-GCM
// envelopes whose serialized header is the additional data, and Ed25519
// signatures. internal/e2e/testdata/vectors.json keeps the implementations
// in step; kotlin-vectors.json is the same check in the other direction.
package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import com.google.protobuf.InvalidProtocolBufferException
import com.google.protobuf.MessageLite
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.PayloadKind
import org.bouncycastle.crypto.digests.SHA256Digest
import org.bouncycastle.crypto.generators.HKDFBytesGenerator
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import org.bouncycastle.crypto.params.Ed25519PublicKeyParameters
import org.bouncycastle.crypto.params.HKDFParameters
import org.bouncycastle.crypto.signers.Ed25519Signer
import java.security.GeneralSecurityException
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

const val KEY_SIZE = 32
const val REGISTER_CONTEXT = "remote-agent/v1/register\u0000"
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

/** HKDF-SHA256 with the given salt and info. */
fun hkdf(secret: ByteArray, salt: ByteArray?, info: ByteArray, size: Int): ByteArray {
    val gen = HKDFBytesGenerator(SHA256Digest())
    gen.init(HKDFParameters(secret, salt, info))
    return ByteArray(size).also { gen.generateBytes(it, 0, size) }
}

/** An Ed25519 key pair from a 32-byte seed. */
class SigningKey(seed: ByteArray) {
    private val priv = Ed25519PrivateKeyParameters(seed.copyOf(), 0)
    val publicKey: ByteArray = priv.generatePublicKey().encoded

    fun sign(message: ByteArray): ByteArray {
        val s = Ed25519Signer()
        s.init(true, priv)
        s.update(message, 0, message.size)
        return s.generateSignature()
    }
}

fun verifyEd25519(publicKey: ByteArray, message: ByteArray, signature: ByteArray): Boolean {
    if (publicKey.size != 32 || signature.size != 64) return false
    val v = Ed25519Signer()
    v.init(false, Ed25519PublicKeyParameters(publicKey, 0))
    v.update(message, 0, message.size)
    return v.verifySignature(signature)
}

/** The keys derived from an account's root key, one per purpose. */
class Keys private constructor(
    val account: SigningKey,
    val wrap: ByteArray,
    val meta: ByteArray,
    val command: ByteArray,
    val notify: ByteArray,
    /** Seals account preference objects (ProjectPrefs). */
    val prefs: ByteArray,
    /** Seals client devices' metadata (DeviceMeta). */
    val deviceMeta: ByteArray,
) {
    companion object {
        fun derive(root: ByteArray): Keys {
            require(root.size == KEY_SIZE) { "root key must be $KEY_SIZE bytes" }
            return Keys(
                account = SigningKey(deriveKey(root, "account-sign")),
                wrap = deriveKey(root, "wrap"),
                meta = deriveKey(root, "machine-meta"),
                command = deriveKey(root, "command"),
                notify = deriveKey(root, "notify"),
                prefs = deriveKey(root, "account-prefs"),
                deviceMeta = deriveKey(root, "device-meta"),
            )
        }

        fun deriveKey(root: ByteArray, purpose: String): ByteArray =
            hkdf(root, null, ("remote-agent/v1/$purpose").toByteArray(), KEY_SIZE)
    }
}

/** A device is named by the first 16 bytes of its public key's SHA-256. */
fun deviceId(publicKey: ByteArray): String = sha256(publicKey).copyOf(16).hex()

fun registerMessage(registration: ByteArray): ByteArray = REGISTER_CONTEXT.toByteArray() + registration

fun loginMessage(challenge: ByteArray): ByteArray = LOGIN_CONTEXT.toByteArray() + challenge

/** What a new device needs to join the account: "ra1.<root>.<server>". */
data class Secret(val root: ByteArray, val server: String) {
    override fun toString(): String = "Secret(server=$server)"

    companion object {
        fun parse(text: String): Secret {
            val bad = IllegalArgumentException("这不是 remote-agent 的账户密钥")
            val t = text.trim()
            if (!t.startsWith("ra1.")) throw bad
            val parts = t.removePrefix("ra1.").split(".")
            if (parts.size != 2) throw bad
            val dec = Base64.getUrlDecoder()
            val root: ByteArray
            val server: String
            try {
                root = dec.decode(parts[0].trimEnd('='))
                server = String(dec.decode(parts[1].trimEnd('=')), Charsets.UTF_8)
            } catch (e: IllegalArgumentException) {
                throw bad
            }
            if (root.size != KEY_SIZE || server.isEmpty()) throw bad
            return Secret(root, checkServer(server))
        }

        /** The server's origin; plain http only for local addresses. */
        fun checkServer(url: String, allowPlainHttp: Boolean = false): String {
            val u = try {
                java.net.URI(url.trim())
            } catch (e: java.net.URISyntaxException) {
                throw IllegalArgumentException("server 地址无效")
            }
            val host = u.host ?: throw IllegalArgumentException("server 地址无效")
            require(u.rawUserInfo == null) { "server 地址不能包含用户名或密码" }
            val local = host in setOf("localhost", "127.0.0.1", "[::1]", "::1") || (allowPlainHttp && host == "10.0.2.2")
            when (u.scheme) {
                "https" -> {}
                "http" -> if (!local) throw IllegalArgumentException("server 必须使用 https")
                else -> throw IllegalArgumentException("server 地址无效")
            }
            val port = if (u.port == -1) "" else ":${u.port}"
            return "${u.scheme}://$host$port"
        }
    }
}

class OpenException(message: String) : Exception(message)

/** Where an envelope must have been found; unset fields are not checked, except kind. */
data class Place(
    val kind: PayloadKind,
    val machineId: String = "",
    val sessionId: String = "",
    val seq: Long = 0,
    val requestId: String = "",
)

object Envelopes {
    const val VERSION = ENVELOPE_VERSION

    /** Seals plaintext; the header is serialized once and authenticated as sent. */
    fun seal(key: ByteArray, header: Header, plaintext: ByteArray, nonce: ByteArray = randomBytes(12)): Envelope {
        val h = (if (header.version == 0) header.toBuilder().setVersion(VERSION).build() else header).toByteArray()
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, nonce))
        c.updateAAD(h)
        val ct = c.doFinal(plaintext)
        return Envelope.newBuilder()
            .setHeader(ByteString.copyFrom(h))
            .setNonce(ByteString.copyFrom(nonce))
            .setCiphertext(ByteString.copyFrom(ct))
            .build()
    }

    fun seal(key: ByteArray, header: Header, msg: MessageLite): Envelope = seal(key, header, msg.toByteArray())

    /** Opens an envelope and returns the header exactly as sealed. */
    fun open(key: ByteArray, env: Envelope?): Pair<Header, ByteArray> {
        if (env == null || env.nonce.size() != 12) throw OpenException("envelope does not open")
        val header = env.header.toByteArray()
        val pt = try {
            val c = Cipher.getInstance("AES/GCM/NoPadding")
            c.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, env.nonce.toByteArray()))
            c.updateAAD(header)
            c.doFinal(env.ciphertext.toByteArray())
        } catch (e: GeneralSecurityException) {
            throw OpenException("envelope does not open")
        }
        val h = try {
            Header.parseFrom(header)
        } catch (e: InvalidProtocolBufferException) {
            throw OpenException("malformed header")
        }
        if (h.version != VERSION) throw OpenException("unsupported envelope version")
        return h to pt
    }

    fun expect(h: Header, want: Place) {
        if (h.kind != want.kind ||
            (want.machineId.isNotEmpty() && h.machineId != want.machineId) ||
            (want.sessionId.isNotEmpty() && h.sessionId != want.sessionId) ||
            (want.seq != 0L && h.seq != want.seq) ||
            (want.requestId.isNotEmpty() && h.requestId != want.requestId)
        ) {
            throw OpenException("envelope is not where it belongs")
        }
    }

    /** Opens, checks the place and parses. */
    fun <T> openMessage(key: ByteArray, env: Envelope?, want: Place, parse: (ByteArray) -> T): Pair<Header, T> {
        val (h, pt) = open(key, env)
        expect(h, want)
        val msg = try {
            parse(pt)
        } catch (e: InvalidProtocolBufferException) {
            throw OpenException("malformed content")
        }
        return h to msg
    }
}

private const val ENVELOPE_SIGNATURE_CONTEXT = "remote-agent/v1/envelope-signature\u0000"

/**
 * What a device signs for an envelope (Envelope.signature): a context,
 * then the header, nonce and ciphertext as transmitted, each with its
 * length as a big-endian uint32. Same as SignedEnvelopeBytes in Go.
 */
fun signedEnvelopeBytes(env: Envelope): ByteArray {
    val out = java.io.ByteArrayOutputStream()
    out.write(ENVELOPE_SIGNATURE_CONTEXT.toByteArray())
    for (part in listOf(env.header, env.nonce, env.ciphertext)) {
        val n = part.size()
        out.write(byteArrayOf((n ushr 24).toByte(), (n ushr 16).toByte(), (n ushr 8).toByte(), n.toByte()))
        part.writeTo(out)
    }
    return out.toByteArray()
}

/** The envelope with the device's signature, so the daemon can tell which device sent it. */
fun signEnvelope(device: SigningKey, env: Envelope): Envelope =
    env.toBuilder().setSignature(ByteString.copyFrom(device.sign(signedEnvelopeBytes(env)))).build()

fun newRequestId(): String = randomBytes(16).hex()
