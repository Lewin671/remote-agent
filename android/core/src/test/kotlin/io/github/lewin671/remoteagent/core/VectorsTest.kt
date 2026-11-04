package io.github.lewin671.remoteagent.core

import java.util.zip.Deflater
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.Compression
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.PairingPayload
import io.github.lewin671.remoteagent.proto.PayloadKind
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/** The Go vectors, checked in Kotlin. */
class VectorsTest {
    private val v = Json.parseToJsonElement(File(System.getProperty("vectors")).readText()).jsonObject

    private fun s(o: JsonObject, k: String) = o[k]!!.jsonPrimitive.content

    @Test
    fun derivesTheSameKeys() {
        val root = s(v, "root").unhex()
        val k = Keys.derive(root)
        assertEquals(s(v, "account_public_key"), k.account.publicKey.hex())
        assertEquals(s(v, "device_id"), deviceId(s(v, "account_public_key").unhex()))
    }

    @Test
    fun signsRegistrationsTheSameWay() {
        val k = Keys.derive(s(v, "root").unhex())
        val r = v["register_signature"]!!.jsonObject
        val msg = registerMessage(s(r, "message").unhex())
        assertEquals(s(r, "signature"), k.account.sign(msg).hex())
        assertTrue(verifyEd25519(k.account.publicKey, msg, s(r, "signature").unhex()))
    }

    @Test
    fun parsesTheSecret() {
        val sec = Secret.parse(s(v, "secret"))
        assertEquals(s(v, "root"), sec.root.hex())
        assertEquals("https://ra.example.com", sec.server)
        assertThrows(IllegalArgumentException::class.java) { Secret.parse("ra1.abc") }
    }

    @Test
    fun pairsTheSameWay() {
        val p = v["pairing"]!!.jsonObject
        val init = PairKey(s(p, "initiator_private").unhex())
        val join = PairKey(s(p, "joiner_private").unhex())
        assertEquals(s(p, "initiator_public"), init.public.hex())
        assertEquals(s(p, "joiner_public"), join.public.hex())
        val tr = Pairing.transcript(s(p, "pairing_id"), init.public, join.public, p["joiner_kind"]!!.jsonPrimitive.int)
        assertEquals(s(p, "transcript"), tr.hex())
        val a = init.derive(join.public, tr)
        val b = join.derive(init.public, tr)
        assertEquals(s(p, "sas"), a.sas.hex())
        assertEquals(s(p, "key"), a.key.hex())
        assertEquals(s(p, "key"), b.key.hex())
        assertEquals(p["emoji"]!!.jsonArray.map { it.jsonPrimitive.content }, SAS_EMOJI)
    }

    @Test
    fun rejectsAllZeroSharedSecrets() {
        // A low-order point gives an all-zero shared secret.
        assertThrows(IllegalArgumentException::class.java) { PairKey().derive(ByteArray(32), ByteArray(32)) }
    }
}

/** Vectors made by Kotlin for Go and TypeScript to check: the reverse direction. */
class KotlinVectorsTest {
    private val file = File(System.getProperty("kotlinVectors"))

    private fun generate(): JsonObject {
        val root = ByteArray(32) { 0x5a }
        val keys = Keys.derive(root)
        val registration = "kotlin registration".toByteArray()
        val deviceSeed = ByteArray(32) { 0x44 }
        val device = SigningKey(deviceSeed)
        val challenge = ByteArray(32) { it.toByte() }
        val init = PairKey(ByteArray(32) { 0x66 })
        val join = PairKey(ByteArray(32) { 0x77 })
        val pairingId = "fedcba9876543210fedcba9876543210"
        val tr = Pairing.transcript(pairingId, init.public, join.public, DeviceKind.DEVICE_KIND_CLIENT.number)
        val sec = init.derive(join.public, tr)
        val payload =
            PairingPayload.newBuilder()
                .setServer("https://ra.example.com")
                .setAccountPublicKey(ByteString.copyFrom(keys.account.publicKey))
                .build()
        val pairEnv = Envelopes.seal(
            sec.key,
            Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_PAIRING).setRequestId(pairingId).setSender("device-k").build(),
            payload.toByteArray(),
            nonce = ByteArray(12) { 0x0d },
        )
        return buildJsonObject {
            put("root", root.hex())
            put("account_public_key", keys.account.publicKey.hex())
            put("device_id", deviceId(keys.account.publicKey))

            putJsonObject("register_signature") {
                put("message", registration.hex())
                put("signature", keys.account.sign(registerMessage(registration)).hex())
            }
            putJsonObject("login_signature") {
                put("device_seed", deviceSeed.hex())
                put("device_public_key", device.publicKey.hex())
                put("device_id", deviceId(device.publicKey))
                put("challenge", challenge.hex())
                put("signature", device.sign(loginMessage(challenge)).hex())
            }
            putJsonObject("pairing") {
                put("initiator_private", ByteArray(32) { 0x66 }.hex())
                put("joiner_private", ByteArray(32) { 0x77 }.hex())
                put("initiator_public", init.public.hex())
                put("joiner_public", join.public.hex())
                put("pairing_id", pairingId)
                put("joiner_kind", DeviceKind.DEVICE_KIND_CLIENT.number)
                put("transcript", tr.hex())
                put("sas", sec.sas.hex())
                put("key", sec.key.hex())
                putJsonArray("emoji") { sec.emoji.forEach { add(JsonPrimitive(it)) } }
                putJsonObject("payload") {
                    put("header", pairEnv.header.toByteArray().hex())
                    put("nonce", pairEnv.nonce.toByteArray().hex())
                    put("ciphertext", pairEnv.ciphertext.toByteArray().hex())
                }
            }
        }
    }

    @Test
    fun vectorsAreUpToDate() {
        var got = generate()
        if (System.getProperty("updateVectors") != null) {
            file.writeText(Json { prettyPrint = true }.encodeToString(JsonElement.serializer(), got) + "\n")
        }
        val stored = Json.parseToJsonElement(file.readText()).jsonObject
        // The packed envelope's bytes depend on the deflate encoder, which is
        // the JDK's zlib: they are written with -PupdateVectors and otherwise
        // only opened (packedEnvelopeOpens), as Go does with its own.
        assertEquals("kotlin-vectors.json is stale; run ./gradlew :core:test -PupdateVectors", stored, got)
    }

}

