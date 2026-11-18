package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.proto.Session
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class ScheduleNotificationRouteTest {
    private fun schedulesWithTakeover(sessionId: String): SchedulesViewModel =
        SchedulesViewModel.newBuilder()
            .addFresh(
                ScheduleTaskItemView.newBuilder()
                    .setMachineId("computer")
                    .setTaskId("t")
                    .setTakeover(ScheduleTakeoverView.newBuilder().setHasTakeover(true).setSessionId(sessionId)),
            )
            .build()

    @Test fun cachedTakeoverDoesNotWaitForLoadingOrConnectivity() = runTest {
        var loaded = false
        val route = resolveScheduleNotificationRoute(
            cached = { cachedScheduleNotificationRoute("s", null, schedules = schedulesWithTakeover("s")) },
            load = { loaded = true; awaitCancellation() },
        )
        assertEquals(ScheduleNotificationRoute.Task("computer", "t"), route)
        assertFalse(loaded)
        assertEquals(0, currentTime)
    }

    @Test fun visibleSessionStripRoutesToConversationWithoutSessionInfo() = runTest {
        val strip = ScheduleSessionStripViewModel.newBuilder().setSessionId("s").setVisible(true).build()
        assertEquals(
            ScheduleNotificationRoute.Conversation,
            cachedScheduleNotificationRoute("s", null, strip = strip),
        )
    }

    @Test fun sessionViewNonOwnedRoutesToConversation() = runTest {
        val view = SessionView("s", "computer", Session.newBuilder().setId("s").build())
        assertEquals(ScheduleNotificationRoute.Conversation, cachedScheduleNotificationRoute("s", view))
    }

    @Test fun coldLoadCanResolveAnOwnedSessionBeforeTheTaskListArrives() = runTest {
        var view: SessionView? = null
        val route = resolveScheduleNotificationRoute(
            cached = { cachedScheduleNotificationRoute("s", view) },
            load = {
                delay(100)
                view = SessionView("s", "computer", Session.newBuilder().setTaskOwned(true).setScheduledTaskId("t").build())
            },
        )
        assertEquals(ScheduleNotificationRoute.Task("computer", "t"), route)
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
        runCurrent()
        job.cancelAndJoin()
        assertTrue(job.isCancelled)
    }
}
