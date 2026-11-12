package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import io.github.lewin671.remoteagent.core.*
import kotlinx.serialization.json.*
import java.time.*

fun scheduleTiming(trigger: JsonObject, localZone: ZoneId = ZoneId.systemDefault(), copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText, locale: String = "zh-CN"): String {
    fun canonical(id: String) = android.icu.util.TimeZone.getCanonicalID(id) ?: id
    val id = trigger.text("timezone")
    val city = if (canonical(id) == "Asia/Shanghai") copy(UiMessages.scheduleAndroidBeijing()) else
        android.icu.text.TimeZoneNames.getInstance(java.util.Locale.forLanguageTag(MessageLocale.appleTags.getValue(MessageLocale.resolve(locale, emptyList()))))
            .getExemplarLocationName(canonical(id))?.takeIf { it.isNotBlank() }
            ?: id.substringAfterLast('/').replace('_', ' ')
    return scheduleFrequency(trigger, localZone, copy = copy) + if (trigger.text("type") == "calendar" && id.isNotEmpty() && canonical(id) != canonical(localZone.id)) copy(UiMessages.scheduleAndroidValue1Time((city).toString())) else ""
}
fun taskLocation(task: ScheduledTask, state: RemoteState, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String {
    val target = task.target
    val location = if (target.text("type") == "resume_session") state.sessions[target.text("session")]?.takeIf { it.info != null }?.let {
        val agent = it.info?.agent?.takeIf { a -> a != io.github.lewin671.remoteagent.proto.Agent.AGENT_UNSPECIFIED }
        copy(UiMessages.scheduleIn((titleOf(it, copy = copy)).toString())) + agent?.let { a -> " · ${agentName(a, copy = copy)}" }.orEmpty()
    } ?: copy(UiMessages.scheduleInConversation())
        else "${projectName(target.text("cwd"), copy = copy)} · ${(target.text("agent").takeIf { it.isNotEmpty() }?.let(::agentById) ?: agentInfo()).let { it.nameMessage?.let(copy) ?: it.name }}"
    return location + if (state.machines.size > 1) " · ${machineLabel(state, task.machine)}" else ""
}
fun taskPermission(task: ScheduledTask, state: RemoteState, copy: MessageResolver = ::sessionText): String = if (task.target.text("type") == "resume_session")
    state.sessions[task.target.text("session")]?.let { modeOf(it)?.text?.let(copy) } ?: copy(UiMessages.scheduleAndroidFollowConversation())
    else when(task.target.text("mode")) { "ask" -> copy(UiMessages.modesAsk()); "auto" -> copy(UiMessages.modesAuto()); "full" -> copy(UiMessages.modesFullAccess()); else -> copy(UiMessages.newSessionFollowCli()) }
fun scheduleCreation(task: ScheduledTask, now: Instant, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String {
    val kind = if (task.target.text("type") == "resume_session") copy(UiMessages.scheduleMessagesGoToTheCurrentConversationAndWaitFor()) else copy(UiMessages.scheduleEachRunOpensANewConversationGroupedUnderThis())
    val owner = if (task.json.text("source") == "agent") copy(UiMessages.scheduleCreatedByTheAgentInAConversation()) else copy(UiMessages.scheduleCreatedByYou())
    val at = scheduleInstant(task.json.text("created_at"))?.let { " · ${scheduleWhen(it, now, copy = copy)}" }.orEmpty()
    return "$kind $owner$at"
}
fun sessionScheduleText(tasks: List<ScheduledTask>, state: RemoteState, now: Instant = Instant.now(), copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String {
    val next = tasks.minByOrNull { it.next ?: Instant.MAX } ?: return ""
    val status = if (!scheduleWritable(state, next.machine)) copy(SCHEDULE_OFFLINE) else scheduleNext(next, now, copy = copy)
    return if (tasks.size > 1) copy(UiMessages.scheduleStripCount((tasks.size).toInt(), (status).toString())) else copy(UiMessages.scheduleStripSingle((scheduleFrequency(next.trigger, copy = copy)).toString(), (status).toString()))
}

fun schedulePlanBlocked(current: Boolean, config: NewConfig?, state: RemoteState): Boolean =
    !current && config != null && (config.mode ?: defaultModeOf(state.machines[config.machineId],config.agent).mode) == Mode.PLAN

fun scheduleEditingState(state: RemoteState, machine: String): RemoteState = state.copy(
    machines = state.machines.filterKeys { it == machine },
    sessions = state.sessions.filterValues { it.machineId == machine },
    prefs = state.prefs?.let { saved -> saved.copy(prefs = saved.prefs.toBuilder().clearSettings().addAllSettings(saved.prefs.settingsList.filter { it.machineId == machine }).build()) },
)

fun resolvedScheduleConfig(cfg: NewConfig, state: RemoteState): NewConfig = cfg.copy(mode = cfg.mode ?: defaultModeOf(state.machines[cfg.machineId], cfg.agent).mode)

fun scheduleDestination(task: ScheduledTask, state: RemoteState, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String =
    state.sessions[task.target.text("session")]?.takeIf { it.info != null }?.let { titleOf(it, copy = copy) } ?: copy(UiMessages.scheduleConversation())

fun scheduleTargetMissing(task: ScheduledTask): Boolean =
    task.target.text("type") == "resume_session" && task.reason == "execution_session_missing"

fun scheduleFailedOnce(task: ScheduledTask): Boolean = task.status == "failed" &&
    (task.trigger.text("type") == "at" || task.trigger.text("type") == "after_turn" && task.trigger["repeat"]?.jsonPrimitive?.booleanOrNull != true)

fun scheduleAttentionText(task: ScheduledTask, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String {
    if (scheduleTargetMissing(task)) {
        val last = task.allRuns.firstOrNull()
        val started = last?.let { scheduleInstant(it.json.text("started_at")) ?: scheduleInstant(it.json.text("scheduled_at")) }
        return if (started == null) copy(UiMessages.scheduleAndroidMissingTarget())
            else copy(UiMessages.scheduleMissingTargetRun(scheduleReason("execution_session_missing", copy = copy), scheduleWhen(started, now, zone, copy = copy)))
    }
    val next = when {
        task.status == "uncertain" -> copy(UiMessages.scheduleAndroidContinuingMaySendThePreviousMessageAgainOpenTheRun())
        scheduleFailedOnce(task) -> copy(UiMessages.scheduleAndroidYouCanRunAnExtraTimeNowOrChangeThe())
        else -> copy(UiMessages.scheduleAndroidResumingOrReschedulingStartsAtTheNextAutomaticTimeAnd())
    }
    val takeover = if (task.reason == "agent_schedule_permissions_changed") copy(UiMessages.scheduleContinuingTakesOverTheTaskAndRunsItWith()) else ""
    return copy(UiMessages.scheduleAndroidAttentionDetails(next, scheduleReason(task.reason, copy = copy), takeover)).trimEnd()
}

/** Optional row metadata follows the variety within the visible schedule scope. */
fun scheduleRowMetadata(task: ScheduledTask, state: RemoteState, tasks: List<ScheduledTask>, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText, locale: String = "zh-CN"): String {
    fun cwd(t: ScheduledTask) = if (t.target.text("type") == "resume_session") state.sessions[t.target.text("session")]?.info?.cwd.orEmpty() else t.target.text("cwd")
    fun agent(t: ScheduledTask) = if (t.target.text("type") == "resume_session") state.sessions[t.target.text("session")]?.info?.agent?.let { agentName(it, copy = copy) }.orEmpty()
        else (t.target.text("agent").takeIf { it.isNotEmpty() }?.let(::agentById) ?: agentInfo()).let { it.nameMessage?.let(copy) ?: it.name }
    return buildList {
        add(scheduleTiming(task.trigger, copy = copy, locale = locale))
        if (tasks.map(::cwd).filter { it.isNotEmpty() }.distinct().size > 1 && cwd(task).isNotEmpty()) add(projectName(cwd(task), copy = copy))
        if (tasks.map(::agent).filter { it.isNotEmpty() }.distinct().size > 1 && agent(task).isNotEmpty()) add(agent(task))
        if (tasks.map { it.machine }.distinct().size > 1) add(machineLabel(state, task.machine))
        if (task.target.text("type") == "resume_session") add(copy(UiMessages.scheduleIn((scheduleDestination(task, state, copy = copy)).toString())))
        if (task.status == "paused") add(copy(UiMessages.queuePaused()))
        if (!scheduleWritable(state, task.machine)) add(copy(SCHEDULE_OFFLINE_ROW))
    }.joinToString(" · ")
}
fun scheduleDetailSubtitle(task: ScheduledTask, state: RemoteState, now: Instant, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText, locale: String = "zh-CN"): String = when {
    taskGroup(task) == TaskGroup.ATTENTION -> copy(UiMessages.scheduleStopped())
    task.target.text("type") == "resume_session" -> copy(UiMessages.scheduleSendTo((scheduleDestination(task, state, copy = copy)).toString(), (scheduleTiming(task.trigger, copy = copy, locale = locale)).toString()))
    else -> scheduleTiming(task.trigger, copy = copy, locale = locale) + task.next?.let { copy(UiMessages.scheduleAndroidNextValue1((scheduleWhen(it, now, copy = copy)).toString())) }.orEmpty()
}
fun scheduleCanMark(task: ScheduledTask, hasNew: Boolean) = task.latestNotification != null && (taskGroup(task) != TaskGroup.ATTENTION || hasNew)

/** New-session results lead with their task; resumed-session results lead with the conversation. */
fun scheduleNotificationTask(session: String, tasks: List<ScheduledTask>, view: SessionView?): ScheduleTaskKey? {
    val task = tasks.firstOrNull { it.target.text("type") == "new_session" && it.allRuns.any { run -> run.session == session } }
    if (task != null) return ScheduleTaskKey(task.machine, task.id)
    return view?.takeIf { it.info?.taskOwned == true && it.info?.scheduledTaskId?.isNotEmpty() == true }
        ?.let { ScheduleTaskKey(it.machineId, it.info!!.scheduledTaskId) }
}

/** The schedules page's sentence (design-system §6 大标题): an orange [count] before [text] when tasks stopped, and the other kind under it. */
data class ScheduleHeadline(val count: Int?, val text: String, val sub: String)

fun scheduleHeadline(stopped: Int, fresh: Int, empty: Boolean, loading: Boolean, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): ScheduleHeadline = when {
    empty -> ScheduleHeadline(null, if (loading) copy(UiMessages.scheduleReadingTasks()) else copy(UiMessages.scheduleNoScheduledTasksYet()), "")
    stopped > 0 -> ScheduleHeadline(stopped, copy(UiMessages.scheduleStoppedCount(stopped, "\uFFFC")), if (fresh > 0) copy(UiMessages.scheduleMoreNewResults((fresh).toInt())) else "")
    fresh > 0 -> ScheduleHeadline(null, copy(UiMessages.scheduleNewResults((fresh).toInt())), "")
    else -> ScheduleHeadline(null, copy(UiMessages.scheduleNoNewResults()), "")
}
