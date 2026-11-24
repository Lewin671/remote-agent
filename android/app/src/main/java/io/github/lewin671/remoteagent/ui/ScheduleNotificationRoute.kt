package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.proto.ScheduleSessionStripViewModel
import io.github.lewin671.remoteagent.core.proto.SchedulesViewModel
import io.github.lewin671.remoteagent.core.proto.SyncSessionView
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.withTimeoutOrNull

internal sealed interface ScheduleNotificationRoute {
    data class Task(val machineId: String, val taskId: String) : ScheduleNotificationRoute
    data object Conversation : ScheduleNotificationRoute
    data object Inbox : ScheduleNotificationRoute
}

internal fun cachedScheduleNotificationRoute(
    session: String,
    view: SyncSessionView?,
    schedules: SchedulesViewModel? = null,
    strip: ScheduleSessionStripViewModel? = null,
): ScheduleNotificationRoute? {
    val info = view?.takeIf { it.hasInfo() }?.info
    if (info != null) {
        if (info.taskOwned && info.scheduledTaskId.isNotEmpty() && view.machineId.isNotEmpty()) {
            return ScheduleNotificationRoute.Task(view.machineId, info.scheduledTaskId)
        }
        return ScheduleNotificationRoute.Conversation
    }
    if (strip != null && strip.visible && strip.sessionId == session) {
        return ScheduleNotificationRoute.Conversation
    }
    if (schedules != null) {
        val activeTakeover = (schedules.attention14List + schedules.freshList + schedules.othersList + schedules.endedList)
            .firstOrNull { it.takeover.hasTakeover && it.takeover.sessionId == session }
        if (activeTakeover != null) {
            return ScheduleNotificationRoute.Task(activeTakeover.machineId, activeTakeover.taskId)
        }
    }
    return null
}

/** Cache hits navigate without any network wait. A cold/offline miss has a bounded fallback. */
internal suspend fun resolveScheduleNotificationRoute(
    cached: () -> ScheduleNotificationRoute?,
    load: suspend () -> Unit,
    timeoutMillis: Long = 3_000,
): ScheduleNotificationRoute {
    cached()?.let { return it }
    return try {
        withTimeoutOrNull(timeoutMillis) { load(); cached() } ?: ScheduleNotificationRoute.Inbox
    } catch (e: CancellationException) {
        throw e
    } catch (_: Exception) {
        ScheduleNotificationRoute.Inbox
    }
}
