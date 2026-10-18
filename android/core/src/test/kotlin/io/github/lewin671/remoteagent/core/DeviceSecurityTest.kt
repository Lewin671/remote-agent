package io.github.lewin671.remoteagent.core

import java.io.File
import java.util.Base64
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.*
import org.junit.Test

class DeviceSecurityTest {
    private val v =
        Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText()).jsonObject

    private fun s(key: String) = v[key]!!.jsonPrimitive.content

    private fun bytes(key: String) = Base64.getDecoder().decode(s(key))

    private fun array(p: JsonObject, key: String) =
        p[key]!!.jsonArray.map { it.jsonPrimitive.int.toByte() }.toByteArray()

    private fun pin(): DeviceSecurity.Pin {
        val p = v["Pin"]!!.jsonObject
        return DeviceSecurity.Pin(
            p["Account"]!!.jsonPrimitive.content,
            array(p, "Domain"),
            array(p, "Genesis"),
            p["AuthorityID"]!!.jsonPrimitive.content,
            array(p, "AuthorityEd"),
            array(p, "AuthorityX"),
        )
    }

    private fun reject(fn: () -> Unit) {
        try {
            fn()
            fail("unsafe input accepted")
        } catch (expected: DeviceSecurity.Failure) {}
    }

    @Test
    fun validatesCanonicalStateAndRecipientGrant() {
        val p = pin()
        val genesis = DeviceSecurity.parseState(bytes("State"))
        val state = DeviceSecurity.parseState(bytes("NextState"))
        DeviceSecurity.verifyState(p, genesis)
        DeviceSecurity.advance(p, genesis, state)
        assertArrayEquals(bytes("NextState"), DeviceSecurity.stateBytes(state))
        assertArrayEquals(
            s("Secret").unhex(),
            DeviceSecurity.openGrant(p, state, s("RecipientID"), s("WriterX").unhex()),
        )
        reject { DeviceSecurity.advance(p, state, genesis) }
        val changed =
            state.copy(
                core =
                    state.core.copy(
                        authorityEd =
                            state.core.authorityEd.copyOf().also {
                                it[0] = (it[0].toInt() xor 1).toByte()
                            },
                    ),
            )
        reject { DeviceSecurity.verifyState(p, changed) }
    }

    @Test
    fun exactCipherSignaturesKDFAndProjection() {
        val p = pin()
        val state = DeviceSecurity.parseState(bytes("NextState"))
        val wire = DeviceSecurity.parseWire(bytes("Wire"))
        val (h, plain) =
            DeviceSecurity.open(p, state, wire, s("Secret").unhex(), s("SessionSeed").unhex())
        assertArrayEquals(bytes("Plain"), plain)
        assertEquals(
            s("Key"),
            DeviceSecurity.contentKey(s("Secret").unhex(), s("SessionSeed").unhex(), h).hex(),
        )
        assertArrayEquals(bytes("Header"), DeviceSecurity.headerBytes(h))
        assertArrayEquals(bytes("Wire"), DeviceSecurity.wireBytes(wire))
        val env = DeviceSecurity.envelope(wire)
        assertArrayEquals(bytes("Wire"), DeviceSecurity.wireBytes(DeviceSecurity.fromEnvelope(env)))
        val bad =
            wire.copy(
                formed =
                    wire.formed.copy(
                        cipher =
                            wire.formed.cipher.copyOf().also {
                                it[0] = (it[0].toInt() xor 1).toByte()
                            },
                    ),
            )
        reject { DeviceSecurity.verifyWire(p, state, bad) }
        val auth =
            wire.copy(
                authSignature =
                    wire.authSignature.copyOf().also { it[0] = (it[0].toInt() xor 1).toByte() },
            )
        reject { DeviceSecurity.verifyWire(p, state, auth) }
    }

    @Test
    fun rejectsTruncationAndSeparatesCounterKeys() {
        val raw = bytes("Header")
        for (i in raw.indices) reject { DeviceSecurity.parseHeader(raw.copyOf(i)) }
        reject { DeviceSecurity.parseHeader(raw + byteArrayOf(0)) }
        val h = DeviceSecurity.parseHeader(raw)
        val secret = s("Secret").unhex()
        val seed = s("SessionSeed").unhex()
        assertFalse(
            DeviceSecurity.contentKey(secret, seed, h)
                .contentEquals(
                    DeviceSecurity.contentKey(secret, seed, h.copy(counter = h.counter + 1u)),
                ),
        )
        val c =
            DeviceSecurity.form(
                secret,
                seed,
                h.copy(counter = h.counter + 1u),
                bytes("Plain"),
                SigningKey(s("WriterSeed").unhex()),
            )
        assertEquals(64, c.origin.size)
        assertEquals(32, DeviceSecurity.commitment(c).size)
    }

    @Test
    fun opensGoNonceBoundHPKERPC() {
        val p = pin()
        val r = v["Reply"]!!.jsonObject
        val w = v["Waiter"]!!.jsonObject
        val capsule =
            DeviceSecurityRPC.Capsule(
                r["From"]!!.jsonPrimitive.content,
                r["To"]!!.jsonPrimitive.content,
                r["Method"]!!.jsonPrimitive.int.toUInt(),
                Base64.getDecoder().decode(r["Enc"]!!.jsonPrimitive.content),
                Base64.getDecoder().decode(r["Cipher"]!!.jsonPrimitive.content),
            )
        val waiter =
            DeviceSecurityRPC.Waiter(
                p,
                w["Sender"]!!.jsonPrimitive.content,
                w["Method"]!!.jsonPrimitive.int.toUInt(),
                array(w, "Nonce"),
                array(w, "RequestHash"),
            )
        val x = s("WriterX").unhex()
        val reply = DeviceSecurityRPC.reply(waiter, capsule, x)
        val wire = DeviceSecurity.parseWire(bytes("Wire"))
        val state = DeviceSecurity.parseState(bytes("NextState"))
        DeviceSecurityRPC.attach(wire.formed, reply, p, state)
        assertArrayEquals(
            DeviceSecurityRPC.capsuleBytes(capsule),
            DeviceSecurityRPC.capsuleBytes(
                DeviceSecurityRPC.parseCapsule(DeviceSecurityRPC.capsuleBytes(capsule)),
            ),
        )
        reject {
            DeviceSecurityRPC.reply(
                waiter.copy(
                    nonce = waiter.nonce.copyOf().also { it[0] = (it[0].toInt() xor 1).toByte() },
                ),
                capsule,
                x,
            )
        }
        val (request, _) =
            DeviceSecurityRPC.request(
                p,
                s("RecipientID"),
                SigningKey(s("WriterSeed").unhex()),
                1u,
                DeviceSecurityRPC.authorizationRequest(wire.formed),
            )
        assertTrue(request.cipher.size > 16)
    }

    @Test
    fun validatesGoCompletionAndEveryFinalAck() {
        val v =
            Json.parseToJsonElement(File(System.getProperty("completionVectors")).readText())
                .jsonObject
        val p = v["Pin"]!!.jsonObject
        val pin =
            DeviceSecurity.Pin(
                p["Account"]!!.jsonPrimitive.content,
                array(p, "Domain"),
                array(p, "Genesis"),
                p["AuthorityID"]!!.jsonPrimitive.content,
                array(p, "AuthorityEd"),
                array(p, "AuthorityX"),
            )
        val chain =
            v["Chain"]!!.jsonArray.map {
                DeviceSecurity.parseState(Base64.getDecoder().decode(it.jsonPrimitive.content))
            }
        val original = v["Original"]!!.jsonObject
        DeviceSecurityRemoval.verify(pin, v["Completion"]!!, original, chain)
        reject { DeviceSecurityRemoval.verify(pin, v["Completion"]!!, original, chain.drop(1)) }
    }

    @Test
    fun matchesGoNotificationRoutingIdentifiers() {
        val h =
            DeviceSecurity.parseHeader(bytes("Header"))
                .copy(kind = 10u, purpose = DeviceSecurity.Purpose.NOTIFY, request = "notify:1:7")
        assertEquals(
            "notify:1:7",
            DeviceSecurity.parseHeader(DeviceSecurity.headerBytes(h)).request,
        )
    }

    @Test
    fun legacyBodiesCannotEstablishCurrentInputState() {
        val forged =
            io.github.lewin671.remoteagent.proto.Event.newBuilder()
                .setSeq(1)
                .setInputState(
                    io.github.lewin671.remoteagent.proto.InputState.newBuilder()
                        .setRequestId("same-id")
                        .setStatus(
                            io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_CANCELED,
                        ),
                )
                .build()
        val old =
            SessionEvents(
                events = listOf(forged),
                loaded = true,
                unverifiedLegacy = true,
                verifiedEvents = emptyList(),
            )
        assertFalse(currentState(old).inputs.containsKey("same-id"))
        val current =
            io.github.lewin671.remoteagent.proto.Event.newBuilder()
                .setSessionId("synthetic-session")
                .setInputState(
                    io.github.lewin671.remoteagent.proto.InputState.newBuilder()
                        .setRequestId("same-id")
                        .setStatus(
                            io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_QUEUED,
                        )
                        .setText("trusted owner queue"),
                )
                .build()
        val live =
            io.github.lewin671.remoteagent.proto.LiveSnapshot.newBuilder()
                .setSession(
                    io.github.lewin671.remoteagent.proto.Session.newBuilder()
                        .setId("synthetic-session"),
                )
                .setRunning(true)
                .addActions(current)
                .build()
        val derived = currentState(old.copy(live = live))
        assertEquals(
            io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_QUEUED,
            derived.inputs["same-id"],
        )
        assertEquals("trusted owner queue", derived.accepted["same-id"]?.text)
        assertTrue(derived.running)
    }
}
