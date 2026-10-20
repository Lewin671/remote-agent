package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Status
import org.junit.Assert.assertEquals
import org.junit.Test

class SessionSubtitleTest {
    @Test fun unknownModeLeavesOnlyTheStatus() {
        Status.entries.forEach { assertEquals(it.text, sessionSubtitle(it, null)) }
        assertEquals(Status.RUNNING.text + " · 询问", sessionSubtitle(Status.RUNNING, "询问"))
        assertEquals(Status.EXITED.text, sessionSubtitle(Status.EXITED, "询问"))
    }
}
