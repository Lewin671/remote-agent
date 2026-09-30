package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Mode
import org.junit.Assert.assertEquals
import org.junit.Test

class ProjectTest {
    @Test
    fun aNewSessionCannotBeStartedInPlan() {
        // Both CLIs reject starting in plan; full access is only a confirmed default.
        assertEquals(listOf(Mode.ASK, Mode.AUTO), newSessionModes)
    }
}
