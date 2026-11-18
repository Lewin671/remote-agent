package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import android.view.inspector.WindowInspector
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
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
import com.google.protobuf.ByteString
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import io.github.lewin671.remoteagent.proto.Event
import java.io.File
import java.time.*
import kotlinx.coroutines.*
import kotlinx.serialization.json.*
import org.junit.After
import org.junit.Rule
import org.junit.Test

/** Fixed examples driving the real CoreHost with local fixture snapshots (no network). */
@OptIn(ExperimentalTestApi::class)
class ScheduleLayoutGallery {
    @get:Rule val ui = createAndroidComposeRule<ComponentActivity>()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "layout-client"), Api.defaultHttp())
    private val fixtureScope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private var dark by mutableStateOf(false)
    private val machine = "layout-computer"
    private val project = "/work/project"

    private fun at(hour: Int) = LocalDate.now().atTime(hour, 0).atZone(ZoneId.systemDefault()).toInstant()
    private fun stamp(hour: Int) = Timestamp.newBuilder().setSeconds(at(hour).epochSecond).build()

    private fun taskJson(
        id: String = "layout-task",
        title: String = "项目早报",
        message: String = "检查项目的更新，有值得注意的变化再通知我。",
        status: String = "scheduled",
        reason: String = "",
        trigger: JsonObject = buildJsonObject {
            put("type", "calendar"); put("time", "08:00"); put("timezone", ZoneId.systemDefault().id)
        },
        target: JsonObject = buildJsonObject {
            put("type", "new_session"); put("cwd", project); put("agent", "claude"); put("model", "claude-haiku-5-5"); put("mode", "auto")
        },
        runs: JsonArray = buildJsonArray {
            add(buildJsonObject {
                put("id", "daily-run"); put("session_id", "layout-owned"); put("started_at", at(8).toString())
                put("status", "completed"); put("notified", true); put("notification_text", "发现一条需要关注的更新。")
            })
        },
    ): JsonObject = buildJsonObject {
        put("id", id); put("revision", 1); put("status", status)
        if (reason.isNotEmpty()) put("reason", reason)
        put("source", "local"); put("created_at", at(7).toString())
        put("next_run_at", LocalDate.now().plusDays(1).atTime(8, 0).atZone(ZoneId.systemDefault()).toInstant().toString())
        put("spec", buildJsonObject {
            put("title", title); put("message", message); put("trigger", trigger); put("target", target)
        })
        put("runs", runs)
    }

    private fun history(msg: String): List<Event> = listOf(
        Event.newBuilder().setSeq(1).setTime(stamp(7)).setOrigin(Origin.newBuilder().setKind(OriginKind.ORIGIN_KIND_CLIENT).setDeviceId("layout-client")).setUserMessage(UserMessage.newBuilder().setText("检查 PR 的回复。")),
        Event.newBuilder().setSeq(2).setTime(stamp(7)).setAgentMessage(AgentMessage.newBuilder().setText("有变化时会通知你。")),
    ).map { it.build() } + (8..9).flatMap { hour ->
        val seq = if (hour == 8) 3L else 6L
        listOf(
            Event.newBuilder().setSeq(seq).setTime(stamp(hour)).setOrigin(Origin.newBuilder().setScheduledTaskId("current-task").setScheduledRunId("quiet-$hour")).setUserMessage(UserMessage.newBuilder().setText(msg)).build(),
            Event.newBuilder().setSeq(seq + 1).setTime(stamp(hour)).setAgentMessage(AgentMessage.newBuilder().setText("目前没有新的回复。")).build(),
            Event.newBuilder().setSeq(seq + 2).setTime(stamp(hour)).setTurnEnded(TurnEnded.newBuilder().setOutcome(TurnOutcome.TURN_OUTCOME_COMPLETED).setScheduledTaskId("current-task").setScheduledRunId("quiet-$hour")).build(),
        )
    }

    private fun prepare(tasks: List<JsonObject> = listOf(taskJson()), offline: Boolean = false, owned: Boolean = false) {
        val meta = MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setHostname("Dev Computer").setScheduleRevision(1)
            .setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_AUTO)).build()
        val settings = ProjectSettings.newBuilder().setPath(project).setMachineId(machine).setAgent(Agent.AGENT_CLAUDE)
            .addAgents(AgentSettings.newBuilder().setAgent(Agent.AGENT_CLAUDE).setModel("claude-haiku-5-5").setMode(ApprovalMode.APPROVAL_MODE_AUTO)).build()
        val session = Session.newBuilder().setId("layout-session").setCwd(project).setAgent(Agent.AGENT_CLAUDE).setTitle("修复登录超时").setCreatedAt(stamp(7))
            .setStatus(SessionStatus.SESSION_STATUS_IDLE).setMode(ApprovalMode.APPROVAL_MODE_AUTO).build()
        val ownedSession = session.toBuilder().setId("layout-owned").setTitle("项目早报").setTaskOwned(owned).setScheduledTaskId("layout-task").build()
        val state = RemoteState(
            conn = if (offline) ConnState.OFFLINE else ConnState.ONLINE,
            link = if (offline) io.github.lewin671.remoteagent.core.Link.NO_SERVER else io.github.lewin671.remoteagent.core.Link.ONLINE,
            loaded = true, synced = true,
            machines = mapOf(machine to MachineView(machine, !offline, 0, meta)),
            sessions = mapOf(
                "layout-session" to SessionView("layout-session", machine, session),
                "layout-owned" to SessionView("layout-owned", machine, ownedSession),
            ),
            prefs = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1).addSettings(settings).build()),
        )
        val histories = mapOf("layout-session" to history("每小时检查 PR 是否有新回复。"), "layout-owned" to listOf(
            Event.newBuilder().setSeq(1).setTime(stamp(8)).setOrigin(Origin.newBuilder().setScheduledTaskId("layout-task")).setUserMessage(UserMessage.newBuilder().setText("检查项目")).build(),
            Event.newBuilder().setSeq(2).setTime(stamp(8)).setAgentMessage(AgentMessage.newBuilder().setText("发现一条需要关注的更新。")).build(),
        ))
        val live = state.sessions.mapValues { LiveSnapshot.newBuilder().setSession(it.value.info).build() }
        remote.showForTest(state, sessions = sessionVmsForTest(state, histories, live))
        val rawTasks = buildJsonObject { put("tasks", JsonArray(tasks)) }.toString().toByteArray()
        remote.coreHost.deliverScheduleSeenLoaded(machine, found = true)
        remote.coreHost.deliverScheduleCacheLoaded(1L, machine, rawTasks)
        ui.activityRule.scenario.onActivity { it.enableEdgeToEdge() }
    }

    private fun content(screen: @Composable () -> Unit) {
        ui.setContent {
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                SideEffect {
                    WindowCompat.getInsetsController(ui.activity.window, ui.activity.window.decorView).apply {
                        isAppearanceLightStatusBars = !dark; isAppearanceLightNavigationBars = !dark
                    }
                }
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.fillMaxSize().background(pal.paper).safeDrawingPadding()) { screen() }
                }
            }
        }
    }

    private fun save(name: String) {
        ui.waitForIdle()
        Thread.sleep(350)
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        repeat(2) {
            lateinit var committed: CountDownLatch
            instrumentation.runOnMainSync {
                val roots = WindowInspector.getGlobalWindowViews().filter { it.isShown && it.isHardwareAccelerated }
                check(roots.isNotEmpty())
                committed = CountDownLatch(roots.size)
                roots.forEach { view -> view.viewTreeObserver.registerFrameCommitCallback { committed.countDown() }; view.postInvalidateOnAnimation() }
            }
            check(committed.await(5, TimeUnit.SECONDS)) { "visible windows did not commit a frame" }
        }
        val dir = File(instrumentation.targetContext.getExternalFilesDir(null), "schedule-layout").apply { mkdirs() }
        File(dir, "$name.png").outputStream().use { requireNotNull(instrumentation.uiAutomation.takeScreenshot()).compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun both(name: String) { save("$name-light"); ui.runOnIdle { dark = true }; save("$name-dark"); ui.runOnIdle { dark = false } }

    @Test fun detail() { prepare(); content { ScheduleDetailScreen(machine, "layout-task") }; both("detail") }
    @Test fun tasks() { prepare(); content { SchedulesScreen() }; both("tasks") }
    @Test fun offline() { prepare(offline = true); content { SchedulesScreen() }; both("offline-cache") }
    @Test fun newTask() { prepare(); content { NewScheduleScreen("") }; both("new") }
    @Test fun jump() { prepare(); ScheduleJump.requested["layout-session"] = 6; content { SessionScreen("layout-session") }; save("current-run-jump-light") }
    @Test fun takeover() { prepare(owned = true); content { SessionScreen("layout-owned") }; ui.onNodeWithText("回复后，这个对话会移到会话列表").assertExists(); both("takeover") }

    @After fun close() {
        fixtureScope.cancel(); remote.close(); ScheduleJump.requested.clear()
    }
}
