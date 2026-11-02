package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import io.github.lewin671.remoteagent.push.VisiblePushSession
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.*
import org.junit.*
import org.junit.Assert.*
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** Exercise the actual list/detail and visible-session bindings, not only ScheduleSeen helpers. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ScheduleReadStateTest {
    @get:Rule val ui = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
    private val remote = Remote(Account("https://example.com", ByteArray(32), ByteArray(32), "test-client"), Api.defaultHttp())
    private val app = RaApp()
    private val page = mutableStateOf("list")
    private var tasks = listOf(task("first", listOf("r1")), task("second", listOf("r2")))
    private lateinit var repository: ScheduleRepository
    private fun task(id: String, runs: List<String>) = ScheduledTask("computer", buildJsonObject {
        put("id", id); put("status", "scheduled")
        put("spec", buildJsonObject { put("title", id); put("message", "检查项目"); put("trigger", buildJsonObject { put("type", "interval"); put("interval_seconds", 3600) }) })
        put("runs", JsonArray(runs.map { run -> buildJsonObject {
            put("id", run); put("session_id", "shared"); put("status", "completed"); put("notified", true)
            put("started_at", "2026-10-24T08:00:00Z"); put("notification_text", "摘要 $run")
        } }))
    })
    private fun prepare() {
        repository = ScheduleRepository(scope, remote.state, send = { _, _ ->
            buildJsonObject { put("tasks", JsonArray(tasks.map { it.json })) }.toString().toByteArray()
        }, seen = ScheduleSeen(read = { "[]" }))
        remote.javaClass.getDeclaredField("scheduleRepository").apply { isAccessible = true }.set(remote, lazy { repository })
        @Suppress("UNCHECKED_CAST")
        val active = app.javaClass.getDeclaredField("_remote").apply { isAccessible = true }.get(app) as MutableStateFlow<Remote?>
        active.value = remote
        remote.showForTest(RemoteState(conn = ConnState.ONLINE, loaded = true, machines = mapOf("computer" to MachineView("computer", true, 0,
            MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).build()))))
        val nav = Nav({ page.value = "list" }, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {},
            task = { _, _ -> page.value = "detail" }, scheduledSession = { _, _ -> page.value = "session" })
        ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
            VisiblePushSession(app, "shared".takeIf { page.value == "session" })
            when (page.value) {
                "list" -> SchedulesScreen()
                "detail" -> ScheduleDetailScreen("computer", "first")
            }
        } } }
    }
    @After fun close() { scope.cancel(); remote.close() }
    @Test fun listKeepsUnreadAndDetailClearsOnlyItsTaskWhileKeepingVisitDots() {
        prepare()
        ui.runOnIdle { assertEquals(2, repository.badge.value) }
        ui.onAllNodesWithContentDescription("1 条未读通知").assertCountEquals(2)
        ui.onNodeWithText("first").performClick()
        ui.runOnIdle { assertEquals(1, repository.badge.value) }
        ui.onNodeWithContentDescription("本次查看前未读", useUnmergedTree = true).assertExists()
        ui.runOnIdle { tasks = listOf(task("first", listOf("r1", "r3")), tasks[1]); runBlocking { repository.refresh("computer") } }
        ui.runOnIdle { assertEquals(1, repository.badge.value) }
        ui.onAllNodesWithContentDescription("本次查看前未读", useUnmergedTree = true).assertCountEquals(1)
        ui.runOnIdle { page.value = "list" }
        ui.onAllNodesWithContentDescription("1 条未读通知").assertCountEquals(1)
        ui.onNodeWithText("first").performClick()
        ui.onAllNodesWithContentDescription("本次查看前未读", useUnmergedTree = true).assertCountEquals(0)
    }
    @Test fun openingARunSessionClearsAllRunsSharingThatSessionAndStopsOnLeaving() {
        prepare()
        ui.onNodeWithText("first").performClick()
        ui.onNodeWithText("摘要 r1").performScrollTo().performClick()
        ui.runOnIdle { assertEquals("shared", app.visibleSession); assertEquals(0, repository.badge.value) }
        ui.runOnIdle { tasks = listOf(tasks[0], task("second", listOf("r2", "r3"))); runBlocking { repository.refresh("computer") } }
        ui.runOnIdle { assertEquals(0, repository.badge.value); page.value = "list" }
        ui.runOnIdle { assertNull(app.visibleSession); tasks = listOf(tasks[0], task("second", listOf("r2", "r3", "r4"))); runBlocking { repository.refresh("computer") } }
        ui.runOnIdle { assertEquals(1, repository.badge.value) }
    }
}
