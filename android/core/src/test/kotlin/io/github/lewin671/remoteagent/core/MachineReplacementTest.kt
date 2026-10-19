package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.MachineReplacementProof
import java.io.File
import java.util.Base64
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class MachineReplacementTest {
    @Test
    fun sharedReplacementLineagePreservesOnlyAuthorityApprovedOrigins() {
        val file =
            File(
                File(System.getProperty("kotlinVectors")).parentFile,
                "machine-replacement-vectors.json",
            )
        val v = Json.parseToJsonElement(file.readText()).jsonObject
        fun raw(s: String) = Base64.getDecoder().decode(s)
        val states =
            v["states"]!!.jsonArray.map { DeviceSecurity.parseState(raw(it.jsonPrimitive.content)) }
        val target = v["target"]!!.jsonPrimitive.content
        val old = v["old"]!!.jsonPrimitive.content
        val record =
            DeviceSecurity.Record(
                DeviceSecurity.pinOf(states[0]),
                states,
                target,
                raw(v["x"]!!.jsonPrimitive.content),
            )
        fun proofList(key: String) =
            v[key]!!.jsonArray.map {
                val p = it.jsonObject
                MachineReplacementProof.newBuilder()
                    .setApproval(ByteString.copyFrom(raw(p["approval"]!!.jsonPrimitive.content)))
                    .setSignature(ByteString.copyFrom(raw(p["signature"]!!.jsonPrimitive.content)))
                    .build()
            }
        val proofs = proofList("proofs")
        assertTrue(old in machineReplacementOrigins(record, target, proofs))
        assertTrue(old in machineReplacementOrigins(record, target, proofList("clock_skew_proofs")))
        assertThrows(DeviceSecurity.Failure::class.java) {
            machineReplacementOrigins(record, target, proofList("expired_proofs"))
        }
        assertFalse(old in machineReplacementOrigins(record, target, emptyList()))
        val bad = proofs.toMutableList()
        val signature = bad[0].signature.toByteArray()
        signature[0] = (signature[0].toInt() xor 1).toByte()
        bad[0] = bad[0].toBuilder().setSignature(ByteString.copyFrom(signature)).build()
        assertThrows(DeviceSecurity.Failure::class.java) {
            machineReplacementOrigins(record, target, bad)
        }
        assertThrows(DeviceSecurity.Failure::class.java) {
            machineReplacementOrigins(record, old, proofs)
        }
    }
}
