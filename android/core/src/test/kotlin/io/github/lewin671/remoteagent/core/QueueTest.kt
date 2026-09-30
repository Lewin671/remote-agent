package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionState
import io.github.lewin671.remoteagent.proto.SessionStatus
import org.junit.Assert.assertEquals
import org.junit.Test

class QueueTest {
    private fun input(seq: Long, id: String, st: InputStatus, reason: String = "") =
        Event.newBuilder().setSeq(seq).setInputState(InputState.newBuilder().setRequestId(id).setStatus(st).setReason(reason)).build()

    @Test
    fun theQueueKeepsTheDaemonsOrder() {
        val d = derive(
            listOf(
                input(1, "b", InputStatus.INPUT_STATUS_QUEUED),
                input(2, "a", InputStatus.INPUT_STATUS_QUEUED),
                input(3, "c", InputStatus.INPUT_STATUS_QUEUED),
                input(4, "a", InputStatus.INPUT_STATUS_CANCELED),
            ),
        )
        assertEquals(listOf("b", "c"), queued(d))
    }

    @Test
    fun anEndedSessionEmptiesTheQueue() {
        val d = derive(
            listOf(
                input(1, "b", InputStatus.INPUT_STATUS_QUEUED),
                Event.newBuilder().setSeq(2).setSessionState(SessionState.newBuilder().setStatus(SessionStatus.SESSION_STATUS_EXITED)).build(),
            ),
        )
        assertEquals(emptyList<String>(), queued(d))
        assertEquals(2L, d.unsent["b"])
    }

    @Test
    fun theDaemonsReasonComesFirst() {
        val s = SessionView("s", "m", Session.newBuilder().setInputWait("draft").setAgent(Agent.AGENT_CODEX).build())
        assertEquals("终端里有草稿", queueWhy(s, Status.RUNNING).short)
        val r = SessionView("s", "m", Session.newBuilder().build())
        assertEquals("本轮结束后发送", queueWhy(r, Status.RUNNING).short)
        assertEquals("等待本轮结束", queueWhy(r, Status.RUNNING).long)
    }

    @Test
    fun aCancelOutcomeSaysWhoCanceled() {
        assertEquals(CancelOutcome.CANCELED, cancelOutcome(InputStatus.INPUT_STATUS_CANCELED, canceledHere = true))
        assertEquals(CancelOutcome.CANCELED_ELSEWHERE, cancelOutcome(InputStatus.INPUT_STATUS_CANCELED, canceledHere = false))
        assertEquals(CancelOutcome.WRITTEN, cancelOutcome(InputStatus.INPUT_STATUS_SUBMITTED, canceledHere = true))
    }
}
