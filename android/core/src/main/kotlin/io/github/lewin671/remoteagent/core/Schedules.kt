package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.*
import java.time.*
import java.time.format.DateTimeFormatter
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*

/** JSON owned by internal/schedule. Keep the original object when editing: MCP-only triggers and
 * execution settings must survive a title/time edit without guessing defaults. */
data class ScheduledTask(val machine: String, val json: JsonObject) {
    val id get() = json.text("id")
    val revision get() = json.long("revision")
    val spec get() = json.obj("spec")
    val trigger get() = spec.obj("trigger")
    val target get() = spec.obj("target")
    val title get() = spec.text("title")
    val message get() = spec.text("message")
    val notifyOnFinish get() = spec.text("notify") != "agent_only"
    val status get() = json.text("status")
    val reason get() = json.text("reason")
    val next get() = scheduleInstant(json.text("next_run_at"))
    val runs get() = (json["runs"] as? JsonArray).orEmpty().map { ScheduledRun(it.jsonObject) }
    val active get() = (json["active_run"] as? JsonObject)?.let(::ScheduledRun)
    val allRuns get() = (listOfNotNull(active) + runs).distinctBy { it.id }.sortedByDescending { it.time }
    val latestNotification get() = allRuns.filter { it.notified }.maxByOrNull { it.notificationTime }
}
data class ScheduledRun(val json: JsonObject) {
    val id get() = json.text("id")
    val session get() = json.text("session_id")
    val seq get() = (json.long("start_seq").takeIf { it > 0 } ?: json.long("after_seq")) + 1
    val time get() = scheduleInstant(json.text("started_at")) ?: scheduleInstant(json.text("scheduled_at")) ?: Instant.EPOCH
    val notificationTime get() = scheduleInstant(json.text("ended_at")) ?: time
    val status get() = json.text("status")
    val notificationSource get() = when (json.text("notification_source")) { "agent" -> (UiMessages.scheduleAndroidAgentNotification()).messageText(); "daemon" -> (UiMessages.scheduleNotifyOnCompletion()).messageText(); "agent_and_daemon" -> (UiMessages.scheduleAndroidAgentNotificationCompletionNotification()).messageText(); "undeliverable" -> (UiMessages.scheduleAndroidCannotRequestNotification()).messageText(); else -> "".messageText() }
    val notified get() = (json["notified"] as? JsonPrimitive)?.booleanOrNull == true
    val summary: MessageText get() {
        if (status in stoppedScheduleRuns) return localized { copy -> scheduleReason(json.text("reason"), copy = copy) }
        val content = json.text("notification_text").ifEmpty { json.text("final_summary") }
        if (content.isNotEmpty()) return content.messageText()
        return json.text("reason").takeIf { it.isNotEmpty() }?.let { reason -> localized { copy -> scheduleReason(reason, copy = copy) } } ?: "".messageText()
    }
    val resultTitle get() = json.text("notification_text").trim().takeIf {
        json.text("notification_source") in setOf("agent", "agent_and_daemon") && it.isNotEmpty() && it != json.text("final_summary").trim()
    }.orEmpty()
    val foldedSummary get() = json.text("final_summary").takeIf { it.isNotBlank() }?.messageText() ?: UiMessages.scheduleNoNotification().messageText()
    val quiet get() = status == "completed" && !notified
}
fun JsonObject.text(k: String) = (this[k] as? JsonPrimitive)?.contentOrNull.orEmpty()
fun JsonObject.long(k: String) = (this[k] as? JsonPrimitive)?.longOrNull ?: 0L
fun JsonObject.obj(k: String) = this[k] as? JsonObject ?: JsonObject(emptyMap())
fun scheduleInstant(value: String): Instant? = runCatching { Instant.parse(value) }.getOrNull()
fun taskSpec(title: String, message: String, trigger: JsonObject, target: JsonObject) = buildJsonObject {
    put("title", title); put("message", message); put("trigger", trigger); put("target", target)
}

