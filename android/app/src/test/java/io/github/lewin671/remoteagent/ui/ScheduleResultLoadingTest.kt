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
    private val complete = io.github.lewin671.remoteagent.core.proto.SessionViewModel.newBuilder()
        .setSessionId("session")
        .setTimeline(
            io.github.lewin671.remoteagent.core.proto.SessionTimelineView.newBuilder()
                .setLoaded(true)
                .addItems(
                    io.github.lewin671.remoteagent.core.proto.SessionTimelineItem.newBuilder()
                        .setId("e1").setSeq(1)
                        .setUserMessage(
                            io.github.lewin671.remoteagent.core.proto.SessionUserMessageItem.newBuilder()
                                .setText("Check layout")
                                .setScheduledRunId("run"),
                        ),
                )
                .addItems(
                    io.github.lewin671.remoteagent.core.proto.SessionTimelineItem.newBuilder()
                        .setId("e2").setSeq(2)
                        .setAgentMessage(
                            io.github.lewin671.remoteagent.core.proto.SessionAgentMessageItem.newBuilder()
                                .setMarkdown(answer),
                        ),
                ),
        )
        .build()

    @Test fun aPendingReadNeverPublishesTheInterimPlainSummary() = runTest {
        val vm = CompletableDeferred<io.github.lewin671.remoteagent.core.proto.SessionViewModel>()
        val result = async { loadScheduleResultText(run) { vm.await() } }
        runCurrent()
        advanceTimeBy(1_000)
        assertFalse(result.isCompleted)
        vm.complete(complete)
        assertEquals(answer, result.await())
    }

    @Test fun failedOrIncompleteHistoryUsesTheSavedSummaryWithoutBorrowingAnAnswer() = runTest {
        assertEquals("Plain saved summary", loadScheduleResultText(run) { throw java.io.IOException("synthetic failure") })
        val partial = complete.toBuilder().setTimeline(complete.timeline.toBuilder().setHistoryPartial(true)).build()
        assertEquals("Plain saved summary", resolvedScheduleResult(run, partial))
        val result = async { loadScheduleResultText(run) { awaitCancellation() } }
        advanceUntilIdle()
        assertEquals("Plain saved summary", result.await())
    }
}
