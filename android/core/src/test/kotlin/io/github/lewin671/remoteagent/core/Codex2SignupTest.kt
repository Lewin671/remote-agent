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
                            val exact = RegisterDeviceRequest.parseFrom(body.readByteArray())
                            assertArrayEquals(
                                "registration was not durable before network",
                                draft!!.registration,
                                exact.toByteArray(),
                            )
                            registrations += exact
                        }
                        chain.proceed(chain.request())
                    }
                    .build()
            try {
                val result =
                    createAccount(
                        server.url,
                        http,
                        persistDraft = {
                            draft = CreatedAccount.readOperationMaterial(it.operationMaterial())
                        },
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
    fun correctedSetupTokenPreservesSignedRegistration() =
        runBlocking<Unit> {
            val server = TestServer()
            var draft: CreatedAccount? = null
            try {
                assertTrue(
                    runCatching {
                        createAccount(
                            server.url,
                            Api.defaultHttp(),
                            "wrong",
                            persistDraft = {
                                draft = CreatedAccount.readOperationMaterial(it.operationMaterial())
                            },
                        )
                    }
                        .isFailure,
                )
                val before = RegisterDeviceRequest.parseFrom(draft!!.registration)
                val result =
                    createAccount(
                        server.url,
                        Api.defaultHttp(),
                        server.setupToken,
                        draft = draft,
                        persistDraft = {
                            draft = CreatedAccount.readOperationMaterial(it.operationMaterial())
                        },
                    )
                val after = RegisterDeviceRequest.parseFrom(draft!!.registration)
                assertEquals(before.registration, after.registration)
                assertEquals(before.signature, after.signature)
                assertEquals(server.setupToken, after.setupToken)
                assertEquals(result.account.deviceId, draft!!.account.deviceId)
            } finally {
                server.close()
            }
        }

    @Test
    fun expiredSignupProofIsRefreshedWithoutResigningRegistration() =
        runBlocking<Unit> {
            val server = TestServer(signupMode = "open", signupBits = 8)
            var draft: CreatedAccount? = null
            try {
                assertTrue(
                    runCatching {
                        createAccount(
                            server.url,
                            Api.defaultHttp(),
                            persistDraft = {
                                draft = CreatedAccount.readOperationMaterial(it.operationMaterial())
                                if (
                                    draft!!.registration.isNotEmpty() &&
                                        RegisterDeviceRequest.parseFrom(draft!!.registration)
                                            .hasSignupProof()
                                )
                                    throw java.io.IOException(
                                        "synthetic interruption before registration",
                                    )
                            },
                        )
                    }
                        .isFailure,
                )
                val before = RegisterDeviceRequest.parseFrom(draft!!.registration)
                val invalid =
                    before
                        .toBuilder()
                        .setSignupProof(
                            before.signupProof
                                .toBuilder()
                                .setChallenge(
                                    com.google.protobuf.ByteString.copyFrom(ByteArray(32) { 99 }),
                                ),
                        )
                        .build()
                draft = draft!!.copy(registration = invalid.toByteArray())
                createAccount(
                    server.url,
                    Api.defaultHttp(),
                    draft = draft,
                    persistDraft = {
                        draft = CreatedAccount.readOperationMaterial(it.operationMaterial())
                    },
                )
                val after = RegisterDeviceRequest.parseFrom(draft!!.registration)
                assertEquals(before.registration, after.registration)
                assertEquals(before.signature, after.signature)
                assertNotEquals(invalid.signupProof, after.signupProof)
            } finally {
                server.close()
            }
        }

    @Test
    fun proofRequiredAtStartupReturnsToCreationInsteadOfBlockingAccountLoad() =
        runBlocking<Unit> {
            val server = TestServer(signupMode = "open", signupBits = 8)
            var draft: CreatedAccount? = null
            try {
                runCatching {
                    createAccount(
                        server.url,
                        Api.defaultHttp(),
                        persistDraft = {
                            draft = CreatedAccount.readOperationMaterial(it.operationMaterial())
                            if (draft!!.registration.isNotEmpty())
                                throw java.io.IOException("stopped before proof")
                        },
                    )
                }
                retryCreationRegistration(draft!!, Api.defaultHttp()) {
                    draft = CreatedAccount.readOperationMaterial(it.operationMaterial())
                }
                assertFalse(RegisterDeviceRequest.parseFrom(draft!!.registration).hasSignupProof())
            } finally {
                server.close()
            }
        }

    @Test
    fun excessiveProofDifficultyIsRejectedBeforeSolving() =
        runBlocking<Unit> {
            val server = TestServer(signupMode = "open", signupBits = 29)
            try {
                val failure = runCatching {
                    createAccount(server.url, Api.defaultHttp(), persistDraft = {})
                }
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
