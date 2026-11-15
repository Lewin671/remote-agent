package io.github.lewin671.remoteagent.core.transport

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class RecoveryTest {
    @Test fun bucketsAndFullJitterSurviveNotifications() {
        var now = 0L
        val budget = RecoveryBudget()
        val a = Recovery({ now }, { .5 }, budget)
        val b = Recovery({ now }, { .5 }, budget)
        assertTrue(a.take()); assertTrue(a.take()); assertFalse(a.take())
        a.ready(); assertFalse(a.take())
        assertTrue(b.take()); assertTrue(b.take()); assertFalse(b.take())
        assertEquals(1000L, a.delay())
        now = 1000; assertTrue(a.take()); now += 1000
        for (cap in listOf(1000L,2000,4000,8000,16000,30000,30000)) {
            a.failed()
            repeat(1000) { assertEquals(cap / 2, a.delay()) }
            now += cap / 2
        }
    }
    @Test fun authenticatedReplacementCancelsOnlyTheHttpWaiter() = runBlocking {
        val r = Recovery(global = RecoveryBudget())
        val entered = CompletableDeferred<Unit>()
        var cancelled = false
        val task = async {
            runCatching { r.follow<Unit> { entered.complete(Unit); try { awaitCancellation() } finally { cancelled = true } } }.exceptionOrNull()
        }
        entered.await(); r.ready()
        assertTrue(task.await() is PathChanged)
        assertTrue(cancelled)
    }
    @Test fun adaptiveLivenessUses60sBeforeTwoHeartbeatsAnd3xAfterwards() {
        var now = 0L
        val liveness = LegLiveness(now) { now }
        assertEquals(60_000L, liveness.thresholdMs())
        now = 59_000L
        assertFalse(liveness.suspect(now))
        liveness.onFrame(1_000L, isHeartbeat = true)
        assertEquals(60_000L, liveness.thresholdMs())
        assertFalse(liveness.suspect(59_000L))
        assertTrue(liveness.suspect(61_000L))

        // 20s legacy server cadence -> max(3s, 3 * 20s) = 60s.
        val legacy = LegLiveness(0L) { now }
        legacy.onFrame(20_000L, isHeartbeat = true)
        legacy.onFrame(40_000L, isHeartbeat = true)
        assertEquals(60_000L, legacy.thresholdMs())
        assertFalse(legacy.suspect(99_000L))
        assertTrue(legacy.suspect(100_000L))

        // 1s upgraded server cadence -> max(3s, 3 * 1s) = 3s.
        val fast = LegLiveness(0L) { now }
        fast.onFrame(1_000L, isHeartbeat = true)
        fast.onFrame(2_000L, isHeartbeat = true)
        assertEquals(3_000L, fast.thresholdMs())
        assertFalse(fast.suspect(4_500L))
        assertTrue(fast.suspect(5_000L))
    }
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
