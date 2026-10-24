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
    val seq get() = json.long("start_seq").takeIf { it > 0 } ?: (json.long("after_seq") + 1)
    val time get() = scheduleInstant(json.text("started_at")) ?: scheduleInstant(json.text("scheduled_at")) ?: Instant.EPOCH
    val status get() = json.text("status")
    val notified get() = (json["notified"] as? JsonPrimitive)?.booleanOrNull == true
    val summary get() = if (notified) json.text("notification_text") else json.text("final_summary")
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
fun scheduleReason(reason: String): String = when (reason) {
    "relay_session_limit" -> "中继上的会话数已满"
    "agent_schedule_permissions_changed" -> "项目权限改了"
    "agent_schedule_interval_below_minimum" -> "Agent 创建的任务间隔低于允许的下限"
    "agent_schedule_scope_required" -> "需要确认任务权限"
    "restarted", "dispatch_uncertain", "delivery_uncertain", "uncertain" -> "没能确认是否发出"
    "late", "too_late", "missed" -> "超过补跑时间，已跳过"
    "aborted", "interrupted", "execution_interrupted", "source_turn_interrupted" -> "本轮被中断"
    "exited", "execution_session_exited", "source_exited_before_turn_end" -> "会话已结束"
    "execution_failed", "dispatch_failed" -> "运行失败"
    "execution_session_missing", "source_session_missing" -> "找不到原来的会话"
    "daemon_restarted_during_dispatch", "could_not_persist_dispatch_result" -> "电脑重启时没能确认是否发出"
    "message_delivery_failed" -> "消息未能发到对话"
    "execution_input_paused" -> "消息发送已暂停，请先在对话里处理"
    "message_cancelled" -> "消息已撤回"
    "user", "user_paused", "" -> "已暂停"
    else -> reason
}
fun scheduleFrequency(t: JsonObject): String = when (t.text("type")) {
    "at" -> scheduleInstant(t.text("at"))?.let { "一次 · ${scheduleWhen(it, zone = scheduleZone(t))}" } ?: "一次"
    "calendar" -> {
        val days = (t["weekdays"] as? JsonArray).orEmpty().mapNotNull { it.jsonPrimitive.intOrNull }
        val prefix = when { days.isEmpty() -> "每天"; days == listOf(1,2,3,4,5) -> "工作日"; else -> "每周" + days.joinToString("、") { listOf("", "一", "二", "三", "四", "五", "六", "日").getOrElse(it) { "?" } } }
        "$prefix ${t.text("time")}".trim()
    }
    "interval" -> t.long("interval_seconds").let { when {
        it == 3600L -> "每小时"
        it % 86400 == 0L -> "每 ${it / 86400} 天"
        it % 3600 == 0L -> "每 ${it / 3600} 小时"
        it % 60 == 0L -> "每 ${it / 60} 分钟"
        else -> "每 $it 秒"
    } }
    "after_turn" -> "本轮结束 ${t.long("delay_seconds")} 秒后" + if ((t["repeat"] as? JsonPrimitive)?.booleanOrNull == true) "，重复" else ""
    else -> "时间未知"
}
fun scheduleLimits(task: ScheduledTask, now: Instant = Instant.now()): String = buildList {
    val max = task.trigger.long("max_runs")
    if (max > 0) add("还剩 ${(max - task.json.long("dispatches")).coerceAtLeast(0)} 次")
    scheduleInstant(task.trigger.text("until"))?.let { add("到 ${scheduleWhen(it, now)}") }
}.joinToString(" · ")

