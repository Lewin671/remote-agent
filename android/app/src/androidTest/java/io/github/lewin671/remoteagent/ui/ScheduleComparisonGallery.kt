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
import kotlinx.serialization.json.*
import org.junit.*

/** Synthetic shared input, rendered through the real four screens. No Remote is started. */
class ScheduleComparisonGallery {
    @get:Rule val ui=createAndroidComposeRule<ComponentActivity>()
    private val remote=Remote(Account("http://127.0.0.1:1",ByteArray(32),ByteArray(32),"sample-client"),Api.defaultHttp())
    private val nav=Nav({},{},{},{},{},{},{},{},{_,_->},{},{},{})
    private val fixture by lazy { Json.parseToJsonElement(InstrumentationRegistry.getInstrumentation().context.assets.open("clients.json").bufferedReader().readText()).jsonObject }
    private val machine get()=fixture.text("machine")
    private val sid get()=fixture.text("session")
    private val tasks by lazy { fixture["tasks"]!!.jsonArray.map { ScheduledTask(machine,it.jsonObject) } }
    private val task get()=tasks.first { it.id=="quiet" }
    @Before fun prepare() {
        val meta=MachineMeta.newBuilder().setHostname(fixture.text("machineName")).setProtocolVersion(PROTOCOL_VERSION).setScheduleRevision(1)
            .setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setDefaultMode(ApprovalMode.APPROVAL_MODE_AUTO))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_AUTO)).build()
        val session=Session.newBuilder().setId(sid).setTitle(fixture.text("sessionTitle")).setCwd("/example/project").setAgent(Agent.AGENT_CLAUDE).setMode(ApprovalMode.APPROVAL_MODE_AUTO).setStatus(SessionStatus.SESSION_STATUS_IDLE).build()
        val prefs=ProjectPrefs.newBuilder().setSchema(1).addSettings(ProjectSettings.newBuilder().setPath("/example/project").setMachineId(machine).setAgent(Agent.AGENT_CLAUDE)
            .addAgents(AgentSettings.newBuilder().setAgent(Agent.AGENT_CLAUDE).setMode(ApprovalMode.APPROVAL_MODE_AUTO).setModel("haiku"))).build()
        val state=RemoteState(conn=ConnState.ONLINE,link=Link.ONLINE,loaded=true,synced=true,machines=mapOf(machine to MachineView(machine,true,0,meta)),sessions=mapOf(sid to SessionView(sid,machine,session)),prefs=PrefsObject(1,prefs))
        val events=listOf(Event.newBuilder().setSeq(1).setSessionId(sid).setUserMessage(UserMessage.newBuilder().setText("检查 PR 的回复。")).build(),Event.newBuilder().setSeq(2).setSessionId(sid).setAgentMessage(AgentMessage.newBuilder().setText("有变化时会通知你。")).build())
        remote.showForTest(state.copy(conn=ConnState.OFFLINE),mapOf(sid to events),live=mapOf(sid to LiveSnapshot.newBuilder().setSession(session).build()))
        @Suppress("UNCHECKED_CAST")
        val snapshots=remote.schedules.javaClass.getDeclaredField("_state").apply { isAccessible=true }.get(remote.schedules) as MutableStateFlow<Map<String,ScheduleSnapshot>>
        snapshots.value=mapOf(machine to ScheduleSnapshot(1,tasks.filter(::scheduleVisible)))
        remote.showForTest(state,mapOf(sid to events),live=mapOf(sid to LiveSnapshot.newBuilder().setSession(session).build()))
        ui.activityRule.scenario.onActivity { it.enableEdgeToEdge() }
    }
    private fun content(screen:@Composable ()->Unit) {
        ui.setContent { RaTheme(ThemeMode.LIGHT) {
            SideEffect { WindowCompat.getInsetsController(ui.activity.window,ui.activity.window.decorView).apply { isAppearanceLightStatusBars=true;isAppearanceLightNavigationBars=true } }
            CompositionLocalProvider(LocalRemote provides remote,LocalNav provides nav) { Column(Modifier.fillMaxSize().background(pal.card).safeDrawingPadding()) { screen() } }
        } }
    }
    private fun save(name:String) {
        ui.waitForIdle()
        ui.waitUntil(5000) { androidx.core.view.ViewCompat.getRootWindowInsets(ui.activity.window.decorView)?.isVisible(androidx.core.view.WindowInsetsCompat.Type.ime()) != true }
        val i=InstrumentationRegistry.getInstrumentation()
        val dir=File(i.targetContext.getExternalFilesDir(null),"schedule-layout").apply { mkdirs() }
        File(dir,"$name-light.png").outputStream().use { requireNotNull(i.uiAutomation.takeScreenshot()).compress(Bitmap.CompressFormat.PNG,100,it) }
    }
    @Test fun list() { content { Column { Box(Modifier.weight(1f)) { SchedulesScreen() };TabBar("schedules",0,1) {} } };ui.onAllNodesWithText("检查 PR 回复")[0].assertExists();save("tasks") }
    @Test fun detail() { content { ScheduleDetailScreen(machine,"quiet") };ui.onAllNodesWithText("检查 PR 回复")[0].assertExists();save("detail") }
    @Test fun chat() { content { SessionScreen(sid) };ui.waitUntil(5000) { ui.onAllNodesWithText("定时 ·",substring=true).fetchSemanticsNodes().isNotEmpty() };save("chat-strip") }
    @Test fun newTask() {
        var clear:()->Unit={}
        content { val focus=LocalFocusManager.current;clear={focus.clearFocus(force=true)};NewScheduleScreen("") }
        ui.onAllNodes(hasSetTextAction())[0].performTextReplacement(task.title)
        ui.onAllNodes(hasSetTextAction())[1].performTextReplacement(task.message)
        ui.runOnIdle { clear() }
        ui.onNodeWithText("选择项目").performClick();ui.onNodeWithText("project",useUnmergedTree=true).performClick()
        ui.onNodeWithText("什么时候").performClick();ui.onNodeWithText("每隔").performClick()
        ui.onNodeWithText("小时").performClick();ui.onNodeWithText("完成").performClick()
        ui.runOnIdle { clear() };ui.onAllNodes(hasSetTextAction())[0].assert(hasText(task.title))
        ui.onNodeWithText("每小时").assertExists();save("new")
    }
    @After fun close() { remote.close() }
}
