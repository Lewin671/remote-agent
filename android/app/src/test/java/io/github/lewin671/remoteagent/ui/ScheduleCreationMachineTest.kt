package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.serialization.json.*
import org.junit.*
import org.junit.Assert.*
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk=[35],application=Application::class)
class ScheduleCreationMachineTest {
    @get:Rule val ui=createComposeRule()
    private val remote=Remote(Account("http://127.0.0.1:1",ByteArray(32),ByteArray(32),"test-client"),Api.defaultHttp())
    private val scope=CoroutineScope(SupervisorJob()+Dispatchers.Unconfined)
    private val path="/example/project"
    private val initial=NewConfig("old",path,Agent.AGENT_CLAUDE,"old-model","high",Mode.FULL)
    private val nav=Nav({},{},{},{},{},{},{},{},{_,_->},{},{},{})
    private val creates=mutableListOf<Pair<String,ScheduleRequest>>()
    private fun state(both:Boolean=false):RemoteState {
        fun machine(id:String,name:String,agents:List<Agent>)=MachineView(id,true,0,MachineMeta.newBuilder().setHostname(name).setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).addAllAgents(agents.map { AgentInfo.newBuilder().setAgent(it).setDefaultMode(if(id=="old")ApprovalMode.APPROVAL_MODE_FULL_ACCESS else ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG).build() }).build())
        val prefs=ProjectPrefs.newBuilder().addSettings(ProjectSettings.newBuilder().setPath(path).setMachineId("old").setAgent(Agent.AGENT_CLAUDE).addAgents(initial.agentSettings())).build()
        return RemoteState(conn=ConnState.ONLINE,loaded=true,synced=true,prefs=PrefsObject(1,prefs),machines=linkedMapOf("old" to machine("old","旧电脑",listOf(Agent.AGENT_CLAUDE)),"new" to machine("new","新电脑",if(both)listOf(Agent.AGENT_CODEX,Agent.AGENT_CLAUDE)else listOf(Agent.AGENT_CODEX))),sessions=listOf("old","new").associateWith { id->SessionView(id,id,Session.newBuilder().setId(id).setCwd(path).setAgent(if(id=="old")Agent.AGENT_CLAUDE else Agent.AGENT_CODEX).build()) })
    }
    private fun prepare(state:RemoteState) {
        val repository=ScheduleRepository(scope,remote.state,send={machine,r->
            if(r.operation=="create") {creates+=machine to r;("""{"id":"created","revision":1,"status":"scheduled","spec":${r.specJson.toStringUtf8()}}""").toByteArray()}
            else "{\"tasks\":[]}".toByteArray()
        })
        remote.javaClass.getDeclaredField("scheduleRepository").apply{isAccessible=true}.set(remote,lazy{repository})
        remote.showForTest(state)
    }
    @After fun close(){scope.cancel();remote.close()}
    @Test fun changingComputerWithoutEditingSettingsCreatesWithDisplayedDefaults() {
        prepare(state())
        ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalRemote provides remote,LocalNav provides nav) { ScheduleEditor(""){} } } }
        ui.onNodeWithText("选择项目",useUnmergedTree=true).performClick()
        ui.onNodeWithText("project",useUnmergedTree=true).performClick()
        ui.onAllNodes(hasSetTextAction()).onFirst().performTextReplacement("检查回复")
        ui.onNodeWithText("机器和模型",useUnmergedTree=true).performScrollTo().performClick()
        ui.onNodeWithText("机器",useUnmergedTree=true).performClick();ui.onNodeWithText("新电脑",useUnmergedTree=true).performClick()
        ui.onNodeWithText("默认模型",useUnmergedTree=true).assertExists();ui.onNodeWithText("询问",useUnmergedTree=true).assertExists()
        ui.onNodeWithText("完成").performScrollTo().performClick()
        ui.onNodeWithText("创建").performScrollTo().performClick()
        ui.runOnIdle {
            assertEquals(1,creates.size);val (machine,request)=creates.single()
            val target=Json.parseToJsonElement(request.specJson.toStringUtf8()).jsonObject["target"]!!.jsonObject
            assertEquals("new",machine);assertEquals("codex",target.text("agent"));assertEquals("",target.text("model"));assertEquals("",target.text("effort"));assertEquals("ask",target.text("mode"))
        }
    }
    @Test fun completingAfterSwitchAndCliSelectionReturnsExactlyTheDisplayedConfig() {
        val state=state(both=true);prepare(state);var result:PrefsOp.SetSettings?=null
        ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalRemote provides remote,LocalNav provides nav) { NewSettingsSheet(state,path,"project","old",Agent.AGENT_CLAUDE,onDone={result=it},onClose={},initial=initial,scheduling=true) } } }
        ui.onNodeWithText("机器",useUnmergedTree=true).performClick();ui.onNodeWithText("新电脑",useUnmergedTree=true).performClick()
        ui.onNodeWithText("Codex",useUnmergedTree=true).performClick();ui.onNodeWithText("默认模型",useUnmergedTree=true).assertExists()
        ui.onNodeWithText("完成").performScrollTo().performClick()
        ui.runOnIdle {
            assertNotNull(result);assertEquals("new",result!!.machineId);assertEquals(Agent.AGENT_CODEX,result!!.agent)
            assertEquals(1,result!!.sets.size)
            assertEquals(NewConfig("new",path,Agent.AGENT_CODEX),agentConfig("new",path,result!!.sets.single()))
        }
    }
}
