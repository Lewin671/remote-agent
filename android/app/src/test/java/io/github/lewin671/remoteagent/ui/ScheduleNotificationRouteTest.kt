package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.Session
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class ScheduleNotificationRouteTest {
    private val task = ScheduledTask("computer", Json.parseToJsonElement("""{"id":"t","spec":{"target":{"type":"new_session"}},"runs":[{"id":"r","session_id":"s"}]}""").jsonObject)
    @Test fun cachedTaskDoesNotWaitForLoadingOrConnectivity() = runTest {
        var loaded = false
        val route = resolveScheduleNotificationRoute(
            cached = { cachedScheduleNotificationRoute("s", listOf(task), null) },
            load = { loaded = true; awaitCancellation() },
        )
        assertEquals(ScheduleNotificationRoute.Task(ScheduleTaskKey("computer", "t")), route)
        assertFalse(loaded); assertEquals(0, currentTime)
    }
    @Test fun resumedConversationCanBeResolvedFromTheCachedTaskWithoutSessionInfo() = runTest {
        val resumed = task.copy(json = JsonObject(task.json + ("spec" to buildJsonObject { put("target", buildJsonObject { put("type", "resume_session"); put("session", "s") }) })))
        assertEquals(ScheduleNotificationRoute.Conversation, cachedScheduleNotificationRoute("s", listOf(resumed), null))
    }
    @Test fun coldLoadCanResolveAnOwnedSessionBeforeTheTaskListArrives() = runTest {
        var view: SessionView? = null
        val route = resolveScheduleNotificationRoute(
            cached = { cachedScheduleNotificationRoute("s", emptyList(), view) },
            load = { delay(100); view = SessionView("s", "computer", Session.newBuilder().setTaskOwned(true).setScheduledTaskId("t").build()) },
        )
        assertEquals(ScheduleNotificationRoute.Task(ScheduleTaskKey("computer", "t")), route)
        assertEquals(100, currentTime)
    }
    @Test fun offlineWithoutCacheFallsBackToInboxAfterThreeSeconds() = runTest {
        val route = resolveScheduleNotificationRoute(cached = { null }, load = { awaitCancellation() })
        assertEquals(ScheduleNotificationRoute.Inbox, route)
        assertEquals(3_000, currentTime)
    }
    @Test fun failedOrEmptyLoadsFallBackToInboxAndParentCancellationStillWorks() = runTest {
        assertEquals(ScheduleNotificationRoute.Inbox, resolveScheduleNotificationRoute(cached = { null }, load = { error("unavailable") }))
        assertEquals(ScheduleNotificationRoute.Inbox, resolveScheduleNotificationRoute(cached = { null }, load = {}))
        val job = async { resolveScheduleNotificationRoute(cached = { null }, load = { awaitCancellation() }) }
        runCurrent(); job.cancelAndJoin(); assertTrue(job.isCancelled)
    }
}
