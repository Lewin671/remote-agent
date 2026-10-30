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
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import java.io.File
import java.time.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.*
import kotlinx.serialization.json.*
import org.junit.After
import org.junit.Rule
import org.junit.Test

/** Fixed v52 examples. The stopped Remote has no account, server or command authority. */
@OptIn(ExperimentalTestApi::class)
class ScheduleLayoutGallery {
    @get:Rule val ui = createAndroidComposeRule<ComponentActivity>()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "layout-client"), Api.defaultHttp())
    private val fixtureScope=CoroutineScope(SupervisorJob()+Dispatchers.Main.immediate)
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
        val repository=ScheduleRepository(fixtureScope,remote.state,send={ _, request ->
            check(request.operation == "list")
            buildJsonObject { put("tasks",JsonArray(tasks.map { it.json })) }.toString().toByteArray()
        })
        remote.javaClass.getDeclaredField("scheduleRepository").apply { isAccessible=true }.set(remote,lazy { repository })
        remote.showForTest(state.copy(conn=ConnState.OFFLINE),histories,live=live)
        @Suppress("UNCHECKED_CAST")
        val snapshots=repository.javaClass.getDeclaredField("_state").apply { isAccessible=true }.get(repository) as MutableStateFlow<Map<String,ScheduleSnapshot>>
        snapshots.value=mapOf(machine to ScheduleSnapshot(1,tasks,cached=offline))
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
        ui.waitForIdle()
        // System bars and the native sheet window animate outside Compose's test clock.
        Thread.sleep(350)
        val instrumentation=InstrumentationRegistry.getInstrumentation()
        repeat(2) {
            lateinit var committed: CountDownLatch
            instrumentation.runOnMainSync {
                val roots=WindowInspector.getGlobalWindowViews().filter { it.isShown && it.isHardwareAccelerated }
                check(roots.isNotEmpty())
                committed=CountDownLatch(roots.size)
                roots.forEach { view -> view.viewTreeObserver.registerFrameCommitCallback { committed.countDown() }; view.postInvalidateOnAnimation() }
            }
            check(committed.await(5,TimeUnit.SECONDS)) { "visible windows did not commit a frame" }
        }
        val dir=File(instrumentation.targetContext.getExternalFilesDir(null),"schedule-layout").apply { mkdirs() }
        File(dir,"$name.png").outputStream().use { requireNotNull(instrumentation.uiAutomation.takeScreenshot()).compress(Bitmap.CompressFormat.PNG,100,it) }
    }
    private fun both(name: String) { save("$name-light");ui.runOnIdle { dark=true };save("$name-dark");ui.runOnIdle { dark=false } }
    @Composable private fun TaskPage() {
        val badge by remote.schedules.badge.collectAsState()
        Column(Modifier.fillMaxSize()) { Box(Modifier.weight(1f)) { SchedulesScreen() }; TabBar("schedules",0,badge) {} }
    }
    @Test fun detail() { prepare();content { ScheduleDetailScreen(machine,task.id) };both("detail") }
    @Test fun foreignTimezone() {
        val zone = if (ZoneId.systemDefault().id == "Asia/Shanghai") "America/Los_Angeles" else "Asia/Shanghai"
        val trigger = JsonObject(task.trigger + ("timezone" to JsonPrimitive(zone)))
        val spec = JsonObject(task.spec + ("trigger" to trigger))
        val foreign = ScheduledTask(machine, JsonObject(task.json + ("spec" to spec)))
        prepare(listOf(foreign));content { ScheduleDetailScreen(machine,foreign.id) }
        ui.onNodeWithText("每天 08:00 · $zone").assertExists();both("detail-foreign")
    }
    @Test fun currentDetail() { prepare();content { ScheduleDetailScreen(machine,current.id) };both("current-detail") }
    @Test fun rename() { prepare();content { ScheduleDetailScreen(machine,task.id) };ui.onNodeWithText("任务名").performScrollTo().performClick();ui.onNodeWithText("重命名任务").assertIsDisplayed();both("rename") }
    @Test fun failedTask() { prepare(listOf(failed));content { ScheduleDetailScreen(machine,failed.id) };ui.onNodeWithText("任务名").assertHasClickAction();both("detail-stopped-failure");ui.onNodeWithText("任务名").performScrollTo().performClick();ui.onNodeWithText("重命名任务").assertIsDisplayed() }
    @Test fun newTask() {
        prepare();var clear:()->Unit={}
        content { val focus=LocalFocusManager.current;clear={focus.clearFocus(force=true)};NewScheduleScreen("") }
        ui.onAllNodes(hasSetTextAction())[0].performTextReplacement("每天检查项目的更新，有值得注意的变化再通知我。")
        ui.runOnIdle { clear() };ui.onNodeWithText("选择项目").performClick();ui.onNodeWithText("project",useUnmergedTree=true).performClick()
        ui.onNodeWithText("留空会用：",substring=true).assertExists();ui.onNodeWithText("权限").assertExists();ui.onNodeWithText("模型").assertExists();both("new")
    }
    @Test fun timeSettings() {
        prepare();content { ScheduleTimeSheet(task.trigger,{}, {}) };both("time-daily")
        ui.onNodeWithText("一次").performClick();both("time-once")
        ui.onNodeWithText("每周").performClick();ui.onNodeWithText("五").performClick();both("time-weekly")
        ui.onNodeWithText("每隔").performClick();both("time-interval")
    }
    @Test fun scheduledSend() {
        prepare();var clear:()->Unit={}
        content { val focus=LocalFocusManager.current;clear={focus.clearFocus(force=true)};NewScheduleScreen("layout-session") }
        ui.onAllNodes(hasSetTextAction())[0].performTextReplacement(current.message);ui.runOnIdle {clear()};both("send")
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

    private val longMessage = (1..16).joinToString("\n") { "$it. 检查项目里的更新、构建和测试；有重要结果再通知我。" }
    @Test fun empty() { prepare(emptyList());content { TaskPage() };both("tasks-empty") }
    @Test fun offlineDetail() { prepare(offline=true);content { ScheduleDetailScreen(machine,task.id) };both("detail-offline");ui.onNodeWithText("连上后才能修改、运行或删除。",substring=true).performScrollTo();both("detail-offline-actions") }
    @Test fun permissionsStopped() {
        val stopped=ScheduledTask(machine,JsonObject(task.json+mapOf("status" to JsonPrimitive("paused"),"reason" to JsonPrimitive("agent_schedule_permissions_changed"))))
        prepare(listOf(stopped));content { ScheduleDetailScreen(machine,stopped.id) };both("detail-stopped-permissions")
    }
    @Test fun uncertainTask() {
        val stopped=ScheduledTask(machine,JsonObject(task.json+mapOf("status" to JsonPrimitive("uncertain"),"reason" to JsonPrimitive("restart_dispatching"))))
        prepare(listOf(stopped));content { ScheduleDetailScreen(machine,stopped.id) }
        ui.onNodeWithText("继续可能重复执行上次的消息，请先打开运行记录核实。",substring=true).assertExists()
        both("detail-stopped-uncertain")
    }
    @Test fun failedOnceTask() {
        val spec=JsonObject(failed.spec+("trigger" to buildJsonObject { put("type","at");put("at",at(8).toString()) }))
        val stopped=ScheduledTask(machine,JsonObject(failed.json+("spec" to spec)))
        prepare(listOf(stopped));content { ScheduleDetailScreen(machine,stopped.id) }
        ui.onNodeWithText("可立即额外执行一次，或改时间重排。",substring=true).assertExists()
        both("detail-stopped-once")
    }
    @Test fun missingTarget() {
        val failedRun=buildJsonObject { put("id","missing-target-run");put("started_at",at(15).toString());put("status","failed");put("reason","execution_session_missing") }
        val stopped=ScheduledTask(machine,JsonObject(current.json+mapOf("status" to JsonPrimitive("failed"),"reason" to JsonPrimitive("execution_session_missing"),"runs" to JsonArray(current.runs.map { it.json }+failedRun))))
        prepare(listOf(stopped));remote.showForTest(remote.state.value.copy(sessions=remote.state.value.sessions - "layout-session"));content { ScheduleDetailScreen(machine,stopped.id) };both("detail-stopped-missing-target")
    }
    @Test fun highFrequency() {
        val runs = buildJsonArray {
            (1..38).forEach { n -> add(buildJsonObject {
                put("id","frequent-$n"); put("started_at",at(8).plusSeconds(n*60L).toString())
                put("status","completed"); put("notified",false)
            }) }
        }
        val many=ScheduledTask(machine,JsonObject(current.json+("runs" to runs)))
        prepare(listOf(many));content { ScheduleDetailScreen(machine,many.id) };both("detail-high-frequency")
        ui.onNodeWithText("查看全部 38 次").assertExists()
    }
    @Test fun crowdedOffline() {
        val crowded=(1..12).map { n -> ScheduledTask(machine,JsonObject(task.json+mapOf("id" to JsonPrimitive("crowded-$n"),"spec" to JsonObject(task.spec+("title" to JsonPrimitive("每周检查依赖并把需要升级的大版本整理成清单 $n")))))) }
        prepare(crowded,offline=true);content { TaskPage() };both("tasks-crowded-offline")
    }
    @Test fun longDetail() {
        val long=ScheduledTask(machine,JsonObject(task.json+("spec" to JsonObject(task.spec+("message" to JsonPrimitive(longMessage))))))
        prepare(listOf(long));content { ScheduleDetailScreen(machine,long.id) };both("detail-long")
        ui.onNodeWithText("展开全文").performClick();both("detail-long-expanded")
    }
    @Test fun messageEditor() { prepare();content { ScheduleMessageEditor(longMessage,false,"",{},{}) };both("message-editor") }
    @Test fun longNewTask() {
        prepare();var clear:()->Unit={}
        content { val focus=LocalFocusManager.current;clear={focus.clearFocus(force=true)};NewScheduleScreen("") }
        ui.onAllNodes(hasSetTextAction())[0].performTextReplacement(longMessage);ui.onAllNodes(hasSetTextAction())[0].performTextInputSelection(androidx.compose.ui.text.TextRange.Zero);ui.runOnIdle { clear() };ui.onNodeWithText("选择项目").performClick();ui.onNodeWithText("project",useUnmergedTree=true).performClick();both("new-long")
    }
    @Test fun longChatMessage() {
        prepare();content { Column { NavBar("修复登录超时",{});MeBubble(longMessage,caption="定时任务 · 08:00",foldScheduled=true) } };both("chat-long-scheduled")
    }
    @Test fun queuedMessage() {
        prepare();val origin=Origin.newBuilder().setScheduledTaskId(current.id).build()
        val queued=Event.newBuilder().setSeq(9).setTime(stamp(15)).setOrigin(origin).setInputState(InputState.newBuilder().setRequestId("queued-schedule").setText(current.message).setStatus(InputStatus.INPUT_STATUS_QUEUED).setMode(SendMode.SEND_MODE_QUEUE)).build()
        val status=Event.newBuilder().setSeq(10).setTime(stamp(15)).setInputState(InputState.newBuilder().setRequestId("queued-schedule").setStatus(InputStatus.INPUT_STATUS_QUEUED).setMode(SendMode.SEND_MODE_QUEUE)).build()
        remote.showForTest(remote.state.value,mapOf("layout-session" to history()+listOf(queued,status)),live=mapOf("layout-session" to LiveSnapshot.newBuilder().setSession(remote.state.value.sessions["layout-session"]!!.info).addActions(queued).build()))
        content { SessionScreen("layout-session") };ui.onNodeWithText("定时任务 · 15:00 · 这一轮结束后发").assertExists();both("chat-queued-scheduled")
    }
    private fun shell(command: String) {
        android.os.ParcelFileDescriptor.AutoCloseInputStream(InstrumentationRegistry.getInstrumentation().uiAutomation.executeShellCommand(command)).use { it.readBytes() }
    }
    @After fun close() {
        fixtureScope.cancel();remote.close();ScheduleJump.requested.clear()
        shell("wm size 1080x1920");shell("wm density 420")
    }

}
