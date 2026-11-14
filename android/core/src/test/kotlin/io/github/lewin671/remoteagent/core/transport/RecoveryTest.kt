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
}
