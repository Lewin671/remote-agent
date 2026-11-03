package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import kotlinx.serialization.json.*
import java.time.*

fun scheduleTiming(trigger: JsonObject, localZone: ZoneId = ZoneId.systemDefault()): String {
    fun canonical(id: String) = android.icu.util.TimeZone.getCanonicalID(id) ?: id
    val id = trigger.text("timezone")
    val city = if (canonical(id) == "Asia/Shanghai") "北京" else
        android.icu.text.TimeZoneNames.getInstance(java.util.Locale.SIMPLIFIED_CHINESE)
            .getExemplarLocationName(canonical(id))?.takeIf { it.isNotBlank() }
            ?: id.substringAfterLast('/').replace('_', ' ')
    return scheduleFrequency(trigger, localZone) + if (trigger.text("type") == "calendar" && id.isNotEmpty() && canonical(id) != canonical(localZone.id)) "（${city}时间）" else ""
}
fun taskLocation(task: ScheduledTask, state: RemoteState): String {
    val target = task.target
    val location = if (target.text("type") == "resume_session") state.sessions[target.text("session")]?.takeIf { it.info != null }?.let {
        val agent = it.info?.agent?.takeIf { a -> a != io.github.lewin671.remoteagent.proto.Agent.AGENT_UNSPECIFIED }
        "在「${titleOf(it)}」里" + agent?.let { a -> " · ${agentName(a)}" }.orEmpty()
    } ?: "在对话里"
        else "${projectName(target.text("cwd"))} · ${(target.text("agent").takeIf { it.isNotEmpty() }?.let(::agentById) ?: agentInfo()).name}"
    return location + if (state.machines.size > 1) " · ${machineLabel(state, task.machine)}" else ""
}
fun taskPermission(task: ScheduledTask, state: RemoteState): String = if (task.target.text("type") == "resume_session")
    state.sessions[task.target.text("session")]?.let { modeOf(it)?.text } ?: "跟随对话"
    else when(task.target.text("mode")) { "ask" -> "询问"; "auto" -> "自动"; "full" -> "完全放行"; else -> "跟随 CLI 设置" }
fun scheduleCreation(task: ScheduledTask, now: Instant): String {
    val kind = if (task.target.text("type") == "resume_session") "消息发到当前对话，等待当前轮结束。" else "每次新开一个对话，归在这个任务下面。"
    val owner = if (task.json.text("source") == "agent") "Agent 在对话里创建" else "你创建"
    val at = scheduleInstant(task.json.text("created_at"))?.let { " · ${scheduleWhen(it, now)}" }.orEmpty()
    return "$kind $owner$at"
}
fun sessionScheduleText(tasks: List<ScheduledTask>, state: RemoteState, now: Instant = Instant.now()): String {
    val next = tasks.minByOrNull { it.next ?: Instant.MAX } ?: return ""
    val status = if (!scheduleWritable(state, next.machine)) SCHEDULE_OFFLINE else scheduleNext(next, now)
    return if (tasks.size > 1) "${tasks.size} 个定时 · $status" else "定时 · ${scheduleFrequency(next.trigger)}，$status"
}

fun schedulePlanBlocked(current: Boolean, config: NewConfig?, state: RemoteState): Boolean =
    !current && config != null && (config.mode ?: defaultModeOf(state.machines[config.machineId],config.agent).mode) == Mode.PLAN

fun scheduleEditingState(state: RemoteState, machine: String): RemoteState = state.copy(
    machines = state.machines.filterKeys { it == machine },
    sessions = state.sessions.filterValues { it.machineId == machine },
    prefs = state.prefs?.let { saved -> saved.copy(prefs = saved.prefs.toBuilder().clearSettings().addAllSettings(saved.prefs.settingsList.filter { it.machineId == machine }).build()) },
)

fun resolvedScheduleConfig(cfg: NewConfig, state: RemoteState): NewConfig = cfg.copy(mode = cfg.mode ?: defaultModeOf(state.machines[cfg.machineId], cfg.agent).mode)

fun scheduleDestination(task: ScheduledTask, state: RemoteState): String =
    state.sessions[task.target.text("session")]?.takeIf { it.info != null }?.let(::titleOf) ?: "对话"