enum class TaskGroup(val label: UiMessage) { ATTENTION(UiMessages.scheduleAndroidNeedsYourAttention()), ACTIVE(UiMessages.homeLive()), PAUSED(UiMessages.queuePaused()), ENDED(UiMessages.listStateEnded()) }
fun taskGroup(t: ScheduledTask): TaskGroup = when {
    t.status == "cancelled" && t.reason.isNotEmpty() && t.reason != "user_cancelled" -> TaskGroup.ATTENTION
    t.status in setOf("failed", "uncertain") || (t.status == "paused" && t.reason !in setOf("", "user_paused", "completed_while_paused")) -> TaskGroup.ATTENTION
    t.status == "paused" -> TaskGroup.PAUSED
    t.status in setOf("completed", "cancelled") -> TaskGroup.ENDED
    else -> TaskGroup.ACTIVE
}
val SCHEDULE_OFFLINE = (UiMessages.scheduleNotConnectedShowingThePreviousState())
val SCHEDULE_OFFLINE_ROW = (UiMessages.listStateUnlinked())
const val SCHEDULE_MESSAGE_MAX_BYTES = 32 * 1024
fun scheduleMessageValid(message: String) = message.isNotBlank() && message.toByteArray(Charsets.UTF_8).size <= SCHEDULE_MESSAGE_MAX_BYTES
val SCHEDULE_EMPTY = (UiMessages.scheduleTellTheAgentInAConversationCheckForMe())
val SCHEDULE_INTERVAL_HINT = (UiMessages.scheduleTheFirstRunIsAfterAFullIntervalTo())
fun scheduleVisible(t: ScheduledTask) = t.status != "cancelled" || t.reason.isNotEmpty() && t.reason != "user_cancelled"
enum class ScheduleInboxGroup(val label: UiMessage) { ATTENTION(UiMessages.scheduleAndroidNeedsYourAttention()), RECENT(UiMessages.scheduleAndroidRecent()), ENDED(UiMessages.listStateEnded()) }
fun scheduleStopTime(task: ScheduledTask): Instant = task.allRuns.firstOrNull()?.notificationTime
    ?: scheduleInstant(task.json.text("updated_at")) ?: Instant.EPOCH
fun scheduleInboxGroup(task: ScheduledTask, hasNew: Boolean): ScheduleInboxGroup = when {
    taskGroup(task) == TaskGroup.ATTENTION -> ScheduleInboxGroup.ATTENTION
    taskGroup(task) == TaskGroup.ENDED && !hasNew -> ScheduleInboxGroup.ENDED
    else -> ScheduleInboxGroup.RECENT
}
fun scheduleSorted(tasks: List<ScheduledTask>, group: ScheduleInboxGroup, hasNew: (ScheduledTask) -> Boolean = { false }): List<ScheduledTask> =
    tasks.filter { scheduleVisible(it) && scheduleInboxGroup(it, hasNew(it)) == group }.sortedWith(
        (when (group) {
            ScheduleInboxGroup.ATTENTION -> compareByDescending<ScheduledTask> { scheduleStopTime(it) }
            ScheduleInboxGroup.ENDED -> compareByDescending<ScheduledTask> { it.latestNotification?.notificationTime }
                .thenByDescending { scheduleInstant(it.json.text("updated_at")) ?: Instant.EPOCH }
            ScheduleInboxGroup.RECENT -> compareByDescending<ScheduledTask> { it.latestNotification?.notificationTime }
                .thenBy { it.next ?: Instant.MAX }
        })
            .thenBy { it.machine }.thenBy { it.id })
fun scheduleAttentionWord(task: ScheduledTask, copy: MessageResolver) = if (task.status == "failed" &&
    (task.trigger.text("type") == "at" || task.trigger.text("type") == "after_turn" && task.trigger["repeat"]?.jsonPrimitive?.booleanOrNull != true)) copy(UiMessages.scheduleCouldNotRun()) else copy(UiMessages.scheduleStopped())
