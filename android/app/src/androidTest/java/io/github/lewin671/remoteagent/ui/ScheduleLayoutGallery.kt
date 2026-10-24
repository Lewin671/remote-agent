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
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import java.io.File
import java.time.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.*
import org.junit.After
import org.junit.Rule
import org.junit.Test

/** Fixed v49 examples. The stopped Remote has no account, server or command authority. */
class ScheduleLayoutGallery {
    @get:Rule val ui = createAndroidComposeRule<ComponentActivity>()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "layout-client"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private var dark by mutableStateOf(false)
    private val machine = "layout-computer"
    private val project = "/work/project"
    private fun at(hour: Int) = LocalDate.now().atTime(hour,0).atZone(ZoneId.systemDefault()).toInstant()
    private fun stamp(hour: Int) = Timestamp.newBuilder().setSeconds(at(hour).epochSecond).build()
    private val task by lazy { ScheduledTask(machine, buildJsonObject {
        put("id","layout-task"); put("revision",1); put("status","scheduled"); put("source","local"); put("created_at",at(7).toString())
        put("next_run_at",LocalDate.now().plusDays(1).atTime(8,0).atZone(ZoneId.systemDefault()).toInstant().toString())
        put("spec",taskSpec("项目早报","检查项目的更新，有值得注意的变化再通知我。",
            buildJsonObject { put("type","calendar"); put("time","08:00"); put("timezone",ZoneId.systemDefault().id) },
            buildJsonObject { put("type","new_session");put("cwd",project);put("agent","claude");put("model","claude-haiku-5-5");put("mode","auto") }))
        put("runs",buildJsonArray { add(buildJsonObject { put("id","daily-run");put("session_id","layout-owned");put("started_at",at(8).toString());put("status","completed");put("notified",true);put("notification_text","发现一条需要关注的更新。") }) })
    }) }
    private val current by lazy { ScheduledTask(machine, JsonObject(task.json + mapOf(
        "id" to JsonPrimitive("current-task"), "next_run_at" to JsonPrimitive(at(15).toString()),
        "spec" to taskSpec("检查 PR 回复","每小时检查 PR 是否有新回复，有变化再通知我。",buildJsonObject { put("type","interval");put("interval_seconds",3600) },buildJsonObject { put("type","resume_session");put("session","layout-session") }),
        "runs" to buildJsonArray { (8..9).forEach { hour -> add(buildJsonObject { put("id","quiet-$hour");put("session_id","layout-session");put("start_seq",if(hour==8) 3 else 6);put("started_at",at(hour).toString());put("status","completed");put("notified",false);put("final_summary","目前没有新的回复。") }) } },
    ))) }
    private val failed by lazy { ScheduledTask(machine, JsonObject(task.json + mapOf("id" to JsonPrimitive("failed-task"), "status" to JsonPrimitive("failed"), "reason" to JsonPrimitive("relay_session_limit"),
        "spec" to JsonObject(task.spec + ("title" to JsonPrimitive("每日依赖检查"))), "runs" to buildJsonArray { add(buildJsonObject { put("id","failed-run");put("started_at",at(8).toString());put("status","failed");put("notified",true);put("notification_text","旧通知不能盖住失败原因");put("reason","relay_session_limit") }) }))) }
    private fun history(): List<Event> = listOf(
        Event.newBuilder().setSeq(1).setTime(stamp(7)).setOrigin(Origin.newBuilder().setKind(OriginKind.ORIGIN_KIND_CLIENT).setDeviceId("layout-client")).setUserMessage(UserMessage.newBuilder().setText("检查 PR 的回复。")),
        Event.newBuilder().setSeq(2).setTime(stamp(7)).setAgentMessage(AgentMessage.newBuilder().setText("有变化时会通知你。")),
    ).map { it.build() } + (8..9).flatMap { hour ->
        val seq = if(hour==8) 3L else 6L
        listOf(
            Event.newBuilder().setSeq(seq).setTime(stamp(hour)).setOrigin(Origin.newBuilder().setScheduledTaskId(current.id).setScheduledRunId("quiet-$hour")).setUserMessage(UserMessage.newBuilder().setText(current.message)).build(),
            Event.newBuilder().setSeq(seq+1).setTime(stamp(hour)).setAgentMessage(AgentMessage.newBuilder().setText("目前没有新的回复。" )).build(),
            Event.newBuilder().setSeq(seq+2).setTime(stamp(hour)).setTurnEnded(TurnEnded.newBuilder().setOutcome(TurnOutcome.TURN_OUTCOME_COMPLETED).setScheduledTaskId(current.id).setScheduledRunId("quiet-$hour")).build(),
        )
    }
    private fun prepare(tasks: List<ScheduledTask> = listOf(task,current), offline: Boolean = false, owned: Boolean = false) {
        val meta = MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setHostname("Dev Computer").setScheduleRevision(1)
            .setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_AUTO)).build()
        val settings = ProjectSettings.newBuilder().setPath(project).setMachineId(machine).setAgent(Agent.AGENT_CLAUDE)
            .addAgents(AgentSettings.newBuilder().setAgent(Agent.AGENT_CLAUDE).setModel("claude-haiku-5-5").setMode(ApprovalMode.APPROVAL_MODE_AUTO)).build()
        val session = Session.newBuilder().setId("layout-session").setCwd(project).setAgent(Agent.AGENT_CLAUDE).setTitle("修复登录超时").setCreatedAt(stamp(7))
            .setStatus(SessionStatus.SESSION_STATUS_IDLE).setMode(ApprovalMode.APPROVAL_MODE_AUTO).build()
        val ownedSession = session.toBuilder().setId("layout-owned").setTitle("项目早报").setTaskOwned(owned).setScheduledTaskId(task.id).build()
        val state = RemoteState(conn=ConnState.ONLINE,link=Link.ONLINE,loaded=true,synced=true,
            machines=mapOf(machine to MachineView(machine,!offline,0,meta)),
            sessions=mapOf("layout-session" to SessionView("layout-session",machine,session),"layout-owned" to SessionView("layout-owned",machine,ownedSession)),
            prefs=PrefsObject(1,ProjectPrefs.newBuilder().setSchema(1).addSettings(settings).build()))
        val ownedHistory = listOf(
            Event.newBuilder().setSeq(1).setTime(stamp(8)).setOrigin(Origin.newBuilder().setScheduledTaskId(task.id)).setUserMessage(UserMessage.newBuilder().setText(task.message)).build(),
            Event.newBuilder().setSeq(2).setTime(stamp(8)).setAgentMessage(AgentMessage.newBuilder().setText("发现一条需要关注的更新。" )).build(),
            Event.newBuilder().setSeq(3).setTime(stamp(8)).setScheduledNotification(ScheduledNotification.newBuilder().setTaskId(task.id).setRunId("daily-run").setText("发现一条需要关注的更新。" )).build(),
        )
        val histories=mapOf("layout-session" to history(),"layout-owned" to ownedHistory)
        val live=state.sessions.mapValues { LiveSnapshot.newBuilder().setSession(it.value.info).build() }
        remote.showForTest(state.copy(conn=ConnState.OFFLINE),histories,live=live)
        val repo=remote.schedules
        @Suppress("UNCHECKED_CAST")
        val snapshots=repo.javaClass.getDeclaredField("_state").apply { isAccessible=true }.get(repo) as MutableStateFlow<Map<String,ScheduleSnapshot>>
        snapshots.value=mapOf(machine to ScheduleSnapshot(1,tasks,cached=false))
        remote.showForTest(state,histories,live=live)
        ui.activityRule.scenario.onActivity { it.enableEdgeToEdge() }
    }
    private fun content(screen: @Composable () -> Unit) {
        ui.setContent { RaTheme(if(dark) ThemeMode.DARK else ThemeMode.LIGHT) {
            SideEffect { WindowCompat.getInsetsController(ui.activity.window,ui.activity.window.decorView).apply { isAppearanceLightStatusBars=!dark;isAppearanceLightNavigationBars=!dark } }
            CompositionLocalProvider(LocalRemote provides remote,LocalNav provides nav) {
                Column(Modifier.fillMaxSize().background(pal.card).safeDrawingPadding()) { screen() }
            }
        } }
    }
    private fun save(name: String) {
        ui.waitForIdle(); Thread.sleep(600)
        val instrumentation=InstrumentationRegistry.getInstrumentation()
        val dir=File(instrumentation.targetContext.getExternalFilesDir(null),"schedule-layout").apply { mkdirs() }
        File(dir,"$name.png").outputStream().use { requireNotNull(instrumentation.uiAutomation.takeScreenshot()).compress(Bitmap.CompressFormat.PNG,100,it) }
    }
    private fun both(name: String) { save("$name-light");ui.runOnIdle { dark=true };save("$name-dark");ui.runOnIdle { dark=false } }
    @Composable private fun TaskPage() {
        Column(Modifier.fillMaxSize()) { Box(Modifier.weight(1f)) { SchedulesScreen() }; TabBar("schedules",0,1) {} }
    }
    @Test fun detail() { prepare();content { ScheduleDetailScreen(machine,task.id) };both("detail") }
    @Test fun foreignTimezone() {
        val zone = if (ZoneId.systemDefault().id == "Asia/Shanghai") "America/Los_Angeles" else "Asia/Shanghai"
        val trigger = JsonObject(task.trigger + ("timezone" to JsonPrimitive(zone)))
        val spec = JsonObject(task.spec + ("trigger" to trigger))
        val foreign = ScheduledTask(machine, JsonObject(task.json + ("spec" to spec)))
        prepare(listOf(foreign));content { ScheduleDetailScreen(machine,foreign.id) }
        ui.onNodeWithText("每天 08:00 · $zone").assertExists();save("detail-foreign-light")
    }
    @Test fun currentDetail() { prepare();content { ScheduleDetailScreen(machine,current.id) };both("current-detail") }
    @Test fun rename() { prepare();content { ScheduleDetailScreen(machine,task.id) };ui.onNodeWithText("任务名").performClick();ui.onNodeWithText("重命名任务").assertIsDisplayed();both("rename") }
    @Test fun failedTask() { prepare(listOf(failed));content { ScheduleDetailScreen(machine,failed.id) };ui.onNodeWithText("任务名").assertHasClickAction();both("failure");ui.onNodeWithText("任务名").performClick();ui.onNodeWithText("重命名任务").assertIsDisplayed() }
    @Test fun newTask() {
        prepare();var clear:()->Unit={}
        content { val focus=LocalFocusManager.current;clear={focus.clearFocus(force=true)};NewScheduleScreen("") }
        ui.onAllNodes(hasSetTextAction())[1].performTextReplacement("每天检查项目的更新，有值得注意的变化再通知我。")
        ui.runOnIdle { clear() };ui.onNodeWithText("选择项目").performClick();ui.onNodeWithText("project",useUnmergedTree=true).performClick()
        ui.onNodeWithText("留空就用消息的第一句").assertExists();ui.onNodeWithText("权限").assertExists();ui.onNodeWithText("模型").assertExists();both("new")
    }
    @Test fun timeSettings() { prepare();content { ScheduleTimeSheet(task.trigger,{}, {}) };both("time");ui.onNodeWithText("每隔").performClick();both("interval") }
    @Test fun scheduledSend() {
        prepare();var clear:()->Unit={}
        content { val focus=LocalFocusManager.current;clear={focus.clearFocus(force=true)};NewScheduleScreen("layout-session") }
        ui.onAllNodes(hasSetTextAction())[1].performTextReplacement(current.message);ui.runOnIdle {clear()};both("send")
    }
    @Test fun tasks() {
        val paused=ScheduledTask(machine,JsonObject(task.json+mapOf("id" to JsonPrimitive("paused"),"status" to JsonPrimitive("paused"),"reason" to JsonPrimitive("user_paused"))))
        val ended=ScheduledTask(machine,JsonObject(task.json+mapOf("id" to JsonPrimitive("ended"),"status" to JsonPrimitive("completed"))))
        prepare(listOf(task,current,failed,paused,ended));content { TaskPage() };both("tasks");save("tasks-layout-light")
    }
    @Test fun offline() { prepare(offline=true);content { TaskPage() };both("offline-cache") }
    @Test fun chat() {
        prepare();content { SessionScreen("layout-session") }
        ui.waitUntil(5_000) { ui.onAllNodesWithText("没有新回复",substring=true).fetchSemanticsNodes().isNotEmpty() }
        both("chat-strip");save("quiet-fold-light")
        ui.onNodeWithText("定时检查",substring=true).performClick();save("quiet-expanded-light")
    }
    @Test fun jump() { prepare();ScheduleJump.requested["layout-session"]=6;content { SessionScreen("layout-session") };save("current-run-jump-light") }
    @Test fun takeover() { prepare(owned=true);content { SessionScreen("layout-owned") };ui.onNodeWithText("回复后，这个对话会移到会话列表").assertExists();both("takeover") }
    @Test fun deleteConfirm() { prepare();content { ScheduleDetailScreen(machine,task.id) };ui.onNodeWithText("删除任务").performScrollTo().performClick();ui.onNodeWithText("删除这个任务？").assertExists();save("delete-confirm-light") }
    private fun shell(command: String) {
        android.os.ParcelFileDescriptor.AutoCloseInputStream(InstrumentationRegistry.getInstrumentation().uiAutomation.executeShellCommand(command)).use { it.readBytes() }
    }
    @Test fun wide() {
        shell("wm size 1920x1200");shell("wm density 240");Thread.sleep(1000)
        prepare();content { Row { SidebarTabs("schedules",0,1) {};Box(Modifier.weight(1f)) { SchedulesScreen() } } };save("tasks-wide-light")
    }
    @After fun close() {
        remote.close();ScheduleJump.requested.clear()
        shell("wm size 1080x1920");shell("wm density 420")
    }

}
