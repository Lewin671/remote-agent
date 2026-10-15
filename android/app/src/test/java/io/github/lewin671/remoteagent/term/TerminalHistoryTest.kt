package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.proto.TerminalHistoryPage
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.advanceTimeBy
import org.junit.Test
import org.junit.Assert.*

class TerminalHistoryTest {
    private fun plain(page: TerminalHistory.Page) = page.lines.map { line -> line.joinToString("") { it.text } }

    @Test fun emptyCaptureDoesNotCreateABlankPageAndDoesNotEnter() = runTest {
        val history = TerminalHistory(this)
        history.request(true) { _, _ -> }
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(history.state.value.pending).build())
        assertTrue(history.state.value.pages.isEmpty())
        assertEquals("", history.state.value.cursor)
        // Nothing to read: reading is not entered, and the toolbar goes back to the live one.
        assertFalse(history.state.value.entered)
        assertFalse(history.state.value.visible)
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
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(first).addLines("new \u001b[31m中文\u001b[0m").setNextCursor("capture:200").build())
        // The page's lines arrive parsed: the colours of the capture are kept for the reader.
        assertTrue(history.state.value.entered)
        assertEquals(SgrColor.Palette(1), history.state.value.pages.single().lines.single().first { it.fg != null }.fg)
        history.request(false, send)
        val timedOut = history.state.value.pending
        advanceTimeBy(10_001)
        assertEquals("", history.state.value.pending)
        assertTrue(history.state.value.failed)
        history.request(false, send)
        assertEquals("capture:200", requests.last().second)
        // A late answer to the request that timed out, and a page from before this reading, are
        // both dropped: only the newest request's answer counts.
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(timedOut).addLines("late").build())
        assertEquals(1, history.state.value.pages.size)
        val id = history.state.value.pending
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(id).addLines("old").build())
        assertEquals(listOf(listOf("new 中文"), listOf("old")), history.state.value.pages.map { plain(it) })
        assertFalse(history.state.value.failed)
        // The last page carried no cursor: the oldest record is here, so nothing more is asked.
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

    @Test fun aBrokenConnectionAndAnExpiredCaptureKeepWhatWasRead() = runTest {
        val history = TerminalHistory(this)
        val send: (String, String) -> Unit = { _, _ -> }
        history.request(true, send)
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(history.state.value.pending).addLines("read so far").setNextCursor("capture:1").build())
        history.disconnected()
        assertTrue(history.state.value.disconnected)
        assertEquals(listOf(listOf("read so far")), history.state.value.pages.map { plain(it) })
        assertEquals("终端断开了，已读到的内容还能看；重新连接后再往上滑读取更早的", historyStatus(history.state.value, waiting = false))
        history.close()
        // A cursor that expired in the reader has its own wording and no retry: it went with its
        // stream, so earlier pages have to be asked for from the live screen again.
        history.request(true, send)
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(history.state.value.pending).addLines("here").setNextCursor("capture:5").build())
        history.request(false, send)
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(history.state.value.pending).setError("history expired; reopen history").build())
        assertTrue(history.state.value.expired)
        assertFalse(history.state.value.failed)
        assertEquals("记录已过期，回到实时后重新往上滑", historyStatus(history.state.value, waiting = false))
        history.close()
    }

    @Test fun aFailedFirstCaptureDoesNotOpenTheReaderAndIsRetriedFromScratch() = runTest {
        val history = TerminalHistory(this)
        val requests = mutableListOf<Pair<String, String>>()
        val send: (String, String) -> Unit = { id, cursor -> requests += id to cursor }
        history.request(true, send)
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(history.state.value.pending).setError("cannot capture terminal history; retry or return to the live terminal").build())
        // No reader: the live screen stays, with one line over it and its retry at hand.
        assertFalse(history.state.value.entered)
        assertTrue(history.state.value.failed)
        assertEquals("读取失败", historyStatus(history.state.value, waiting = true))
        history.request(true, send)
        assertEquals("", requests.last().second)
        assertFalse(history.state.value.failed)
        // Before the reader is open even an expired capture is a failure, with a plain retry.
        history.receive(TerminalHistoryPage.newBuilder().setRequestId(history.state.value.pending).setError("history expired; reopen history").build())
        assertTrue(history.state.value.failed)
        assertFalse(history.state.value.expired)
        history.close()
    }

    @Test fun aDisconnectWhileOpeningJustEndsReading() = runTest {
        val history = TerminalHistory(this)
        history.request(true) { _, _ -> }
        history.disconnected()
        assertFalse(history.state.value.visible)
        assertFalse(history.state.value.disconnected)
        history.close()
    }

    @Test fun theStatusLineSaysOneThingAtATime() {
        fun state(visible: Boolean = true, entered: Boolean = true, cursor: String = "", pending: String = "", failed: Boolean = false, expired: Boolean = false, disconnected: Boolean = false) =
            TerminalHistory.State(visible, entered, emptyList(), cursor, pending, failed, expired, disconnected)
        assertEquals("读取记录…", historyStatus(state(entered = false, pending = "r"), waiting = true))
        assertEquals("读取更早的记录…", historyStatus(state(pending = "r"), waiting = false))
        assertEquals("读取失败", historyStatus(state(failed = true, pending = "r"), waiting = false))
        assertEquals("记录已过期，回到实时后重新往上滑", historyStatus(state(expired = true), waiting = false))
        assertEquals("已经是保留的最早记录", historyStatus(state(), waiting = false))
        assertEquals("终端断开了，已读到的内容还能看；重新连接后再往上滑读取更早的", historyStatus(state(disconnected = true, cursor = "capture:1"), waiting = false))
        // A page still to come is no reason to say anything, and the first capture says nothing else.
        assertNull(historyStatus(state(cursor = "capture:1"), waiting = false))
        assertNull(historyStatus(state(entered = false), waiting = true))
    }
}
