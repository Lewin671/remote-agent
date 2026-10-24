package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class ScheduledChatTest {
    private fun turn(from: Long, run: String, notified: Boolean = false, outcome: TurnOutcome = TurnOutcome.TURN_OUTCOME_COMPLETED, scheduled: Boolean = true): List<Event> {
        val origin = if(scheduled) Origin.newBuilder().setScheduledTaskId("task").setScheduledRunId(run).build() else Origin.getDefaultInstance()
        return listOf(
            Event.newBuilder().setSeq(from).setOrigin(origin).setTime(com.google.protobuf.Timestamp.newBuilder().setSeconds(1_792_850_400L + from * 3600)).setUserMessage(UserMessage.newBuilder().setText("Check")).build(),
            Event.newBuilder().setSeq(from+1).setAgentMessage(AgentMessage.newBuilder().setText("No changes")).build(),
            Event.newBuilder().setSeq(from+2).setTurnEnded(TurnEnded.newBuilder().setOutcome(outcome).setScheduledTaskId(if(scheduled) "task" else "").setScheduledRunId(if(scheduled) run else "").setScheduledNotified(notified)).build(),
        )
    }
    @Test fun consecutiveQuietTurnsFoldAndKeepOriginalText() {
        val events = turn(1,"a") + turn(4,"b")
        val rows = buildChat(events, derive(events), "Computer")
        val folded = foldScheduledChat(rows, events).single() as ChatItem.QuietScheduled
        assertEquals(rows, folded.original); assertEquals(2, folded.times.size); assertEquals(6L, folded.through)
        assertEquals(rows, foldScheduledChat(rows, events, revealSeq = 4))
    }
    @Test fun nativeTurnStartsBeforeTheNextScheduledMessageDoNotBlockFolding() {
        val nextStart = Event.newBuilder().setSeq(4).setTurnStarted(TurnStarted.getDefaultInstance()).build()
        val events = turn(1, "a") + nextStart + turn(5, "b")
        val rows = buildChat(events, derive(events), "Computer")
        assertEquals(2, (foldScheduledChat(rows, events).single() as ChatItem.QuietScheduled).times.size)
        // An unfinished follow-up with no next message remains visible.
        assertTrue(quietScheduledSpans(turn(1, "a") + nextStart).isEmpty())
    }
    @Test fun notificationHumanReplyFailureAndPartialRunPreventFolding() {
        val cases = listOf(turn(1,"a") + turn(4,"b", true), turn(1,"a") + turn(4,"human", scheduled=false) + turn(7,"b"), turn(1,"a") + turn(4,"b", outcome=TurnOutcome.TURN_OUTCOME_ABORTED), turn(1,"a") + turn(4,"b").dropLast(1))
        cases.forEach { events ->
            val rows = buildChat(events, derive(events), "Computer")
            assertEquals(rows, foldScheduledChat(rows, events))
        }
    }
    @Test fun scheduledMessagesAndNotificationsAreExplicitInBothProjectionPaths() {
        val first = turn(1,"a").take(1)
        val projection = ChatProjection()
        val rows = projection.update(first, derive(first), "Computer")
        assertTrue((rows.first() as ChatItem.User).from!!.startsWith("定时任务 · "))
        val next = first + turn(4,"b").take(1)
        assertTrue((projection.update(next, derive(next), "Computer").last() as ChatItem.User).from!!.startsWith("定时任务 · "))
        val notification = Event.newBuilder().setSeq(5).setScheduledNotification(ScheduledNotification.newBuilder().setTaskId("task").setRunId("a").setText("New result")).build()
        val updated = next + notification
        val note = projection.update(updated, derive(updated), "Computer").last() as ChatItem.Note
        assertEquals("通知你：New result", note.text)
    }
    @Test fun quietLabelsUsePaddedClockListsAndSharedCopy() {
        val zone = java.time.ZoneId.of("UTC")
        val start = java.time.Instant.parse("2026-10-24T14:00:00Z")
        val many = (0..199).map { start.plusSeconds(it * 60L).toEpochMilli() }
        val label = quietScheduledLabel(many, start, zone)
        assertTrue(label.startsWith("14:00、14:01")); assertTrue(label.endsWith("定时检查 · 没有新回复"))
        val days = quietScheduledLabel(listOf(start.toEpochMilli(), start.plusSeconds(86400).toEpochMilli()), start, zone)
        assertTrue(days.contains("今天")); assertTrue(days.contains("10月25日"))
    }

    @Test fun crossDayRunsAndAutonomousFollowupsRemainVisible() {
        val first = turn(1,"a")
        val nextDay = turn(4,"b").map { it.toBuilder().setTime(com.google.protobuf.Timestamp.newBuilder().setSeconds(first.first().time.seconds + 86400)).build() }
        val both = first + nextDay
        assertFalse(foldScheduledChat(buildChat(both, derive(both), "Computer"), both).any { it is ChatItem.QuietScheduled })
        val followup = turn(1,"a") + Event.newBuilder().setSeq(4).setAgentMessage(AgentMessage.newBuilder().setText("Autonomous follow-up")).build() +
            Event.newBuilder().setSeq(5).setTurnEnded(TurnEnded.newBuilder().setOutcome(TurnOutcome.TURN_OUTCOME_COMPLETED).setScheduledTaskId("task").setScheduledRunId("a")).build() + turn(6,"b")
        assertFalse(foldScheduledChat(buildChat(followup, derive(followup), "Computer"), followup).any { it is ChatItem.QuietScheduled })
        assertTrue(quietScheduledLabel(listOf(first.first().time.seconds*1000, first.first().time.seconds*1000+3600000)).endsWith("定时检查 · 没有新回复"))
    }

    @Test fun oversizedContentAndUnfinishedAutonomousMessagesNeverDisappearInAFold() {
        val skeleton = turn(1,"a").toMutableList()
        skeleton[1] = skeleton[1].toBuilder().setAgentMessage(AgentMessage.newBuilder().setText("内容" + io.github.lewin671.remoteagent.core.skeletonNote)).build()
        val cases = listOf(
            skeleton + turn(4,"b"),
            turn(1,"a") + Event.newBuilder().setSeq(4).setAgentMessage(AgentMessage.newBuilder().setText("自主后续回复")).build() + turn(5,"b"),
        )
        cases.forEach { events -> assertFalse(foldScheduledChat(buildChat(events,derive(events),"Computer"),events).any { it is ChatItem.QuietScheduled }) }
    }

}
