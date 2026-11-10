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
    val notificationSource get() = when (json.text("notification_source")) { "agent" -> "agent 通知"; "daemon" -> "完成后通知"; "agent_and_daemon" -> "agent 通知、完成后通知"; "undeliverable" -> "无法请求通知"; else -> "" }
    val notified get() = (json["notified"] as? JsonPrimitive)?.booleanOrNull == true
    val summary get() = if (status in stoppedScheduleRuns) scheduleReason(json.text("reason")) else json.text("notification_text").ifEmpty { json.text("final_summary") }.ifEmpty { json.text("reason").takeIf { it.isNotEmpty() }?.let(::scheduleReason).orEmpty() }
    val resultTitle get() = json.text("notification_text").trim().takeIf {
        json.text("notification_source") in setOf("agent", "agent_and_daemon") && it.isNotEmpty() && it != json.text("final_summary").trim()
    }.orEmpty()
    val foldedSummary get() = json.text("final_summary").ifBlank { "没有通知" }
    val quiet get() = status == "completed" && !notified
}
fun JsonObject.text(k: String) = (this[k] as? JsonPrimitive)?.contentOrNull.orEmpty()
fun JsonObject.long(k: String) = (this[k] as? JsonPrimitive)?.longOrNull ?: 0L
fun JsonObject.obj(k: String) = this[k] as? JsonObject ?: JsonObject(emptyMap())
fun scheduleInstant(value: String): Instant? = runCatching { Instant.parse(value) }.getOrNull()
fun taskSpec(title: String, message: String, trigger: JsonObject, target: JsonObject) = buildJsonObject {
    put("title", title); put("message", message); put("trigger", trigger); put("target", target)
}

enum class TaskGroup(val label: String) { ATTENTION("需要你看一下"), ACTIVE("进行中"), PAUSED("已暂停"), ENDED("已结束") }
fun taskGroup(t: ScheduledTask): TaskGroup = when {
    t.status == "cancelled" && t.reason.isNotEmpty() && t.reason != "user_cancelled" -> TaskGroup.ATTENTION
    t.status in setOf("failed", "uncertain") || (t.status == "paused" && t.reason !in setOf("", "user_paused", "completed_while_paused")) -> TaskGroup.ATTENTION
    t.status == "paused" -> TaskGroup.PAUSED
    t.status in setOf("completed", "cancelled") -> TaskGroup.ENDED
    else -> TaskGroup.ACTIVE
}
const val SCHEDULE_OFFLINE = "未连接，显示的是上次的状态"
const val SCHEDULE_OFFLINE_ROW = "未连接"
const val SCHEDULE_MESSAGE_MAX_BYTES = 32 * 1024
fun scheduleMessageValid(message: String) = message.isNotBlank() && message.toByteArray(Charsets.UTF_8).size <= SCHEDULE_MESSAGE_MAX_BYTES
const val SCHEDULE_EMPTY = "在对话里跟 agent 说「每天 8 点帮我看看…」，它会替你建好；也可以自己新建一个。"
const val SCHEDULE_INTERVAL_HINT = "第一次会在一个完整间隔后运行，想现在执行可以创建后点“立即运行一次”。"
fun scheduleVisible(t: ScheduledTask) = t.status != "cancelled" || t.reason.isNotEmpty() && t.reason != "user_cancelled"
enum class ScheduleInboxGroup(val label: String) { ATTENTION("需要你看一下"), RECENT("最近"), ENDED("已结束") }
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
fun scheduleAttentionWord(task: ScheduledTask) = if (task.status == "failed" &&
    (task.trigger.text("type") == "at" || task.trigger.text("type") == "after_turn" && task.trigger["repeat"]?.jsonPrimitive?.booleanOrNull != true)) "没能运行" else "已停下"
