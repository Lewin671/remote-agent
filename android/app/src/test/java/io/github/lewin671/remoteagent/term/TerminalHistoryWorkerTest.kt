package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.proto.TerminalHistoryPage
import kotlin.coroutines.CoroutineContext
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class TerminalHistoryWorkerTest {
    private class Worker : CoroutineDispatcher() {
        val tasks = ArrayDeque<Runnable>()
        var working = false
        override fun dispatch(context: CoroutineContext, block: Runnable) { tasks.add(block) }
        fun drain() {
            working = true
            try { while (tasks.isNotEmpty()) tasks.removeFirst().run() } finally { working = false }
        }
    }

    @Test fun parsesOnlyOnWorkerAndAppendsWithoutChangingPublishedRows() = runTest {
        val worker = Worker()
        var parsed = 0
        val history = TerminalHistory(this, worker) {
            assertTrue("parser ran on UI", worker.working)
            parsed++
            TerminalSgr.parse(it)
        }
        fun receive(vararg lines: String) {
            history.receive(TerminalHistoryPage.newBuilder().setRequestId(history.state.value.pending)
                .addAllLines(lines.toList()).setNextCursor("older").build())
        }
        try {
            history.request(true) { _, _ -> }
            receive("a", "\u001b[31m中文\u001b[0m")
            runCurrent()
            assertEquals(0, parsed)
            assertTrue(history.state.value.items.isEmpty())
            worker.drain(); runCurrent()
            val first = history.state.value
            assertEquals(2, parsed)
            assertEquals(4, first.maxCells)
            assertEquals(listOf("中文", "a"), first.items.map { it.second.joinToString("") { r -> r.text } })
            history.request(false) { _, _ -> }
            receive("123456")
            runCurrent(); worker.drain(); runCurrent()
            val second = history.state.value
            assertEquals(3, parsed)
            assertEquals(6, second.maxCells)
            assertEquals(2, first.items.size)
            assertEquals(3, second.items.size)
            assertSame(first.items[0], second.items[0])
            assertSame(first.pages[0], second.pages[0])
            assertEquals(4, first.maxCells)
        } finally { history.close(); worker.drain(); runCurrent() }
    }

    @Test fun closingOrDisconnectingBeforeWorkerCompletionDiscardsThePage() = runTest {
        for (disconnect in listOf(false, true)) {
            val worker = Worker()
            val history = TerminalHistory(this, worker)
            history.request(true) { _, _ -> }
            history.receive(TerminalHistoryPage.newBuilder().setRequestId(history.state.value.pending).addLines("late").build())
            runCurrent()
            if (disconnect) history.disconnected() else history.close()
            worker.drain(); runCurrent()
            assertFalse(history.state.value.entered)
            assertTrue(history.state.value.items.isEmpty())
            assertEquals(0, history.state.value.maxCells)
        }
    }
}
