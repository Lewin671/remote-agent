package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.TerminalHistoryLine
import io.github.lewin671.remoteagent.core.proto.TerminalHistoryParsedPage
import io.github.lewin671.remoteagent.core.proto.TerminalHistoryPhase
import io.github.lewin671.remoteagent.core.proto.TerminalHistoryRun
import io.github.lewin671.remoteagent.core.proto.TerminalHistoryUpdatedEffect
import io.github.lewin671.remoteagent.ui.messageTextForTest
import org.junit.Assert.*
import org.junit.Test

/** What the live screen shows and does while the reader opens, and what the pull has to be. */
@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class TerminalReadingTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    private fun reading(
        phase: TerminalHistoryPhase = TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING,
        pending: Boolean = false,
    ) = TerminalHistoryState(phase = phase, pending = pending)

    private fun toolbar(
        reading: TerminalHistoryState,
        state: RemoteTerminal.State = RemoteTerminal.State.Live,
        fitted: Boolean = true,
        resized: Boolean = false,
    ) = terminalToolbar(state, reading, fitted = fitted, resized = resized, cols = 80, rows = 24)

    @Test fun theToolbarStaysTheLiveOneUntilTheReaderIsOnScreen() {
        val pending = toolbar(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING, pending = true))
        assertEquals("已适配这块屏幕 · 80×24", pending.text)
        assertTrue(pending.fit)
        // A first capture that failed is still the live screen, with one line and a retry over it.
        val failed = toolbar(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING_FAILED))
        assertEquals("已适配这块屏幕 · 80×24", failed.text)
        assertTrue(failed.fit)
        // Only with the reader up does the toolbar say so, and the fit button go away.
        val reader = toolbar(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPEN))
        assertEquals(messageTextForTest(UiMessages.terminalReadonly()), reader.text)
        assertFalse(reader.fit)
        // The size is what the live toolbar shows, whatever it shows.
        assertEquals("电脑上 80×24", toolbar(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING, pending = true), fitted = false).text)
        assertEquals("远端尺寸已变化 · 80×24", toolbar(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING, pending = true), resized = true).text)
        assertEquals(messageTextForTest(UiMessages.sessionTerminal()), toolbar(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING, pending = true), state = RemoteTerminal.State.Connecting).text)
    }

    @Test fun theItemsRunBottomUpSoTheCaptureReadsTopDown() {
        fun page(id: String, vararg lines: String) = TerminalHistoryParsedPage.newBuilder()
            .setId(id)
            .addAllLines(lines.map { text ->
                TerminalHistoryLine.newBuilder()
                    .addRuns(TerminalHistoryRun.newBuilder().setText(text))
                    .build()
            })
            .build()

        var state = TerminalHistoryState()
        state = state.applyUpdate(
            TerminalHistoryUpdatedEffect.newBuilder()
                .setPhase(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPEN)
                .setAddedPage(page("new", "a1", "a2", "a3"))
                .build(),
        )
        val firstThree = state.items.toList()
        state = state.applyUpdate(
            TerminalHistoryUpdatedEffect.newBuilder()
                .setPhase(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPEN)
                .setAddedPage(page("old", "b1", "b2", "b3"))
                .build(),
        )
        val items = state.items
        // Item 0 is drawn at the bottom, so the list starts with the newest line of the newest page.
        assertEquals(listOf("a3", "a2", "a1", "b3", "b2", "b1"), items.map { it.second.single().text })
        // What the screen shows from top to bottom is the capture's own order, oldest line first.
        assertEquals(listOf("b1", "b2", "b3", "a1", "a2", "a3"), items.asReversed().map { it.second.single().text })
        // A key names a line's place in its own capture, so an older page only adds items above.
        assertEquals(listOf("new:2", "new:1", "new:0", "old:2", "old:1", "old:0"), items.map { it.first })
        assertEquals(items.take(3), firstThree)
    }

    @Test fun aSmallPullLeavesTheScreenAlone() {
        val pull = ReadingPull()
        val live = reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_IDLE)
        assertFalse(pull.onScroll(live, -2))
        assertEquals(2, pull.rows)
        // Half a row of a drag is no pull at all, and one going the other way takes it back.
        assertFalse(pull.onScroll(live, 1))
        assertEquals(1, pull.rows)
    }

    @Test fun threeRowsAskOnceAndOnlyTheFirstPageAsks() {
        val pull = ReadingPull()
        val live = reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_IDLE)
        assertFalse(pull.onScroll(live, -2))
        assertTrue(pull.onScroll(live, -1))
        assertEquals(3, pull.rows)
        // The capture is on its way: pulling further only sizes the reader, which opens that far up.
        assertFalse(pull.onScroll(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING, pending = true), -4))
        assertEquals(7, pull.rows)
        assertFalse(pull.onScroll(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING, pending = true), -1))
        // And a reader that is on screen scrolls itself.
        assertFalse(pull.onScroll(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPEN), -5))
    }

    @Test fun anotherPullAsksAgainAfterAFailure() {
        val pull = ReadingPull()
        assertTrue(pull.onScroll(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_IDLE), -3))
        // The capture failed, so the pane starts the pull over: another one has to be made.
        pull.reset()
        assertFalse(pull.onScroll(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING_FAILED), -2))
        assertTrue(pull.onScroll(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING_FAILED), -1))
        // Left alone, the retry is on its way and nothing more is asked for.
        assertFalse(pull.onScroll(reading(TerminalHistoryPhase.TERMINAL_HISTORY_PHASE_OPENING, pending = true), -5))
    }
}
