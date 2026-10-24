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
    val status get() = json.text("status")
    val reason get() = json.text("reason")
    val next get() = scheduleInstant(json.text("next_run_at"))
    val runs get() = (json["runs"] as? JsonArray).orEmpty().map { ScheduledRun(it.jsonObject) }
    val active get() = (json["active_run"] as? JsonObject)?.let(::ScheduledRun)
    val allRuns get() = (runs + listOfNotNull(active)).distinctBy { it.id }.sortedByDescending { it.time }
}
data class ScheduledRun(val json: JsonObject) {
    val id get() = json.text("id")
    val session get() = json.text("session_id")
    val seq get() = (json.long("start_seq").takeIf { it > 0 } ?: json.long("after_seq")) + 1
    val time get() = scheduleInstant(json.text("started_at")) ?: scheduleInstant(json.text("scheduled_at")) ?: Instant.EPOCH
    val status get() = json.text("status")
    val notified get() = (json["notified"] as? JsonPrimitive)?.booleanOrNull == true
    val summary get() = if (status in stoppedScheduleRuns) scheduleReason(json.text("reason")) else json.text("notification_text").ifEmpty { json.text("final_summary") }.ifEmpty { json.text("reason").takeIf { it.isNotEmpty() }?.let(::scheduleReason).orEmpty() }
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
    t.status in setOf("failed", "uncertain") || (t.status == "paused" && t.reason !in setOf("", "user_paused", "completed_while_paused")) -> TaskGroup.ATTENTION
    t.status == "paused" -> TaskGroup.PAUSED
    t.status in setOf("completed", "cancelled") -> TaskGroup.ENDED
    else -> TaskGroup.ACTIVE
}
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
    "agent_schedule_permissions_changed" -> "创建任务时的权限已改变，任务已暂停"
    "agent_schedule_interval_below_minimum" -> "Agent 创建的任务间隔过短，任务已暂停"
    "catch_up_window_expired" -> "已超过补跑时间，本次跳过"
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
    val first = text.split(Regex("\\r?\\n|[。！？!?]|\\.(?=\\s|$)"), limit = 2).firstOrNull().orEmpty()
    val explicit = name.replace(scheduleWhitespace, " ").trim()
    return clipScheduleName(explicit.ifEmpty { first.ifEmpty { text } }.replace(scheduleWhitespace, " ").trim())
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
fun scheduleLimits(task: ScheduledTask, now: Instant = Instant.now()): String = buildList {
    val max = task.trigger.long("max_runs")
    if (max > 0) add("自动运行 $max 次后")
    scheduleInstant(task.trigger.text("until"))?.let { add(scheduleWhen(it, now)) }
}.joinToString(" · ")

