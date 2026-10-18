package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.DeviceKind
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import java.io.File

class ApprovalTest {
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
