package io.github.lewin671.remoteagent.data

import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.fail
import org.junit.Test

class EraseTest {
    @Test fun aStepThatFailsDoesNotKeepTheOnesAfterItFromRunning() = runTest {
        val ran = mutableListOf<String>()
        try {
            everyStep(
                { ran += "cache"; throw IllegalStateException("cache") },
                { ran += "pending" },
                { ran += "prefs"; throw IllegalStateException("prefs") },
            )
            fail("the failure was swallowed")
        } catch (e: IllegalStateException) {
            // The first failure is the one reported.
            assertEquals("cache", e.message)
        }
        assertEquals(listOf("cache", "pending", "prefs"), ran)
    }

    @Test fun nothingIsThrownWhenEveryStepWorks() = runTest {
        var ran = 0
        everyStep({ ran++ }, { ran++ })
        assertEquals(2, ran)
    }
}
