package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.DeviceKind
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import java.io.File
import kotlinx.coroutines.*

class ApprovalTest {
    @Test fun approvedPairingCanBeRetriedAfterDelivery() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val owner = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
        val sponsor = Remote(owner.account, http)
        try {
            withTimeout(15_000) {
                val pairing = Initiation.start(sponsor)
                val shown = CompletableDeferred<List<String>>()
                val joining = async { joinByPairing(pairing.link, http, { shown.complete(it) }, { false }) }
                assertEquals(pairing.joined().first, shown.await())
                val approving = async { pairing.approve() }
                val account = joining.await()
                approving.await()
                pairing.approve() // The same operation, not a fresh approval.
                assertTrue(login(Api(server.url, http), account).isNotBlank())
            }
        } finally {
            sponsor.close(); owner.close(); server.close()
        }
    }

    @Test fun verifiesGoApprovalAndCandidateIdentitySas() {
        val v = Json.parseToJsonElement(File(File(System.getProperty("vectors")).parentFile, "pair-approval-vectors.json").readText()).jsonObject
        fun b(k: String) = v[k]!!.jsonPrimitive.content.unhex()
        val id = v["pairing_id"]!!.jsonPrimitive.content
        val e = Enrollment(b("candidate_seed"), id, b("approval"), b("signature"))
        verifyPairApproval(e, b("root"), DeviceKind.DEVICE_KIND_CLIENT, 0)
        val bad = e.signature.copyOf(); bad[0] = (bad[0].toInt() xor 1).toByte()
        assertThrows(IllegalArgumentException::class.java) { verifyPairApproval(Enrollment(e.seed, id, e.approval, bad), b("root"), DeviceKind.DEVICE_KIND_CLIENT, 0) }
        assertThrows(IllegalArgumentException::class.java) { verifyPairApproval(e, b("root"), DeviceKind.DEVICE_KIND_MACHINE, 0) }
        val actual = Pairing.identityTranscript(id, b("initiator_public"), b("joiner_public"), b("candidate_public"), 2)
        assertArrayEquals(b("transcript"), actual)
        assertFalse(actual.contentEquals(Pairing.identityTranscript(id, b("initiator_public"), b("joiner_public"), ByteArray(32), 2)))
    }
}
