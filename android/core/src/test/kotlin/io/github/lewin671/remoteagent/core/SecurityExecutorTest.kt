package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SecurityKeyKind
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageOp
import io.github.lewin671.remoteagent.core.proto.SecurityStoreBucket
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.NotifyContent
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.RelayErrorDetail
import io.github.lewin671.remoteagent.proto.RelayErrorReason
import java.io.File
import java.util.Base64
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SecurityExecutorTest {
    @get:org.junit.Rule val hangGuard = hangGuard()

    @Test
    fun keySignAndKeyAgreeDelegatePrivateOperationsAndValidateInputs() {
        val store = MemorySecurityStoreV4()
        val edPub = store.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "", persist = true)
        val xPub = store.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "", persist = true)
        val peerXPriv = ByteArray(32) { (it + 51).toByte() }
        val peerXPub = Corebind.x25519PublicKey(peerXPriv)

        val sig = store.signKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "", "hello-sign".toByteArray())
        assertTrue(Corebind.ed25519Verify(edPub, "hello-sign".toByteArray(), sig))

        val shared1 = store.agreeKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "", peerXPub)
        val shared2 = Corebind.x25519Shared(peerXPriv, xPub)
        assertArrayEquals(shared2, shared1)

        assertTrue(runCatching { store.agreeKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "", ByteArray(16)) }.isFailure)

        store.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(1L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_CLEAR_ALL)
                .build(),
        )
        assertTrue(runCatching { store.signKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "", "x".toByteArray()) }.isFailure)
        assertTrue(runCatching { store.agreeKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "", peerXPub) }.isFailure)
    }

    @Test
    fun openNotifyEnvelopeViaCoreRoundTripsValidAndRejectsTampered() {
        val v = Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText()).jsonObject
        val genesisRaw = Base64.getDecoder().decode(v["genesis"]!!.jsonObject["state"]!!.jsonPrimitive.content)
        val addedRaw = Base64.getDecoder().decode(v["added"]!!.jsonObject["state"]!!.jsonPrimitive.content)
        val accountId = v["account"]!!.jsonPrimitive.content
        val domain = v["domain"]!!.jsonPrimitive.content.unhex()
        val genesisHash = v["genesis"]!!.jsonObject["hash"]!!.jsonPrimitive.content.unhex()
        val machineSeed = v["machine_seed"]!!.jsonPrimitive.content.unhex()
        val machineX = v["machine_x"]!!.jsonPrimitive.content.unhex()
        val lease = Base64.getDecoder().decode(v["lease"]!!.jsonPrimitive.content)
        val secret = v["content_secret_2"]!!.jsonPrimitive.content.unhex()
        val machineId = deviceIdForEdPublic(Corebind.ed25519PublicKey(machineSeed))
        val (contentEpoch, _) = parseStateContentEpochAndParent(addedRaw)

        val notifyBlob = encodeV4NotifyRecordBytes(
            account = accountId,
            domain = domain,
            genesis = genesisHash,
            headState = addedRaw,
            purposeKey = deriveNotifyPurposeKey(secret, accountId, domain, contentEpoch),
        )

        val machineStore = MemorySecurityStoreV4().apply {
            val recBytes = encodeV4RecordBytes(
                account = accountId,
                domain = domain,
                genesis = genesisHash,
                memberId = machineId,
                server = "https://ra.example.com",
                states = listOf(genesisRaw, addedRaw),
                lease = lease,
                contentKeys = listOf(contentEpoch to secret),
            )
            seedTestRecord(
                edPrivate = machineSeed,
                edPublic = Corebind.ed25519PublicKey(machineSeed),
                xPrivate = machineX,
                xPublic = Corebind.x25519PublicKey(machineX),
                recordBytes = recBytes,
                notifyBytes = notifyBlob,
            )
        }
        val content = NotifyContent.newBuilder()
            .setMachineId(machineId)
            .setSessionId("s-notify")
            .setKind(NotifyKind.NOTIFY_KIND_ATTENTION)
            .setSourceEventSeq(1L)
            .setTitle("Title")
            .setText("Body")
            .build()

        val env: Envelope = CoreMemoryBridge(
            store = machineStore,
            server = "https://ra.example.com",
            deviceId = machineId,
            accountId = accountId,
            clock = { parseLeaseIssuedAt(lease) + 1000L },
        ).use { bridge ->
            bridge.seal(
                purpose = PayloadKind.PAYLOAD_KIND_NOTIFY_VALUE,
                machine = machineId,
                session = "s-notify",
                request = "notify:1:1",
                plain = content.toByteArray(),
            )
        }

        val nowMs = parseLeaseIssuedAt(lease) + 1000L
        val opened = openNotifyEnvelopeViaCore(
            notifyBlob = notifyBlob,
            rawEnvelope = env.toByteArray(),
            nowMs = nowMs,
            sessionId = "s-notify",
            tag = "s-notify/NOTIFY_KIND_ATTENTION",
            machineId = machineId,
            expectedKind = NotifyKind.NOTIFY_KIND_ATTENTION,
        )
        assertNotNull(opened)
        assertTrue(opened!!.valid)
        assertEquals("s-notify", opened.sessionId)
        assertEquals("Title", opened.title.literal)
        assertEquals("Body", opened.body.literal)

        val tamperedSig = env.signature.toByteArray().also { it[0] = (it[0].toInt() xor 1).toByte() }
        val tampered = env.toBuilder().setSignature(ByteString.copyFrom(tamperedSig)).build()
        assertNull(
            openNotifyEnvelopeViaCore(
                notifyBlob = notifyBlob,
                rawEnvelope = tampered.toByteArray(),
                nowMs = nowMs,
                sessionId = "s-notify",
                tag = "s-notify/NOTIFY_KIND_ATTENTION",
                machineId = machineId,
                expectedKind = NotifyKind.NOTIFY_KIND_ATTENTION,
            ),
        )
    }

    @Test
    fun parseRpcErrorForwardsUnpaddedBase64ConnectErrorDetails() {
        val detail = RelayErrorDetail.newBuilder()
            .setReason(RelayErrorReason.RELAY_ERROR_REASON_PROTOCOL_MISMATCH)
            .setServerRelayVersion(17)
            .build()
        val rawBytes = detail.toByteArray()
        val unpadded = Base64.getEncoder().withoutPadding().encodeToString(rawBytes)
        val body = """{"code":"failed_precondition","message":"relay version mismatch","details":[{"type":"remoteagent.v1.RelayErrorDetail","value":"$unpadded"},{"type":"ignored.bad","value":"!!!"}]}"""

        val ev = parseSecurityRpcError(opId = 42L, httpCode = 412, bodyBytes = body.toByteArray())
        assertEquals(42L, ev.opId)
        assertEquals(9, ev.connectCode)
        assertEquals("relay version mismatch", ev.error)
        assertEquals(1, ev.errorDetailsCount)
        assertEquals("remoteagent.v1.RelayErrorDetail", ev.getErrorDetails(0).typeName)
        assertArrayEquals(rawBytes, ev.getErrorDetails(0).value.toByteArray())

        // Non-primitive JSON fields (objects/arrays/numbers) must not throw and valid entries still pass through.
        val malformed = """{"code":{"nested":1},"message":["bad"],"details":["not-an-object",{"type":{},"value":[]},{"type":123,"value":true},{"type":"remoteagent.v1.RelayErrorDetail","value":"$unpadded"}]}"""
        val evMalformed = parseSecurityRpcError(opId = 43L, httpCode = 412, bodyBytes = malformed.toByteArray())
        assertEquals(43L, evMalformed.opId)
        assertEquals(9, evMalformed.connectCode)
        assertEquals("HTTP 412", evMalformed.error)
        assertEquals(1, evMalformed.errorDetailsCount)
        assertEquals("remoteagent.v1.RelayErrorDetail", evMalformed.getErrorDetails(0).typeName)
        assertArrayEquals(rawBytes, evMalformed.getErrorDetails(0).value.toByteArray())
    }
}