fun scheduleTargetMissing(task: ScheduledTask): Boolean =
    task.target.text("type") == "resume_session" && task.reason == "execution_session_missing"

fun scheduleFailedOnce(task: ScheduledTask): Boolean = task.status == "failed" &&
    (task.trigger.text("type") == "at" || task.trigger.text("type") == "after_turn" && task.trigger["repeat"]?.jsonPrimitive?.booleanOrNull != true)

fun scheduleAttentionText(task: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    if (scheduleTargetMissing(task)) {
        val last = task.allRuns.firstOrNull()
        val started = last?.let { scheduleInstant(it.json.text("started_at")) ?: scheduleInstant(it.json.text("scheduled_at")) }
        val prefix = started?.let { "${scheduleWhen(it, now, zone)} 的" }.orEmpty()
        return "${prefix}运行失败，任务已停下：找不到运行的对话。这个任务只发到原来的对话，恢复后还会失败。"
    }
    val next = when {
        task.status == "uncertain" -> "继续可能重复执行上次的消息，请先打开运行记录核实。"
        scheduleFailedOnce(task) -> "可立即额外执行一次，或改时间重排。"
        else -> "恢复或重新安排后从下一次自动时间开始，不重发上次消息。"
    }
    val takeover = if (task.reason == "agent_schedule_permissions_changed") "继续会由你接管任务，按任务中显示的权限运行。" else ""
    return "${scheduleReason(task.reason)}。$next$takeover"
}

/** Optional row metadata follows the variety within the visible schedule scope. */
fun scheduleRowMetadata(task: ScheduledTask, state: RemoteState, tasks: List<ScheduledTask>): String {
    fun cwd(t: ScheduledTask) = if (t.target.text("type") == "resume_session") state.sessions[t.target.text("session")]?.info?.cwd.orEmpty() else t.target.text("cwd")
    fun agent(t: ScheduledTask) = if (t.target.text("type") == "resume_session") state.sessions[t.target.text("session")]?.info?.agent?.let(::agentName).orEmpty()
        else (t.target.text("agent").takeIf { it.isNotEmpty() }?.let(::agentById) ?: agentInfo()).name
    return buildList {
        add(scheduleTiming(task.trigger))
        if (tasks.map(::cwd).filter { it.isNotEmpty() }.distinct().size > 1 && cwd(task).isNotEmpty()) add(projectName(cwd(task)))
        if (tasks.map(::agent).filter { it.isNotEmpty() }.distinct().size > 1 && agent(task).isNotEmpty()) add(agent(task))
        if (tasks.map { it.machine }.distinct().size > 1) add(machineLabel(state, task.machine))
        if (task.target.text("type") == "resume_session") add("在「${scheduleDestination(task, state)}」里")
        if (task.status == "paused") add("已暂停")
        if (!scheduleWritable(state, task.machine)) add(SCHEDULE_OFFLINE_ROW)
    }.joinToString(" · ")
}
fun scheduleDetailSubtitle(task: ScheduledTask, state: RemoteState, now: Instant): String = when {
    taskGroup(task) == TaskGroup.ATTENTION -> "已停下"
    task.target.text("type") == "resume_session" -> "${scheduleTiming(task.trigger)} · 发到「${scheduleDestination(task, state)}」"
    else -> scheduleTiming(task.trigger) + task.next?.let { " · 下次 ${scheduleWhen(it, now)}" }.orEmpty()
}
fun scheduleCanMark(task: ScheduledTask, hasNew: Boolean) = task.latestNotification != null && (taskGroup(task) != TaskGroup.ATTENTION || hasNew)

/** New-session results lead with their task; resumed-session results lead with the conversation. */
fun scheduleNotificationTask(session: String, tasks: List<ScheduledTask>, view: SessionView?): ScheduleTaskKey? {
    val task = tasks.firstOrNull { it.target.text("type") == "new_session" && it.allRuns.any { run -> run.session == session } }
    if (task != null) return ScheduleTaskKey(task.machine, task.id)
    return view?.takeIf { it.info?.taskOwned == true && it.info?.scheduledTaskId?.isNotEmpty() == true }
        ?.let { ScheduleTaskKey(it.machineId, it.info!!.scheduledTaskId) }
}
