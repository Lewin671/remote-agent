package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class ScheduleResultLoadingTest {
    private val run = ScheduledRun(buildJsonObject {
        put("id", "run"); put("session_id", "session"); put("final_summary", "Plain saved summary")
    })
    private val answer = "**Final result**\n\n# Report\n\nDetails."
    private val complete = SessionEvents(events = listOf(
        Event.newBuilder().setSeq(1).setOrigin(Origin.newBuilder().setScheduledRunId("run"))
            .setUserMessage(UserMessage.newBuilder().setText("Check layout")).build(),
        Event.newBuilder().setSeq(2).setAgentMessage(AgentMessage.newBuilder().setPhase("final_answer").setText(answer)).build(),
    ), loaded = true)

    @Test fun aPendingReadNeverPublishesTheInterimPlainSummary() = runTest {
        val events = CompletableDeferred<SessionEvents>()
        val result = async { loadScheduleResultText(run) { events.await() } }
        runCurrent()
        advanceTimeBy(1_000)
        assertFalse(result.isCompleted)
        events.complete(complete)
        assertEquals(answer, result.await())
    }

    @Test fun failedOrIncompleteHistoryUsesTheSavedSummaryWithoutBorrowingAnAnswer() = runTest {
        assertEquals("Plain saved summary", loadScheduleResultText(run) { throw java.io.IOException("synthetic failure") })
        assertEquals("Plain saved summary", resolvedScheduleResult(run, complete.copy(historyPartial = true)))
        val result = async { loadScheduleResultText(run) { awaitCancellation() } }
        advanceUntilIdle()
        assertEquals("Plain saved summary", result.await())
    }
}