fun scheduleInProgress(task: ScheduledTask) = task.active != null || task.allRuns.any { it.status in setOf("dispatching", "queued", "running") }
fun schedulePreview(task: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String = when {
    scheduleInProgress(task) -> task.latestNotification?.summary ?: "还没有结果"
    taskGroup(task) == TaskGroup.ATTENTION -> "${scheduleAttentionWord(task)} · ${scheduleReason(task.reason).removeSuffix("，任务已停下")}"
    else -> task.latestNotification?.summary ?: if (task.allRuns.isEmpty()) "还没有运行" + task.next?.let { " · 下次 ${scheduleWhen(it, now, zone)}" }.orEmpty()
        else scheduleLastResult(task, now, zone)
}

/** Task rows deliberately use clock/calendar dates, independently of conversation rows. */
fun scheduleRowTime(at: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val dt = at.atZone(zone); val today = now.atZone(zone).toLocalDate(); val date = dt.toLocalDate()
    return when {
        date == today -> scheduleClock(at, zone)
        date == today.minusDays(1) -> "昨天"
        date < today && date >= today.minusDays(6) -> "周" + listOf("一", "二", "三", "四", "五", "六", "日")[date.dayOfWeek.value - 1]
        dt.year != today.year -> "${dt.year}年${dt.monthValue}月${dt.dayOfMonth}日"
        else -> "${dt.monthValue}月${dt.dayOfMonth}日"
    }
}
fun scheduleNotificationTime(task: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String =
    (if (taskGroup(task) == TaskGroup.ATTENTION) scheduleStopTime(task) else task.latestNotification?.notificationTime)
        ?.let { scheduleRowTime(it, now, zone) }.orEmpty()

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
fun scheduleReason(reason: String): String = when (reason) {
    "relay_session_limit" -> "中继上的会话数已满"
    "execution_failed" -> "运行失败，任务已停下"
    "execution_interrupted" -> "运行被中断，任务已停下"
    "execution_session_exited" -> "运行中的对话已结束"
    "execution_session_missing" -> "找不到运行的对话"
    "message_delivery_failed" -> "消息没有送达 CLI"
    "could_not_queue_message" -> "消息没能加入待发队列"
    "message_cancelled" -> "定时消息在送达前被取消，任务已停下"
    "execution_input_paused" -> "定时消息已暂停，请先在对话里恢复或取消这条消息"
    "restart_dispatching" -> "电脑重启前的发送结果无法确认"
    "agent_schedule_permissions_changed" -> "创建任务时的权限已改变，任务已停下"
    "agent_schedule_interval_below_minimum" -> "Agent 创建的任务间隔过短，任务已停下"
    "catch_up_window_expired" -> "已超过补跑时间，本次跳过"
    "completed_while_paused" -> "已暂停：期间计划已到期"
    "user_paused" -> "已暂停"
    "user_cancelled" -> "已删除"
    "" -> "运行结果无法确认"
    else -> "电脑返回的原因：$reason"
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

fun scheduleFrequency(t: JsonObject, localZone: ZoneId = ZoneId.systemDefault()): String = when (t.text("type")) {
    "at" -> scheduleInstant(t.text("at"))?.let { "${scheduleWhen(it, zone = localZone)}，一次" } ?: "一次"
    "calendar" -> {
        val days = (t["weekdays"] as? JsonArray).orEmpty().mapNotNull { it.jsonPrimitive.intOrNull }
        val prefix = when { days.isEmpty() || days.size == 7 -> "每天"; days == listOf(1,2,3,4,5) -> "工作日"; else -> "每周" + days.joinToString("、") { listOf("", "一", "二", "三", "四", "五", "六", "日").getOrElse(it) { "?" } } }
        "$prefix ${t.text("time")}".trim()
    }
    "interval" -> t.long("interval_seconds").let { when {
        it == 3600L -> "每小时"
        it % 86400 == 0L -> "每 ${it / 86400} 天"
        it % 3600 == 0L -> "每 ${it / 3600} 小时"
        it % 60 == 0L -> "每 ${it / 60} 分钟"
        else -> "每 $it 秒"
    } }
    "after_turn" -> "${if ((t["repeat"] as? JsonPrimitive)?.booleanOrNull == true) "每轮" else "当前轮"}结束 ${t.long("delay_seconds")} 秒后"
    else -> "时间未知"
}
fun scheduleLimits(task: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String = buildList {
    val max = task.trigger.long("max_runs")
    if (max > 0) add("共 $max 次")
    scheduleInstant(task.trigger.text("until"))?.let { add("到 ${scheduleWhen(it, now, zone)}") }
}.joinToString("，或")

fun scheduleZone(t: JsonObject): ZoneId = if (t.text("type") == "calendar")
    runCatching { ZoneId.of(t.text("timezone")) }.getOrElse { ZoneId.systemDefault() }
else ZoneId.systemDefault()
fun scheduleWhen(at: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val dt = at.atZone(zone); val today = now.atZone(zone).toLocalDate()
    val day = when (dt.toLocalDate()) { today -> "今天"; today.plusDays(1) -> "明天"; today.minusDays(1) -> "昨天"; else -> "${if (dt.year != today.year) "${dt.year}年" else ""}${dt.monthValue}月${dt.dayOfMonth}日" }
    return "$day ${dt.format(DateTimeFormatter.ofPattern("HH:mm"))}"
}
fun scheduleClock(at: Instant, zone: ZoneId = ZoneId.systemDefault()): String = at.atZone(zone).format(DateTimeFormatter.ofPattern("HH:mm"))
fun scheduleNext(t: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String = when {
    t.active?.status in setOf("dispatching", "queued", "running") -> "运行中"
    taskGroup(t) == TaskGroup.ATTENTION -> "已停下"
    taskGroup(t) == TaskGroup.PAUSED -> "已暂停"
    taskGroup(t) == TaskGroup.ENDED -> "已结束"
    t.next != null -> "下次 " + scheduleWhen(t.next!!, now, zone)
    else -> "下次待定"
}
fun runResult(r: ScheduledRun): String = when (r.status) {
    "failed" -> "失败"
    "uncertain" -> "无法确认"
    "interrupted" -> "已中断"
    "skipped" -> "跳过"
    "cancelled" -> "已取消"
    else -> if (r.notified) "通知了你" else when(r.status) {
        "completed" -> "没有通知"
        "dispatching" -> "正在启动"
        "queued" -> "等待当前轮结束"
        "running" -> "正在运行"
        else -> "等待结果"
    }
}
fun scheduleLastResult(t: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    if (taskGroup(t) == TaskGroup.ATTENTION) return scheduleReason(t.reason)
    val last = t.allRuns.firstOrNull() ?: return "还没有运行"
    val today = t.allRuns.filter { it.time.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate() }
    if (today.size > 1 && today.all { it.quiet }) return "今天 ${today.size} 次，都没有通知"
    val detail = if (last.notified || last.status in stoppedScheduleRuns) last.summary else ""
    return "${scheduleWhen(last.time, now, zone)} ${runResult(last)}" + detail.takeIf { it.isNotBlank() }?.let { "：$it" }.orEmpty()
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

class ScheduleConflictException : IllegalStateException("任务刚被修改，已更新为最新内容")

data class ScheduleSnapshot(val revision: Long = -1, val tasks: List<ScheduledTask> = emptyList(), val loading: Boolean = false, val error: String = "", val cached: Boolean = true)

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
        set(id) { it.copy(loading = true, error = "") }
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
        } catch (_: Exception) { if (id !in removed) set(id) { it.copy(error = "上次的任务列表无法读取，联网后重新读取") } }
    }
    suspend fun rename(task: ScheduledTask, name: String): ScheduledTask? {
        val spec = renamedScheduleSpec(task, name) ?: return task
        check(scheduleRenameAllowed(scheduleWritable(remoteState.value, task.machine))) { "当前任务不能单独改名" }
        return operate(task.machine, "update", task, spec)
    }
    suspend fun operate(machine: String, op: String, task: ScheduledTask? = null, spec: JsonObject? = null, requestId: String = ""): ScheduledTask? = withContext(worker) {
        check(scheduleWritable(remoteState.value, machine)) { "电脑不在线，不能修改任务" }
        val req = ScheduleRequest.newBuilder().setOperation(op).setTaskId(task?.id.orEmpty()).setExpectedRevision(task?.revision ?: 0).setRequestId(requestId)
        if (spec != null) req.specJson = ByteString.copyFromUtf8(spec.toString())
        try {
            val raw = send(machine, req.build())
            val obj = Json.parseToJsonElement(raw.decodeToString()).jsonObject
            val result = obj.takeIf { it.text("id").isNotEmpty() }?.let { ScheduledTask(machine, it) }
            refresh(machine)
            result
        } catch (e: CommandException) {
            if (e.code == "aborted" || e.code == "failed_precondition" && e.message.orEmpty().contains("revision", true)) {
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
