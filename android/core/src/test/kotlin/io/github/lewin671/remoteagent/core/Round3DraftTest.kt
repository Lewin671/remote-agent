package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class Round3DraftTest {
    @Test
    fun creationPersistsBeforeNetworkAndResumesLostPinResponse() =
        runBlocking<Unit> {
            val server = TestServer()
            val http = Api.defaultHttp()
            var draft: CreatedAccount? = null
            var dropped = false
            try {
                val flaky =
                    http
                        .newBuilder()
                        .addInterceptor { chain ->
                            assertNotNull("network preceded durable draft", draft)
                            val response = chain.proceed(chain.request())
                            if (
                                chain.request().url.encodedPath.endsWith("/PinSecurityDomain") &&
                                    !dropped
                            ) {
                                dropped = true
                                response.close()
                                throw java.io.IOException("lost pin response")
                            }
                            response
                        }
                        .build()
                assertTrue(
                    runCatching {
                            createAccount(
                                server.url,
                                flaky,
                                server.setupToken,
                                persistDraft = {
                                    draft = CreatedAccount.readOperationMaterial(it.operationMaterial())
                                },
                            )
                        }
                        .isFailure
                )
                val first = draft!!
                assertEquals("", first.recoveryCode)
                val resumed =
                    createAccount(
                        server.url,
                        http,
                        server.setupToken,
                        draft = first,
                        persistDraft = { draft = CreatedAccount.readOperationMaterial(it.operationMaterial()) },
                    )
                assertEquals(first.account.deviceId, resumed.account.deviceId)
                assertArrayEquals(first.account.root, resumed.account.root)
                assertNotNull(RecoveryCode.decode(resumed.recoveryCode))
            } finally {
                server.close()
            }
        }

    @Test
    fun recoveryPersistsExactSubmissionBeforeLostResponse() =
        runBlocking<Unit> {
            val server = TestServer()
            val http = Api.defaultHttp()
            val recoverySeed = ByteArray(32) { 0x31 }
            var saved: RecoveryPublication? = null
            var dropped = false
            try {
                val phone =
                    newAuthorityAccount(
                        randomBytes(32),
                        server.url,
                        http,
                        server.setupToken,
                        recoverySeed,
                    )
                val chain =
                    Recovery.context(
                        server.url,
                        http,
                        SigningKey(recoverySeed).publicKey,
                        recoverySeed,
                    )
                val flaky =
                    http
                        .newBuilder()
                        .addInterceptor { c ->
                            if (c.request().url.encodedPath.endsWith("/SubmitRecovery"))
                                assertNotNull(saved)
                            val response = c.proceed(c.request())
                            if (
                                c.request().url.encodedPath.endsWith("/SubmitRecovery") && !dropped
                            ) {
                                dropped = true
                                response.close()
                                throw java.io.IOException("lost submit response")
                            }
                            response
                        }
                        .build()
                val device = randomBytes(32)
                assertTrue(
                    runCatching {
                            Recovery.submit(
                                server.url,
                                flaky,
                                recoverySeed,
                                device,
                                chain.pin.genesis.copyOf(10),
                                persist = { saved = RecoveryPublication.parse(it.bytes()) },
                            )
                        }
                        .isFailure
                )
                val resumed =
                    Recovery.submit(
                        server.url,
                        http,
                        recoverySeed,
                        device,
                        chain.pin.genesis.copyOf(10),
                        draft = saved,
                        persist = { saved = RecoveryPublication.parse(it.bytes()) },
                    )
                assertArrayEquals(saved!!.submission.deviceSeed, resumed.deviceSeed)
                assertArrayEquals(saved!!.submission.stateHash, resumed.stateHash)
                assertEquals(phone.accountId, resumed.accountId)
                assertEquals("", saved!!.submission.newCode)
            } finally {
                server.close()
            }
        }
}
