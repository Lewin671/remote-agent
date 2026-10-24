package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import kotlinx.serialization.json.*
import java.time.*

fun scheduleTiming(trigger: JsonObject, localZone: ZoneId = ZoneId.systemDefault()): String {
    fun canonical(id: String) = android.icu.util.TimeZone.getCanonicalID(id) ?: id
    return scheduleFrequency(trigger, localZone) + if (trigger.text("type") == "calendar" && trigger.text("timezone").isNotEmpty() && canonical(trigger.text("timezone")) != canonical(localZone.id)) " · ${trigger.text("timezone")}" else ""
}
fun taskLocation(task: ScheduledTask, state: RemoteState): String {
    val target = task.target
    val location = if (target.text("type") == "resume_session") state.sessions[target.text("session")]?.takeIf { it.info != null }?.let { "在「${titleOf(it)}」里" } ?: "在对话里"
        else "${projectName(target.text("cwd"))} · ${if (target.text("agent") == "claude") "Claude Code" else "Codex"}"
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
