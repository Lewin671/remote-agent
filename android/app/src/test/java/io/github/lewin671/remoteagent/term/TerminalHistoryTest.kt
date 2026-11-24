package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.core.CoreHost
import io.github.lewin671.remoteagent.core.proto.TerminalHistoryEventKind
import io.github.lewin671.remoteagent.core.proto.TerminalHistoryPhase
import io.github.lewin671.remoteagent.core.proto.TerminalHistorySendRequestEffect
import io.github.lewin671.remoteagent.core.proto.TerminalHistoryStatus
import io.github.lewin671.remoteagent.core.proto.TerminalSgrColorKind
import io.github.lewin671.remoteagent.proto.TerminalHistoryPage
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicReference

/**
 * Integration tests verifying Android's CoreHost <-> clientcore terminal history
 * paging state machine, SGR parsing, immutable PagedList updates, and timeout/error handling.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class TerminalHistoryTest {
    private fun plainLines(state: TerminalHistoryState): List<String> =
        state.items.map { (_, runs) -> runs.joinToString("") { it.text } }

    private fun withCoreHistory(
        terminalId: String = "term-test-1",
        block: TestScope.(CoreHost, AtomicReference<TerminalHistoryState>, CopyOnWriteArrayList<TerminalHistorySendRequestEffect>) -> Unit,
    ) = runTest {
        val dispatcher = StandardTestDispatcher(testScheduler)
        val host = CoreHost(externalScope = backgroundScope, workerDispatcher = dispatcher)
        val state = AtomicReference(TerminalHistoryState())
        val requests = CopyOnWriteArrayList<TerminalHistorySendRequestEffect>()
        val offReq = host.onTerminalHistorySendRequest(terminalId) { requests += it }
        val offUpd = host.onTerminalHistoryUpdated(terminalId) { upd ->
            state.updateAndGet { it.applyUpdate(upd) }
        }
        try {
            runCurrent()
            block(host, state, requests)
        } finally {
            offReq()
            offUpd()
            host.sendTerminalHistory(terminalId, TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_DISPOSED)
            runCurrent()
            host.close()
        }
    }

    @Test fun emptyCaptureDoesNotEnterReader() = withCoreHistory { host, state, requests ->
        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_OPEN)
        runCurrent()
        assertEquals(1, requests.size)
        assertEquals(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING, state.get().phase)

        host.sendTerminalHistory(
            "term-test-1",
            TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_PAGE_RECEIVED,
            TerminalHistoryPage.newBuilder().setRequestId(requests[0].requestId).build(),
        )
        runCurrent()
        assertFalse(state.get().visible)
        assertFalse(state.get().entered)
        assertTrue(state.get().items.isEmpty())
    }

    @Test fun pagesAreParsedOnceOrderedBottomUpAndKeepPriorItemIdentity() = withCoreHistory { host, state, requests ->
        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_OPEN)
        runCurrent()
        val firstReq = requests.last()
        assertEquals("", firstReq.cursor)

        // Duplicate open while pending is ignored.
        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_OPEN)
        runCurrent()
        assertEquals(1, requests.size)

        host.sendTerminalHistory(
            "term-test-1",
            TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_PAGE_RECEIVED,
            TerminalHistoryPage.newBuilder()
                .setRequestId(firstReq.requestId)
                .addLines("a")
                .addLines("new \u001b[31m中文\u001b[0m")
                .setNextCursor("capture:200")
                .build(),
        )
        runCurrent()

        val afterFirst = state.get()
        assertTrue(afterFirst.entered)
        assertTrue(afterFirst.canLoadEarlier)
        assertEquals(8, afterFirst.maxCells)
        assertEquals(listOf("new 中文", "a"), plainLines(afterFirst))
        val coloredRun = afterFirst.items[0].second.first { it.fgKind != TerminalSgrColorKind.TERMINAL_SGR_COLOR_KIND_DEFAULT }
        assertEquals(TerminalSgrColorKind.TERMINAL_SGR_COLOR_KIND_PALETTE, coloredRun.fgKind)
        assertEquals(1, coloredRun.fgValue)

        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_LOAD_EARLIER)
        runCurrent()
        val secondReq = requests.last()
        assertEquals("capture:200", secondReq.cursor)

        host.sendTerminalHistory(
            "term-test-1",
            TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_PAGE_RECEIVED,
            TerminalHistoryPage.newBuilder()
                .setRequestId(secondReq.requestId)
                .addLines("1234567890")
                .build(),
        )
        runCurrent()

        val afterSecond = state.get()
        assertEquals(10, afterSecond.maxCells)
        assertEquals(listOf("new 中文", "a", "1234567890"), plainLines(afterSecond))
        // PagedList preserves object identity of already-published rows.
        assertSame(afterFirst.items[0], afterSecond.items[0])
        assertEquals(TerminalHistoryStatus.TERMINAL_HISTORY_STATUS_START, afterSecond.status)
        assertEquals("terminal.history_start", afterSecond.statusText?.key)
        assertFalse(afterSecond.canLoadEarlier)

        // Close resets pages and ignores stale replies.
        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_CLOSE)
        runCurrent()
        assertFalse(state.get().visible)
        assertTrue(state.get().items.isEmpty())
    }

    @Test fun timeoutRetryExpiredAndDisconnectFollowUnifiedCoreRules() = withCoreHistory { host, state, requests ->
        // Opening failure stays retryable (never expired).
        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_OPEN)
        runCurrent()
        host.sendTerminalHistory(
            "term-test-1",
            TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_PAGE_RECEIVED,
            TerminalHistoryPage.newBuilder()
                .setRequestId(requests.last().requestId)
                .setError("history expired; reopen history")
                .build(),
        )
        runCurrent()
        assertEquals(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING_FAILED, state.get().phase)
        assertTrue(state.get().canRetry)
        assertEquals("terminal.read_failed", state.get().statusText?.key)

        // Retry opens fresh capture.
        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_RETRY)
        runCurrent()
        assertEquals("", requests.last().cursor)
        host.sendTerminalHistory(
            "term-test-1",
            TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_PAGE_RECEIVED,
            TerminalHistoryPage.newBuilder()
                .setRequestId(requests.last().requestId)
                .addLines("here")
                .setNextCursor("capture:5")
                .build(),
        )
        runCurrent()
        assertTrue(state.get().entered)

        // Load earlier times out -> FAILED + retryable, late reply ignored.
        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_LOAD_EARLIER)
        runCurrent()
        val timedOutId = requests.last().requestId
        advanceTimeBy(10_001)
        runCurrent()
        assertEquals(TerminalHistoryStatus.TERMINAL_HISTORY_STATUS_FAILED, state.get().status)
        assertTrue(state.get().canRetry)
        host.sendTerminalHistory(
            "term-test-1",
            TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_PAGE_RECEIVED,
            TerminalHistoryPage.newBuilder().setRequestId(timedOutId).addLines("late").build(),
        )
        runCurrent()
        assertEquals(1, state.get().items.size)

        // Retry reuses cursor, receives expired error -> EXPIRED, not retryable.
        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_RETRY)
        runCurrent()
        assertEquals("capture:5", requests.last().cursor)
        host.sendTerminalHistory(
            "term-test-1",
            TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_PAGE_RECEIVED,
            TerminalHistoryPage.newBuilder()
                .setRequestId(requests.last().requestId)
                .setError("history expired; reopen history")
                .build(),
        )
        runCurrent()
        assertEquals(TerminalHistoryStatus.TERMINAL_HISTORY_STATUS_EXPIRED, state.get().status)
        assertFalse(state.get().canRetry)
        assertEquals("terminal.history_expired", state.get().statusText?.key)

        // Disconnected while open keeps loaded lines.
        host.sendTerminalHistory("term-test-1", TerminalHistoryEventKind.TERMINAL_HISTORY_EVENT_KIND_DISCONNECTED)
        runCurrent()
        assertTrue(state.get().entered)
        assertEquals(TerminalHistoryStatus.TERMINAL_HISTORY_STATUS_DISCONNECTED, state.get().status)
        assertEquals("terminal.history_closed", state.get().statusText?.key)
        assertEquals(listOf("here"), plainLines(state.get()))
    }
}
