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
            val status =
                io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus.newBuilder()
                    .setBootstrap(
                        io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_READY
                    )
                    .setServer("https://example.com")
                    .setDeviceId("recovered-device")
                    .setAccountId("acct-1")
                    .setIsMember(true)
                    .setIsAuthority(true)
                    .build()
            for (cancel in listOf(false, true)) {
                val file = kotlin.io.path.createTempFile("recovery-adoption").toFile()
                val durableIdentity = ByteArray(32) { 7 }
                file.writeBytes(durableIdentity)
                try {
                    val failure =
                        runCatching {
                                completeRecoveryAndAdopt(
                                    complete = { status },
                                    adopt = { _ ->
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