fun scheduleInProgress(task: ScheduledTask) = task.active != null || task.allRuns.any { it.status in setOf("dispatching", "queued", "running") }
fun schedulePreview(task: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String = when {
    scheduleInProgress(task) -> task.latestNotification?.summary?.resolve(copy) ?: copy(UiMessages.scheduleNoResultsYet())
    taskGroup(task) == TaskGroup.ATTENTION -> "${scheduleAttentionWord(task, copy = copy)} · ${scheduleReason(task.reason, short = true, copy = copy)}"
    else -> task.latestNotification?.summary?.resolve(copy) ?: if (task.allRuns.isEmpty()) copy(UiMessages.scheduleHasNotRunYet()) + task.next?.let { copy(UiMessages.scheduleAndroidNextValue1((scheduleWhen(it, now, zone, copy = copy)).toString())) }.orEmpty()
        else scheduleLastResult(task, now, zone, copy = copy)
}

/** Task rows deliberately use clock/calendar dates, independently of conversation rows. */
fun scheduleRowTime(at: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String {
    val dt = at.atZone(zone); val today = now.atZone(zone).toLocalDate(); val date = dt.toLocalDate()
    return when {
        date == today -> scheduleClock(at, zone)
        date == today.minusDays(1) -> copy(UiMessages.timeYesterday())
        date < today && date >= today.minusDays(6) -> copy(UiMessages.timeWeekday(date.dayOfWeek.name.take(3).lowercase()))
        dt.year != today.year -> copy(UiMessages.timeCalendarDateYear((dt.dayOfMonth).toInt(), (dt.monthValue).toInt(), (dt.year).toInt()))
        else -> copy(UiMessages.timeMonthDay((dt.dayOfMonth).toInt(), (dt.monthValue).toInt()))
    }
}
fun scheduleNotificationTime(task: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String =
    (if (taskGroup(task) == TaskGroup.ATTENTION) scheduleStopTime(task) else task.latestNotification?.notificationTime)
        ?.let { scheduleRowTime(it, now, zone, copy = copy) }.orEmpty()

/** Match the daemon's follow-up boundary; never borrow another scheduled or human turn's reply. */
fun scheduleFinalAnswer(run: ScheduledRun, events: List<Event>): String? {
    if (run.id.isEmpty()) return null
    val ordered = events.sortedBy { it.seq }
    val start = ordered.indexOfFirst { it.hasUserMessage() && it.origin.scheduledRunId == run.id }
    if (start < 0) return null
    var seq = ordered[start].seq
    var answer: String? = null
    for (event in ordered.drop(start + 1)) {
        if (event.seq != seq + 1) return null
        seq = event.seq
        val human = event.origin.kind in setOf(OriginKind.ORIGIN_KIND_CLIENT, OriginKind.ORIGIN_KIND_LOCAL)
        if (human && (event.hasUserMessage() || event.hasSessionState() && event.sessionState.detail == "resumed") ||
            event.hasUserMessage() && event.origin.scheduledRunId.isNotEmpty() && event.origin.scheduledRunId != run.id ||
            event.hasSessionState() && event.sessionState.status == SessionStatus.SESSION_STATUS_EXITED) break
        if (event.hasAgentMessage() && event.agentMessage.phase == "final_answer") answer = event.agentMessage.text.takeIf { it.isNotBlank() }
    }
    return answer
}
fun scheduleBadgeText(count: Int) = if (count > 99) "99+" else count.toString()

data class ScheduleTaskKey(val machine: String, val task: String)
fun scheduleRunAllowed(t: ScheduledTask) = t.active == null && t.json["manual_request"] !is JsonObject && taskGroup(t) !in setOf(TaskGroup.ATTENTION, TaskGroup.ENDED)
fun scheduleDefinitionAllowed(t: ScheduledTask) = t.active == null
fun scheduleSupported(m: MachineView?) = capabilities(m)?.scheduledTasks == true && protocolMismatch(m) == null
fun scheduleWritable(s: RemoteState, machine: String) = s.conn == ConnState.ONLINE && s.machines[machine]?.online == true && scheduleSupported(s.machines[machine])
val stoppedScheduleRuns = setOf("failed", "uncertain", "interrupted", "skipped", "cancelled")
fun scheduleReason(reason: String, short: Boolean = false, copy: MessageResolver): String = when (reason) {
    "relay_session_limit" -> copy(UiMessages.scheduleTheRelaySSessionLimitHasBeenReached())
    "execution_failed" -> if (short) copy(UiMessages.scheduleExecutionFailed()) else copy(UiMessages.scheduleExecutionFailedTheTaskStopped())
    "execution_interrupted" -> if (short) copy(UiMessages.scheduleExecutionInterrupted()) else copy(UiMessages.scheduleExecutionInterruptedTheTaskStopped())
    "execution_session_exited" -> copy(UiMessages.scheduleTheRunningConversationEnded())
    "execution_session_missing" -> copy(UiMessages.scheduleTheRunningConversationCouldNotBeFound())
    "message_delivery_failed" -> copy(UiMessages.scheduleTheMessageWasNotDeliveredToTheCli())
    "could_not_queue_message" -> copy(UiMessages.scheduleTheMessageCouldNotBeQueued())
    "message_cancelled" -> if (short) copy(UiMessages.scheduleTheScheduledMessageWasCanceledBeforeDelivery()) else copy(UiMessages.scheduleTheScheduledMessageWasCanceledBeforeDeliveryTheTask())
    "execution_input_paused" -> copy(UiMessages.scheduleTheScheduledMessageIsPausedResumeOrCancelIt())
    "restart_dispatching" -> copy(UiMessages.scheduleDeliveryBeforeTheComputerRestartedCouldNotBeConfirmed())
    "agent_schedule_permissions_changed" -> if (short) copy(UiMessages.scheduleTaskCreationPermissionsChanged()) else copy(UiMessages.scheduleTaskCreationPermissionsChangedTheTaskStopped())
    "agent_schedule_interval_below_minimum" -> if (short) copy(UiMessages.scheduleTheAgentCreatedTaskIntervalIsTooShort()) else copy(UiMessages.scheduleTheAgentCreatedTaskIntervalIsTooShortThe())
    "catch_up_window_expired" -> copy(UiMessages.scheduleTheCatchUpWindowExpiredThisRunWasSkipped())
    "completed_while_paused" -> copy(UiMessages.schedulePausedTheScheduleExpiredInTheMeantime())
    "user_paused" -> copy(UiMessages.queuePaused())
    "user_cancelled" -> copy(UiMessages.scheduleDeleted())
    "" -> copy(UiMessages.scheduleExecutionOutcomeIsUnconfirmed())
    else -> copy(UiMessages.scheduleReasonReturnedByTheComputer((reason).toString()))
}

/** Unicode characters, rather than UTF-16 code units, match the other clients' 60-character limit. */
fun clipScheduleName(value: String): String = value.codePoints().limit(60).toArray().let { String(it, 0, it.size) }
private val scheduleWhitespace = Regex("[\\s\\p{Z}\\uFEFF]+")
fun scheduleNameInput(value: String): String = clipScheduleName(value.replace(scheduleWhitespace, " ").trimStart())
fun scheduleName(name: String, message: String): String {
    val text = message.trim { it.isWhitespace() || it == '\uFEFF' }
    val line = text.lineSequence().firstOrNull { it.isNotBlank() }.orEmpty()
    val first = line.split(Regex("\\r?\\n|[。！？!?]|\\.(?=\\s|$)"), limit = 2).firstOrNull().orEmpty()
    val explicit = name.replace(scheduleWhitespace, " ").trim()
    return clipScheduleName(explicit.ifEmpty { first.ifEmpty { line }.replace(scheduleWhitespace, " ").trim().trimEnd(',', '，', ':', '：', ';', '；').trimEnd() })
}
fun renamedScheduleSpec(task: ScheduledTask, name: String): JsonObject? {
    val resolved = scheduleName(name, task.message)
    return if (resolved == task.title) null else JsonObject(task.spec + ("title" to JsonPrimitive(resolved)))
}
fun scheduleRenameAllowed(online: Boolean, changing: Boolean = false): Boolean = online && !changing

fun scheduleFrequency(t: JsonObject, localZone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String = when (t.text("type")) {
    "at" -> scheduleInstant(t.text("at"))?.let { copy(UiMessages.scheduleOnceAt((scheduleWhen(it, zone = localZone, copy = copy)).toString())) } ?: copy(UiMessages.scheduleOnceSchedules())
    "calendar" -> {
        val days = (t["weekdays"] as? JsonArray).orEmpty().mapNotNull { it.jsonPrimitive.intOrNull }
        val clock = t.text("time")
        when {
            days.isEmpty() || days.size == 7 -> copy(UiMessages.scheduleDailyAt(clock)).trim()
            days == listOf(1,2,3,4,5) -> copy(UiMessages.scheduleWorkdaysAt(clock)).trim()
            else -> copy(UiMessages.scheduleWeeklyAt(days.joinToString("、") { listOf("", copy(UiMessages.scheduleMon()), copy(UiMessages.scheduleTue()), copy(UiMessages.scheduleWed()), copy(UiMessages.scheduleThu()), copy(UiMessages.scheduleFri()), copy(UiMessages.scheduleSat()), copy(UiMessages.scheduleSun())).getOrElse(it) { "?" } }, clock)).trim()
        }
    }
    "interval" -> t.long("interval_seconds").let { when {
        it == 3600L -> copy(UiMessages.scheduleHourly())
        it % 86400 == 0L -> copy(scheduleQuantity(it / 86400, { UiMessages.scheduleEveryDays(it) }, { count, total -> UiMessages.scheduleAndroidLargeEveryDays(count, total) }))
        it % 3600 == 0L -> copy(scheduleQuantity(it / 3600, { UiMessages.scheduleEveryHr(it) }, { count, total -> UiMessages.scheduleAndroidLargeEveryHours(count, total) }))
        it % 60 == 0L -> copy(scheduleQuantity(it / 60, { UiMessages.scheduleEveryMin(it) }, { count, total -> UiMessages.scheduleAndroidLargeEveryMinutes(count, total) }))
        else -> copy(scheduleQuantity(it, { UiMessages.scheduleEverySec(it) }, { count, total -> UiMessages.scheduleAndroidLargeEverySeconds(count, total) }))
    } }
    "after_turn" -> copy(if ((t["repeat"] as? JsonPrimitive)?.booleanOrNull == true) scheduleQuantity(t.long("delay_seconds"), { UiMessages.scheduleAfterEachTurn(it) }, { count, total -> UiMessages.scheduleAndroidLargeAfterEachTurn(count, total) }) else scheduleQuantity(t.long("delay_seconds"), { UiMessages.scheduleAfterCurrentTurn(it) }, { count, total -> UiMessages.scheduleAndroidLargeAfterCurrentTurn(count, total) }))
    else -> copy(UiMessages.scheduleAndroidUnknownTime())
}
fun scheduleLimits(task: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String = buildList {
    val max = task.trigger.long("max_runs")
    if (max > 0) add(copy(scheduleQuantity(max, { UiMessages.scheduleRunsTotal(it) }, { count, total -> UiMessages.scheduleAndroidLargeRunsTotal(count, total) })))
    scheduleInstant(task.trigger.text("until"))?.let { add(copy(UiMessages.scheduleUntil((scheduleWhen(it, now, zone, copy = copy)).toString()))) }
}.let { limits -> if (limits.size == 2) copy(UiMessages.scheduleAndroidAlternativeLimit(limits[0], limits[1])) else limits.firstOrNull().orEmpty() }

fun scheduleZone(t: JsonObject): ZoneId = if (t.text("type") == "calendar")
    runCatching { ZoneId.of(t.text("timezone")) }.getOrElse { ZoneId.systemDefault() }
else ZoneId.systemDefault()
fun scheduleDay(at: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String {
    val dt = at.atZone(zone); val today = now.atZone(zone).toLocalDate()
    return when (dt.toLocalDate()) { today -> copy(UiMessages.timeToday()); today.plusDays(1) -> copy(UiMessages.timeTomorrow()); today.minusDays(1) -> copy(UiMessages.timeYesterday()); else -> if (dt.year != today.year) copy(UiMessages.timeCalendarDateYear(dt.dayOfMonth, dt.monthValue, dt.year)) else copy(UiMessages.timeMonthDay(dt.dayOfMonth, dt.monthValue)) }
}
fun scheduleWhen(at: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String =
    copy(UiMessages.timeAndroidDayClock(scheduleClock(at, zone), scheduleDay(at, now, zone, copy)))
fun scheduleClock(at: Instant, zone: ZoneId = ZoneId.systemDefault()): String = at.atZone(zone).format(DateTimeFormatter.ofPattern("HH:mm"))
fun scheduleNext(t: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String = when {
    t.active?.status in setOf("dispatching", "queued", "running") -> copy(UiMessages.listStateRunning())
    taskGroup(t) == TaskGroup.ATTENTION -> copy(UiMessages.scheduleStopped())
    taskGroup(t) == TaskGroup.PAUSED -> copy(UiMessages.queuePaused())
    taskGroup(t) == TaskGroup.ENDED -> copy(UiMessages.listStateEnded())
    t.next != null -> copy(UiMessages.scheduleAndroidNextRun(scheduleWhen(t.next!!, now, zone, copy = copy)))
    else -> copy(UiMessages.scheduleNextRunPending())
}
fun runResult(r: ScheduledRun, copy: MessageResolver): String = when (r.status) {
    "failed" -> copy(UiMessages.attachmentFailed())
    "uncertain" -> copy(UiMessages.scheduleUnconfirmed())
    "interrupted" -> copy(UiMessages.sessionInterrupted())
    "skipped" -> copy(UiMessages.scheduleSkipped())
    "cancelled" -> copy(UiMessages.queueCanceled())
    else -> if (r.notified) copy(UiMessages.scheduleNotifiedYou()) else when(r.status) {
        "completed" -> copy(UiMessages.scheduleNoNotification())
        "dispatching" -> copy(UiMessages.scheduleStarting())
        "queued" -> copy(UiMessages.scheduleWaitingForTheCurrentTurnToEnd())
        "running" -> copy(UiMessages.scheduleRunning())
        else -> copy(UiMessages.scheduleWaitingForResults())
    }
}
fun scheduleLastResult(t: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String {
    if (taskGroup(t) == TaskGroup.ATTENTION) return scheduleReason(t.reason, copy = copy)
    val last = t.allRuns.firstOrNull() ?: return copy(UiMessages.scheduleHasNotRunYet())
    val today = t.allRuns.filter { it.time.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate() }
    if (today.size > 1 && today.all { it.quiet }) return copy(UiMessages.scheduleRunsTodayNoneNotifiedYou((today.size).toInt()))
    val detail = if (last.notified || last.status in stoppedScheduleRuns) last.summary.resolve(copy) else ""
    return if (detail.isNotBlank()) copy(UiMessages.scheduleLastResultDetail(detail, runResult(last, copy = copy), scheduleWhen(last.time, now, zone, copy = copy)))
        else copy(UiMessages.scheduleLastResult(runResult(last, copy = copy), scheduleWhen(last.time, now, zone, copy = copy)))
}
data class RunGroup(val key: String, val runs: List<ScheduledRun>, val quiet: Boolean)
fun scheduleRunGroups(runs: List<ScheduledRun>, zone: ZoneId = ZoneId.systemDefault()): List<RunGroup> {
    val days = runs.filter { it.quiet }.sortedByDescending { it.notificationTime }.groupBy { it.notificationTime.atZone(zone).toLocalDate().toString() }
    val emitted = HashSet<String>()
    return runs.sortedByDescending { it.notificationTime }.mapNotNull { r ->
        val day = r.notificationTime.atZone(zone).toLocalDate().toString()
        if (!r.quiet) RunGroup(r.id, listOf(r), false)
        else if (emitted.add(day)) RunGroup("quiet-$day", days.getValue(day), true) else null
    }
}

class ScheduleConflictException : MessageException(UiMessages.errorsTheTaskWasJustChangedUpdatedToTheLatest().messageText())

data class ScheduleSnapshot(val revision: Long = -1, val tasks: List<ScheduledTask> = emptyList(), val loading: Boolean = false, val error: MessageText = "".messageText(), val cached: Boolean = true)

/** Persist only opaque task/run identities, scoped to this relay and client identity. */
class ScheduleSeen(
    private val scope: List<String> = emptyList(),
    private val read: (String) -> String? = { null },
    private val write: (String, String) -> Unit = { _, _ -> },
    private val onRead: (List<ScheduledRun>) -> Unit = {},
) {
    private val seen = HashMap<String, Set<String>>()
    private val loaded = HashMap<String, List<ScheduledTask>>()
    private val verified = HashSet<String>()
    private data class UnreadRun(val machine: String, val task: String, val run: String)
    // Explicit unread marks survive repeated automatic reads in this viewing context.
    private val manuallyUnread = HashSet<UnreadRun>()
    private var unreadReturnSession: String? = null
    private fun clearUnreadGuards() { manuallyUnread.clear(); unreadReturnSession = null }
    private var detail: ScheduleTaskKey? = null
    // The detail page whose visit the dots belong to; it survives the page going to the background.
    private var visit: ScheduleTaskKey? = null
    private var entryUnread: Set<String>? = null
    private var session: String? = null
    val detailUnread: Set<String> @Synchronized get() = entryUnread.orEmpty()
    val capturedDetail: ScheduleTaskKey? @Synchronized get() = visit?.takeIf { entryUnread != null }
    private fun key(machine: String) = "ra-schedule-seen:" + JsonArray((scope + machine).map(::JsonPrimitive)).toString()
    private fun token(task: String, run: String) = JsonArray(listOf(task, run).map(::JsonPrimitive)).toString()
    private fun save(machine: String, ids: Set<String>) {
        seen[machine] = ids
        runCatching { write(key(machine), JsonArray(ids.map(::JsonPrimitive)).toString()) }
    }
    @Synchronized fun observe(machine: String, tasks: List<ScheduledTask>, authoritative: Boolean = true) {
        val ids = tasks.filter(::scheduleVisible).flatMap { t -> t.allRuns.filter { it.notified }.map { token(t.id, it.id) } }.toSet()
        loaded[machine] = tasks
        if (authoritative) verified.add(machine) else verified.remove(machine)
        if (machine !in seen) {
            val stored = runCatching { read(key(machine))?.let { raw -> Json.parseToJsonElement(raw).jsonArray.map { it.jsonPrimitive.also { p -> require(p.isString) }.content }.toSet() } }.getOrNull()
            if (stored != null) seen[machine] = stored else save(machine, ids)
        }
        // Read identities only need to cover the retained run history.
        val retained = seen[machine].orEmpty().intersect(ids)
        if (authoritative && retained != seen[machine]) save(machine, retained)
        markViewed(machine)
    }
    @Synchronized fun unreadIds(task: ScheduledTask): Set<String> = task.allRuns.filter {
        it.notified && seen[task.machine]?.contains(token(task.id, it.id)) == false
    }.map { it.id }.toSet()
    @Synchronized fun openTask(machine: String, task: String) {
        val next = ScheduleTaskKey(machine, task)
        if (detail != next) clearUnreadGuards()
        if (visit != next) { visit = next; entryUnread = null }
        // Back from the background: notifications that arrived meanwhile were not seen, so they get dots too.
        else entryUnread = entryUnread?.let { kept -> kept + loaded[machine]?.firstOrNull { it.id == task }?.let(::unreadIds).orEmpty() }
        detail = next
        markViewed(machine)
    }
    /** The page went to the background: stop reading, but keep this visit's dots. */
    @Synchronized fun hideTask(machine: String, task: String) {
        if (detail == ScheduleTaskKey(machine, task)) {
            detail = null
            if (session == null && unreadReturnSession == null) clearUnreadGuards()
        }
    }
    @Synchronized fun closeTask(machine: String, task: String) {
        val key = ScheduleTaskKey(machine, task)
        if (detail == key) detail = null
        // Keep the explicit unread mark during the immediate return to its source conversation.
        if (visit == key) {
            visit = null; entryUnread = null
            if (session == null && unreadReturnSession == null) clearUnreadGuards()
        }
    }
    @Synchronized fun setVisibleSession(value: String?) {
        val next = value?.takeIf { it.isNotEmpty() }
        if (next != null && next == unreadReturnSession) unreadReturnSession = null
        else if (session != next) clearUnreadGuards()
        session = next
        loaded.keys.forEach(::markViewed)
    }
    @Synchronized fun showList() {
        detail = null; session = null; clearUnreadGuards()
    }
    private fun markViewed(machine: String) {
        // Cached results can render, but a detail visit cannot read them before its full list arrives.
        val detailVerified = machine in verified
        val tasks = loaded[machine].orEmpty().filter(::scheduleVisible)
        if (detailVerified && entryUnread == null) tasks.firstOrNull { detail == ScheduleTaskKey(machine, it.id) }?.let { entryUnread = unreadIds(it) }
        val read = tasks.flatMap { task -> task.allRuns.filter { run -> run.notified &&
            UnreadRun(machine, task.id, run.id) !in manuallyUnread &&
            (detailVerified && detail == ScheduleTaskKey(machine, task.id) || session != null && run.session == session)
        }.map { task.id to it } }
        if (read.isEmpty()) return
        val ids = seen[machine].orEmpty() + read.map { (task, run) -> token(task, run.id) }
        if (ids != seen[machine]) save(machine, ids)
        // Also dismiss later notification events for an already-read, still-visible run.
        runCatching { onRead(read.map { it.second }) }
    }
    @Synchronized fun forget(machine: String) {
        loaded.remove(machine); verified.remove(machine); manuallyUnread.removeAll { it.machine == machine }
    }
    @Synchronized fun markRead(task: ScheduledTask) {
        val runs = task.allRuns.filter { it.notified }
        manuallyUnread.removeAll { it.machine == task.machine && it.task == task.id }
        save(task.machine, seen[task.machine].orEmpty() + runs.map { token(task.id, it.id) })
        runCatching { onRead(runs) }
    }
    @Synchronized fun markUnread(task: ScheduledTask) {
        val run = task.latestNotification ?: return
        manuallyUnread.add(UnreadRun(task.machine, task.id, run.id))
        if (detail == ScheduleTaskKey(task.machine, task.id)) unreadReturnSession = run.session.takeIf { it.isNotEmpty() }
        save(task.machine, seen[task.machine].orEmpty() - token(task.id, run.id))
    }
    @Synchronized fun hasNewResult(task: ScheduledTask) = unreadIds(task).isNotEmpty()
    @Synchronized fun count(tasks: List<ScheduledTask>) = tasks.count { scheduleVisible(it) && taskGroup(it) == TaskGroup.ATTENTION }

}

/** Event-driven discovery. The in-flight request captures the advertised revision, so metadata
 * arriving during a read is fetched afterwards. Failed reads wait for a revision/reconnect/retry. */
class ScheduleRepository(
    private val scope: CoroutineScope,
    private val remoteState: StateFlow<RemoteState>,
    private val send: suspend (String, ScheduleRequest) -> ByteArray,
    private val read: suspend (String) -> ByteArray? = { null },
    private val clear: suspend (String) -> Unit = {},
    private val seen: ScheduleSeen = ScheduleSeen(),
) {
    private val _state = MutableStateFlow<Map<String, ScheduleSnapshot>>(emptyMap())
    val state = _state.asStateFlow()
    private val _badge = MutableStateFlow(0)
    val badge = _badge.asStateFlow()
    private val _unread = MutableStateFlow<Map<ScheduleTaskKey, Boolean>>(emptyMap())
    val unread = _unread.asStateFlow()
    private val _hasNewResults = MutableStateFlow(false)
    val hasNewResults = _hasNewResults.asStateFlow()
    private val _detailUnread = MutableStateFlow<Set<String>>(emptySet())
    val detailUnread = _detailUnread.asStateFlow()
    private val _capturedDetail = MutableStateFlow<ScheduleTaskKey?>(null)
    val capturedDetail = _capturedDetail.asStateFlow()
    fun openTask(machine: String, task: String) { seen.openTask(machine, task); updateBadge() }
    fun hideTask(machine: String, task: String) { seen.hideTask(machine, task); updateBadge() }
    fun closeTask(machine: String, task: String) { seen.closeTask(machine, task); updateBadge() }
    fun setVisibleSession(session: String?) { seen.setVisibleSession(session); updateBadge() }
    fun showList() { seen.showList(); updateBadge() }
    fun markRead(tasks: List<ScheduledTask>) { tasks.forEach(seen::markRead); updateBadge() }
    fun markUnread(task: ScheduledTask) = markUnread(task.machine, task.id)
    fun markUnread(machine: String, id: String) {
        val current = state.value[machine]?.tasks?.firstOrNull { it.id == id && scheduleVisible(it) } ?: return
        seen.markUnread(current); updateBadge()
    }
    private fun updateBadge() {
        val tasks = tasks()
        _unread.value = tasks.associate { ScheduleTaskKey(it.machine, it.id) to seen.hasNewResult(it) }
        _detailUnread.value = seen.detailUnread
        _capturedDetail.value = seen.capturedDetail
        _badge.value = seen.count(tasks)
        _hasNewResults.value = tasks.any(seen::hasNewResult)
    }
    private val worker = scope.coroutineContext.minusKey(Job)
    private val loaded = HashSet<String>()
    private val removed = HashSet<String>()
    fun forget(id: String) { removed.add(id); loaded.remove(id); seen.forget(id); jobs.remove(id)?.cancel(); _state.value = _state.value - id; updateBadge() }
    fun allow(id: String) { removed.remove(id) }
    private val locks = HashMap<String, Mutex>()
    private val jobs = HashMap<String, Job>()
    init { scope.launch {
        remoteState.map { s -> s.machines.mapValues { (_, m) -> Triple(scheduleWritable(s, m.id), m.meta?.scheduleRevision ?: 0L, scheduleSupported(m)) } }.distinctUntilChanged().collect { machines ->
            for (id in _state.value.keys - machines.keys) { forget(id); clear(id) }
            _state.value = _state.value.filterKeys { machines[it]?.third == true }
            updateBadge()
            for ((id, flags) in machines) if (flags.third && id !in removed) {
                if (loaded.add(id)) restore(id)
                if (flags.first && (state.value[id]?.revision != flags.second || state.value[id]?.cached != false)) request(id)
            }
        }
    } }
    fun tasks(s: RemoteState = remoteState.value) = state.value.values.flatMap { it.tasks }.filter { it.machine !in removed && scheduleVisible(it) && scheduleSupported(s.machines[it.machine]) }
    private fun set(id: String, f: (ScheduleSnapshot) -> ScheduleSnapshot) { _state.value = _state.value + (id to f(_state.value[id] ?: ScheduleSnapshot())); updateBadge() }
    private fun request(id: String) {
        if (jobs[id]?.isActive == true) return
        jobs[id] = scope.launch {
            do {
                val revision = remoteState.value.machines[id]?.meta?.scheduleRevision ?: 0L
                refresh(id, revision)
            } while (state.value[id]?.error.isNullOrEmpty() && scheduleWritable(remoteState.value, id) && state.value[id]?.revision != remoteState.value.machines[id]?.meta?.scheduleRevision)
        }
    }
    suspend fun refresh(id: String, revision: Long = remoteState.value.machines[id]?.meta?.scheduleRevision ?: 0L) = withContext(worker) { locks.getOrPut(id) { Mutex() }.withLock {
        if (id in removed || !scheduleWritable(remoteState.value, id)) return@withLock
        set(id) { it.copy(loading = true, error = "".messageText()) }
        try {
            val raw = send(id, ScheduleRequest.newBuilder().setOperation("list").build())
            val obj = Json.parseToJsonElement(raw.decodeToString()).jsonObject
            val tasks = (obj["tasks"] as? JsonArray).orEmpty().map { ScheduledTask(id, it.jsonObject) }
            if (id in removed || !scheduleSupported(remoteState.value.machines[id])) return@withLock
            seen.observe(id, tasks)
            set(id) { ScheduleSnapshot(revision, tasks, cached = false) }
        } catch (e: CancellationException) { throw e
        } catch (e: Exception) { if (id !in removed) set(id) { it.copy(error = errorText(e)) } }
        finally { if (id !in removed) set(id) { it.copy(loading = false) } }
    }
    }
    private suspend fun restore(id: String) {
        try {
            val raw = read(id) ?: return
            val obj = Json.parseToJsonElement(raw.decodeToString()).jsonObject
            val tasks = (obj["tasks"] as? JsonArray).orEmpty().map { ScheduledTask(id, it.jsonObject) }
            if (id !in removed) { seen.observe(id, tasks, authoritative = false); set(id) { ScheduleSnapshot(tasks = tasks, cached = true) } }
        } catch (e: CancellationException) { throw e
        } catch (_: Exception) { if (id !in removed) set(id) { it.copy(error = UiMessages.scheduleAndroidCannotReadThePreviousTaskListConnectToReadIt().messageText()) } }
    }
    suspend fun rename(task: ScheduledTask, name: String): ScheduledTask? {
        val spec = renamedScheduleSpec(task, name) ?: return task
        check(scheduleRenameAllowed(scheduleWritable(remoteState.value, task.machine))) { UiMessages.scheduleAndroidThisTaskCannotBeRenamedSeparately().messageText() }
        return operate(task.machine, "update", task, spec)
    }
    suspend fun operate(machine: String, op: String, task: ScheduledTask? = null, spec: JsonObject? = null, requestId: String = ""): ScheduledTask? = withContext(worker) {
        check(scheduleWritable(remoteState.value, machine)) { UiMessages.scheduleAndroidComputerOfflineTasksCannotBeEdited().messageText() }
        val req = ScheduleRequest.newBuilder().setOperation(op).setTaskId(task?.id.orEmpty()).setExpectedRevision(task?.revision ?: 0).setRequestId(requestId)
        if (spec != null) req.specJson = ByteString.copyFromUtf8(spec.toString())
        try {
            val raw = send(machine, req.build())
            val obj = Json.parseToJsonElement(raw.decodeToString()).jsonObject
            val result = obj.takeIf { it.text("id").isNotEmpty() }?.let { ScheduledTask(machine, it) }
            refresh(machine)
            result
        } catch (e: CommandException) {
            if (e.fromDaemon && e.code == "revision_conflict") {
                refresh(machine)
                throw ScheduleConflictException()
            }
            throw e
        }
    }
}

/** The exact signed response plus its original request binding, never locally re-encrypted JSON. */
internal fun scheduleSnapshot(request: String, envelope: Envelope): ByteArray = buildJsonObject {
    put("request", request); put("envelope", java.util.Base64.getEncoder().encodeToString(envelope.toByteArray()))
}.toString().toByteArray()

/** Preserve the daemon's Long count while selecting an Android integer plural form. */
internal fun scheduleQuantity(value: Long, normal: (Int) -> UiMessage, large: (Int, String) -> UiMessage): UiMessage =
    if (value in 0..Int.MAX_VALUE.toLong()) normal(value.toInt()) else large(2, value.toString())
