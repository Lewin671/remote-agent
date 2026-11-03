package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Derived
import io.github.lewin671.remoteagent.core.EventProjection
import io.github.lewin671.remoteagent.core.PagedList
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class ScheduledFoldTest {
    private fun turn(from: Long, run: String, outcome: TurnOutcome = TurnOutcome.TURN_OUTCOME_COMPLETED, scheduled: Boolean = true): List<Event> {
        val origin = if (scheduled) Origin.newBuilder().setScheduledTaskId("task").setScheduledRunId(run).build() else Origin.getDefaultInstance()
        return listOf(
            Event.newBuilder().setSeq(from).setOrigin(origin).setTime(com.google.protobuf.Timestamp.newBuilder().setSeconds(1_792_850_400L + from * 3600)).setUserMessage(UserMessage.newBuilder().setText("Check")).build(),
            Event.newBuilder().setSeq(from + 1).setAgentMessage(AgentMessage.newBuilder().setText("No changes")).build(),
            Event.newBuilder().setSeq(from + 2).setTurnEnded(TurnEnded.newBuilder().setOutcome(outcome).setScheduledTaskId(if (scheduled) "task" else "").setScheduledRunId(if (scheduled) run else "")).build(),
        )
    }

    /** Quiet runs, an aborted one, a human turn, more quiet runs: every fold the chat goes through. */
    private val script: List<Event> =
        turn(1, "a") + turn(4, "b") + turn(7, "c") + turn(10, "d", TurnOutcome.TURN_OUTCOME_ABORTED) +
            turn(13, "e") + turn(16, "human", scheduled = false) + turn(19, "f") + turn(22, "g") + turn(25, "h")

    private class Counting {
        val calls = mutableListOf<Int>()
        private val projection = EventProjection()
        val derive: (List<Event>) -> Derived = { calls += it.size; projection.update(it) }
    }

    @Test fun foldingAGrowingChatGivesWhatAFullFoldGives() {
        val fold = ScheduledFold()
        var events = PagedList.empty<Event>()
        var folds = 0
        script.forEach { e ->
            events = events.append(listOf(e))
            val rows = buildChat(events, derive(events), "Computer")
            for (reveal in listOf(0L, 4L, 19L, 0L)) {
                val expected = foldScheduledChat(rows, events, reveal)
                assertEquals("at ${e.seq}, revealing $reveal", expected, fold.fold(rows, events, reveal))
                if (expected.any { it is ChatItem.QuietScheduled }) folds++
            }
        }
        assertTrue(folds > 0)
        // A list that does not extend the last one is worked out from the start.
        val other = turn(1, "x") + turn(4, "y")
        val rows = buildChat(other, derive(other), "Computer")
        assertEquals(foldScheduledChat(rows, other), fold.fold(rows, other))
        assertTrue(fold.fold(rows, other).single() is ChatItem.QuietScheduled)
    }

    @Test fun unchangedInputIsNotWorkedOutAgain() {
        val counting = Counting()
        val fold = ScheduledFold(counting.derive)
        val events = PagedList.from(turn(1, "a") + turn(4, "b"))
        val rows = buildChat(events, derive(events), "Computer")
        val first = fold.fold(rows, events)
        assertSame(first, fold.fold(rows, events))
        assertEquals(listOf(6), counting.calls)
        // Revealing a run, or new rows for the same events, folds again without deriving.
        assertEquals(rows, fold.fold(rows, events, revealSeq = 4))
        val rebuilt = buildChat(events, derive(events), "Computer")
        assertEquals(first, fold.fold(rebuilt, events))
        assertEquals(listOf(6), counting.calls)
        val more = events.append(turn(7, "c"))
        val grown = buildChat(more, derive(more), "Computer")
        assertEquals(foldScheduledChat(grown, more), fold.fold(grown, more))
        assertEquals(listOf(6, 9), counting.calls)
    }

    @Test fun nothingIsDerivedWhileFewerThanTwoRunsCouldFold() {
        val counting = Counting()
        val fold = ScheduledFold(counting.derive)
        var events = PagedList.from(turn(1, "human", scheduled = false) + turn(4, "a"))
        repeat(5) {
            val rows = buildChat(events, derive(events), "Computer")
            assertSame(rows, fold.fold(rows, events))
            events = events.append(listOf(Event.newBuilder().setSeq(7L + it).setAgentMessage(AgentMessage.newBuilder().setText("more $it")).build()))
        }
        assertTrue(counting.calls.isEmpty())
        events = events.append(turn(12, "b"))
        val rows = buildChat(events, derive(events), "Computer")
        assertEquals(foldScheduledChat(rows, events), fold.fold(rows, events))
        assertEquals(1, counting.calls.size)
    }

    @Test fun theWorkerFoldsOffTheCallerAndNeverPublishesBeforeItsFirstProjection() = runBlocking {
        val worker = ChatProjectionWorker()
        assertNull(worker.rows())
        val events = turn(1, "a") + turn(4, "b")
        val raw = worker.update(events, derive(events), "Computer")
        val first = worker.rows()!!
        assertEquals(foldScheduledChat(raw, events), first.items)
        assertSame(events, first.events)
        val revealed = worker.rows { 4 }!!
        assertEquals(raw, revealed.items)
        assertTrue(revealed.version > first.version)
    }
}
