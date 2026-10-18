package io.github.lewin671.remoteagent.core

import java.io.File
import java.util.Base64
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.*
import org.junit.Test

/** The Go v3 state chain, recovery proof and envelope vectors, checked in Kotlin. */
class DeviceSecurityTest {
    private val v =
        Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText()).jsonObject
    private val env =
        Json.parseToJsonElement(File(System.getProperty("v3Vectors")).readText()).jsonObject

    private fun s(o: JsonObject, key: String) = o[key]!!.jsonPrimitive.content
    private fun s(key: String) = s(v, key)
    private fun decode(key: String) = Base64.getDecoder().decode(s(key))

    private fun state(o: JsonObject): DeviceSecurity.State =
        DeviceSecurity.parseState(Base64.getDecoder().decode(s(o, "state")))

    private fun reject(fn: () -> Unit) {
        try {
            fn()
            fail("unsafe input accepted")
        } catch (expected: DeviceSecurity.Failure) {}
    }

    private fun genesis() = state(v["genesis"]!!.jsonObject)
    private fun added() = state(v["added"]!!.jsonObject)
    private fun renewal() = state(v["renewal"]!!.jsonObject)
    private fun recovery() = state(v["recovery"]!!.jsonObject)

    @Test
    fun parsesGoStatesAndVerifiesTheChain() {
        val g = genesis()
        val pin = DeviceSecurity.pinOf(g)
        assertEquals(s("account"), pin.account)
        assertEquals(s("domain"), pin.domain.hex())
        assertEquals(s(v["genesis"]!!.jsonObject, "hash"), pin.genesis.hex())
        DeviceSecurity.verifyGenesis(pin, g)
        DeviceSecurity.verifyTransition(g, added())
        DeviceSecurity.verifyTransition(added(), renewal())
        val statement = DeviceSecurity.parseRecoveryStatement(decode("recovery_statement"))
        DeviceSecurity.verifyTransition(renewal(), recovery(), statement)
        assertEquals(s(v["recovery"]!!.jsonObject, "hash"), DeviceSecurity.stateHash(recovery()).hex())
        assertTrue(recovery().prevSecret.isEmpty())
        assertNull(recovery().core.member(g.core.authorityID))
    }

    @Test
    fun opensTheGoGrantsAndWalksPrevSecret() {
        val g = genesis()
        val a = added()
        val r = renewal()
        val pin = DeviceSecurity.pinOf(g)
        val secret1 = s("content_secret_1").unhex()
        val secret2 = s("content_secret_2").unhex()
        assertArrayEquals(secret2, DeviceSecurity.openGrant(pin, a, g.core.authorityID, s("phone_x").unhex()))
        val phone =
            DeviceSecurity.Record.fromChain(
                pin.account,
                pin.domain,
                pin.genesis,
                g.core.authorityID,
                s("phone_x").unhex(),
                listOf(g, a, r).map { DeviceSecurity.stateBytes(it) },
                listOf(null, null, null),
            )
        assertArrayEquals(secret1, phone.contentKey(1uL))
        assertArrayEquals(secret2, phone.contentKey(2uL))
    }

    @Test
    fun aRecoveryBoundaryIsUnavailableToTheNewPhone() {
        val g = genesis()
        val a = added()
        val r = renewal()
        val rec = recovery()
        val pin = DeviceSecurity.pinOf(g)
        val statement = DeviceSecurity.parseRecoveryStatement(decode("recovery_statement"))
        val chain =
            listOf(g, a, r, rec).map { DeviceSecurity.stateBytes(it) }
        val proofs = listOf(null, null, null, decode("recovery_statement"))
        // A retained machine can still read what it had before the recovery.
        val machineId = a.core.members.first { it.kind == 1u }.id
        val retained =
            DeviceSecurity.Record.fromChain(
                pin.account,
                pin.domain,
                pin.genesis,
                machineId,
                s("machine_x").unhex(),
                chain,
                proofs,
            )
        // Without keys cached before the recovery, pre-recovery content is unavailable.
        assertNull(retained.contentKey(1uL))
        assertNull(retained.contentKey(2uL))
        assertArrayEquals(s("content_secret_3").unhex(), retained.contentKey(3uL))
        val cached =
            retained
                .withKey(1uL, s("content_secret_1").unhex())
                .withKey(2uL, s("content_secret_2").unhex())
        assertArrayEquals(s("content_secret_1").unhex(), cached.contentKey(1uL))
        assertArrayEquals(s("content_secret_2").unhex(), cached.contentKey(2uL))
        // The recovered phone has no grant in the old states, so it cannot open them at all.
        val oldAuthority = g.core.authorityID
        assertTrue(rec.core.members.none { it.id == oldAuthority })
        assertTrue(statement.signature.size == 64)
    }

    @Test
    fun buildsGenesisAndMembershipThatTheSameRulesAccept() {
        val domain = s("domain").unhex()
        val phoneSeed = s("phone_seed").unhex()
        val machineSeed = s("machine_seed").unhex()
        val recoveryEd = SigningKey(s("recovery_seed").unhex()).publicKey
        val signer = SigningKey(phoneSeed)
        val phone = Authority.newMember(signer, s("phone_x").unhex(), 2u)
        val g =
            Authority.genesis(
                s("account"),
                domain,
                phone,
                recoveryEd,
                s("content_secret_1").unhex(),
                signer,
                1_700_000_000_000,
            )
        val pin = DeviceSecurity.pinOf(g)
        DeviceSecurity.verifyGenesis(pin, g)
        assertEquals(phone.id, g.core.authorityID)
        assertEquals(1uL, g.core.contentEpoch)
        assertEquals(1_700_000_000_000uL + DeviceSecurity.LEASE_U, g.core.notAfter)
        val issued = 1_700_000_000_000uL
        val nearing = g.copy(core = g.core.copy(issuedAt = issued, notAfter = issued + 23uL * 3_600_000uL))
        assertTrue(Authority.needsRenewal(nearing, (issued + 12uL * 3_600_000uL).toLong()))

        val machine = Authority.newMember(SigningKey(machineSeed), s("machine_x").unhex(), 1u)
        val next =
            Authority.pair(
                g,
                machine,
                s("content_secret_1").unhex(),
                s("content_secret_2").unhex(),
                signer,
                1_700_000_100_000,
            )
        DeviceSecurity.verifyTransition(g, next)
        assertEquals(2uL, next.core.epoch)
        assertEquals(2uL, next.core.contentEpoch)
        assertEquals(60, next.prevSecret.size)
        assertArrayEquals(
            s("content_secret_1").unhex(),
            DeviceSecurity.openPrevSecret(s("content_secret_2").unhex(), next.prevSecret),
        )
    }

    @Test
    fun opensTheGoEnvelopeAndSeparatesCounters() {
        val state = DeviceSecurity.parseState(Base64.getDecoder().decode(s(env, "state")))
        val secret = s(env, "content_secret").unhex()
        val wire =
            DeviceSecurity.Wire(
                DeviceSecurity.Formed(
                    Base64.getDecoder().decode(s(env, "header")),
                    Base64.getDecoder().decode(s(env, "nonce")),
                    Base64.getDecoder().decode(s(env, "cipher")),
                    Base64.getDecoder().decode(s(env, "origin")),
                ),
            )
        val (h, plain) = DeviceSecurity.open(state, state, false, 0uL, wire, secret, null)
        assertArrayEquals(Base64.getDecoder().decode(s(env, "plain")), plain)
        assertArrayEquals(Base64.getDecoder().decode(s(env, "header")), DeviceSecurity.headerBytes(h))
        assertArrayEquals(wire.formed.cipher, DeviceSecurity.parseWire(DeviceSecurity.wireBytes(wire)).formed.cipher)
        val changed =
            DeviceSecurity.Wire(
                wire.formed.copy(
                    cipher = wire.formed.cipher.copyOf().also { it[0] = (it[0].toInt() xor 1).toByte() },
                ),
            )
        reject { DeviceSecurity.verifyOrigin(state, state, false, 0uL, changed) }
        assertFalse(
            DeviceSecurity.contentKey(secret, null, h)
                .contentEquals(
                    DeviceSecurity.contentKey(secret, null, h.copy(counter = h.counter + 1uL)),
                ),
        )
    }

    @Test
    fun rejectsTruncationAndForks() {
        val raw = Base64.getDecoder().decode(s(env, "header"))
        for (i in raw.indices) reject { DeviceSecurity.parseHeader(raw.copyOf(i)) }
        reject { DeviceSecurity.parseHeader(raw + byteArrayOf(0)) }
        val g = genesis()
        val a = added()
        reject { DeviceSecurity.verifyTransition(a, g) }
        val fork =
            DeviceSecurity.parseState(
                DeviceSecurity.stateBytes(
                    a.copy(core = a.core.copy(parent = DeviceSecurity.stateHash(g).also { it[0] = 1 }))
                )
            )
        reject { DeviceSecurity.verifyTransition(g, fork) }
    }

    @Test
    fun recoveryCodeRoundTripsAndIsTypedForgivingly() {
        val seed = ByteArray(32) { (it * 7 + 3).toByte() }
        val text = RecoveryCode.encode(seed)
        assertArrayEquals(seed, RecoveryCode.decode(text))
        assertArrayEquals(seed, RecoveryCode.decode(text.lowercase().replace("-", " ")))
        assertNull(RecoveryCode.decode("not a recovery code"))
        assertNull(RecoveryCode.decode(text + "Z"))
    }

    @Test
    fun matchesGoNotificationRoutingIdentifiers() {
        val g = genesis()
        val h =
            DeviceSecurity.Header(
                g.core.account,
                g.core.domain,
                g.core.epoch,
                DeviceSecurity.stateHash(g),
                g.core.contentEpoch,
                g.core.authorityID,
                10u,
                DeviceSecurity.Purpose.NOTIFY,
                g.core.authorityID,
                "s",
                "notify:1:7",
                0uL,
                0uL,
                0u,
                1uL,
            )
        assertEquals("notify:1:7", DeviceSecurity.parseHeader(DeviceSecurity.headerBytes(h)).request)
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
    }

    @Test
    fun counterSurvivesLeaseRenewalsButResetsWithTheContentKey() {
        val signer = SigningKey(s("phone_seed").unhex())
        val phone = Authority.newMember(signer, s("phone_x").unhex(), 2u)
        val g =
            Authority.genesis(
                s("account"),
                s("domain").unhex(),
                phone,
                SigningKey(s("recovery_seed").unhex()).publicKey,
                s("content_secret_1").unhex(),
                signer,
                1_700_000_000_000,
            )
        val pin = DeviceSecurity.pinOf(g)
        val store =
            TestSecurityStore(
                DeviceSecurity.Record.fromChain(
                    pin.account,
                    pin.domain,
                    pin.genesis,
                    phone.id,
                    s("phone_x").unhex(),
                    listOf(DeviceSecurity.stateBytes(g)),
                    listOf(null),
                ),
            )
        repeat(3) {
            val r = store.readSecurity()
            store.reserveSecurity(DeviceSecurity.stateHash(r.head))
        }
        val lease =
            Authority.renewal(
                store.readSecurity().head,
                s("content_secret_1").unhex(),
                signer,
                1_700_000_100_000,
            )
        store.installSecurity(
            DeviceSecurity.stateHash(store.readSecurity().head),
            listOf(lease),
            listOf(null),
        )
        assertEquals(3uL, store.readSecurity().counter)
        val pair =
            Authority.pair(
                store.readSecurity().head,
                Authority.newMember(SigningKey(s("machine_seed").unhex()), s("machine_x").unhex(), 1u),
                s("content_secret_1").unhex(),
                s("content_secret_2").unhex(),
                signer,
                1_700_000_200_000,
            )
        store.installSecurity(
            DeviceSecurity.stateHash(store.readSecurity().head),
            listOf(pair),
            listOf(null),
        )
        assertEquals(0uL, store.readSecurity().counter)
        assertEquals(DeviceSecurity.COUNTER_BLOCK_U, store.readSecurity().counterBlockEnd)
    }

    @Test
    fun rePairingKeepsTheEdIdentityAndOnlyReplacesX() {
        val signer = SigningKey(s("phone_seed").unhex())
        val phone = Authority.newMember(signer, s("phone_x").unhex(), 2u)
        val g =
            Authority.genesis(
                s("account"),
                s("domain").unhex(),
                phone,
                SigningKey(s("recovery_seed").unhex()).publicKey,
                s("content_secret_1").unhex(),
                signer,
                1_700_000_000_000,
            )
        val newX = ByteArray(32) { 0x5a }
        val rotated =
            Authority.rotateX(
                g,
                phone.id,
                newX,
                s("content_secret_1").unhex(),
                s("content_secret_2").unhex(),
                signer,
                1_700_000_100_000,
            )
        DeviceSecurity.verifyTransition(g, rotated)
        val m = rotated.core.member(phone.id)!!
        assertArrayEquals(phone.ed, m.ed)
        assertEquals(phone.id, m.id)
        assertArrayEquals(X25519.publicKey(newX), m.x)
    }

    @Test
    fun anExpiredLeasePausesControlButNotHistory() {
        val g = genesis()
        val state = added()
        val secret = s("content_secret_2").unhex()
        val signer = SigningKey(s("phone_seed").unhex())
        val h =
            DeviceSecurity.Header(
                state.core.account,
                state.core.domain,
                state.core.epoch,
                DeviceSecurity.stateHash(state),
                state.core.contentEpoch,
                g.core.authorityID,
                5u,
                DeviceSecurity.Purpose.COMMAND,
                state.core.members.first { it.kind == 1u }.id,
                "",
                "0123456789abcdef",
                0uL,
                state.core.notAfter,
                0u,
                1uL,
            )
        val wire = DeviceSecurity.Wire(DeviceSecurity.form(secret, null, h, byteArrayOf(1, 2, 3), signer))
        DeviceSecurity.verifyOrigin(state, state, true, state.core.notAfter, wire)
        reject { DeviceSecurity.verifyOrigin(state, state, true, state.core.notAfter + 1uL, wire) }
    }

}
