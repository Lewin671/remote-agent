package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.core.view.WindowCompat
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import java.io.File
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import org.junit.After
import org.junit.Rule
import org.junit.Test

/** Fixed data for reviewing native layouts on an emulator. No account, server or command authority. */
class ScheduleLayoutGallery {
    @get:Rule val ui = createAndroidComposeRule<ComponentActivity>()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "layout-client"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private var dark by mutableStateOf(false)
    private val machine = "layout-computer"
    private val project = "/work/project"
    private val task = ScheduledTask(machine, Json.parseToJsonElement("""{
      "id":"layout-task","revision":1,"status":"scheduled",
      "next_run_at":"2026-10-25T16:36:00Z","source":"local",
      "spec":{"title":"Android daily check","message":"Call the remote_agent notify_user tool with text Android scheduled check succeeded. Then reply Done.",
        "trigger":{"type":"calendar","time":"09:36","timezone":"America/Los_Angeles"},
        "target":{"type":"new_session","cwd":"/work/project","agent":"claude","model":"claude-haiku-5-5","mode":"auto"}},
      "runs":[{"id":"layout-run","session_id":"layout-run-session","started_at":"2026-10-24T16:36:00Z","status":"completed","notified":true,"notification_text":"Android scheduled check succeeded"}]
    }""").jsonObject)

    private fun prepare() {
        val meta = MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setHostname("Dev Computer").setScheduleRevision(1)
            .setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_AUTO)).build()
        val settings = ProjectSettings.newBuilder().setPath(project).setMachineId(machine).setAgent(Agent.AGENT_CLAUDE)
            .addAgents(AgentSettings.newBuilder().setAgent(Agent.AGENT_CLAUDE).setModel("claude-haiku-5-5").setMode(ApprovalMode.APPROVAL_MODE_AUTO)).build()
        val session = Session.newBuilder().setId("layout-session").setCwd(project).setAgent(Agent.AGENT_CLAUDE).setTitle("Project review")
            .setStatus(SessionStatus.SESSION_STATUS_IDLE).setMode(ApprovalMode.APPROVAL_MODE_AUTO).build()
        val state = RemoteState(
            conn = ConnState.ONLINE, link = Link.ONLINE, loaded = true, synced = true,
            machines = mapOf(machine to MachineView(machine, true, 0, meta)),
            sessions = mapOf("layout-session" to SessionView("layout-session", machine, session)),
            prefs = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1).addSettings(settings).build()),
        )
        remote.showForTest(state.copy(conn = ConnState.OFFLINE))
        val repo = remote.schedules
        @Suppress("UNCHECKED_CAST")
        val snapshots = repo.javaClass.getDeclaredField("_state").apply { isAccessible = true }.get(repo) as MutableStateFlow<Map<String, ScheduleSnapshot>>
        snapshots.value = mapOf(machine to ScheduleSnapshot(1, listOf(task), cached = false))
        remote.showForTest(state)
        ui.activityRule.scenario.onActivity { it.enableEdgeToEdge() }
    }

    private fun content(screen: @Composable () -> Unit) {
        ui.setContent {
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                SideEffect { WindowCompat.getInsetsController(ui.activity.window, ui.activity.window.decorView).apply {
                    isAppearanceLightStatusBars = !dark
                    isAppearanceLightNavigationBars = !dark
                } }
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.fillMaxSize().background(pal.card).safeDrawingPadding()) { screen() }
                }
            }
        }
    }

    private fun save(name: String) {
        ui.waitForIdle()
        Thread.sleep(1_000)
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val bitmap = requireNotNull(instrumentation.uiAutomation.takeScreenshot())
        val dir = File(instrumentation.targetContext.getExternalFilesDir(null), "schedule-layout").apply { mkdirs() }
        File(dir, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun detail() {
        prepare()
        content { ScheduleDetailScreen(machine, task.id) }
        ui.onNodeWithText("每天 09:36", substring = true).assertIsDisplayed()
        save("detail-light")
        ui.runOnIdle { dark = true }
        save("detail-dark")
    }

    @Test fun newTask() {
        prepare()
        var clearFocus: () -> Unit = {}
        content {
            val focus = LocalFocusManager.current
            clearFocus = { focus.clearFocus(force = true) }
            NewScheduleScreen("")
        }
        ui.onAllNodes(hasSetTextAction())[0].performTextReplacement("Daily project review")
        ui.onAllNodes(hasSetTextAction())[1].performTextReplacement("Review the project and notify me only when something needs attention.")
        ui.runOnIdle { clearFocus() }
        ui.onNodeWithText("选择项目").performClick()
        ui.onNodeWithText("project", useUnmergedTree = true).performClick()
        ui.onNodeWithText("CLI").assertIsDisplayed()
        ui.onNodeWithText("Claude Code").assertIsSelected()
        save("new-light")
        ui.runOnIdle { dark = true }
        save("new-dark")
    }

    @Test fun timeSettings() {
        prepare()
        content { ScheduleTimeSheet(task.trigger, {}, {}) }
        save("time-light")
        ui.onNodeWithText("每隔").performClick()
        save("interval-light")
    }

    @Test fun scheduledSend() {
        prepare()
        var clearFocus: () -> Unit = {}
        content {
            val focus = LocalFocusManager.current
            clearFocus = { focus.clearFocus(force = true) }
            NewScheduleScreen("layout-session")
        }
        ui.onAllNodes(hasSetTextAction())[0].performTextReplacement("Check the project and notify me when something needs attention.")
        ui.runOnIdle { clearFocus() }
        ui.onNodeWithText("定时发到这个对话").assertIsDisplayed()
        save("send-light")
    }

    @Test fun tasks() {
        prepare()
        content { SchedulesScreen() }
        ui.onNodeWithText("定时任务").assertIsDisplayed()
        save("tasks-layout-light")
    }

    @After fun close() { remote.close() }
}
