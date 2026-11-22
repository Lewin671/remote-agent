package io.github.lewin671.remoteagent.data

import android.os.Bundle
import androidx.test.platform.app.InstrumentationRegistry
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.MemorySecurityStoreV4
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageOp
import io.github.lewin671.remoteagent.core.proto.SecurityStorageWrite
import io.github.lewin671.remoteagent.core.proto.SecurityStoreBucket
import io.github.lewin671.remoteagent.corebind.Corebind
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assume.assumeTrue
import org.junit.Test

/** Synthetic history, real Android Keystore v4 Vault; no network or existing account is used. */
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
        val machineStore = MemorySecurityStoreV4().apply {
            seedTestRecord(
                edPrivate = BenchmarkVectors.machineSeed,
                edPublic = BenchmarkVectors.machineEdPub,
                xPrivate = BenchmarkVectors.machineX,
                xPublic = Corebind.x25519PublicKey(BenchmarkVectors.machineX),
                recordBytes = BenchmarkVectors.machineRecordBytes,
            )
        }
        val seed = ByteArray(32) { 6 }
        val plain = "synthetic history message".toByteArray()
        // Each envelope has its own nonce and sequence, just as persisted history does.
        val events = BenchmarkCoreSecurity(
            machineStore,
            BenchmarkVectors.ACCOUNT_ID,
            BenchmarkVectors.machineId,
            BenchmarkVectors.SERVER,
            BenchmarkVectors.nowMs + 1,
        ).use { sealer ->
            (1..300).map { seq -> sealer.sealEvent("test-session", seq.toLong(), seed, plain) }
        }
        try {
            val samples = mutableListOf<Long>()
            repeat(3) { sample ->
                vault.clear()
                val seedRes = vault.executeStorage(
                    SecurityStorageEffect.newBuilder()
                        .setOpId(1L)
                        .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_BATCH_COMMIT)
                        .addBatch(
                            SecurityStorageWrite.newBuilder()
                                .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD)
                                .setKey("record")
                                .setCreateOnly(true)
                                .setNextRevision("r1")
                                .setData(ByteString.copyFrom(BenchmarkVectors.phoneRecordBytes)),
                        )
                        .build(),
                )
                assertEquals("", seedRes.error)
                vault.clearMemoryCache()
                decrypts.clear()
                parses = 0
                val client = BenchmarkCoreSecurity(
                    vault,
                    BenchmarkVectors.ACCOUNT_ID,
                    BenchmarkVectors.phoneId,
                    BenchmarkVectors.SERVER,
                    BenchmarkVectors.nowMs + 1,
                )
                val start = System.nanoTime()
                for (event in events) assertArrayEquals(plain, client.openHistory(event, seed))
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
