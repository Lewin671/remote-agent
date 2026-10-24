package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import java.time.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class SchedulesTest {
    private val now = Instant.parse("2026-10-24T14:00:00Z")
    private val utc = ZoneId.of("UTC")
    private fun task(status: String = "scheduled", reason: String = "", runs: String = "[]") = ScheduledTask("computer", Json.parseToJsonElement("""{"id":"task","revision":3,"status":"$status","reason":"$reason","next_run_at":"2026-10-25T08:00:00Z","spec":{"title":"Check","message":"Look","trigger":{"type":"interval","interval_seconds":3600},"target":{"type":"new_session"}},"runs":$runs}""").jsonObject)
    private fun run(id: String, hour: Int, notified: Boolean = false, status: String = "completed") = """{"id":"$id","started_at":"2026-10-24T${hour}:00:00Z","status":"$status","notified":$notified,"notification_text":"Important","final_summary":"Nothing changed"}"""
    @Test fun groupsStopAfterOneFailureAndSeparateUserPauses() {
        assertEquals(TaskGroup.ATTENTION, taskGroup(task("failed", "execution_failed", "[${run("r", 12, status = "failed")}]")))
        assertEquals(TaskGroup.ATTENTION, taskGroup(task("uncertain")))
        assertEquals(TaskGroup.ATTENTION, taskGroup(task("paused", "agent_schedule_permissions_changed")))
        assertEquals(TaskGroup.PAUSED, taskGroup(task("paused", "user_paused")))
        assertEquals(TaskGroup.PAUSED, taskGroup(task("paused", "completed_while_paused")))
        assertEquals(TaskGroup.ACTIVE, taskGroup(task()))
        assertEquals(TaskGroup.ENDED, taskGroup(task("completed")))
        assertEquals(TaskGroup.ENDED, taskGroup(task("cancelled")))
        assertEquals("中继上的会话数已满", scheduleReason("relay_session_limit"))
    }
    @Test fun nextTimeUsesLocalDateAndTaskState() {
        assertEquals("明天 8:00", scheduleNext(task(), now, utc))
        assertEquals("已暂停", scheduleNext(task("paused", "user_paused"), now, utc))
        assertEquals("已停下", scheduleNext(task("failed"), now, utc))
        assertEquals("已结束", scheduleNext(task("completed"), now, utc))
        assertEquals("今天 1:00", scheduleWhen(Instant.parse("2026-10-25T08:00:00Z"), Instant.parse("2026-10-25T07:00:00Z"), ZoneId.of("America/Los_Angeles")))
    }
    @Test fun lastResultCountsOnlyQuietCompletedRunsToday() {
        val quiet = "[${run("a", 12)},${run("b", 13)}]"
        assertEquals("今天 2 次都没有通知", scheduleLastResult(task(runs = quiet), now, utc))
        val notified = "[${run("a", 12)},${run("b", 13, true)}]"
        assertEquals("今天 13:00 通知了你：Important", scheduleLastResult(task(runs = notified), now, utc))
        assertEquals("今天 13:00 失败：Nothing changed", scheduleLastResult(task(runs = "[${run("b", 13, status = "failed")}]"), now, utc))
    }
    @Test fun historyGroupsQuietRunsByDayWithoutHidingResults() {
        val t = task(runs = "[${run("a", 12)},${run("b", 13, true)},${run("c", 14)}]")
        val groups = scheduleRunGroups(t.allRuns, utc)
        assertEquals(2, groups.size)
        assertEquals(listOf("c", "a"), groups.first().runs.map { it.id })
        assertEquals("b", groups.last().runs.single().id)
        assertFalse(groups.last().quiet)
    }
    @Test fun timeEditorMapsTheFourTriggersAndLimits() {
        assertEquals("at", ScheduleTime(0, "15:00", "2026-10-24", "UTC").trigger(now).text("type"))
        val daily = ScheduleTime(1, zone = "Asia/Shanghai", workdays = true, end = 1, count = "4").trigger(now)
        assertEquals("calendar", daily.text("type")); assertEquals("08:00", daily.text("time"))
        assertEquals(listOf(1,2,3,4,5), daily["weekdays"]!!.jsonArray.map { it.jsonPrimitive.int })
        assertEquals(4L, daily.long("max_runs"))
        val weekly = ScheduleTime(2, zone = "UTC", weekdays = setOf(7,1)).trigger(now)
        assertEquals("每周一、日 08:00", scheduleFrequency(weekly))
        val interval = ScheduleTime(3, zone = "UTC", interval = "15", unit = 0, end = 2, until = "2026-10-25").trigger(now)
        assertEquals(900L, interval.long("interval_seconds")); assertEquals("2026-10-25T23:59:59Z", interval.text("until"))
        assertEquals("每 15 分钟", scheduleFrequency(interval))
        assertEquals("每小时", scheduleFrequency(task().trigger))
        assertEquals(interval, ScheduleTime.from(interval).trigger(now))
    }
    @Test fun editingPreservesAnExactDeadlineAndSubMinuteIntervals() {
        val trigger = Json.parseToJsonElement("""{"type":"interval","interval_seconds":90,"until":"2026-10-25T18:00:00Z"}""").jsonObject
        assertEquals(90L, ScheduleTime.from(trigger).trigger(now).long("interval_seconds"))
        assertEquals("2026-10-25T18:00:00Z", ScheduleTime.from(trigger).trigger(now).text("until"))
        val limited = ScheduledTask("computer", JsonObject(task().json + ("spec" to JsonObject(task().spec + ("trigger" to JsonObject(task().trigger + ("max_runs" to JsonPrimitive(4)))))) + ("dispatches" to JsonPrimitive(1))))
        assertEquals("还剩 3 次", scheduleLimits(limited, now))
    }
    @Test fun invalidTimesAndEndingsAreRejected() {
        listOf(ScheduleTime(0, date = "2020-01-01"), ScheduleTime(zone = "invalid"), ScheduleTime(3, interval = "0"), ScheduleTime(2, weekdays = emptySet()), ScheduleTime(end = 1, count = "0"), ScheduleTime(end = 2, until = "2020-01-01")).forEach {
            assertTrue(runCatching { it.trigger(now) }.isFailure)
        }
    }
    private fun online(revision: Long = 1, connected: Boolean = true, capable: Boolean = true) = RemoteState(conn = if(connected) ConnState.ONLINE else ConnState.OFFLINE, machines = mapOf("computer" to MachineView("computer", connected, 0,
        MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setScheduleRevision(revision).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(capable)).build())))
    @Test fun repositorySubscribesWithoutPollingAndRefetchesRevisionArrivingDuringRead() = runTest {
        val remote = MutableStateFlow(online())
        var calls = 0
        val gate = CompletableDeferred<Unit>()
        val repo = ScheduleRepository(backgroundScope, remote, send = { _, _ ->
            calls++; if (calls == 1) gate.await()
            """{"tasks":[]}""".toByteArray()
        }, cacheKey = ByteArray(32))
        runCurrent(); assertEquals(1, calls)
        remote.value = online(2); runCurrent(); gate.complete(Unit); runCurrent()
        assertEquals(2, calls); assertEquals(2L, repo.state.value["computer"]!!.revision)
        advanceTimeBy(120_000); runCurrent(); assertEquals(2, calls)
        remote.value = online(2, false); runCurrent(); remote.value = online(2); runCurrent()
        // No change and a verified list: reconnect does not poll.
        assertEquals(2, calls)
    }
    @Test fun cacheIsCiphertextAndOfflineListsAreReadOnly() = runTest {
        val remote = MutableStateFlow(online())
        var saved: ByteArray? = null
        val json = """{"tasks":[${task().json}]}""".toByteArray()
        val repo = ScheduleRepository(backgroundScope, remote, { _, _ -> json }, write = { _, b -> saved = b }, cacheKey = ByteArray(32))
        runCurrent()
        assertFalse(saved!!.decodeToString().contains("Nothing changed"))
        assertFalse(saved!!.decodeToString().contains("Check"))
        remote.value = online(1, false); runCurrent()
        assertEquals(1, repo.tasks().size)
        assertTrue(runCatching { repo.operate("computer", "pause", task()) }.isFailure)
        val restored = ScheduleRepository(backgroundScope, remote, { _, _ -> error("offline must not send") }, read = { saved }, cacheKey = ByteArray(32))
        runCurrent(); assertEquals("Check", restored.tasks().single().title); assertTrue(restored.state.value["computer"]!!.cached)
        remote.value = online(capable = false); runCurrent(); assertTrue(restored.tasks().isEmpty())
    }
    @Test fun mutationsCarryRevisionAndCreationIdentityAndConflictsRefresh() = runTest {
        val remote = MutableStateFlow(online())
        val requests = mutableListOf<ScheduleRequest>()
        val repo = ScheduleRepository(backgroundScope, remote, { _, req ->
            requests += req
            if(req.operation == "update") throw CommandException("aborted", "scheduled task changed", true)
            (if(req.operation == "list") """{"tasks":[${task().json}]}""" else task().json.toString()).toByteArray()
        }, cacheKey = ByteArray(32))
        runCurrent()
        repo.operate("computer", "create", spec = task().spec, requestId = "stable")
        repo.operate("computer", "create", spec = task().spec, requestId = "stable")
        assertEquals(listOf("stable", "stable"), requests.filter { it.operation == "create" }.map { it.requestId })
        val e = runCatching { repo.operate("computer", "update", task(), task().spec) }.exceptionOrNull()
        assertTrue(e!!.message!!.contains("已重新读取"))
        assertEquals(3L, requests.first { it.operation == "update" }.expectedRevision)
        assertEquals("list", requests.last().operation)
    }
}
