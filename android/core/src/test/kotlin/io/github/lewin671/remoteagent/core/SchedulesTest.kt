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
    @Test fun nonexistentWallTimesAreRejectedForOnceAndCutoffDates() {
        val message = "这个时区在该时刻发生夏令时跳转，请换一个时间"
        val missing = ScheduleTime(kind = 0, date = "2027-03-14", time = "02:30", zone = "America/Los_Angeles")
        assertEquals(message, runCatching { missing.trigger(now) }.exceptionOrNull()?.message)
        // Date-only cutoffs must reject an entirely skipped local date too.
        val skipped = ScheduleTime(end = 2, until = "2011-12-30", zone = "Pacific/Apia")
        assertEquals(message, runCatching { skipped.trigger(Instant.parse("2010-01-01T00:00:00Z")) }.exceptionOrNull()?.message)
        assertEquals("calendar", missing.copy(kind = 1).trigger(now).text("type"))
    }
    @Test fun repeatedWallTimesChooseTheEarlierOccurrenceAndCutoffsKeepTheirInstant() {
        for ((zone, date, time, expected) in listOf(
            listOf("America/Los_Angeles", "2027-11-07", "01:30", "2027-11-07T08:30:00Z"),
            listOf("Europe/Berlin", "2027-10-31", "02:30", "2027-10-31T00:30:00Z")
        )) {
            assertEquals(expected, ScheduleTime(kind = 0, date = date, time = time, zone = zone).trigger(now).text("at"))
        }
        val cutoff = ScheduleTime(end = 2, until = "2027-11-07", zone = "America/Los_Angeles")
        assertEquals("2027-11-08T07:59:59Z", cutoff.trigger(now).text("until"))
        val existing = cutoff.copy(deadline = "2027-11-07T09:30:00Z")
        assertEquals(existing.deadline, existing.inZone("UTC").trigger(now).text("until"))
        // A skipped next day must not invalidate the selected day's last second.
        assertEquals("2011-12-30T09:59:59Z", cutoff.copy(zone = "Pacific/Apia", until = "2011-12-29").trigger(Instant.parse("2010-01-01T00:00:00Z")).text("until"))
    }
    @Test fun switchingToOnceIgnoresHiddenRecurrenceLimits() {
        val t=Json.parseToJsonElement("""{"type":"calendar","time":"08:00","timezone":"UTC","until":"2026-10-01T00:00:00Z"}""").jsonObject
        val once=ScheduleTime.from(t).copy(kind=0,date="2026-10-25")
        assertEquals(2,once.end)
        val trigger=once.trigger(now)
        assertEquals("at",trigger.text("type"));assertNull(trigger["until"])
        assertEquals("at",ScheduleTime(kind=0,date="2026-10-25",end=1,count="").trigger(now).text("type"))
    }
    @Test fun dualLimitsSelectCountAndKeepTheAbsoluteDeadlineAcrossZoneChanges() {
        val t=Json.parseToJsonElement("""{"type":"calendar","time":"08:00","timezone":"America/Los_Angeles","max_runs":5,"until":"2027-10-30T08:00:00Z"}""").jsonObject
        val edit=ScheduleTime.from(t);assertEquals(1,edit.end)
        val changed=edit.copy(zone="Asia/Shanghai").trigger(java.time.Instant.parse("2026-10-24T00:00:00Z"))
        assertEquals(t["until"],changed["until"]);assertEquals(5L,changed.long("max_runs"))
    }
    @Test fun cutoffDisplayAndSubmissionKeepTheirInstantWhenTimezoneChanges() {
        val t=Json.parseToJsonElement("""{"type":"calendar","time":"08:00","timezone":"Asia/Shanghai","until":"2027-10-30T00:15:29Z"}""").jsonObject
        var edit=ScheduleTime.from(t)
        assertEquals("2027-10-30",edit.until)
        edit=edit.inZone("America/Los_Angeles")
        assertEquals("2027-10-29",edit.until);assertEquals(t["until"],edit.trigger(now)["until"])
        edit=edit.inZone("Asia/Tokyo")
        assertEquals("2027-10-30",edit.until);assertEquals(t["until"],edit.trigger(now)["until"])
        edit=edit.copy(until="2027-11-01",deadline="").inZone("UTC")
        assertEquals("2027-11-01",edit.until)
        assertEquals("2027-11-01T14:59:59Z",edit.trigger(now).text("until"))
    }
    @Test fun indivisibleMinuteIntervalsUseExactIntegerSeconds() {
        for(seconds in listOf(31L,62L,123L)) {
            val t=buildJsonObject { put("type","interval");put("interval_seconds",seconds);put("delay_seconds",29);put("at","2026-10-01T08:00:00Z") }
            val edit=ScheduleTime.from(t)
            assertEquals(3,edit.unit);assertEquals(seconds.toString(),edit.interval)
            val saved=edit.trigger(now)
            assertEquals(seconds,saved.long("interval_seconds"));assertEquals(29L,saved.long("delay_seconds"));assertEquals(t["at"],saved["at"])
        }
    }
    @Test fun nonCalendarTimestampOffsetsDoNotBecomeCalendarTimezoneNames() {
        val old=java.util.TimeZone.getDefault()
        try {
            java.util.TimeZone.setDefault(java.util.TimeZone.getTimeZone("America/Los_Angeles"))
            for(at in listOf("2027-10-24T15:00:00Z","2027-10-24T15:00:00+08:00")) {
                val input=buildJsonObject { put("type","at");put("at",at) }
                val edited=ScheduleTime.from(input).copy(kind=1).trigger(java.time.Instant.parse("2026-10-24T00:00:00Z"))
                assertEquals("America/Los_Angeles",edited.text("timezone"))
            }
        } finally { java.util.TimeZone.setDefault(old) }
    }
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
        assertEquals("下次 明天 08:00", scheduleNext(task(), now, utc))
        assertEquals("已暂停", scheduleNext(task("paused", "user_paused"), now, utc))
        assertEquals("已停下", scheduleNext(task("failed"), now, utc))
        assertEquals("已结束", scheduleNext(task("completed"), now, utc))
        assertEquals("今天 01:00", scheduleWhen(Instant.parse("2026-10-25T08:00:00Z"), Instant.parse("2026-10-25T07:00:00Z"), ZoneId.of("America/Los_Angeles")))
    }
    @Test fun lastResultCountsOnlyQuietCompletedRunsToday() {
        val quiet = "[${run("a", 12)},${run("b", 13)}]"
        assertEquals("今天 2 次，都没有通知", scheduleLastResult(task(runs = quiet), now, utc))
        val notified = "[${run("a", 12)},${run("b", 13, true)}]"
        assertEquals("今天 13:00 通知了你：Important", scheduleLastResult(task(runs = notified), now, utc))
        assertEquals("今天 13:00 失败：运行结果无法确认", scheduleLastResult(task(runs = "[${run("b", 13, status = "failed")}]"), now, utc))
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
        assertEquals("共 4 次", scheduleLimits(limited, now))
    }
    @Test fun invalidTimesAndEndingsAreRejected() {
        listOf(ScheduleTime(0, date = "2020-01-01"), ScheduleTime(zone = "invalid"), ScheduleTime(3, interval = "0"), ScheduleTime(2, weekdays = emptySet()), ScheduleTime(end = 1, count = "0"), ScheduleTime(end = 2, until = "2020-01-01")).forEach {
            assertTrue(runCatching { it.trigger(now) }.isFailure)
        }
    }
    private fun online(revision: Long = 1, connected: Boolean = true, capable: Boolean = true) = RemoteState(conn = if(connected) ConnState.ONLINE else ConnState.OFFLINE, machines = mapOf("computer" to MachineView("computer", connected, 0,
        MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setScheduleRevision(revision).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(capable)).build())))
    @Test fun backgroundListsDriveUnreadBadgeAndOpeningTheListMarksLoadedResultsSeen() = runTest {
        val remote = MutableStateFlow(online())
        var list = emptyList<ScheduledTask>()
        val repo = ScheduleRepository(backgroundScope, remote, send = { _, _ ->
            buildJsonObject { put("tasks", JsonArray(list.map { it.json })) }.toString().toByteArray()
        })
        runCurrent(); assertEquals(0, repo.badge.value)
        list = listOf(task(runs = "[${run("new", 12, true)}]"))
        remote.value = online(2); runCurrent(); assertEquals(1, repo.badge.value)
        remote.value = online(2, false); runCurrent(); assertEquals(1, repo.badge.value)
        repo.setListOpen(true); assertEquals(0, repo.badge.value)
        remote.value = online(3); list = listOf(task(runs = "[${run("arriving", 13, true)}]"))
        runCurrent(); assertEquals(0, repo.badge.value)
        repo.setListOpen(false)
        list = listOf(task(runs = "[${run("later", 14, true)}]"))
        remote.value = online(4); runCurrent(); assertEquals(1, repo.badge.value)
        remote.value = RemoteState(); runCurrent(); assertEquals(0, repo.badge.value)
    }
    @Test fun repositorySubscribesWithoutPollingAndRefetchesRevisionArrivingDuringRead() = runTest {
        val remote = MutableStateFlow(online())
        var calls = 0
        val gate = CompletableDeferred<Unit>()
        val repo = ScheduleRepository(backgroundScope, remote, send = { _, _ ->
            calls++; if (calls == 1) gate.await()
            """{"tasks":[]}""".toByteArray()
        })
        runCurrent(); assertEquals(1, calls)
        remote.value = online(2); runCurrent(); gate.complete(Unit); runCurrent()
        assertEquals(2, calls); assertEquals(2L, repo.state.value["computer"]!!.revision)
        advanceTimeBy(120_000); runCurrent(); assertEquals(2, calls)
        remote.value = online(2, false); runCurrent(); remote.value = online(2); runCurrent()
        // No change and a verified list: reconnect does not poll.
        assertEquals(2, calls)
    }
    @Test fun verifiedOfflineListsAreReadOnlyAndRemovedOrDeletedTasksDisappear() = runTest {
        val remote = MutableStateFlow(online(connected = false))
        val deleted = ScheduledTask("computer", JsonObject(task().json + mapOf("status" to JsonPrimitive("cancelled"), "reason" to JsonPrimitive("user_cancelled"))))
        val bytes = """{"tasks":[${task().json},${deleted.json}]}""".toByteArray()
        var cleared = false
        val repo = ScheduleRepository(backgroundScope, remote, { _, _ -> error("offline must not send") }, read = { bytes }, clear = { cleared = true })
        runCurrent(); assertEquals(1,repo.tasks().size); assertEquals("Check",repo.tasks().single().title); assertTrue(repo.state.value["computer"]!!.cached)
        assertTrue(runCatching { repo.operate("computer","pause",task()) }.isFailure)
        remote.value = RemoteState(); runCurrent(); assertTrue(repo.tasks().isEmpty()); assertTrue(cleared)
    }
    @Test fun forgottenMachineCannotReturnThroughLateReadErrors() = runTest {
        for(cached in listOf(true,false)) {
            val remote=MutableStateFlow(online(connected=false));val entered=CompletableDeferred<Unit>();val release=CompletableDeferred<Unit>()
            val repo=ScheduleRepository(backgroundScope,remote,send={_,_->entered.complete(Unit);release.await();error("late list failure")},read={if(cached){entered.complete(Unit);release.await();error("late cache failure")}else null})
            runCurrent()
            val request=if(cached)null else {
                remote.value=online()
                backgroundScope.launch(start=CoroutineStart.UNDISPATCHED) {repo.refresh("computer")}
            }
            runCurrent();assertTrue(entered.isCompleted)
            repo.forget("computer");release.complete(Unit);runCurrent()
            assertFalse("removed snapshot resurrected by error, cached=$cached",repo.state.value.containsKey("computer"))
            request?.join()
        }
    }
    @Test fun mutationsCarryRevisionAndCreationIdentityAndConflictsRefresh() = runTest {
        val remote = MutableStateFlow(online())
        val requests = mutableListOf<ScheduleRequest>()
        val repo = ScheduleRepository(backgroundScope, remote, { _, req ->
            requests += req
            if(req.operation == "update") throw CommandException("aborted", "scheduled task changed", true)
            (if(req.operation == "list") """{"tasks":[${task().json}]}""" else task().json.toString()).toByteArray()
        })
        runCurrent()
        repo.operate("computer", "create", spec = task().spec, requestId = "stable")
        repo.operate("computer", "create", spec = task().spec, requestId = "stable")
        assertEquals(listOf("stable", "stable"), requests.filter { it.operation == "create" }.map { it.requestId })
        val e = runCatching { repo.operate("computer", "update", task(), task().spec) }.exceptionOrNull()
        assertEquals("任务刚被修改，已更新为最新内容",e!!.message)
        assertEquals(3L, requests.first { it.operation == "update" }.expectedRevision)
        assertEquals("list", requests.last().operation)
    }
    @Test fun optionalNamesMatchFirstSentenceWhitespaceAndUnicodeLimits() {
        assertEquals("项目 早报", scheduleName("\uFEFF  项目\u3000早报 \uFEFF", "消息"))
        assertEquals("项目早报", scheduleName("  项目早报 ", "其他消息"))
        assertEquals("每天 检查", scheduleName("", "  每天  检查。发现问题通知我。\n不要做其他事"))
        assertEquals("Review v1.2", scheduleName("", "Review v1.2. Notify me!"))
        assertEquals("First", scheduleName("", "First! Second"))
        assertEquals("第一行", scheduleName("", "第一行\r\n第二行"))
        val emoji = "😀".repeat(61)
        assertEquals(60, scheduleName(emoji, "").codePointCount(0, scheduleName(emoji, "").length))
    }
    @Test fun renamePreservesTheCompleteSpecAndSameNameDoesNotSend() = runTest {
        val t = task()
        val remote = MutableStateFlow(online())
        val requests = mutableListOf<ScheduleRequest>()
        val repo = ScheduleRepository(backgroundScope, remote, { _, req ->
            requests += req
            if (req.operation == "update") {
                val spec = Json.parseToJsonElement(req.specJson.toStringUtf8()).jsonObject
                assertEquals(t.spec - "title", spec - "title")
                assertEquals("Renamed", spec.text("title")); assertEquals(t.revision, req.expectedRevision)
                JsonObject(t.json + ("spec" to spec)).toString().toByteArray()
            } else """{"tasks":[]}""".toByteArray()
        })
        runCurrent(); requests.clear()
        repo.rename(t, "Check")
        assertTrue(requests.isEmpty())
        repo.rename(t, " Renamed ")
        assertEquals(1, requests.count { it.operation == "update" })
        val states = listOf("scheduled", "waiting_turn", "paused", "failed", "uncertain", "completed", "cancelled", "running")
        states.forEach { repo.rename(task(it), "Renamed") }
        repo.rename(ScheduledTask(t.machine, JsonObject(t.json + ("manual_request" to buildJsonObject { put("request_id", "pending") }))), "Renamed")
        repo.rename(ScheduledTask(t.machine, JsonObject(t.json + ("active_run" to buildJsonObject { put("status", "running") }))), "Renamed")
        assertEquals(1 + states.size + 2, requests.count { it.operation == "update" })
        assertTrue(scheduleRenameAllowed(true))
        assertFalse(scheduleRenameAllowed(false)); assertFalse(scheduleRenameAllowed(true, true))
    }
    @Test fun abnormalResultsAndReasonsWinOverEarlierNotificationOrSummary() {
        val failed = ScheduledRun(Json.parseToJsonElement("""{"status":"failed","notified":true,"notification_text":"Old notice","final_summary":"Old final","reason":"relay_session_limit"}""").jsonObject)
        assertEquals("失败", runResult(failed)); assertEquals("中继上的会话数已满", failed.summary)
        assertEquals("无法确认", runResult(ScheduledRun(buildJsonObject { put("status", "uncertain"); put("notified", true) })))
        assertEquals("电脑返回的原因：new_reason", scheduleReason("new_reason"))
        assertEquals("运行结果无法确认", scheduleReason(""))
        assertEquals("消息没能加入待发队列", scheduleReason("could_not_queue_message"))
        assertEquals("电脑重启前的发送结果无法确认", scheduleReason("restart_dispatching"))
        assertEquals("还没有运行", scheduleLastResult(task(), now, utc))
        val active = ScheduledTask("computer", JsonObject(task().json + ("active_run" to Json.parseToJsonElement(run("live", 14, status="running")))))
        assertFalse(scheduleLastResult(active, now, utc).contains("次都没有通知"))
    }

    @Test fun finalClientContractUsesActualGroupingSortingAndReasons() {
        val data = Json.parseToJsonElement(java.io.File(System.getProperty("scheduleClients")).readText()).jsonObject
        assertEquals(data.text("empty"),SCHEDULE_EMPTY);assertEquals(data.text("offline"),SCHEDULE_OFFLINE)
        assertEquals(data.text("offlineRow"),SCHEDULE_OFFLINE_ROW)
        data["titleFromMessage"]!!.jsonArray.forEach { pair -> assertEquals(pair.jsonArray[1].jsonPrimitive.content, scheduleName("",pair.jsonArray[0].jsonPrimitive.content)) }
        val tasks = data["tasks"]!!.jsonArray.map { ScheduledTask("computer", it.jsonObject) }
        TaskGroup.entries.forEach { group -> assertEquals(data["groups"]!!.jsonObject[group.label]!!.jsonArray.map { it.jsonPrimitive.content }, scheduleSorted(tasks,group).map { it.id }) }
        val stopped=tasks.first { it.id=="attention" }
        assertEquals("创建任务时的权限已改变，任务已停下",scheduleLastResult(stopped))
        assertEquals("没有通知",runResult(stopped.runs.single()))
        assertEquals("没有通知",stopped.runs.single().foldedSummary)
        val limited=ScheduledTask(stopped.machine,JsonObject(stopped.json+("spec" to JsonObject(stopped.spec+("trigger" to JsonObject(stopped.trigger+mapOf("max_runs" to JsonPrimitive(5),"until" to JsonPrimitive("2026-10-30T08:00:00Z"))))))))
        assertEquals("共 5 次，或到 10月30日 08:00",scheduleLimits(limited,now,utc))
        assertEquals("已暂停：期间计划已到期",scheduleReason("completed_while_paused"))
        val old=ScheduledTask("computer",JsonObject(task("paused","user_paused").json + mapOf("id" to JsonPrimitive("old"),"updated_at" to JsonPrimitive("2026-10-24T10:00:00Z"),"next_run_at" to JsonPrimitive("2026-10-24T13:00:00Z"))))
        val newer=ScheduledTask("computer",JsonObject(old.json + mapOf("id" to JsonPrimitive("new"),"updated_at" to JsonPrimitive("2026-10-24T11:00:00Z"),"next_run_at" to JsonPrimitive("2026-10-24T14:00:00Z"))))
        assertEquals(listOf("new","old"),scheduleSorted(listOf(old,newer),TaskGroup.PAUSED).map { it.id })
    }
    @Test fun messageLimitCountsUtf8Bytes() {
        assertTrue(scheduleMessageValid("a".repeat(32768)))
        assertFalse(scheduleMessageValid("a".repeat(32769)))
        assertTrue(scheduleMessageValid("中".repeat(10922)))
        assertFalse(scheduleMessageValid("中".repeat(10923)))
        assertFalse(scheduleMessageValid(" \n "))
    }
    @Test fun cursorStartsAfterDispatchWithZeroStartFallback() {
        assertEquals(43L,ScheduledRun(buildJsonObject { put("start_seq",42);put("after_seq",9) }).seq)
        assertEquals(10L,ScheduledRun(buildJsonObject { put("start_seq",0);put("after_seq",9) }).seq)
    }
    @Test fun intervalEditsPreservePhaseAndUntouchedEndings() {
        val original=Json.parseToJsonElement("""{"type":"interval","interval_seconds":900,"at":"2026-10-01T08:00:00Z","delay_seconds":29,"max_runs":4,"until":"2026-10-30T08:00:29Z"}""").jsonObject
        val edited=ScheduleTime.from(original).copy(count="5").trigger(now)
        assertEquals(original["at"],edited["at"]);assertEquals(original["delay_seconds"],edited["delay_seconds"]);assertEquals(original["until"],edited["until"]);assertEquals(5L,edited.long("max_runs"))
        assertEquals(original,ScheduleTime.from(original).trigger(now))
        val fresh=ScheduleTime(kind=3,interval="15",unit=0).trigger(now)
        assertEquals(900L,fresh.long("delay_seconds"))
    }
    @Test fun activeAndQueuedManualWorkDisableAnotherRunAndActiveDefinitions() {
        val t=task()
        val active=ScheduledTask(t.machine,JsonObject(t.json + ("active_run" to buildJsonObject { put("status","running") })))
        val manual=ScheduledTask(t.machine,JsonObject(t.json + ("manual_request" to buildJsonObject { put("request_id","q") })))
        assertFalse(scheduleRunAllowed(active));assertFalse(scheduleDefinitionAllowed(active));assertFalse(scheduleRunAllowed(manual))
        assertTrue(scheduleDefinitionAllowed(manual));assertTrue(scheduleRunAllowed(t))
        assertEquals("运行中",scheduleNext(active,now,utc))
        assertEquals("明天 08:00",scheduleWhen(Instant.parse("2026-10-25T08:00:00Z"),now,utc))
    }

}