fun scheduleZone(t: JsonObject): ZoneId = runCatching { ZoneId.of(t.text("timezone")) }.getOrElse {
    runCatching { OffsetDateTime.parse(t.text("at").ifEmpty { t.text("until") }).offset }.getOrElse { ZoneId.systemDefault() }
}
fun scheduleWhen(at: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val dt = at.atZone(zone); val today = now.atZone(zone).toLocalDate()
    val day = when (dt.toLocalDate()) { today -> "今天"; today.minusDays(1) -> "昨天"; else -> "${if (dt.year != today.year) "${dt.year}年" else ""}${dt.monthValue}月${dt.dayOfMonth}日" }
    return "$day ${dt.format(DateTimeFormatter.ofPattern("HH:mm"))}"
}
fun scheduleClock(at: Instant, zone: ZoneId = ZoneId.systemDefault()): String = at.atZone(zone).format(DateTimeFormatter.ofPattern("HH:mm"))
fun scheduleNext(t: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String = when {
    taskGroup(t) == TaskGroup.ATTENTION -> "已停下"
    t.status == "paused" -> "已暂停"
    taskGroup(t) == TaskGroup.ENDED -> "已结束"
    t.active != null -> when(t.active!!.status) { "dispatching" -> "正在启动"; "queued" -> "等待当前轮结束"; else -> "正在运行" }
    t.next != null -> "下次 " + if (t.next!!.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate()) scheduleClock(t.next!!, zone) else scheduleWhen(t.next!!, now, zone)
    else -> "等待本轮结束"
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
    val last = t.allRuns.firstOrNull() ?: return "还没有运行"
    val today = t.allRuns.filter { it.time.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate() }
    if (today.size > 1 && today.all { it.quiet }) return "今天 ${today.size} 次都没有通知"
    val detail = if (last.notified || last.status in stoppedScheduleRuns) last.summary else ""
    return "${scheduleWhen(last.time, now, zone)} ${runResult(last)}" + detail.takeIf { it.isNotBlank() }?.let { "：$it" }.orEmpty()
}
data class RunGroup(val key: String, val runs: List<ScheduledRun>, val quiet: Boolean)
fun scheduleRunGroups(runs: List<ScheduledRun>, zone: ZoneId = ZoneId.systemDefault()): List<RunGroup> {
    val days = runs.filter { it.quiet }.groupBy { it.time.atZone(zone).toLocalDate().toString() }
    val emitted = HashSet<String>()
    return runs.sortedByDescending { it.time }.mapNotNull { r ->
        val day = r.time.atZone(zone).toLocalDate().toString()
        if (!r.quiet) RunGroup(r.id, listOf(r), false)
        else if (emitted.add(day)) RunGroup("quiet-$day", days.getValue(day), true) else null
    }
}

class ScheduleConflictException : IllegalStateException("任务已在另一端修改，已重新读取，请核对后再试")

data class ScheduleSnapshot(val revision: Long = -1, val tasks: List<ScheduledTask> = emptyList(), val loading: Boolean = false, val error: String = "", val cached: Boolean = true)

/** Event-driven discovery. The in-flight request captures the advertised revision, so metadata
 * arriving during a read is fetched afterwards. Failed reads wait for a revision/reconnect/retry. */
fun scheduleVisible(t: ScheduledTask) = t.status != "cancelled" || t.reason.isNotEmpty() && t.reason != "user_cancelled"

class ScheduleRepository(
    private val scope: CoroutineScope,
    private val remoteState: StateFlow<RemoteState>,
    private val send: suspend (String, ScheduleRequest) -> ByteArray,
    private val read: suspend (String) -> ByteArray? = { null },
    private val clear: suspend (String) -> Unit = {},
) {
    private val _state = MutableStateFlow<Map<String, ScheduleSnapshot>>(emptyMap())
    val state = _state.asStateFlow()
    private val worker = scope.coroutineContext.minusKey(Job)
    private val loaded = HashSet<String>()
    private val removed = HashSet<String>()
    fun forget(id: String) { removed.add(id); loaded.remove(id); jobs.remove(id)?.cancel(); _state.value = _state.value - id }
    fun allow(id: String) { removed.remove(id) }
    private val locks = HashMap<String, Mutex>()
    private val jobs = HashMap<String, Job>()
    init { scope.launch {
        remoteState.map { s -> s.machines.mapValues { (_, m) -> Triple(scheduleWritable(s, m.id), m.meta?.scheduleRevision ?: 0L, scheduleSupported(m)) } }.distinctUntilChanged().collect { machines ->
            for (id in _state.value.keys - machines.keys) { forget(id); clear(id) }
            _state.value = _state.value.filterKeys { machines[it]?.third == true }
            for ((id, flags) in machines) if (flags.third && id !in removed) {
                if (loaded.add(id)) restore(id)
                if (flags.first && (state.value[id]?.revision != flags.second || state.value[id]?.cached != false)) request(id)
            }
        }
    } }
    fun tasks(s: RemoteState = remoteState.value) = state.value.values.flatMap { it.tasks }.filter { it.machine !in removed && scheduleVisible(it) && scheduleSupported(s.machines[it.machine]) }
    private fun set(id: String, f: (ScheduleSnapshot) -> ScheduleSnapshot) { _state.value = _state.value + (id to f(_state.value[id] ?: ScheduleSnapshot())) }
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
            set(id) { ScheduleSnapshot(revision, tasks, cached = false) }
        } catch (e: CancellationException) { throw e
        } catch (e: Exception) { set(id) { it.copy(error = errorText(e)) } }
        finally { if (id !in removed) set(id) { it.copy(loading = false) } }
    }
    }
    private suspend fun restore(id: String) {
        try {
            val raw = read(id) ?: return
            val obj = Json.parseToJsonElement(raw.decodeToString()).jsonObject
            val tasks = (obj["tasks"] as? JsonArray).orEmpty().map { ScheduledTask(id, it.jsonObject) }
            if (id !in removed) set(id) { ScheduleSnapshot(tasks = tasks, cached = true) }
        } catch (e: CancellationException) { throw e
        } catch (_: Exception) { set(id) { it.copy(error = "上次的任务列表无法读取，联网后重新读取") } }
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
