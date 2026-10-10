package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Status
import org.junit.Assert.assertEquals
import org.junit.Test

class SessionViewTest {
    @Test
    fun theTerminalGivesWayToTheChatWhenItCannotBeShown() {
        // The computer is away: the cached chat can still be read, the terminal cannot.
        assertEquals(setOf(Status.EXITED, Status.OFFLINE), Status.entries.filterNot(::terminalAvailable).toSet())
    }

    @Test
    fun aPlanOfMoreLinesThanItsPreviewCanBeOpenedHoweverItLaysOut() {
        assertEquals(true, planFolds((1..7).joinToString("\n") { "$it. 步骤" }, 6))
        // One long paragraph is a single line to count: whether it is cut is the layout's to say.
        assertEquals(false, planFolds("先把筛选条件抽成一个 hook，".repeat(40), 6))
    }
}
