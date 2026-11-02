package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class ScheduleFinalAnswerTest {
    private val run = ScheduledRun(buildJsonObject { put("id", "run-a") })
    private fun user(seq: Long, run: String = "", kind: OriginKind = OriginKind.ORIGIN_KIND_SYSTEM) = Event.newBuilder().setSeq(seq)
        .setOrigin(Origin.newBuilder().setKind(kind).setScheduledRunId(run))
        .setUserMessage(UserMessage.newBuilder().setText("Check the project")).build()
    private fun answer(seq: Long, text: String, phase: String = "final_answer") = Event.newBuilder().setSeq(seq)
        .setAgentMessage(AgentMessage.newBuilder().setText(text).setPhase(phase)).build()
    private fun state(seq: Long, detail: String, status: SessionStatus = SessionStatus.SESSION_STATUS_RUNNING) = Event.newBuilder().setSeq(seq)
        .setOrigin(Origin.newBuilder().setKind(OriginKind.ORIGIN_KIND_CLIENT))
        .setSessionState(SessionState.newBuilder().setDetail(detail).setStatus(status)).build()
    @Test fun keepsFullMarkdownAndLastFinalAnswerWithinThisRun() {
        val full = "# Report\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n" + "long result ".repeat(100)
        val events = listOf(answer(1, "previous"), user(2, "run-a"), answer(3, "interim", "commentary"), answer(4, "first"), answer(5, full))
        assertEquals(full, scheduleFinalAnswer(run, events.reversed()))
    }
    @Test fun stopsAtHumanInputAnotherScheduleResumptionAndExit() {
        val prefix = listOf(user(1, "run-a"), answer(2, "correct"))
        val boundaries = listOf(user(3, kind = OriginKind.ORIGIN_KIND_CLIENT), user(3, kind = OriginKind.ORIGIN_KIND_LOCAL),
            user(3, "run-b"), state(3, "resumed"), state(3, "", SessionStatus.SESSION_STATUS_EXITED))
        boundaries.forEach { assertEquals("correct", scheduleFinalAnswer(run, prefix + it + answer(4, "unrelated"))) }
    }
    @Test fun missingOriginMissingFinalOrMissingEventsNeverBorrowAnotherReply() {
        assertNull(scheduleFinalAnswer(run, listOf(user(1, "run-b"), answer(2, "wrong"))))
        assertNull(scheduleFinalAnswer(run, listOf(user(1), answer(2, "wrong"))))
        assertNull(scheduleFinalAnswer(run, listOf(user(1, "run-a"), answer(2, "working", "commentary"))))
        assertNull(scheduleFinalAnswer(run, listOf(user(1, "run-a"), answer(3, "gap"))))
        assertNull(scheduleFinalAnswer(run, emptyList()))
    }
    @Test fun unspecifiedAndSystemInputsDoNotCountAsHumanTakeover() {
        val events = listOf(user(1, "run-a"), answer(2, "first"), user(3), answer(4, "follow up"))
        assertEquals("follow up", scheduleFinalAnswer(run, events))
    }
}
