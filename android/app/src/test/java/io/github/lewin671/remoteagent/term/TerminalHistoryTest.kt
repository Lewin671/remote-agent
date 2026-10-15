package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.proto.TerminalHistoryPage
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.advanceTimeBy
import org.junit.Test
import org.junit.Assert.*

class TerminalHistoryTest {
    @Test fun emptyCaptureDoesNotCreateABlankPage() = runTest {
        val history = TerminalHistory(this)
        history.request(true) { _, _ -> }
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(history.state.value.pending).build())
        assertTrue(history.state.value.pages.isEmpty())
        assertEquals("", history.state.value.cursor)
        history.close()
    }
    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun pagesAreOrderedTimeoutsRetryAndStaleFramesAreIgnored() = runTest {
        val history = TerminalHistory(this)
        val requests = mutableListOf<Pair<String, String>>()
        val send: (String, String) -> Unit = { id, cursor -> requests += id to cursor }
        history.request(true, send)
        val first = history.state.value.pending
        history.request(false, send)
        assertEquals(1, requests.size)
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(first).addLines("new 中文").setNextCursor("capture:200").build())
        history.request(false, send)
        val timedOut = history.state.value.pending
        advanceTimeBy(10_001)
        assertEquals("", history.state.value.pending)
        assertTrue(history.state.value.error.contains("超时"))
        history.request(false, send)
        assertEquals("capture:200", requests.last().second)
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(timedOut).addLines("late").build())
        assertEquals(1, history.state.value.pages.size)
        val id = history.state.value.pending
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(id).addLines("old").build())
        assertEquals(listOf("new 中文", "old"), history.state.value.pages.map { it.text })
        history.request(false, send)
        assertEquals(3, requests.size)
        history.close()
        history.request(true, send)
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(id).addLines("stale").build())
        assertTrue(history.state.value.pages.isEmpty())
        history.disconnected()
        assertEquals("", history.state.value.pending)
        history.close()
    }
}
