package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class HistoryKeyCacheTest {
    private val signer = SigningKey(ByteArray(32) { 1 })
    private val x = ByteArray(32) { 2 }
    private val phone = Authority.newMember(signer, x, 2u)
    private val machineSigner = SigningKey(ByteArray(32) { 3 })
    private val machine = Authority.newMember(machineSigner, ByteArray(32) { 4 }, 1u)
    private val secret = ByteArray(32) { 5 }
    private val genesis = Authority.genesis("a".repeat(32), ByteArray(32) { 6 }, phone,
        SigningKey(ByteArray(32) { 7 }).publicKey, secret, signer, 1_700_000_000_000)
    private val state = Authority.pair(genesis, machine, secret, secret, signer, 1_700_000_000_001)
    private val initial = DeviceSecurity.Record.fromChain(
        genesis.core.account, genesis.core.domain, DeviceSecurity.stateHash(genesis), phone.id, x,
        listOf(genesis, state).map { DeviceSecurity.stateBytes(it) }, listOf(null, null))
    private var current = initial
    private var clearCount = 0
    private var readFailure = false
    private val store = object : DeviceSecurity.Storage by TestSecurityStore(initial) {
        override fun readSecurity(): DeviceSecurity.Record {
            if (readFailure) throw DeviceSecurity.Failure("storage")
            return current
        }
        override fun clearSecurityCache() { clearCount++ }
        override fun cacheSecurityKey(expectedState: ByteArray, epoch: ULong, secret: ByteArray) {}
    }
    private val client = DeviceSecurityClient(store, phone.id, signer,
        expectedAccount = initial.pin.account, fetchStates = { error("no network") })
    private val seed = ByteArray(32) { 8 }
    private val plain = "history".toByteArray()
    private fun envelope(signer: SigningKey = machineSigner): io.github.lewin671.remoteagent.proto.Envelope {
        val h = DeviceSecurity.Header(initial.pin.account, initial.pin.domain, state.core.epoch,
            DeviceSecurity.stateHash(state), state.core.contentEpoch, machine.id,
            DeviceSecurity.Purpose.EVENT.kind, DeviceSecurity.Purpose.EVENT,
            machine.id, "test-session", "", 1uL, 0uL, 0u)
        return DeviceSecurity.envelope(DeviceSecurity.Wire(DeviceSecurity.form(secret, seed, h, plain, signer)))
    }

    @Test fun aDifferentRecordWithTheSameHeadCannotReuseAMemoizedKey() = runBlocking {
        val event = envelope()
        assertArrayEquals(plain, client.openHistory(event, seed).second)
        current = initial.withKey(state.core.contentEpoch, ByteArray(32) { 9 })
        assertTrue(runCatching { client.openHistory(event, seed) }.isFailure)
        current = DeviceSecurity.Record.parse(initial.bytes())
        assertArrayEquals(plain, client.openHistory(event, seed).second)
    }

    @Test fun returnedKeysCannotMutateTheRecordOrMemoizedKeys() {
        current = initial.withKey(state.core.contentEpoch, secret.copyOf())
        client.contentKey(state.core.contentEpoch)!!.fill(0)
        assertArrayEquals(secret, client.contentKey(state.core.contentEpoch))
        assertArrayEquals(secret, current.contentKey(state.core.contentEpoch))
    }

    @Test fun closeClearsStorageAndPreventsFurtherHistoryReads() = runBlocking {
        val event = envelope()
        client.openHistory(event, seed)
        client.close()
        assertEquals(1, clearCount)
        assertTrue(runCatching { client.openHistory(event, seed) }.isFailure)
    }

    @Test fun storageFailureAndUntrustedIdentityCannotUseCachedKeys() = runBlocking {
        val event = envelope()
        client.openHistory(event, seed)
        readFailure = true
        assertTrue(runCatching { client.openHistory(event, seed) }.isFailure)
        assertEquals(1, clearCount)
        readFailure = false
        current = DeviceSecurity.Record(initial.pin, initial.chain, machine.id, ByteArray(32) { 4 })
        val failure = runCatching { client.openHistory(event, seed) }.exceptionOrNull()
        assertEquals("untrusted", (failure as DeviceSecurity.Failure).code)
    }

    @Test fun cachedContentKeysDoNotSkipEnvelopeSignatureVerification() = runBlocking {
        val event = envelope()
        client.openHistory(event, seed)
        val broken = event.toBuilder().setSignature(com.google.protobuf.ByteString.copyFrom(ByteArray(64))).build()
        assertTrue(runCatching { client.openHistory(broken, seed) }.isFailure)
    }
}
