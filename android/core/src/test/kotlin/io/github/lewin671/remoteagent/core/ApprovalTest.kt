package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.DeviceKind
import java.io.File
import kotlinx.coroutines.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class ApprovalTest {
    @Test
    fun approvedPairingCanBeRetriedAfterDelivery() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val owner = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val sponsor = fixtureRemote(owner.phone, http)
        try {
            owner.putMachine("synthetic-manager")
            owner.connect()
            owner.waitReady()
            withTimeout(15_000) {
                val pairing = Initiation.start(sponsor)
                val shown = CompletableDeferred<List<String>>()
                val joining = async {
                    joinByPairing(
                        pairing.link,
                        http,
                        { shown.complete(it) },
                        { false },
                        persist = { acct, record ->
                            TestSecurity.get(acct.server).stores[acct.deviceId] =
                                TestSecurityStore(record)
                        },
                        confirm = { true },
                    )
                }
                assertEquals(pairing.joined().first, shown.await())
                val approving = async { pairing.approve() }
                val account = joining.await()
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
                val pairing = Initiation.start(sponsor)
                val displayed = CompletableDeferred<List<String>>()
                var persisted = false
                val joining = async {
                    runCatching {
                        joinByPairing(
                            pairing.link,
                            http,
                            { displayed.complete(it) },
                            { false },
                            persist = { _, _ -> persisted = true },
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
