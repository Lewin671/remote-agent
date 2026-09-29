package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
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
        assertEquals(s(v, "wrap"), k.wrap.hex())
        assertEquals(s(v, "meta"), k.meta.hex())
        assertEquals(s(v, "command"), k.command.hex())
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
    fun opensAGoEnvelope() {
        val e = v["envelope"]!!.jsonObject
        val key = s(e, "key").unhex()
        val env = Envelope.newBuilder()
            .setHeader(ByteString.copyFrom(s(e, "header").unhex()))
            .setNonce(ByteString.copyFrom(s(e, "nonce").unhex()))
            .setCiphertext(ByteString.copyFrom(s(e, "ciphertext").unhex()))
            .build()
        val (h, pt) = Envelopes.open(key, env)
        assertEquals(s(e, "plaintext"), String(pt, Charsets.UTF_8))
        Envelopes.expect(h, Place(PayloadKind.PAYLOAD_KIND_EVENT, machineId = "machine", sessionId = "session", seq = 3))
        assertThrows(OpenException::class.java) { Envelopes.expect(h, Place(PayloadKind.PAYLOAD_KIND_EVENT, seq = 4)) }
        val moved = s(e, "header").unhex().also { it[it.size - 1] = (it[it.size - 1].toInt() xor 1).toByte() }
        assertThrows(OpenException::class.java) {
            Envelopes.open(key, env.toBuilder().setHeader(ByteString.copyFrom(moved)).build())
        }
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
        assertEquals(s(p, "commit"), Pairing.commit(init.public).hex())
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
        val envKey = ByteArray(32) { 0x33 }
        // Largest-but-15 uint64: seq must not pass through a signed number.
        val seq = -16L
        val header = Header.newBuilder()
            .setKind(PayloadKind.PAYLOAD_KIND_EVENT).setMachineId("machine-k").setSessionId("session-k")
            .setSeq(seq).setRequestId("req-k").setDeadlineMs(1_900_000_000_000).setSender("device-k").build()
        val plaintext = "来自 Kotlin ✓"
        val env = Envelopes.seal(envKey, header, plaintext.toByteArray(), nonce = ByteArray(12) { 0x0c })
        val registration = "kotlin registration".toByteArray()
        val deviceSeed = ByteArray(32) { 0x44 }
        val device = SigningKey(deviceSeed)
        val challenge = ByteArray(32) { it.toByte() }
        val init = PairKey(ByteArray(32) { 0x66 })
        val join = PairKey(ByteArray(32) { 0x77 })
        val pairingId = "fedcba9876543210fedcba9876543210"
        val tr = Pairing.transcript(pairingId, init.public, join.public, DeviceKind.DEVICE_KIND_CLIENT.number)
        val sec = init.derive(join.public, tr)
        val payload = PairingPayload.newBuilder().setRoot(ByteString.copyFrom(root)).setServer("https://ra.example.com").build()
        val pairEnv = Envelopes.seal(
            sec.key,
            Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_PAIRING).setRequestId(pairingId).setSender("device-k").build(),
            payload.toByteArray(),
            nonce = ByteArray(12) { 0x0d },
        )
        return buildJsonObject {
            put("root", root.hex())
            put("account_public_key", keys.account.publicKey.hex())
            put("wrap", keys.wrap.hex())
            put("meta", keys.meta.hex())
            put("command", keys.command.hex())
            put("notify", keys.notify.hex())
            put("device_id", deviceId(keys.account.publicKey))
            putJsonObject("envelope") {
                put("key", envKey.hex())
                put("header", env.header.toByteArray().hex())
                put("nonce", env.nonce.toByteArray().hex())
                put("ciphertext", env.ciphertext.toByteArray().hex())
                put("plaintext", plaintext)
                put("seq", java.lang.Long.toUnsignedString(seq))
            }
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
                put("commit", Pairing.commit(init.public).hex())
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
        val got = generate()
        if (System.getProperty("updateVectors") != null) {
            file.writeText(Json { prettyPrint = true }.encodeToString(JsonElement.serializer(), got) + "\n")
        }
        assertEquals("kotlin-vectors.json is stale; run ./gradlew :core:test -PupdateVectors", Json.parseToJsonElement(file.readText()), got)
    }

    @Test
    fun uint64SeqSurvives() {
        val e = Json.parseToJsonElement(file.readText()).jsonObject["envelope"]!!.jsonObject
        val env = Envelope.newBuilder()
            .setHeader(ByteString.copyFrom(e["header"]!!.jsonPrimitive.content.unhex()))
            .setNonce(ByteString.copyFrom(e["nonce"]!!.jsonPrimitive.content.unhex()))
            .setCiphertext(ByteString.copyFrom(e["ciphertext"]!!.jsonPrimitive.content.unhex()))
            .build()
        val (h, pt) = Envelopes.open(e["key"]!!.jsonPrimitive.content.unhex(), env)
        assertEquals("18446744073709551600", java.lang.Long.toUnsignedString(h.seq))
        assertArrayEquals(e["plaintext"]!!.jsonPrimitive.content.toByteArray(), pt)
        assertFalse(h.seq > 0)
    }
}