/** Payloads are packed before sealing when that makes them smaller (architecture §10 「压缩」), as TestPacking in Go. */

class PackingTest {
    private val key = randomBytes(KEY_SIZE)
    private val big = "the same line again\n".repeat(500).toByteArray()

    private fun header(kind: PayloadKind = PayloadKind.PAYLOAD_KIND_PAIRING) = Header.newBuilder().setKind(kind).setMachineId("m").setSessionId("s").setSeq(1).build()

    /** An envelope whose header and plaintext are taken as given, as a sender that does not follow the rules would make it. */
    private fun raw(compression: Int, plaintext: ByteArray): Envelope {
        val h = header().toBuilder().setVersion(Envelopes.VERSION).setCompressionValue(compression).build().toByteArray()
        val nonce = ByteArray(12)
        val c = javax.crypto.Cipher.getInstance("AES/GCM/NoPadding")
        c.init(javax.crypto.Cipher.ENCRYPT_MODE, javax.crypto.spec.SecretKeySpec(key, "AES"), javax.crypto.spec.GCMParameterSpec(128, nonce))
        c.updateAAD(h)
        return Envelope.newBuilder().setHeader(ByteString.copyFrom(h)).setNonce(ByteString.copyFrom(nonce)).setCiphertext(ByteString.copyFrom(c.doFinal(plaintext))).build()
    }

    @Test
    fun anUnknownCompressionIsRefused() {
        assertThrows(OpenException::class.java) { Envelopes.open(key, raw(9, big)) }
        // The same envelope with a compression it knows opens.
        assertArrayEquals(big, Envelopes.open(key, raw(Compression.COMPRESSION_NONE_VALUE, big)).second)
    }

    @Test
    fun aPayloadThatIsNotDeflateIsRefused() {
        assertThrows(OpenException::class.java) { Envelopes.open(key, raw(Compression.COMPRESSION_DEFLATE_VALUE, "not deflate at all, surely".toByteArray())) }
        // Deflate that stops before its end, and nothing at all.
        val packed = pack(big)!!
        assertArrayEquals(big, Envelopes.open(key, raw(Compression.COMPRESSION_DEFLATE_VALUE, packed)).second)
        assertThrows(OpenException::class.java) { Envelopes.open(key, raw(Compression.COMPRESSION_DEFLATE_VALUE, packed.copyOf(packed.size / 2))) }
        assertThrows(OpenException::class.java) { Envelopes.open(key, raw(Compression.COMPRESSION_DEFLATE_VALUE, ByteArray(0))) }
    }

    @Test
    fun aPayloadThatUnpacksPastTheLimitIsRefused() {
        // A few kilobytes that unpack to one byte more than the limit, made without holding them unpacked.
        val d = java.util.zip.Deflater(java.util.zip.Deflater.BEST_SPEED, true)
        val out = java.io.ByteArrayOutputStream()
        val zeros = ByteArray(1 shl 20)
        val buf = ByteArray(64 * 1024)
        fun drain() {
            while (true) {
                val n = d.deflate(buf)
                if (n == 0) break
                out.write(buf, 0, n)
            }
        }
        repeat(MAX_UNPACKED shr 20) { d.setInput(zeros); drain() }
        d.setInput(ByteArray(1))
        d.finish()
        while (!d.finished()) out.write(buf, 0, d.deflate(buf))
        d.end()
        val bomb = out.toByteArray()
        assertTrue(bomb.size < 1 shl 20)
        assertThrows(OpenException::class.java) { unpack(bomb) }
        assertThrows(OpenException::class.java) { Envelopes.open(key, raw(Compression.COMPRESSION_DEFLATE_VALUE, bomb)) }
        // The limit itself is allowed, and a lower one stops before the rest is produced.
        assertArrayEquals(big, unpack(pack(big)!!, big.size))
        assertThrows(OpenException::class.java) { unpack(pack(big)!!, big.size - 1) }
    }
}

// Builds compressed fixtures for the current bounded decoder.
/** [plaintext] as raw DEFLATE (RFC 1951, no zlib or gzip wrapper), or null when that is not smaller. */
internal fun pack(plaintext: ByteArray): ByteArray? {
    val d = Deflater(Deflater.DEFAULT_COMPRESSION, true)
    try {
        d.setInput(plaintext)
        d.finish()
        // Only a result smaller than the input is of use: a buffer of that size is enough to tell.
        val out = ByteArray(plaintext.size)
        val n = d.deflate(out)
        return if (d.finished() && n < plaintext.size) out.copyOf(n) else null
    } finally {
        d.end()
    }
}
