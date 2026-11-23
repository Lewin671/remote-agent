package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.SendMode
import org.junit.Test

class ModelTest {
    @Test fun nativeSendCapabilityControlsOperationAndCaption() {
        for (agent in listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE)) {
            assertEquals(SendMode.SEND_MODE_STEER, sendModeFor(agent, running = true))
        }
        for (mode in listOf(SendMode.SEND_MODE_UNSPECIFIED, SendMode.SEND_MODE_STEER, SendMode.SEND_MODE_QUEUE)) {
            assertEquals(SendMode.SEND_MODE_QUEUE, sendModeFor(Agent.AGENT_ANTIGRAVITY, mode, true))
        }
        assertEquals(SendMode.SEND_MODE_STEER, sendModeFor(Agent.AGENT_ANTIGRAVITY, SendMode.SEND_MODE_STEER, false))
    }

    @Test fun markdownTablePreviewMatchesSharedVectors() {
        val vectors = kotlinx.serialization.json.Json.parseToJsonElement(
            java.io.File(System.getProperty("previewTableVectors")).readText()) as kotlinx.serialization.json.JsonArray
        for (vector in vectors) {
            val fields = vector as kotlinx.serialization.json.JsonObject
            fun value(key: String) = (fields.getValue(key) as kotlinx.serialization.json.JsonPrimitive).content
            org.junit.Assert.assertEquals(value("input"), value("expected"), plain(value("input")))
        }
        assertEquals("修 bug", plain("修 `bug`"))
    }

    @Test
    fun saysWhichSideToUpdateWhenProtocolVersionsDiffer() {
        fun m(v: Int) = MachineView("m", true, 0, MachineMeta.newBuilder().setHostname("mac.local").setProtocolVersion(v).build())
        assertEquals(null, protocolMismatch(m(PEER_PROTOCOL_VERSION)))
        assertEquals("mac 上的 remote-agent 版本较旧，请在电脑上更新后再试", protocolMismatch(m(PEER_PROTOCOL_VERSION - 1)))
        assertEquals(messageTextForTest(UiMessages.commonAndroidThisAppVersionIsOldUpdateAndRetry()), protocolMismatch(m(PEER_PROTOCOL_VERSION + 1)))
        // Not read yet: the daemon answers for itself.
        assertEquals(null, protocolMismatch(MachineView("m", true, 0)))
        assertEquals(null, protocolMismatch(null))
    }
}
