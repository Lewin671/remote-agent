package io.github.lewin671.remoteagent.core.transport

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class RecoveryTest {
    @Test fun terminalRecoveryBackoffAndSharedGlobalBucket() {
        var now = 0L
        val budget = RecoveryBudget()
        val t1 = TerminalRecovery({ now }, { 0.5 }, budget)
        val t2 = TerminalRecovery({ now }, { 0.5 }, budget)
        val t3 = TerminalRecovery({ now }, { 0.5 }, budget)
        val t4 = TerminalRecovery({ now }, { 0.5 }, budget)
        val t5 = TerminalRecovery({ now }, { 0.5 }, budget)
        // First 4 concurrent reopens across streams consume the 4/2s global bucket immediately.
        assertNull(t1.takeOrDelayMs())
        assertNull(t2.takeOrDelayMs())
        assertNull(t3.takeOrDelayMs())
        assertNull(t4.takeOrDelayMs())
        // 5th stream must wait for global refill (500ms at 2 tokens/s).
        assertEquals(500L, t5.takeOrDelayMs())
        now += 500L
        assertNull(t5.takeOrDelayMs())
        // Failing t1 again before stabilization invokes full-jitter backoff (0.5 * 1000ms = 500ms).
        t1.recordFailureIfUnstabilized()
        assertEquals(500L, t1.takeOrDelayMs())
        now += 500L
        assertNull(t1.takeOrDelayMs())
        // Stabilizing resets the backoff sequence.
        t1.onStabilized()
        now += 2_000L
        t1.recordFailureIfUnstabilized()
        assertNull(t1.takeOrDelayMs())
    }
}
