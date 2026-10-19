package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import java.io.IOException
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class RecoveryAdoptionTest {
    @Test
    fun failedOrCanceledAdoptionRetainsTheCompletedRecoveryIdentity() =
        runBlocking<Unit> {
            val signer = SigningKey(ByteArray(32) { 1 })
            val x = ByteArray(32) { 2 }
            val account =
                Account(
                    "https://example.com",
                    ByteArray(32) { 3 },
                    ByteArray(32) { 1 },
                    deviceId(signer.publicKey),
                )
            val member = Authority.newMember(signer, x, 2u)
            val genesis =
                Authority.genesis(
                    account.accountId,
                    ByteArray(32) { 4 },
                    member,
                    SigningKey(ByteArray(32) { 5 }).publicKey,
                    ByteArray(32) { 6 },
                    signer,
                    System.currentTimeMillis(),
                )
            val record =
                DeviceSecurity.Record(DeviceSecurity.pinOf(genesis), listOf(genesis), member.id, x)
            for (cancel in listOf(false, true)) {
                val file = kotlin.io.path.createTempFile("recovery-adoption").toFile()
                val durableIdentity = ByteArray(32) { 7 }
                file.writeBytes(durableIdentity)
                try {
                    val failure =
                        runCatching {
                                completeRecoveryAndAdopt(
                                    complete = { account to record },
                                    adopt = { _, _ ->
                                        assertTrue(
                                            "draft was removed before adoption",
                                            file.exists(),
                                        )
                                        if (cancel)
                                            throw CancellationException(
                                                "process stopped during adoption"
                                            )
                                        throw IOException("account save failed")
                                    },
                                )
                            }
                            .exceptionOrNull()
                    assertTrue(
                        if (cancel) failure is CancellationException else failure is IOException
                    )
                    assertTrue("completed recovery cannot be resumed", file.exists())
                    assertArrayEquals(durableIdentity, file.readBytes())
                } finally {
                    file.delete()
                }
            }
        }
}
