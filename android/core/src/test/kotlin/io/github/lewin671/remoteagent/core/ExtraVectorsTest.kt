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

/** internal/deviceauth/testdata/recovery-v3.json and chain-v3.json, checked in Kotlin. */
class ExtraVectorsTest {
    private val recovery =
        Json.parseToJsonElement(File(System.getProperty("recoveryVectors")).readText()).jsonObject
    private val chain =
        Json.parseToJsonElement(File(System.getProperty("chainVectors")).readText()).jsonObject

    private fun s(o: JsonObject, key: String) = o[key]!!.jsonPrimitive.content
    private fun bytes(o: JsonObject, key: String) = Base64.getDecoder().decode(s(o, key))
    private fun state(o: JsonObject): DeviceSecurity.State = DeviceSecurity.parseState(bytes(o, "state"))

    private fun reject(fn: () -> Unit) {
        try {
            fn()
            fail("unsafe input accepted")
        } catch (_: DeviceSecurity.Failure) {}
    }

    @Test
    fun theRecoveryVectorOpensOnlyFromTheBoundaryOnward() {
        val genesis = state(recovery["genesis"]!!.jsonObject)
        val parent = state(recovery["parent"]!!.jsonObject)
        val rec = state(recovery["recovery"]!!.jsonObject)
        val pin = DeviceSecurity.pinOf(genesis)
        assertEquals(s(recovery["genesis"]!!.jsonObject, "hash"), pin.genesis.hex())
        DeviceSecurity.verifyGenesis(pin, genesis)
        DeviceSecurity.verifyTransition(genesis, parent)
        val statement =
            DeviceSecurity.parseRecoveryStatement(bytes(recovery, "recovery_statement"))
        DeviceSecurity.verifyTransition(parent, rec, statement)
        assertTrue(rec.prevSecret.isEmpty())
        assertNull(rec.core.member(parent.core.authorityID))
        assertEquals(0x88.toByte(), rec.core.recoveryEd[0])

        val newSeed = s(recovery, "new_phone_seed").unhex()
        val newX = s(recovery, "new_phone_x").unhex()
        val newId = deviceId(SigningKey(newSeed).publicKey)
        val record =
            DeviceSecurity.Record.fromChain(
                pin.account,
                pin.domain,
                pin.genesis,
                newId,
                newX,
                listOf(genesis, parent, rec).map { DeviceSecurity.stateBytes(it) },
                listOf(null, null, bytes(recovery, "recovery_statement")),
            )
        assertNull(record.contentKey(parent.core.contentEpoch))
        assertNotNull(record.contentKey(rec.core.contentEpoch))
    }

    @Test
    fun theChainVectorRejectsForkAndTamperedReplay() {
        val states = chain["states"]!!.jsonArray.map { state(it.jsonObject) }
        assertEquals(8, states.size)
        val pin = DeviceSecurity.pinOf(states[0])
        states.forEachIndexed { i, state ->
            DeviceSecurity.verifyState(pin, state)
            if (i > 0) DeviceSecurity.verifyTransition(states[i - 1], state)
        }
        // The last state is a normal renewal that rotates the recovery key.
        assertFalse(states.last().core.recoveryEd.contentEquals(states[states.size - 2].core.recoveryEd))
        assertEquals(1, states.last().core.removed.size)

        val fork = state(chain["fork"]!!.jsonObject)
        DeviceSecurity.verifyTransition(states[0], fork)
        assertFalse(
            DeviceSecurity.stateHash(fork).contentEquals(DeviceSecurity.stateHash(states[1])),
        )
        val bad = state(chain["bad_replay"]!!.jsonObject)
        reject { DeviceSecurity.verifyTransition(states[0], bad) }
    }
}
