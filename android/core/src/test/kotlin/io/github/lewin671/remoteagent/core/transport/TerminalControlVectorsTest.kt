package io.github.lewin671.remoteagent.core.transport

import com.google.protobuf.CodedInputStream
import com.google.protobuf.WireFormat
import io.github.lewin671.remoteagent.proto.Ack
import io.github.lewin671.remoteagent.proto.AttachPath
import io.github.lewin671.remoteagent.proto.Barrier
import io.github.lewin671.remoteagent.proto.BarrierReply
import io.github.lewin671.remoteagent.proto.ClosePath
import io.github.lewin671.remoteagent.proto.InputStatusReply
import io.github.lewin671.remoteagent.proto.PeerInputStatus
import io.github.lewin671.remoteagent.proto.Replay
import io.github.lewin671.remoteagent.proto.StreamFailed
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class TerminalControlVectorsTest {
    private fun loadFixture(): File {
        val rel = "testdata/transport/terminal/control-vectors.json"
        var dir: File? = File("").absoluteFile
        while (dir != null && !File(dir, rel).isFile) dir = dir.parentFile
        return File(checkNotNull(dir) { "$rel not found above ${File("").absolutePath}" }, rel)
    }

    private fun hexToBytes(hex: String): ByteArray {
        val out = ByteArray(hex.length / 2)
        for (i in out.indices) {
            out[i] = hex.substring(i * 2, i * 2 + 2).toInt(16).toByte()
        }
        return out
    }

    private fun hasOnlyFields(raw: ByteArray, allowed: Set<Int>): Boolean {
        val input = CodedInputStream.newInstance(raw)
        while (!input.isAtEnd) {
            val tag = input.readTag()
            if (tag == 0) break
            val fieldNum = WireFormat.getTagFieldNumber(tag)
            if (fieldNum !in allowed) return false
            if (!input.skipField(tag)) return false
        }
        return true
    }

    @Test
    fun phase3TerminalControlAndProbeVectors() {
        val root = Json.parseToJsonElement(loadFixture().readText()).jsonObject
        val controls = root.getValue("controls").jsonArray
        assertTrue(controls.size >= 11)
        for (elem in controls) {
            val obj = elem.jsonObject
            val name = obj.getValue("name").jsonPrimitive.content
            val msgName = obj.getValue("message").jsonPrimitive.content
            val raw = hexToBytes(obj.getValue("wire_hex").jsonPrimitive.content)
            val wantValid = obj.getValue("valid").jsonPrimitive.boolean
            val wantStream = obj["stream"]?.jsonPrimitive?.content ?: ""

            var ok = false
            var stream = ""
            when (msgName) {
                "AttachPath" -> {
                    val m = AttachPath.parseFrom(raw)
                    ok = hasOnlyFields(raw, setOf(1, 2, 3, 4, 5, 6)) &&
                        m.stream.size() >= 16 &&
                        m.session.size() >= 16 &&
                        m.sender.size() == 32 &&
                        m.legId.size() == 16 &&
                        m.pathGeneration >= 1L &&
                        m.hasState()
                    stream = m.stream.toStringUtf8()
                }
                "InputStatus" -> {
                    val m = PeerInputStatus.parseFrom(raw)
                    ok = hasOnlyFields(raw, setOf(1, 2, 3, 4, 5)) &&
                        m.stream.size() >= 16 &&
                        m.queryId.size() == 16 &&
                        m.fromSeq >= 1L &&
                        m.limit in 1..128
                    stream = m.stream.toStringUtf8()
                }
                "InputStatusReply" -> {
                    val m = InputStatusReply.parseFrom(raw)
                    ok = hasOnlyFields(raw, setOf(1, 2, 3, 4, 5, 6, 7)) &&
                        m.stream.size() >= 16 &&
                        m.queryId.size() == 16 &&
                        (!m.available || (m.snapshotToken.size() == 32 && m.hasState()))
                    stream = m.stream.toStringUtf8()
                }
                "Ack" -> {
                    val m = Ack.parseFrom(raw)
                    ok = hasOnlyFields(raw, setOf(1, 2, 3, 4, 5, 6, 7, 8)) &&
                        m.stream.size() >= 16 &&
                        m.directionValue != 0 &&
                        m.hasState() &&
                        m.acknowledgedSha256.isEmpty
                    stream = m.stream.toStringUtf8()
                }
                "Replay" -> {
                    val m = Replay.parseFrom(raw)
                    ok = hasOnlyFields(raw, setOf(1, 2, 3, 4, 5, 6)) &&
                        m.stream.size() >= 16 &&
                        m.directionValue != 0 &&
                        m.fromSeq >= 1L &&
                        m.maxBytes in 1..(1 shl 20) &&
                        m.maxFrames in 1..128
                    stream = m.stream.toStringUtf8()
                }
                "Barrier" -> {
                    val m = Barrier.parseFrom(raw)
                    ok = hasOnlyFields(raw, setOf(1, 2, 3, 4, 5, 6)) &&
                        m.stream.size() >= 16 &&
                        m.barrierId.size() == 16 &&
                        (m.stopAdmission || (m.newLegId.size() == 16 && m.pathGeneration >= 1L))
                    stream = m.stream.toStringUtf8()
                }
                "BarrierReply" -> {
                    val m = BarrierReply.parseFrom(raw)
                    ok = hasOnlyFields(raw, setOf(1, 2, 3, 4, 5)) &&
                        m.stream.size() >= 16 &&
                        m.barrierId.size() == 16 &&
                        (!m.final || m.accepted) &&
                        (!m.accepted || m.hasState())
                    stream = m.stream.toStringUtf8()
                }
                "StreamFailed" -> {
                    val m = StreamFailed.parseFrom(raw)
                    ok = hasOnlyFields(raw, setOf(1, 2, 3)) &&
                        m.stream.size() >= 16 &&
                        m.hasInputFailure() &&
                        m.hasState()
                    stream = m.stream.toStringUtf8()
                }
                "ClosePath" -> {
                    val m = ClosePath.parseFrom(raw)
                    ok = hasOnlyFields(raw, setOf(1, 2, 3, 4)) &&
                        m.stream.size() >= 16 &&
                        m.legId.size() == 16 &&
                        m.pathGeneration >= 1L
                    stream = m.stream.toStringUtf8()
                }
                else -> error("unexpected vector message $msgName")
            }
            assertEquals("vector $name validity mismatch", wantValid, ok)
            if (wantValid && wantStream.isNotEmpty()) {
                assertEquals("vector $name stream mismatch", wantStream, stream)
            }
        }

        val probes = root.getValue("probes").jsonArray
        assertTrue(probes.size >= 3)
        for (elem in probes) {
            val obj = elem.jsonObject
            val name = obj.getValue("name").jsonPrimitive.content
            val raw = hexToBytes(obj.getValue("wire_hex").jsonPrimitive.content)
            val wantValid = obj.getValue("valid").jsonPrimitive.boolean
            val kind = obj.getValue("kind").jsonPrimitive.int
            val ok = raw.size == 17 && (raw[0].toInt() == 0 || raw[0].toInt() == 1)
            assertEquals("probe $name validity mismatch", wantValid, ok)
            if (wantValid) {
                assertEquals("probe $name kind mismatch", kind, raw[0].toInt() and 0xff)
            }
        }
    }
}
