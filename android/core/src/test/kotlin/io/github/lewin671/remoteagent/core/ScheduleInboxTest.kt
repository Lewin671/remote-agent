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
        for ((status, group) in listOf("scheduled" to TaskGroup.ACTIVE, "paused" to TaskGroup.PAUSED, "failed" to TaskGroup.ATTENTION, "completed" to TaskGroup.ENDED)) {
            val old = task("old", status, runs=listOf(run("old", "2026-10-26T08:00:00Z"), run("quiet", "2026-10-28T11:00:00Z", false)))
            val recent = task("recent", status, runs=listOf(run("recent", "2026-10-28T09:00:00Z")))
            val quietA = task("quiet-a", status, next="2026-10-29T10:00:00Z", updated="2026-10-28T11:00:00Z")
            val quietB = task("quiet-b", status, next="2026-10-29T08:00:00Z", updated="2026-10-28T10:00:00Z")
            assertEquals(listOf("recent", "old") + if (group == TaskGroup.ACTIVE) listOf("quiet-b", "quiet-a") else listOf("quiet-a", "quiet-b"),
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
    @Test fun laterCompletionWinsForLatestNotificationTaskOrderAndDisplayWhileHistoryUsesStart() {
        val slow = run("slow", "2026-10-28T08:00:00Z", endedAt = "2026-10-28T11:00:00Z")
        val fast = run("fast", "2026-10-28T10:00:00Z", endedAt = "2026-10-28T10:30:00Z")
        val combined = task("both", runs = listOf(slow, fast))
        assertEquals(listOf("fast", "slow"), combined.allRuns.map { it.id })
        assertEquals("slow", combined.latestNotification?.id)
        assertEquals("通知 slow", schedulePreview(combined, now, utc))
        assertEquals("11:00", scheduleNotificationTime(combined, now, utc))
        for ((status, group) in listOf("scheduled" to TaskGroup.ACTIVE, "paused" to TaskGroup.PAUSED, "failed" to TaskGroup.ATTENTION, "completed" to TaskGroup.ENDED)) {
            val slowTask = task("slow", status, runs = listOf(slow))
            val fastTask = task("fast", status, runs = listOf(fast))
            assertEquals(listOf("slow", "fast"), scheduleSorted(listOf(fastTask, slowTask), group).map { it.id })
        }
    }
    @Test fun summariesAndDatesUseTheLatestNotifiedRunEvenAfterAQuietRun() {
        val task = task("task", runs=listOf(run("older", "2026-10-26T09:30:00Z"), run("latest", "2026-10-28T09:30:00Z"), run("quiet", "2026-10-28T11:00:00Z", false)))
        assertEquals("通知 latest", schedulePreview(task, now, utc))
        assertEquals("9:30", scheduleNotificationTime(task, now, utc))
        assertEquals("昨天", scheduleNotificationTime(task("yesterday", runs=listOf(run("r", "2026-10-27T08:00:00Z"))), now, utc))
        assertEquals("10月26日", scheduleNotificationTime(task("older", runs=listOf(run("r", "2026-10-26T08:00:00Z"))), now, utc))
        assertEquals("", scheduleNotificationTime(task("never"), now, utc))
        assertEquals("还没有运行", schedulePreview(task("never"), now, utc))
        val attention = task("failed", "failed", runs=task.runs.map { it.json })
        assertEquals(scheduleReason(""), schedulePreview(attention, now, utc))
        val summaryOnly = task("summary", runs=listOf(JsonObject(run("r", "2026-10-28T09:00:00Z") - "notification_text")))
        assertEquals("结果 r", schedulePreview(summaryOnly, now, utc))
    }
}
