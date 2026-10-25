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
@Config(sdk = [35], application = Application::class)
class ScheduleDetailEditsTest {
    @get:Rule val ui = createComposeRule()
    private val remote = Remote(Account("http://127.0.0.1:1",ByteArray(32),ByteArray(32),"test-client"),Api.defaultHttp())
    private val scope=CoroutineScope(SupervisorJob()+Dispatchers.Unconfined)
    private val original=ScheduledTask("computer",Json.parseToJsonElement("""{"id":"task","revision":3,"status":"scheduled","source":"client","created_at":"2026-10-24T10:00:00Z","spec":{"title":"打开时的名字","message":"打开时的消息","trigger":{"type":"interval","interval_seconds":3600,"delay_seconds":29},"target":{"type":"new_session","cwd":"/example/project","agent":"claude","mode":"auto"}}}""").jsonObject)
    private var latest=original
    private val requests=mutableListOf<ScheduleRequest>()
    private var conflict=false
    private var backs=0
    private val nav=Nav({backs++},{},{},{},{},{},{},{},{_,_->},{},{},{})
    private lateinit var repository:ScheduleRepository
    private fun prepare() {
        repository=ScheduleRepository(scope,remote.state,send={_,r->
            requests+=r
            if(conflict && r.operation=="update") throw CommandException("aborted","revision",fromDaemon=true)
            if(r.operation=="cancel") latest=ScheduledTask(latest.machine,JsonObject(latest.json+mapOf("status" to JsonPrimitive("cancelled"),"reason" to JsonPrimitive("user_cancelled"))))
            (if(r.operation=="list") buildJsonObject { put("tasks",JsonArray(listOf(latest.json))) } else latest.json).toString().toByteArray()
        })
        remote.javaClass.getDeclaredField("scheduleRepository").apply { isAccessible=true }.set(remote,lazy { repository })
        remote.showForTest(RemoteState(conn=ConnState.ONLINE,loaded=true,machines=mapOf("computer" to MachineView("computer",true,0,MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).build()))))
    }
    private fun render() { ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalRemote provides remote,LocalNav provides nav) { ScheduleDetailScreen("computer","task") } } } }
    @After fun close() { scope.cancel();remote.close() }
    @Test fun renameConflictClosesTheStaleDialogAndRefreshes() {
        prepare();render();ui.onNodeWithText("任务名").performClick()
        ui.onNode(hasSetTextAction()).performTextReplacement("新名字")
        conflict=true;ui.onNodeWithText("保存").performClick()
        ui.onNode(hasSetTextAction()).assertDoesNotExist()
        ui.onNodeWithText("任务刚被修改，已更新为最新内容").assertExists()
        ui.runOnIdle { assertEquals("list",requests.last().operation) }
    }
    @Test fun existingTaskCannotSelectAnotherComputer() {
        prepare();render();ui.onNodeWithText("在哪运行").performClick()
        ui.onNodeWithText("机器").assertHasNoClickAction()
        ui.onNodeWithText("任务设置").assertExists();ui.onNodeWithText("设为常用").assertDoesNotExist();ui.onNodeWithText("移除项目…").assertDoesNotExist()
    }
    @Test fun editorKeepsOpeningRevisionAndFullSpecAfterAnotherClientWrites() {
        prepare();render();ui.onNodeWithText("消息",useUnmergedTree=true).performClick()
        ui.runOnIdle { latest=ScheduledTask(original.machine,JsonObject(original.json+mapOf("revision" to JsonPrimitive(9),"spec" to JsonObject(original.spec+("title" to JsonPrimitive("另一端改名"))))));runBlocking { repository.refresh("computer") } }
        ui.onNode(hasSetTextAction()).performTextReplacement("本端修改");ui.onNodeWithText("保存").performClick()
        ui.runOnIdle {
            val sent=requests.single { it.operation=="update" }
            assertEquals(3L,sent.expectedRevision)
            assertEquals(JsonObject(original.spec+("message" to JsonPrimitive("本端修改"))),Json.parseToJsonElement(sent.specJson.toStringUtf8()))
        }
    }
    @Test fun deletionReturnsToListAndHidesTheCancelledTask() {
        prepare();render();ui.onNodeWithText("删除任务").performScrollTo().performClick()
        ui.onNodeWithText("删除这个任务？").assertExists();ui.onNodeWithText("删除",useUnmergedTree=true).performClick()
        ui.runOnIdle { assertEquals(1,backs);assertTrue(repository.tasks().isEmpty()) }
    }
    @Test fun activeExecutionDisablesDefinitionAndRunButStillAllowsRename() {
        latest=ScheduledTask(original.machine,JsonObject(original.json+("active_run" to buildJsonObject { put("id","active");put("status","running") })))
        prepare();render()
        ui.onNodeWithText("任务名").assertHasClickAction()
        for(label in listOf("什么时候","消息","在哪运行")) ui.onNodeWithText(label).assertIsNotEnabled()
        ui.onNodeWithText("立即运行一次").performScrollTo().assertIsNotEnabled()
    }
    @Test fun pendingManualRequestDisablesAnotherRun() {
        latest=ScheduledTask(original.machine,JsonObject(original.json+("manual_request" to buildJsonObject { put("request_id","queued") })))
        prepare();render();ui.onNodeWithText("立即运行一次").performScrollTo().assertIsNotEnabled()
    }
}
