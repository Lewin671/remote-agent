package io.github.lewin671.remoteagent.term

import org.junit.Assert.*
import org.junit.Test

/** What the live screen shows and does while the reader opens, and what the pull has to be. */
class TerminalReadingTest {
    private fun reading(visible: Boolean = true, entered: Boolean = false, cursor: String = "", pending: String = "", failed: Boolean = false) =
        TerminalHistory.State(visible = visible, entered = entered, cursor = cursor, pending = pending, failed = failed)

    private fun toolbar(reading: TerminalHistory.State, state: RemoteTerminal.State = RemoteTerminal.State.Live, fitted: Boolean = true, resized: Boolean = false) =
        terminalToolbar(state, reading, fitted = fitted, resized = resized, cols = 80, rows = 24)

    @Test fun theToolbarStaysTheLiveOneUntilTheReaderIsOnScreen() {
        val pending = toolbar(reading(pending = "r"))
        assertEquals("已适配这块屏幕 · 80×24", pending.text)
        assertTrue(pending.fit)
        // A first capture that failed is still the live screen, with one line and a retry over it.
        val failed = toolbar(reading(pending = "", failed = true))
        assertEquals("已适配这块屏幕 · 80×24", failed.text)
        assertTrue(failed.fit)
        // Only with the reader up does the toolbar say so, and the fit button go away.
        val reader = toolbar(reading(entered = true, cursor = "capture:1"))
        assertEquals("回看中 · 只读", reader.text)
        assertFalse(reader.fit)
        // The size is what the live toolbar shows, whatever it shows.
        assertEquals("电脑上 80×24", toolbar(reading(pending = "r"), fitted = false).text)
        assertEquals("远端尺寸已变化 · 80×24", toolbar(reading(pending = "r"), resized = true).text)
        assertEquals("终端", toolbar(reading(pending = "r"), state = RemoteTerminal.State.Connecting).text)
    }

    @Test fun theItemsRunBottomUpSoTheCaptureReadsTopDown() {
        fun line(text: String) = listOf(SgrRun(text))
        // A page's lines are oldest to newest, and its request id reaches us with the newest page first.
        val newest = TerminalHistory.Page("new", listOf(line("a1"), line("a2"), line("a3")))
        val older = TerminalHistory.Page("old", listOf(line("b1"), line("b2"), line("b3")))

        val items = historyItems(listOf(newest, older))
        // Item 0 is drawn at the bottom, so the list starts with the newest line of the newest page.
        assertEquals(listOf("a3", "a2", "a1", "b3", "b2", "b1"), items.map { it.second.single().text })
        // What the screen shows from top to bottom is the capture's own order, oldest line first.
        assertEquals(listOf("b1", "b2", "b3", "a1", "a2", "a3"), items.asReversed().map { it.second.single().text })
        // A key names a line's place in its own capture, so an older page only adds items above.
        assertEquals(listOf("new:2", "new:1", "new:0", "old:2", "old:1", "old:0"), items.map { it.first })
        assertEquals(items.take(3), historyItems(listOf(newest)).take(3))
    }

    @Test fun aSmallPullLeavesTheScreenAlone() {
        val pull = ReadingPull()
        val live = reading(visible = false)
        assertFalse(pull.onScroll(live, -2))
        assertEquals(2, pull.rows)
        // Half a row of a drag is no pull at all, and one going the other way takes it back.
        assertFalse(pull.onScroll(live, 1))
        assertEquals(1, pull.rows)
    }

    @Test fun threeRowsAskOnceAndOnlyTheFirstPageAsks() {
        val pull = ReadingPull()
        val live = reading(visible = false)
        assertFalse(pull.onScroll(live, -2))
        assertTrue(pull.onScroll(live, -1))
        assertEquals(3, pull.rows)
        // The capture is on its way: pulling further only sizes the reader, which opens that far up.
        assertFalse(pull.onScroll(reading(pending = "r"), -4))
        assertEquals(7, pull.rows)
        assertFalse(pull.onScroll(reading(pending = "r"), -1))
        // And a reader that is on screen scrolls itself.
        assertFalse(pull.onScroll(reading(entered = true, cursor = "capture:1"), -5))
    }

    @Test fun anotherPullAsksAgainAfterAFailure() {
        val pull = ReadingPull()
        assertTrue(pull.onScroll(reading(visible = false), -3))
        // The capture failed, so the pane starts the pull over: another one has to be made.
        pull.reset()
        assertFalse(pull.onScroll(reading(failed = true), -2))
        assertTrue(pull.onScroll(reading(failed = true), -1))
        // Left alone, the retry is on its way and nothing more is asked for.
        assertFalse(pull.onScroll(reading(pending = "retry"), -5))
    }
}