fun scheduleZone(t: JsonObject): ZoneId = runCatching { ZoneId.of(t.text("timezone")) }.getOrElse {
    runCatching { OffsetDateTime.parse(t.text("at").ifEmpty { t.text("until") }).offset }.getOrElse { ZoneId.systemDefault() }
}
fun scheduleWhen(at: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val dt = at.atZone(zone); val today = now.atZone(zone).toLocalDate()
    val day = when (dt.toLocalDate()) { today -> "今天"; today.plusDays(1) -> "明天"; today.minusDays(1) -> "昨天"; else -> "${if (dt.year != today.year) "${dt.year}年" else ""}${dt.monthValue}月${dt.dayOfMonth}日" }
    return "$day ${dt.format(DateTimeFormatter.ofPattern("H:mm"))}"
}
fun scheduleNext(t: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String = when {
    taskGroup(t) == TaskGroup.ATTENTION -> "已停下"
    t.status == "paused" -> "已暂停"
    t.active != null -> "运行中"
    taskGroup(t) == TaskGroup.ENDED -> "已结束"
    t.next != null -> scheduleWhen(t.next!!, now, zone)
    else -> "等待本轮结束"
}
fun runResult(r: ScheduledRun): String = when {
    r.status == "failed" -> "失败"
    r.notified -> "通知了你"
    r.status == "completed" -> "没有通知"
    r.status == "uncertain" -> "没能确认是否发出"
    r.status == "skipped" -> "跳过"
    r.status in setOf("aborted", "interrupted") -> "已中断"
    else -> "正在运行"
}
fun scheduleLastResult(t: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val last = t.allRuns.firstOrNull() ?: return "尚未运行"
    val today = t.runs.filter { it.time.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate() }
    if (t.trigger.text("type") == "interval" && today.size > 1 && today.all { it.quiet }) return "今天 ${today.size} 次都没有通知"
    return "${scheduleWhen(last.time, now, zone)} ${runResult(last)}" + last.summary.takeIf { it.isNotBlank() }?.let { "：$it" }.orEmpty()
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
class ScheduleRepository(
    private val scope: CoroutineScope,
    private val remoteState: StateFlow<RemoteState>,
    private val send: suspend (String, ScheduleRequest) -> ByteArray,
    private val read: suspend (String) -> ByteArray? = { null },
    private val write: suspend (String, ByteArray) -> Unit = { _, _ -> },
    private val cacheKey: ByteArray,
) {
    private val _state = MutableStateFlow<Map<String, ScheduleSnapshot>>(emptyMap())
    val state = _state.asStateFlow()
    private val worker = scope.coroutineContext.minusKey(Job)
    private val loaded = HashSet<String>()
    private val locks = HashMap<String, Mutex>()
    private val jobs = HashMap<String, Job>()
    init { scope.launch {
        remoteState.map { s -> s.machines.mapValues { (_, m) -> Triple(scheduleWritable(s, m.id), m.meta?.scheduleRevision ?: 0L, scheduleSupported(m)) } }.distinctUntilChanged().collect { machines ->
            _state.value = _state.value.filterKeys { machines[it]?.third == true }
            for ((id, flags) in machines) if (flags.third) {
                if (loaded.add(id)) restore(id)
                if (flags.first && (state.value[id]?.revision != flags.second || state.value[id]?.cached != false)) request(id)
            }
        }
    } }
    fun tasks(s: RemoteState = remoteState.value) = state.value.values.flatMap { it.tasks }.filter { scheduleSupported(s.machines[it.machine]) }
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
        if (!scheduleWritable(remoteState.value, id)) return@withLock
        set(id) { it.copy(loading = true, error = "") }
        try {
            val raw = send(id, ScheduleRequest.newBuilder().setOperation("list").build())
            val obj = Json.parseToJsonElement(raw.decodeToString()).jsonObject
            val tasks = (obj["tasks"] as? JsonArray).orEmpty().map { ScheduledTask(id, it.jsonObject) }
            if (!scheduleSupported(remoteState.value.machines[id])) return@withLock
            set(id) { ScheduleSnapshot(revision, tasks, cached = false) }
            val data = buildJsonObject { put("revision", revision); put("data", obj) }.toString().toByteArray()
            val header = Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT).setMachineId(id).setRequestId("schedule-cache").build()
            write(id, Envelopes.seal(cacheKey, header, data).toByteArray())
        } catch (e: CancellationException) { throw e
        } catch (e: Exception) { set(id) { it.copy(error = errorText(e)) } }
        finally { set(id) { it.copy(loading = false) } }
    }
    }
    private suspend fun restore(id: String) {
        try {
            val bytes = read(id) ?: return
            val (h, raw) = Envelopes.open(cacheKey, Envelope.parseFrom(bytes))
            require(h.machineId == id && h.requestId == "schedule-cache" && h.kind == PayloadKind.PAYLOAD_KIND_COMMAND_RESULT)
            val obj = Json.parseToJsonElement(raw.decodeToString()).jsonObject
            val tasks = (obj.obj("data")["tasks"] as? JsonArray).orEmpty().map { ScheduledTask(id, it.jsonObject) }
            set(id) { ScheduleSnapshot(obj.long("revision"), tasks, cached = true) }
        } catch (e: CancellationException) { throw e
        } catch (_: Exception) { set(id) { it.copy(error = "上次的任务列表无法读取，联网后重新读取") } }
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
