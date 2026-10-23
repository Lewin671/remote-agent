package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.*
import java.util.concurrent.Executors
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

class ChatProjectionWorkerTest {
    private val user = Event.newBuilder().setSeq(1).setUserMessage(UserMessage.newBuilder().setText("hello")).build()
    private val reply = Event.newBuilder().setSeq(2).setAgentMessage(AgentMessage.newBuilder().setText("reply")).build()

    @Test fun projectionLeavesUiAndPublishesStableSnapshots() = runBlocking {
        val ui = Executors.newSingleThreadExecutor { Thread(it, "test-ui") }.asCoroutineDispatcher()
        val worker = Executors.newSingleThreadExecutor { Thread(it, "chat-worker") }.asCoroutineDispatcher()
        try {
            val projection = ChatProjectionWorker(worker)
            withContext(ui) {
                val first = projection.update(listOf(user), derive(listOf(user)), "M", who = {
                    assertTrue(Thread.currentThread().name.startsWith("chat-worker"))
                    "phone"
                })
                val events = listOf(user, reply)
                val next = projection.update(events, derive(events), "M")
                assertEquals(1, first.size)
                assertEquals(2, next.size)
                assertSame(first[0], next[0])
                val changed = events + Event.newBuilder().setSeq(3)
                    .setTokenUsage(TokenUsage.newBuilder().setContextWindow(100).setLastTotalTokens(20)).build()
                assertEquals(buildChat(changed, derive(changed), "M"), projection.update(changed, derive(changed), "M"))
                assertEquals(2, next.size)
            }
        } finally { worker.close(); ui.close() }
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test fun cancelledObsoleteProjectionCannotReplaceTheLatestPublishedResult() = runTest {
        val queued = ArrayDeque<Runnable>()
        val worker = object : CoroutineDispatcher() {
            override fun dispatch(context: kotlin.coroutines.CoroutineContext, block: Runnable) { queued.add(block) }
        }
        val projection = ChatProjectionWorker(worker)
        var published: List<ChatItem> = emptyList()
        val obsolete = launch { published = projection.update(listOf(user), derive(listOf(user)), "old") }
        runCurrent()
        queued.removeFirst().run()
        // The result is computed, but its return to the UI is still queued.
        obsolete.cancel()
        runCurrent()
        assertTrue(published.isEmpty())
        val events = listOf(user, reply)
        val latest = launch { published = projection.update(events, derive(events), "new") }
        runCurrent()
        queued.removeFirst().run()
        latest.join()
        assertEquals(buildChat(events, derive(events), "new"), published)
    }
}
