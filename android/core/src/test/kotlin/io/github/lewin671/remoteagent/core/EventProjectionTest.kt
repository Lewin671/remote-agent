package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.EventBatch
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.LiveSnapshot
import io.github.lewin671.remoteagent.proto.ProjectionState
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnStarted
import io.github.lewin671.remoteagent.proto.UserMessage
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

/** Smoke test verifying the shared Go core JNI binding, ProjectionState decoding, and incremental projection. */
class EventProjectionTest {
    private fun ev(seq: Int, b: Event.Builder) = b.setSeq(seq.toLong()).setSessionId("s").build()

    @Test fun corebindSmokeDecodesProjectionStateAndOverlaysLiveActions() {
        val events = listOf(
            ev(1, Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance())),
            ev(2, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("r1").setStatus(InputStatus.INPUT_STATUS_QUEUED).setText("hi"))),
            ev(3, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setCallId("c1").setTitle("go test").setInputDigest("d1"))),
            ev(4, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("local"))),
        )
        val batchBytes = EventBatch.newBuilder().addAllEvents(events).build().toByteArray()
        val raw = Corebind.reduce(Agent.AGENT_CLAUDE.number, batchBytes)
        val ps = ProjectionState.parseFrom(raw)
        assertTrue(ps.running)
        assertEquals(listOf("r1"), ps.pendingInputsList.map { it.requestId })
        assertEquals(listOf(4L), ps.typedLocallySeqsList)

        val projection = EventProjection()
        val first = projection.update(events.take(2), Agent.AGENT_CLAUDE)
        assertSame(first, projection.update(events.take(2), Agent.AGENT_CLAUDE))
        val second = projection.update(events, Agent.AGENT_CLAUDE)
        assertEquals(derive(events, Agent.AGENT_CLAUDE).tools.map { it.seq }, second.tools.map { it.seq })

        val approval = ev(5, Event.newBuilder().setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("a1").setInputDigest("d1")))
        val live = LiveSnapshot.newBuilder().setSession(Session.newBuilder().setId("s").setLastSeq(4)).setRunning(true).addActions(approval).build()
        val overlaid = projection.currentActions(live)
        assertEquals(listOf("a1"), overlaid.approvals.keys.toList())
        assertTrue(awaitingApproval(overlaid.tools.single(), overlaid))
        assertFalse(projection.currentActions(null).approvals.containsKey("a1"))
    }
}
