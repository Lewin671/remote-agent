package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.ApprovalResolved
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.ImageFormat
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnStarted
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ApprovedBackgroundTest {
    @Test
    fun anApprovedCallThatWentToTheBackgroundStaysUnconfirmed() {
        fun ev(seq: Long, b: Event.Builder) = b.setSeq(seq).build()
        val out = "Command running in background with ID: b1."
        val events = listOf(
            ev(1, Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance())),
            ev(2, Event.newBuilder().setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("a").setToolName("Bash").setInputDigest("d"))),
            ev(3, Event.newBuilder().setApprovalResolved(ApprovalResolved.newBuilder().setApprovalId("a").setDecision(ApprovalDecision.APPROVAL_DECISION_ALLOW))),
            ev(4, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setCallId("c").setItemType("Bash").setInputDigest("d").setTitle("npm run dev"))),
            ev(5, Event.newBuilder().setToolCall(ToolCall.newBuilder().setCallId("c").setItemType("Bash").setInputDigest("d").setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("npm run dev").setOutput(out).setStatus("completed").setBackground(io.github.lewin671.remoteagent.proto.ToolBackground.TOOL_BACKGROUND_REQUESTED))),
        )
        val tool = buildChat(events, derive(events, Agent.AGENT_CLAUDE), "M").filterIsInstance<ChatItem.Tool>().single()
        assertTrue(tool.background)
        assertEquals("已允许", tool.decision)
    }
}
