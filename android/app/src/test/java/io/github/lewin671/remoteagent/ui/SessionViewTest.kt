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
}
