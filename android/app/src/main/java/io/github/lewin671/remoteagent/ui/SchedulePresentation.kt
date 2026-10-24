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
    val location = if (target.text("type") == "resume_session") "在「${state.sessions[target.text("session")]?.let(::titleOf) ?: "当前对话"}」里"
        else "${projectName(target.text("cwd"))} · ${if (target.text("agent") == "claude") "Claude Code" else "Codex"}"
    return location + if (state.machines.size > 1) " · ${machineLabel(state, task.machine)}" else ""
}
fun taskPermission(task: ScheduledTask, state: RemoteState): String = if (task.target.text("type") == "resume_session")
    state.sessions[task.target.text("session")]?.let { modeOf(it)?.text } ?: "跟随对话"
    else when(task.target.text("mode")) { "ask" -> "询问"; "auto" -> "自动"; "full" -> "完全放行"; else -> "跟随 CLI 设置" }
fun scheduleCreation(task: ScheduledTask, now: Instant): String {
    val kind = if (task.target.text("type") == "resume_session") "消息等当前这一轮结束后再发，不会打断正在做的事。" else "每次新开一个对话，归在这个任务下面。"
    val owner = if (task.json.text("source") == "agent") "Agent 创建" else "你创建或接管"
    val at = scheduleInstant(task.json.text("created_at"))?.let { " · ${scheduleWhen(it, now)}" }.orEmpty()
    return "$kind $owner$at"
}
fun sessionScheduleText(tasks: List<ScheduledTask>, state: RemoteState, now: Instant = Instant.now()): String {
    val next = tasks.minByOrNull { it.next ?: Instant.MAX } ?: return ""
    val status = if (!scheduleWritable(state, next.machine)) "未连接" else scheduleNext(next, now)
    return if (tasks.size > 1) "${tasks.size} 个定时 · $status" else "定时 · ${scheduleFrequency(next.trigger)}，$status"
}
