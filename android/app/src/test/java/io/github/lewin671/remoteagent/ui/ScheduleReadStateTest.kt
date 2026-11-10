package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.compose.LocalLifecycleOwner
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
        put("runs", JsonArray(runs.mapIndexed { index, run -> buildJsonObject {
            put("id", run); put("session_id", "shared"); put("status", "completed"); put("notified", true)
            put("ended_at", java.time.Instant.parse("2026-10-24T08:00:00Z").plusSeconds(index * 60L).toString()); put("started_at", "2026-10-24T08:00:00Z"); put("notification_text", "摘要 $run"); put("notification_source", "agent"); put("final_summary", "完整结果 $run")
        } }))
    })
    private fun prepare(owner: LifecycleOwner? = null, cacheOnly: Boolean = false, listFails: Boolean = false, cachedTranscript: Boolean = false, unreadReturnToSession: Boolean = false) {
        repository = ScheduleRepository(scope, remote.state, send = { _, _ ->
            if (listFails) error("list unavailable")
            buildJsonObject { put("tasks", JsonArray(tasks.map { it.json })) }.toString().toByteArray()
        }, read = { if (cacheOnly || listFails) buildJsonObject { put("tasks", JsonArray(tasks.map { it.json })) }.toString().toByteArray() else null }, seen = ScheduleSeen(read = { "[]" }))
        remote.javaClass.getDeclaredField("scheduleRepository").apply { isAccessible = true }.set(remote, lazy { repository })
        @Suppress("UNCHECKED_CAST")
        val active = app.javaClass.getDeclaredField("_remote").apply { isAccessible = true }.get(app) as MutableStateFlow<Remote?>
        active.value = remote
        val meta = MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).build()
        val sessions = mapOf("shared" to SessionView("shared", "computer", Session.newBuilder().setId("shared").setTitle("来源对话").build()))
        val events = if (cachedTranscript) mapOf("shared" to listOf(
            Event.newBuilder().setSeq(1).setOrigin(Origin.newBuilder().setScheduledRunId("r1")).setUserMessage(UserMessage.newBuilder().setText("检查项目")).build(),
            Event.newBuilder().setSeq(2).setAgentMessage(AgentMessage.newBuilder().setPhase("final_answer").setText("缓存会话里的完整回复")).build(),
        )) else emptyMap()
        remote.showForTest(RemoteState(conn = if (cacheOnly) ConnState.OFFLINE else ConnState.ONLINE, loaded = true,
            machines = mapOf("computer" to MachineView("computer", !cacheOnly, 0, meta)), sessions = sessions), events)
        val nav = Nav({ page.value = "list" }, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {},
            schedules = { page.value = if (unreadReturnToSession) "session" else "list" }, task = { _, _ -> page.value = "detail" }, scheduledSession = { _, _ -> page.value = "session" })
        ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalLifecycleOwner provides (owner ?: LocalLifecycleOwner.current)) {
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
        ui.runOnIdle { assertEquals(0, repository.badge.value); assertTrue(repository.hasNewResults.value) }
        ui.onAllNodesWithContentDescription("有新结果").assertCountEquals(2)
        ui.onNodeWithText("first").performClick()
        ui.runOnIdle { assertEquals(0, repository.badge.value); assertTrue(repository.hasNewResults.value) }
        ui.onNodeWithContentDescription("进入时未读", useUnmergedTree = true).assertExists()
        ui.runOnIdle { tasks = listOf(task("first", listOf("r1", "r3")), tasks[1]); runBlocking { repository.refresh("computer") } }
        ui.runOnIdle { assertEquals(0, repository.badge.value); assertTrue(repository.hasNewResults.value) }
        ui.onNodeWithText("有新结果").assertExists()
        ui.onNodeWithText("摘要 r1").assertExists()
        ui.onNodeWithText("摘要 r3").assertDoesNotExist()
        ui.onAllNodesWithContentDescription("进入时未读", useUnmergedTree = true).assertCountEquals(1)
        ui.runOnIdle { page.value = "list" }
        ui.onAllNodesWithContentDescription("有新结果").assertCountEquals(1)
        ui.onNodeWithText("first").performClick()
        ui.onAllNodesWithContentDescription("进入时未读", useUnmergedTree = true).assertCountEquals(0)
    }
    @Test fun openingARunSessionClearsAllRunsSharingThatSessionAndStopsOnLeaving() {
        prepare()
        ui.onNodeWithText("first").performClick()
        ui.onNodeWithText("对话").performClick()
        ui.runOnIdle { assertEquals("shared", app.visibleSession); assertEquals(0, repository.badge.value) }
        ui.runOnIdle { tasks = listOf(tasks[0], task("second", listOf("r2", "r3"))); runBlocking { repository.refresh("computer") } }
        ui.runOnIdle { assertEquals(0, repository.badge.value); page.value = "list" }
        ui.runOnIdle { assertNull(app.visibleSession); tasks = listOf(tasks[0], task("second", listOf("r2", "r3", "r4"))); runBlocking { repository.refresh("computer") } }
        ui.runOnIdle { assertEquals(0, repository.badge.value); assertTrue(repository.hasNewResults.value) }
    }
    @Test fun markingUnreadInDetailLeavesForListWithoutImmediatelyReadingAgain() {
        prepare(); ui.onNodeWithText("first").performClick()
        ui.onNodeWithContentDescription("更多").performClick()
        ui.onNodeWithText("标为未读").performClick()
        ui.onAllNodesWithContentDescription("有新结果").assertCountEquals(2)
        ui.runOnIdle { assertTrue(repository.unread.value.getValue(ScheduleTaskKey("computer", "first"))) }
    }
    @Test fun arrivingResultIsReadButOnlyReplacesTheCardAfterRefresh() {
        prepare(); ui.onNodeWithText("first").performClick()
        ui.runOnIdle { tasks = listOf(task("first", listOf("r1", "r3")), tasks[1]); runBlocking { repository.refresh("computer") } }
        ui.onNodeWithText("摘要 r1").assertExists(); ui.onNodeWithText("摘要 r3").assertDoesNotExist()
        ui.onNodeWithText("有新结果").performClick()
        ui.onNodeWithText("摘要 r3").assertExists()
        ui.runOnIdle { assertFalse(repository.unread.value.getValue(ScheduleTaskKey("computer", "first"))) }
    }

    @Test fun markingRecentGroupReadLeavesAttentionResultsUnread() {
        tasks = listOf(tasks[0].copy(json = JsonObject(tasks[0].json + mapOf("status" to JsonPrimitive("failed"), "reason" to JsonPrimitive("execution_failed")))), tasks[1])
        prepare()
        // One new result: no 全部标为已读; its card is marked read as a swipe would, by its action.
        val card = ui.onNode(hasAnyDescendant(hasText(tasks[1].title)) and SemanticsMatcher("can mark") { androidx.compose.ui.semantics.SemanticsActions.CustomActions in it.config }, useUnmergedTree = true)
        ui.runOnIdle { card.fetchSemanticsNode().config[androidx.compose.ui.semantics.SemanticsActions.CustomActions].single { it.label == "标为已读" }.action() }
        ui.runOnIdle {
            assertEquals(1, repository.badge.value)
            assertTrue(repository.unread.value.getValue(ScheduleTaskKey("computer", "first")))
            assertFalse(repository.unread.value.getValue(ScheduleTaskKey("computer", "second")))
        }
    }

    @Test fun returningFromBackgroundAddsDotsForResultsThatArrivedWhileHidden() {
        val owner = object : LifecycleOwner {
            val registry = LifecycleRegistry(this)
            override val lifecycle: Lifecycle get() = registry
        }
        ui.runOnUiThread { owner.registry.currentState = Lifecycle.State.STARTED }
        prepare(owner); ui.onNodeWithText("first").performClick()
        ui.runOnIdle { owner.registry.currentState = Lifecycle.State.CREATED }
        ui.runOnIdle {
            tasks = listOf(task("first", listOf("r1", "r3")), tasks[1]); runBlocking { repository.refresh("computer") }
            assertTrue(repository.unread.value.getValue(ScheduleTaskKey("computer", "first")))
            assertEquals(setOf("r1"), repository.detailUnread.value)
        }
        ui.runOnIdle { owner.registry.currentState = Lifecycle.State.STARTED }
        ui.runOnIdle {
            assertFalse(repository.unread.value.getValue(ScheduleTaskKey("computer", "first")))
            assertEquals(setOf("r1", "r3"), repository.detailUnread.value)
        }
        ui.onNodeWithText("有新结果").performClick()
        ui.onAllNodesWithContentDescription("进入时未读", useUnmergedTree = true).assertCountEquals(2)
    }

    @Test fun resultCardOnlyShowsADistinctAgentTitleAndDoesNotDuplicateItsBody() {
        prepare(); ui.onNodeWithText("first").performClick()
        ui.onNodeWithText("摘要 r1").assertExists()
        fun change(source: String, notice: String) {
            ui.runOnIdle {
                val first = tasks[0]
                val run = JsonObject(first.runs.single().json + mapOf("notification_source" to JsonPrimitive(source), "notification_text" to JsonPrimitive(notice)))
                tasks = listOf(first.copy(json = JsonObject(first.json + ("runs" to JsonArray(listOf(run))))), tasks[1])
                runBlocking { repository.refresh("computer") }
            }
        }
        change("daemon", "完整结果 r1")
        ui.onNodeWithText("摘要 r1").assertDoesNotExist()
        ui.onAllNodesWithText("完整结果 r1").assertCountEquals(1)
        change("agent", "  完整结果 r1  ")
        ui.onAllNodesWithText("完整结果 r1").assertCountEquals(1)
        change("agent_and_daemon", "独立标题")
        ui.onNodeWithText("独立标题").assertExists()
        ui.onAllNodesWithText("完整结果 r1").assertCountEquals(1)
    }

    @Test fun cachedOnlyDetailShowsLatestResultAndFullTextWithoutDotsOrAutomaticReads() {
        prepare(cacheOnly = true, cachedTranscript = true)
        ui.onNodeWithText("first").performClick()
        ui.onNodeWithText("摘要 r1").assertExists()
        ui.onNodeWithText("缓存会话里的完整回复").assertExists()
        ui.onNodeWithText("更早").assertDoesNotExist()
        ui.onNodeWithContentDescription("进入时未读").assertDoesNotExist()
        ui.runOnIdle {
            assertTrue(repository.unread.value.getValue(ScheduleTaskKey("computer", "first")))
            assertNull(repository.capturedDetail.value)
        }
    }
    @Test fun failedOnlineListStillShowsTheCachedLatestResultWithoutMarkingItRead() {
        prepare(listFails = true)
        ui.onNodeWithText("first").performClick()
        ui.onNodeWithText("摘要 r1").assertExists()
        ui.onNodeWithText("完整结果 r1").assertExists()
        ui.onNodeWithText("更早").assertDoesNotExist()
        ui.onNodeWithContentDescription("进入时未读").assertDoesNotExist()
        ui.runOnIdle {
            assertTrue(repository.state.value.getValue("computer").error.isNotEmpty())
            assertTrue(repository.unread.value.getValue(ScheduleTaskKey("computer", "first")))
            assertNull(repository.capturedDetail.value)
        }
    }
    @Test fun firstCompleteListCapturesTheLatestResultRatherThanFreezingTheCachedOne() {
        prepare(cacheOnly = true)
        ui.onNodeWithText("first").performClick()
        ui.runOnIdle {
            tasks = listOf(task("first", listOf("r1", "r3")), tasks[1])
            val state = remote.state.value
            remote.showForTest(state.copy(conn = ConnState.ONLINE, machines = state.machines.mapValues { it.value.copy(online = true) }))
        }
        ui.onNodeWithText("摘要 r3").assertExists()
        ui.onNodeWithText("有新结果").assertDoesNotExist()
        ui.runOnIdle {
            assertEquals(ScheduleTaskKey("computer", "first"), repository.capturedDetail.value)
            assertFalse(repository.unread.value.getValue(ScheduleTaskKey("computer", "first")))
            assertEquals(setOf("r1", "r3"), repository.detailUnread.value)
        }
    }
    @Test fun menuUnreadUsesTheNewResultThatArrivedAfterOpeningTheSheet() {
        prepare(); ui.onNodeWithText("first").performClick()
        ui.onNodeWithContentDescription("更多").performClick()
        ui.runOnIdle { tasks = listOf(task("first", listOf("r1", "r3")), tasks[1]); runBlocking { repository.refresh("computer") } }
        ui.onNodeWithText("标为未读").performClick()
        ui.onNodeWithText("first").performClick()
        ui.runOnIdle { assertEquals(setOf("r3"), repository.detailUnread.value) }
    }

    @Test fun unreadSurvivesDetailReturningToItsSessionButReadsAfterLeavingAndReturning() {
        prepare(unreadReturnToSession = true)
        ui.runOnIdle { page.value = "session" }
        ui.runOnIdle { page.value = "detail" }
        ui.onNodeWithContentDescription("更多").performClick(); ui.onNodeWithText("标为未读").performClick()
        ui.runOnIdle {
            assertEquals("shared", app.visibleSession)
            assertTrue(repository.unread.value.getValue(ScheduleTaskKey("computer", "first")))
            runBlocking { repository.refresh("computer") }
            assertTrue(repository.unread.value.getValue(ScheduleTaskKey("computer", "first")))
            page.value = "list"
        }
        ui.runOnIdle { page.value = "session" }
        ui.runOnIdle { assertFalse(repository.unread.value.getValue(ScheduleTaskKey("computer", "first"))) }
    }

}
