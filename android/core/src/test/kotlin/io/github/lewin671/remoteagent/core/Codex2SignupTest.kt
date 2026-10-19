package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.RegisterDeviceRequest
import kotlinx.coroutines.runBlocking
import okio.Buffer
import org.junit.Assert.*
import org.junit.Test

class Codex2SignupTest {
    @Test
    fun openRelaySignupSolvesTheExistingChallengeAndKeepsTheDraftIdentity() =
        runBlocking<Unit> {
            val server = TestServer(signupMode = "open", signupBits = 8)
            val registrations = mutableListOf<RegisterDeviceRequest>()
            var draft: CreatedAccount? = null
            val http =
                Api.defaultHttp()
                    .newBuilder()
                    .addInterceptor { chain ->
                        assertNotNull("signup touched network before durable identity", draft)
                        if (chain.request().url.encodedPath.endsWith("/RegisterDevice")) {
                            val body = Buffer()
                            chain.request().body!!.writeTo(body)
                            registrations += RegisterDeviceRequest.parseFrom(body.readByteArray())
                        }
                        chain.proceed(chain.request())
                    }
                    .build()
            try {
                val result =
                    createAccount(
                        server.url,
                        http,
                        persistDraft = { draft = CreatedAccount.parseDraft(it.draftBytes()) },
                    )
                assertEquals(draft!!.account.deviceId, result.account.deviceId)
                assertEquals(2, registrations.size)
                assertFalse(registrations[0].hasSignupProof())
                assertTrue(registrations[1].hasSignupProof())
                assertEquals(registrations[0].registration, registrations[1].registration)
                assertEquals(registrations[0].signature, registrations[1].signature)
            } finally {
                server.close()
            }
        }

    @Test
    fun excessiveProofDifficultyIsRejectedBeforeSolving() =
        runBlocking<Unit> {
            val server = TestServer(signupMode = "open", signupBits = 29)
            try {
                val failure =
                    runCatching { createAccount(server.url, Api.defaultHttp(), persistDraft = {}) }
                        .exceptionOrNull()
                assertNotNull(failure)
                assertTrue(
                    "difficulty bound was not enforced: $failure",
                    failure!!.message.orEmpty().contains("28"),
                )
            } finally {
                server.close()
            }
        }
}
