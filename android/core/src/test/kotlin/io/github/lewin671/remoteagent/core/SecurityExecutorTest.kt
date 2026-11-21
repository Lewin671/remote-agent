package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SecurityKeyAgreeEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeySignEffect
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.NotifyContent
import io.github.lewin671.remoteagent.proto.NotifyKind
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.util.Base64

class SecurityExecutorTest {
    @get:org.junit.Rule val hangGuard = hangGuard()

    @Test
    fun keySignAndKeyAgreeDelegatePrivateOperationsAndValidateInputs() {
        val seed = ByteArray(32) { (it + 11).toByte() }
        val xPriv = ByteArray(32) { (it + 31).toByte() }
        val peerXPub = Corebind.x25519PublicKey(ByteArray(32) { (it + 51).toByte() })
        var activeSigner: SigningKey? = SigningKey(seed)
        var activeXPriv: ByteArray? = xPriv
        val executor = SecurityExecutor(signer = { activeSigner }, xPrivate = { activeXPriv })

        val signed =
            executor.keySign(
                SecurityKeySignEffect.newBuilder()
                    .setOpId(1L)
                    .setMessage(ByteString.copyFromUtf8("hello-sign"))
                    .build(),
            )
        assertEquals(1L, signed.opId)
        assertEquals("", signed.error)
        assertTrue(Corebind.ed25519Verify(SigningKey(seed).publicKey, "hello-sign".toByteArray(), signed.signature.toByteArray()))

        val emptyMsg = executor.keySign(SecurityKeySignEffect.newBuilder().setOpId(2L).build())
        assertEquals(2L, emptyMsg.opId)
        assertTrue(emptyMsg.error.isNotEmpty())
        assertTrue(emptyMsg.signature.isEmpty)

        val agreed =
            executor.keyAgree(
                SecurityKeyAgreeEffect.newBuilder()
                    .setOpId(3L)
                    .setPeerPublic(ByteString.copyFrom(peerXPub))
                    .build(),
            )
        assertEquals(3L, agreed.opId)
        assertEquals("", agreed.error)
        assertArrayEquals(Corebind.x25519Shared(xPriv, peerXPub), agreed.shared.toByteArray())

        val badPeer =
            executor.keyAgree(
                SecurityKeyAgreeEffect.newBuilder()
                    .setOpId(4L)
                    .setPeerPublic(ByteString.copyFrom(ByteArray(16)))
                    .build(),
            )
        assertEquals(4L, badPeer.opId)
        assertTrue(badPeer.error.isNotEmpty())

        activeSigner = null
        activeXPriv = null
        assertTrue(
            executor.keySign(
                SecurityKeySignEffect.newBuilder()
                    .setOpId(5L)
                    .setMessage(ByteString.copyFromUtf8("x"))
                    .build(),
            ).error.isNotEmpty(),
        )
        assertTrue(
            executor.keyAgree(
                SecurityKeyAgreeEffect.newBuilder()
                    .setOpId(6L)
                    .setPeerPublic(ByteString.copyFrom(peerXPub))
                    .build(),
            ).error.isNotEmpty(),
        )
    }

    @Test
    fun secCallDerivePurposeKeyAndOpenNotifyEnvelopeRoundTrip() {
        val v = Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText()).jsonObject
        fun b64(k: String) = Base64.getDecoder().decode(v[k]!!.jsonPrimitive.content)
        val genesisRaw = Base64.getDecoder().decode(v["genesis"]!!.jsonObject["state"]!!.jsonPrimitive.content)
        val addedRaw = Base64.getDecoder().decode(v["added"]!!.jsonObject["state"]!!.jsonPrimitive.content)
        val (genesis, _) = SecCall.parseState(genesisRaw)
        val (added, _) = SecCall.parseState(addedRaw)
        val pin = DeviceSecurity.pinOf(genesis)
        val secret = v["content_secret_2"]!!.jsonPrimitive.content.unhex()

        val key = SecCall.derivePurposeKey(secret, pin.account, pin.domain, added.core.contentEpoch, 10u)
        assertEquals(KEY_SIZE, key.size)

        val phoneSigner = SigningKey(v["phone_seed"]!!.jsonPrimitive.content.unhex())
        val machineSigner = SigningKey(v["machine_seed"]!!.jsonPrimitive.content.unhex())
        val machine = added.core.members.first { it.kind == 1u }
        val content =
            NotifyContent.newBuilder()
                .setMachineId(machine.id)
                .setSessionId("s-notify")
                .setKind(NotifyKind.NOTIFY_KIND_ATTENTION)
                .setTitle("Title")
                .setText("Body")
                .build()
        val store =
            TestSecurityStore(
                DeviceSecurity.Record(
                    pin = pin,
                    chain = listOf(genesis, added),
                    memberID = machine.id,
                    xPrivate = v["machine_x"]!!.jsonPrimitive.content.unhex(),
                    lease = HeadLease.sign(added, phoneSigner, added.core.issuedAt.toLong()),
                ),
            )
        CoreSecurityBridge(
            store = store,
            memberID = machine.id,
            signer = machineSigner,
            clock = { added.core.issuedAt.toLong() + 1000L },
        ).use { bridge ->
            val env =
                bridge.seal(
                    DeviceSecurity.Purpose.NOTIFY,
                    machine = machine.id,
                    session = "s-notify",
                    request = "notify:1:1",
                    plain = content.toByteArray(),
                )
            val opened = SecCall.openNotifyEnvelope(pin, listOf(addedRaw), env, purposeKey = key)
            assertEquals("s-notify", opened.sessionId)
            assertEquals("Title", opened.title)
            assertEquals("Body", opened.text)
        }
    }

    @Test
    fun showForTestWorksWithoutSecurityStorage() {
        val account =
            Account(
                server = "https://example.com",
                root = ByteArray(32) { 1 },
                deviceSeed = ByteArray(32) { 2 },
                deviceId = "d".repeat(32),
                accountId = "a".repeat(32),
            )
        val remote = Remote(account, Api.defaultHttp())
        try {
            val expected = RemoteState(conn = ConnState.ONLINE, loaded = true)
            remote.showForTest(expected)
            assertEquals(expected, remote.state.value)
        } finally {
            remote.close()
        }
    }
}
