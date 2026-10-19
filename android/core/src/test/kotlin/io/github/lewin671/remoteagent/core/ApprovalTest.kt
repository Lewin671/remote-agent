package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.DeviceKind
import java.io.File
import kotlinx.coroutines.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class ApprovalTest {
    @Test
    fun approvalSurvivesAuthorityRestartUntilCandidateReceipt() =
        runBlocking<Unit> {
            val server = TestServer()
            val http = Api.defaultHttp()
            val owner = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
            val sponsor = fixtureRemote(owner.phone, http)
            val restarted = fixtureRemote(owner.phone, http)
            try {
                withTimeout(15_000) {
                    val pairing = Initiation.start(sponsor, pollIntervalMs = 10)
                    val shown = CompletableDeferred<List<String>>()
                    val persisted = CompletableDeferred<Unit>()
                    val receipt = CompletableDeferred<Unit>()
                    val joining = async {
                        joinByPairing(
                            pairing.link,
                            http,
                            { shown.complete(it) },
                            { false },
                            persistRegistration = {},
                            persistIdentity = {},
                            persist = { acct, record ->
                                TestSecurity.get(server.url).stores[acct.deviceId] =
                                    TestSecurityStore(record)
                                persisted.complete(Unit)
                                receipt.await()
                            },
                            confirm = { true },
                            pollIntervalMs = 10,
                        )
                    }
                    assertEquals(pairing.joined().first, shown.await())
                    val approving = async { pairing.approve() }
                    persisted.await()
                    val storage = TestSecurity.get(server.url).stores.getValue(owner.phone.deviceId)
                    val pending = storage.readSecurityOperation()!!
                    val signed = pending.signed.copyOf()
                    val epoch = sponsor.security.head().core.epoch
                    approving.cancelAndJoin() // the authority's approval screen/process disappears
                    restarted.resumeSecurityOperation()
                    assertArrayEquals(signed, storage.readSecurityOperation()!!.signed)
                    assertEquals(epoch, restarted.security.head().core.epoch)
                    receipt.complete(Unit)
                    joining.await()
                    restarted.resumeSecurityOperation()
                    assertNull(storage.readSecurityOperation())
                    assertEquals(epoch, restarted.security.head().core.epoch)
                }
            } finally {
                sponsor.close()
                restarted.close()
                owner.close()
                server.close()
            }
        }

    @Test
    fun approvedPairingCanBeRetriedAfterDelivery() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val owner = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val sponsor = fixtureRemote(owner.phone, http)
        val candidateSaved = java.util.concurrent.atomic.AtomicBoolean(false)
        val accountSaved = java.util.concurrent.atomic.AtomicBoolean(false)
        val intentSaved = java.util.concurrent.atomic.AtomicBoolean(false)
        val savedIntent = java.util.concurrent.atomic.AtomicReference<ByteArray>()
        val joiningHttp =
            http
                .newBuilder()
                .addInterceptor { chain ->
                    if (chain.request().url.encodedPath.endsWith("/JoinPairing"))
                        assertTrue(
                            "candidate keys were published before durable save",
                            candidateSaved.get(),
                        )
                    if (chain.request().url.encodedPath.endsWith("/RegisterDevice")) {
                        assertTrue("registration preceded security record save", accountSaved.get())
                        val body = okio.Buffer()
                        chain.request().body!!.writeTo(body)
                        assertArrayEquals(
                            RegistrationIntent.parse(savedIntent.get()).request.toByteArray(),
                            body.readByteArray(),
                        )
                    }
                    chain.proceed(chain.request())
                }
                .build()
        try {
            owner.putMachine("synthetic-manager")
            owner.connect()
            owner.waitReady()
            withTimeout(15_000) {
                val pairing = Initiation.start(sponsor, pollIntervalMs = 10)
                val shown = CompletableDeferred<List<String>>()
                val joining = async {
                    joinByPairing(
                        pairing.link,
                        joiningHttp,
                        { shown.complete(it) },
                        { false },
                        persistRegistration = { intent ->
                            savedIntent.set(intent.bytes())
                            RegistrationIntent.parse(intent.bytes())
                            intentSaved.set(true)
                        },
                        persistIdentity = { candidateSaved.set(true) },
                        persist = { acct, record ->
                            assertTrue(
                                "account saved before registration intent",
                                intentSaved.get(),
                            )
                            accountSaved.set(true)
                            TestSecurity.get(acct.server).stores[acct.deviceId] =
                                TestSecurityStore(record)
                        },
                        confirm = { true },
                        pollIntervalMs = 10,
                    )
                }
                assertEquals(pairing.joined().first, shown.await())
                val approving = async { pairing.approve() }
                val account = joining.await()
                val restored = RegistrationIntent.parse(savedIntent.get())
                val retried =
                    restored.resume(joiningHttp) { acct, record ->
                        TestSecurity.get(acct.server).stores[acct.deviceId] =
                            TestSecurityStore(record)
                    }
                assertEquals(account.deviceId, retried.deviceId)
                approving.await()
                pairing.approve() // The same operation, not a fresh approval.
                assertTrue(login(Api(server.url, http), account).isNotBlank())
            }
        } finally {
            sponsor.close()
            owner.close()
            server.close()
        }
    }

    @Test
    fun aRotatedRecoveryCodeIsPublishedOnlyAfterTheUserConfirms() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val owner = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val sponsor = fixtureRemote(owner.phone, http)
        try {
            owner.putMachine("synthetic-manager")
            owner.connect()
            owner.waitReady()
            withTimeout(15_000) {
                val before = sponsor.security.head()
                val prepared = sponsor.prepareRecovery()
                assertNotNull(RecoveryCode.decode(prepared.code))
                // Nothing is published before the user confirms the new code.
                assertEquals(before.core.epoch, sponsor.security.head().core.epoch)
                assertTrue(
                    before.core.recoveryEd.contentEquals(
                        sponsor.security.head().core.recoveryEd,
                    ),
                )
                sponsor.publishRecovery(prepared)
                assertEquals(before.core.epoch + 1uL, sponsor.security.head().core.epoch)
                assertFalse(
                    before.core.recoveryEd.contentEquals(
                        sponsor.security.head().core.recoveryEd,
                    ),
                )
            }
        } finally {
            sponsor.close()
            owner.close()
            server.close()
        }
    }

    @Test
    fun candidateDoesNotSignOrPersistWithoutLocalSasConfirmation() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val owner = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val sponsor = fixtureRemote(owner.phone, http)
        try {
            owner.putMachine("synthetic-manager")
            owner.connect()
            owner.waitReady()
            withTimeout(15000) {
                val pairing = Initiation.start(sponsor, pollIntervalMs = 10)
                val displayed = CompletableDeferred<List<String>>()
                var persisted = false
                val joining = async {
                    runCatching {
                        joinByPairing(
                            pairing.link,
                            http,
                            { displayed.complete(it) },
                            { false },
                            persistRegistration = {},
                            persistIdentity = {},
                            persist = { _, _ -> persisted = true },
                            pollIntervalMs = 10,
                        )
                    }
                }
                assertEquals(pairing.joined().first, displayed.await())
                val failure = joining.await().exceptionOrNull()
                assertNotNull(failure)
                assertTrue(failure!!.message.orEmpty().contains("本机未确认"))
                assertFalse(persisted)
                assertEquals(2, sponsor.security.head().core.members.size)
                pairing.cancelOnServer()
            }
        } finally {
            sponsor.close()
            owner.close()
            server.close()
        }
    }

    @Test
    fun verifiesGoApprovalAndCandidateIdentitySas() {
        val v =
            Json.parseToJsonElement(
                    File(
                            File(System.getProperty("vectors")).parentFile,
                            "pair-approval-vectors.json",
                        )
                        .readText(),
                )
                .jsonObject
        fun b(k: String) = v[k]!!.jsonPrimitive.content.unhex()
        val id = v["pairing_id"]!!.jsonPrimitive.content
        val e = Enrollment(b("candidate_seed"), id, b("approval"), b("signature"))
        verifyPairApproval(e, b("root"), DeviceKind.DEVICE_KIND_CLIENT, 0)
        val bad = e.signature.copyOf()
        bad[0] = (bad[0].toInt() xor 1).toByte()
        assertThrows(IllegalArgumentException::class.java) {
            verifyPairApproval(
                Enrollment(e.seed, id, e.approval, bad),
                b("root"),
                DeviceKind.DEVICE_KIND_CLIENT,
                0,
            )
        }
        assertThrows(IllegalArgumentException::class.java) {
            verifyPairApproval(e, b("root"), DeviceKind.DEVICE_KIND_MACHINE, 0)
        }
        val actual =
            Pairing.identityTranscript(
                id,
                b("initiator_public"),
                b("joiner_public"),
                b("candidate_public"),
                2,
            )
        assertArrayEquals(b("transcript"), actual)
        assertFalse(
            actual.contentEquals(
                Pairing.identityTranscript(
                    id,
                    b("initiator_public"),
                    b("joiner_public"),
                    ByteArray(32),
                    2,
                ),
            ),
        )
    }
}
