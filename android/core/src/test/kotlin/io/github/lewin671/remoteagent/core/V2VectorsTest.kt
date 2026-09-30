package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.DeviceMeta
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ImageFormat
import io.github.lewin671.remoteagent.proto.ImagePayload
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/** The Go vectors of the v2 protocol (internal/e2e/testdata/v2-vectors.json), checked in Kotlin. */
class V2VectorsTest {
    private val v = Json.parseToJsonElement(File(System.getProperty("v2Vectors")).readText()).jsonObject

    private fun s(o: JsonObject, k: String) = o[k]!!.jsonPrimitive.content
    private fun o(k: String) = v[k]!!.jsonObject
    private fun env(o: JsonObject) = Envelope.newBuilder()
        .setHeader(ByteString.copyFrom(s(o, "header").unhex()))
        .setNonce(ByteString.copyFrom(s(o, "nonce").unhex()))
        .setCiphertext(ByteString.copyFrom(s(o, "ciphertext").unhex()))
        .build()

    @Test
    fun derivesThePrefsAndDeviceMetaKeys() {
        val k = Keys.derive(s(v, "root").unhex())
        assertEquals(s(v, "account_prefs"), k.prefs.hex())
        assertEquals(s(v, "device_meta"), k.deviceMeta.hex())
        assertEquals(s(v, "notify"), k.notify.hex())
    }

    @Test
    fun signsCommandsAsGoChecksThem() {
        val c = o("command_signature")
        val device = SigningKey(s(c, "device_seed").unhex())
        assertEquals(s(c, "device_public_key"), device.publicKey.hex())
        assertEquals(s(c, "device_id"), deviceId(device.publicKey))
        val e = env(c)
        assertEquals(s(c, "signed_message"), signedEnvelopeBytes(e).hex())
        val signed = signEnvelope(device, e)
        // Ed25519 is deterministic: the same bytes as Go's.
        assertEquals(s(c, "signature"), signed.signature.toByteArray().hex())
        assertTrue(verifyEd25519(device.publicKey, signedEnvelopeBytes(signed), signed.signature.toByteArray()))
        // Any change to what was signed breaks it.
        val moved = signed.toBuilder().setNonce(ByteString.copyFrom(ByteArray(12))).build()
        assertFalse(verifyEd25519(device.publicKey, signedEnvelopeBytes(moved), signed.signature.toByteArray()))
    }

    @Test
    fun opensAnImage() {
        val i = o("image")
        val (h, p) = Envelopes.openMessage(s(i, "key").unhex(), env(i), Place(PayloadKind.PAYLOAD_KIND_IMAGE), ImagePayload::parseFrom)
        assertEquals("machine", h.machineId)
        assertEquals("session", h.sessionId)
        assertArrayEquals(s(i, "plaintext").unhex(), p.toByteArray())
        assertEquals(ImageFormat.IMAGE_FORMAT_PNG, p.format)
    }

    @Test
    fun opensPrefsAndDeviceMeta() {
        val a = o("account_prefs_object")
        val (h, prefs) = Envelopes.openMessage(s(a, "key").unhex(), env(a), Place(PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS, requestId = "project-prefs"), ProjectPrefs::parseFrom)
        assertArrayEquals(s(a, "plaintext").unhex(), prefs.toByteArray())
        assertEquals(1, prefs.schema)
        assertTrue(h.seq > 0)
        val d = o("device_meta_object")
        val (_, meta) = Envelopes.openMessage(s(d, "key").unhex(), env(d), Place(PayloadKind.PAYLOAD_KIND_DEVICE_META), DeviceMeta::parseFrom)
        assertEquals("android", meta.platform)
    }
}
