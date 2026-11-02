package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.ScheduleTaskKey
import io.github.lewin671.remoteagent.core.ScheduledTask
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.text
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.withTimeoutOrNull

internal sealed interface ScheduleNotificationRoute {
    data class Task(val key: ScheduleTaskKey) : ScheduleNotificationRoute
    data object Conversation : ScheduleNotificationRoute
    data object Inbox : ScheduleNotificationRoute
}

internal fun cachedScheduleNotificationRoute(session: String, tasks: List<ScheduledTask>, view: SessionView?): ScheduleNotificationRoute? {
    scheduleNotificationTask(session, tasks, view)?.let { return ScheduleNotificationRoute.Task(it) }
    if (view?.info != null || tasks.any { it.target.text("type") == "resume_session" && it.target.text("session") == session })
        return ScheduleNotificationRoute.Conversation
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
