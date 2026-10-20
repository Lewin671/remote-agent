package io.github.lewin671.remoteagent.data

import android.os.Bundle
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.*
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assume.assumeTrue
import org.junit.Test

/** Synthetic history, real Android Keystore; no network or existing account is used. */
class HistoryOpenBenchmark {
    @Test
    fun openColdHistory() = runBlocking {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        assumeTrue(context.packageName.endsWith(".securityaudit"))
        val decrypts = mutableMapOf<String, Int>()
        var parses = 0
        val vault = Vault(context, { name -> decrypts[name] = (decrypts[name] ?: 0) + 1 }, { parses++ })
        vault.clear()
        val signer = SigningKey(ByteArray(32) { 1 })
        val x = ByteArray(32) { 2 }
        val phone = Authority.newMember(signer, x, 2u)
        val secret = ByteArray(32) { 3 }
        val genesis = Authority.genesis(
            "a".repeat(32), ByteArray(32) { 4 }, phone,
            SigningKey(ByteArray(32) { 5 }).publicKey, secret, signer, 1_700_000_000_000,
        )
        val machineSigner = SigningKey(ByteArray(32) { 7 })
        val machine = Authority.newMember(machineSigner, ByteArray(32) { 8 }, 1u)
        val state = Authority.pair(genesis, machine, secret, secret, signer, 1_700_000_000_001)
        val record = DeviceSecurity.Record.fromChain(
            state.core.account, state.core.domain, DeviceSecurity.stateHash(genesis), phone.id, x,
            listOf(genesis, state).map { DeviceSecurity.stateBytes(it) }, listOf(null, null),
        )
        val seed = ByteArray(32) { 6 }
        val plain = "synthetic history message".toByteArray()
        // Each envelope has its own nonce and sequence, just as persisted history does.
        val events = (1..300).map { seq ->
            val header = DeviceSecurity.Header(
                record.pin.account, record.pin.domain, state.core.epoch,
                DeviceSecurity.stateHash(state), state.core.contentEpoch, machine.id,
                DeviceSecurity.Purpose.EVENT.kind, DeviceSecurity.Purpose.EVENT,
                machine.id, "test-session", "", seq.toULong(), 0uL, 0u,
            )
            DeviceSecurity.envelope(DeviceSecurity.Wire(DeviceSecurity.form(secret, seed, header, plain, machineSigner)))
        }
        try {
            val samples = mutableListOf<Long>()
            repeat(3) { sample ->
                vault.clear()
                vault.initializeSecurity(record)
                decrypts.clear()
                parses = 0
                val client = DeviceSecurityClient(vault, phone.id, signer, fetchStates = { error("no network") })
                val start = System.nanoTime()
                for (event in events) assertArrayEquals(plain, client.openHistory(event, seed).second)
                val elapsed = (System.nanoTime() - start) / 1_000_000
                samples += elapsed
                val result = "sample=$sample events=${events.size} elapsedMs=$elapsed decrypts=$decrypts parses=$parses"
                instrumentation.sendStatus(0, Bundle().apply { putString("stream", "\nHistoryOpenBenchmark $result\n") })
                android.util.Log.i("HistoryOpenBenchmark", result)
                client.close()
            }
            instrumentation.sendStatus(0, Bundle().apply { putString("stream", "\nHistoryOpenBenchmark medianMs=${samples.sorted()[1]}\n") })
        } finally {
            vault.clear()
        }
    }
}
