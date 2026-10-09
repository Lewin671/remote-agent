package io.github.lewin671.remoteagent.core

import org.junit.Assert.assertEquals
import org.junit.Test

class PingTest {
    @Test
    fun oneSlowSampleDoesNotMakeTheLineSlow() {
        assertEquals(95L, median(listOf(900L, 80L, 95L)))
        assertEquals(70L, median(listOf(70L)))
    }
}
