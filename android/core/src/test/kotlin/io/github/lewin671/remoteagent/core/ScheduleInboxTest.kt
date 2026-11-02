package io.github.lewin671.remoteagent.core

import java.time.Instant
import java.time.ZoneId
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class ScheduleInboxTest {
    private val now = Instant.parse("2026-10-28T12:00:00Z")
    private val utc = ZoneId.of("UTC")
    private fun run(id: String, at: String, notified: Boolean = true, endedAt: String? = null) = buildJsonObject {
        put("id", id); put("started_at", at); put("status", "completed"); put("notified", notified)
        put("notification_text", "通知 $id"); put("final_summary", "结果 $id")
        endedAt?.let { put("ended_at", it) }
    }
    private fun task(id: String, status: String = "scheduled", next: String = "2026-10-29T08:00:00Z", updated: String = "2026-10-28T09:00:00Z", runs: List<JsonObject> = emptyList()) = ScheduledTask("computer", buildJsonObject {
        put("id", id); put("status", status); put("next_run_at", next); put("updated_at", updated); put("runs", JsonArray(runs))
    })
    @Test fun groupsSortByLatestNotificationBeforeTasksWithoutNotifications() {
        for ((status, group) in listOf("scheduled" to ScheduleInboxGroup.RECENT, "paused" to ScheduleInboxGroup.RECENT, "completed" to ScheduleInboxGroup.ENDED)) {
            val old = task("old", status, runs=listOf(run("old", "2026-10-26T08:00:00Z"), run("quiet", "2026-10-28T11:00:00Z", false)))
            val recent = task("recent", status, runs=listOf(run("recent", "2026-10-28T09:00:00Z")))
            val quietA = task("quiet-a", status, next="2026-10-29T10:00:00Z", updated="2026-10-28T11:00:00Z")
            val quietB = task("quiet-b", status, next="2026-10-29T08:00:00Z", updated="2026-10-28T10:00:00Z")
            assertEquals(listOf("recent", "old") + if (group == ScheduleInboxGroup.ENDED) listOf("quiet-a", "quiet-b") else listOf("quiet-b", "quiet-a"),
                scheduleSorted(listOf(quietA, old, quietB, recent), group).map { it.id })
        }
    }
    @Test fun notificationTimePrefersEndAndFallsBackWithoutChangingRunTime() {
        val start = "2026-10-28T08:00:00Z"
        val end = "2026-10-28T11:00:00Z"
        val scheduled = "2026-10-28T07:00:00Z"
        val json = JsonObject(run("r", start, endedAt = end) + ("scheduled_at" to JsonPrimitive(scheduled)))
        val finished = ScheduledRun(json)
        assertEquals(Instant.parse(end), finished.notificationTime)
        assertEquals(Instant.parse(start), finished.time)
        val running = ScheduledRun(JsonObject(json - "ended_at"))
        assertEquals(Instant.parse(start), running.notificationTime)
        val pending = ScheduledRun(JsonObject(json - "ended_at" - "started_at"))
        assertEquals(Instant.parse(scheduled), pending.notificationTime)
        assertEquals(Instant.parse(scheduled), pending.time)
        val empty = ScheduledRun(JsonObject(emptyMap()))
        assertEquals(Instant.EPOCH, empty.notificationTime)
        assertEquals(Instant.EPOCH, empty.time)
    }
    @Test fun laterCompletionWinsForLatestNotificationTaskOrderAndDisplayWhileHistoryUsesCompletion() {
        val slow = run("slow", "2026-10-28T08:00:00Z", endedAt = "2026-10-28T11:00:00Z")
        val fast = run("fast", "2026-10-28T10:00:00Z", endedAt = "2026-10-28T10:30:00Z")
        val combined = task("both", runs = listOf(slow, fast))
        assertEquals(listOf("fast", "slow"), combined.allRuns.map { it.id })
        assertEquals("slow", combined.latestNotification?.id)
        assertEquals("通知 slow", schedulePreview(combined, now, utc))
        assertEquals("11:00", scheduleNotificationTime(combined, now, utc))
        for ((status, group) in listOf("scheduled" to ScheduleInboxGroup.RECENT, "paused" to ScheduleInboxGroup.RECENT, "completed" to ScheduleInboxGroup.ENDED)) {
            val slowTask = task("slow", status, runs = listOf(slow))
            val fastTask = task("fast", status, runs = listOf(fast))
            assertEquals(listOf("slow", "fast"), scheduleSorted(listOf(fastTask, slowTask), group).map { it.id })
        }
    }
    @Test fun summariesAndDatesUseTheLatestNotifiedRunEvenAfterAQuietRun() {
        val task = task("task", runs=listOf(run("older", "2026-10-26T09:30:00Z"), run("latest", "2026-10-28T09:30:00Z"), run("quiet", "2026-10-28T11:00:00Z", false)))
        assertEquals("通知 latest", schedulePreview(task, now, utc))
        assertEquals("09:30", scheduleNotificationTime(task, now, utc))
        assertEquals("昨天", scheduleNotificationTime(task("yesterday", runs=listOf(run("r", "2026-10-27T08:00:00Z"))), now, utc))
        assertEquals("周一", scheduleNotificationTime(task("older", runs=listOf(run("r", "2026-10-26T08:00:00Z"))), now, utc))
        assertEquals("", scheduleNotificationTime(task("never"), now, utc))
        assertEquals("还没有运行 · 下次 明天 08:00", schedulePreview(task("never"), now, utc))
        val attention = task("failed", "failed", runs=task.runs.map { it.json })
        assertEquals("已停下 · ${scheduleReason("")}", schedulePreview(attention, now, utc))
        val summaryOnly = task("summary", runs=listOf(JsonObject(run("r", "2026-10-28T09:00:00Z") - "notification_text")))
        assertEquals("结果 r", schedulePreview(summaryOnly, now, utc))
    }
    @Test fun stoppedTasksUseStopTimeAndEndedUnreadStaysRecent() {
        val stopped = task("stopped", "failed", runs = listOf(run("r", "2026-10-28T08:00:00Z", false, "2026-10-28T11:00:00Z")))
        val older = task("older", "failed", updated = "2026-10-28T10:00:00Z")
        assertEquals(listOf("stopped", "older"), scheduleSorted(listOf(older, stopped), ScheduleInboxGroup.ATTENTION).map { it.id })
        assertEquals("11:00", scheduleNotificationTime(stopped, now, utc))
        val ended = task("ended", "completed", runs = listOf(run("r", "2026-10-28T08:00:00Z")))
        assertEquals(ScheduleInboxGroup.RECENT, scheduleInboxGroup(ended, true))
        assertEquals(ScheduleInboxGroup.ENDED, scheduleInboxGroup(ended, false))
        assertEquals(ScheduleInboxGroup.RECENT, scheduleInboxGroup(task("paused", "paused"), false))
        assertEquals(listOf("ended"), scheduleSorted(listOf(ended), ScheduleInboxGroup.RECENT) { true }.map { it.id })
    }
    @Test fun rowDatesUseLocalCalendarAndNeverRelativeMinutes() {
        val local = ZoneId.of("America/Los_Angeles")
        val now = Instant.parse("2026-11-01T09:30:00Z") // DST fallback day
        val cases = mapOf("2026-11-01T09:15:00Z" to "01:15", "2026-11-01T06:59:00Z" to "昨天",
            "2026-10-26T18:00:00Z" to "周一", "2026-10-25T18:00:00Z" to "10月25日", "2025-12-31T23:00:00Z" to "2025年12月31日")
        cases.forEach { (date, expected) -> assertEquals(expected, scheduleRowTime(Instant.parse(date), now, local)) }
        assertEquals("昨天", scheduleRowTime(Instant.parse("2025-12-31T23:00:00Z"), Instant.parse("2026-01-01T12:00:00Z"), utc))
    }

    @Test fun weekdayTakesPriorityOverYearWithinSevenCalendarDays() {
        val newYear = Instant.parse("2027-01-02T12:00:00Z")
        assertEquals("周三", scheduleRowTime(Instant.parse("2026-12-30T08:00:00Z"), newYear, utc))
        assertEquals("周日", scheduleRowTime(Instant.parse("2026-12-27T08:00:00Z"), newYear, utc))
        assertEquals("2026年12月26日", scheduleRowTime(Instant.parse("2026-12-26T08:00:00Z"), newYear, utc))
        assertEquals("昨天", scheduleRowTime(Instant.parse("2026-12-31T08:00:00Z"), Instant.parse("2027-01-01T12:00:00Z"), utc))
    }
    @Test fun endedTasksUseUpdatedTimeAsTheSecondarySortIncludingWithoutResults() {
        val older = task("a", "completed", next = "2026-10-29T08:00:00Z", updated = "2026-10-28T08:00:00Z")
        val newer = task("z", "completed", next = "2026-10-29T10:00:00Z", updated = "2026-10-28T10:00:00Z")
        assertEquals(listOf("z", "a"), scheduleSorted(listOf(older, newer), ScheduleInboxGroup.ENDED).map { it.id })
        val result = listOf(run("r", "2026-10-28T08:00:00Z"))
        val tied = listOf(older, newer).map { it.copy(json = JsonObject(it.json + ("runs" to JsonArray(result)))) }
        assertEquals(listOf("z", "a"), scheduleSorted(tied, ScheduleInboxGroup.ENDED).map { it.id })
    }

    @Test fun activeTaskWithoutAnyNotifiedRunOnlyPreviewsNoResult() {
        for (status in listOf("dispatching", "queued", "running")) {
            val active = JsonObject(run("active", "2026-10-28T11:00:00Z", false) + ("status" to JsonPrimitive(status)))
            val first = task("first").let { it.copy(json = JsonObject(it.json + ("active_run" to active))) }
            assertEquals("还没有结果", schedulePreview(first, now, utc))
            val retrying = first.copy(json = JsonObject(first.json + mapOf("status" to JsonPrimitive("failed"), "reason" to JsonPrimitive("execution_failed"))))
            assertEquals("还没有结果", schedulePreview(retrying, now, utc))
            assertEquals("", scheduleNotificationTime(first, now, utc))
            val quiet = run("quiet", "2026-10-27T08:00:00Z", false)
            assertEquals("还没有结果", schedulePreview(first.copy(json = JsonObject(first.json + ("runs" to JsonArray(listOf(quiet))))), now, utc))
            val result = run("result", "2026-10-28T09:30:00Z")
            val again = first.copy(json = JsonObject(first.json + ("runs" to JsonArray(listOf(result)))))
            assertEquals("通知 result", schedulePreview(again, now, utc))
            assertEquals("通知 result", schedulePreview(again.copy(json = JsonObject(again.json + ("status" to JsonPrimitive("failed")))), now, utc))
            assertEquals("09:30", scheduleNotificationTime(again, now, utc))
            val history = task("history", runs = listOf(active))
            assertEquals("还没有结果", schedulePreview(history, now, utc))
        }
        assertEquals("还没有运行 · 下次 明天 08:00", schedulePreview(task("never"), now, utc))
    }
    @Test fun onlyDistinctAgentNotificationTextBecomesAResultTitle() {
        fun result(source: String?, notice: String = "  简短标题  ", summary: String = "完整结果", status: String = "completed") = ScheduledRun(buildJsonObject {
            put("notified", true); put("status", status); put("notification_text", notice); put("final_summary", summary)
            if (source != null) put("notification_source", source)
        })
        for (source in listOf("agent", "agent_and_daemon")) {
            assertEquals("简短标题", result(source).resultTitle)
            assertEquals("", result(source, "  一样的结果\n", "\n一样的结果 ").resultTitle)
            assertEquals("", result(source, "  ").resultTitle)
            assertEquals("简短标题", result(source, summary = "").resultTitle)
            assertEquals("**标题**", result(source, "**标题**", "标题").resultTitle) // No client Markdown cleaning.
        }
        for (source in listOf(null, "", "daemon", "undeliverable")) {
            for (status in listOf("completed", "failed", "interrupted", "cancelled")) assertEquals("", result(source, status = status).resultTitle)
            assertEquals("  简短标题  ", result(source).summary) // List summaries retain their existing priority and text.
        }
    }

}
