package io.github.lewin671.remoteagent.data

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class CoalescedWorkTest {
    @Test fun aBatchDefersItsScanAndDoesNotRunInsideTheCallersStoreLock() = runTest {
        val storeLock = Any()
        var scans = 0
        val worker = CoalescedWork(backgroundScope, quietMs = 100) {
            // The worker takes a short snapshot, then does its expensive work without the store lock.
            synchronized(storeLock) { Unit }
            assertFalse(Thread.holdsLock(storeLock))
            scans++
        }
        synchronized(storeLock) { repeat(100) { worker.request() }; assertEquals(0, scans) }
        runCurrent()
        advanceTimeBy(99); runCurrent(); assertEquals(0, scans)
        advanceTimeBy(1); runCurrent(); assertEquals(1, scans)
        advanceTimeBy(1000); runCurrent(); assertEquals(1, scans)
    }

    @Test fun aLongSaveBurstWaitsForOneQuietWindowInsteadOfRepeatedlyScanning() = runTest {
        var scans = 0
        val worker = CoalescedWork(backgroundScope, quietMs = 100) { scans++ }
        repeat(5) {
            worker.request(); runCurrent(); advanceTimeBy(90); runCurrent()
            assertEquals(0, scans)
        }
        advanceTimeBy(10); runCurrent()
        assertEquals(1, scans)
    }

    @Test fun aBurstDuringAScanGetsOnlyOneFollowupAndFailureDoesNotStopTheWorker() = runTest {
        val entered = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        var scans = 0
        val worker = CoalescedWork(backgroundScope, quietMs = 100) {
            scans++
            if (scans == 1) { entered.complete(Unit); release.await(); error("provider unavailable") }
        }
        worker.request(); runCurrent(); advanceTimeBy(100); runCurrent()
        assertTrue(entered.isCompleted)
        repeat(100) { worker.request() }
        release.complete(Unit); runCurrent()
        advanceTimeBy(100); runCurrent()
        assertEquals(2, scans)
        advanceTimeBy(1000); runCurrent(); assertEquals(2, scans)
    }
}
