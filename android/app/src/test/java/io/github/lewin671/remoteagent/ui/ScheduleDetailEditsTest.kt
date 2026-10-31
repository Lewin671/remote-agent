package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
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
        remote.showForTest(RemoteState(conn=ConnState.ONLINE,loaded=true,machines=mapOf("computer" to MachineView("computer",true,0,MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX)).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).build()))))
    }
    private fun render() { ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalRemote provides remote,LocalNav provides nav) { ScheduleDetailScreen("computer","task") } } } }
    @After fun close() { scope.cancel();remote.close() }
    @Test fun renameConflictClosesTheStaleDialogAndRefreshes() {
        prepare();render();ui.onNodeWithText("任务名").performScrollTo().performClick()
        ui.onNode(hasSetTextAction()).performTextReplacement("新名字")
        conflict=true;ui.onNodeWithText("保存").performClick()
        ui.onNode(hasSetTextAction()).assertDoesNotExist()
        ui.onNodeWithText("任务刚被修改，已更新为最新内容").assertExists()
        ui.runOnIdle { assertEquals("list",requests.last().operation) }
    }
    @Test fun existingTaskCannotSelectAnotherComputer() {
        prepare();render();ui.onNodeWithText("在哪运行").performScrollTo().performClick()
        ui.onNodeWithText("机器").assertHasNoClickAction()
        ui.onNodeWithText("任务设置").assertExists();ui.onNodeWithText("设为常用").assertDoesNotExist();ui.onNodeWithText("移除项目…").assertDoesNotExist()
    }
    @Test fun targetEditorSavesTheSelectedCliAndItsPermission() {
        prepare();render();ui.onNodeWithText("在哪运行").performScrollTo().performClick()
        ui.onNodeWithText("Codex",useUnmergedTree=true).performClick()
        ui.onAllNodesWithText("权限").onLast().performClick()
        ui.onNodeWithText("完全放行",useUnmergedTree=true).performClick()
        ui.onNodeWithText("完成").performClick()
        ui.runOnIdle {
            assertEquals(1,requests.count { it.operation=="update" })
            val target=Json.parseToJsonElement(requests.single { it.operation=="update" }.specJson.toStringUtf8()).jsonObject["target"]!!.jsonObject
            assertEquals("codex",target.text("agent"));assertEquals("full",target.text("mode"))
        }
    }
    @Test fun editorKeepsOpeningRevisionAndFullSpecAfterAnotherClientWrites() {
        prepare();render();ui.onNodeWithText("编辑",useUnmergedTree=true).performClick()
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
    private fun cancelled(trigger: JsonObject = original.trigger) {
        latest=ScheduledTask(original.machine,JsonObject(original.json+mapOf("status" to JsonPrimitive("cancelled"),"reason" to JsonPrimitive("message_cancelled"),"spec" to JsonObject(original.spec+("trigger" to trigger)))))
    }
    @Test fun systemCancelledRecurringTaskReschedulesWithDefinitionAndCanBeDeleted() {
        cancelled();prepare();render()
        ui.onNodeWithText("恢复").performScrollTo().performClick()
        ui.runOnIdle {
            assertEquals(1,requests.count { it.operation=="update" })
            val sent=requests.single { it.operation=="update" }
            assertEquals(latest.spec,Json.parseToJsonElement(sent.specJson.toStringUtf8()));assertEquals(3L,sent.expectedRevision)
            assertFalse(requests.any { it.operation=="resume" })
        }
        ui.onNodeWithText("删除任务").performScrollTo().assertIsEnabled().performClick()
        ui.onNodeWithText("删除",useUnmergedTree=true).performClick()
        ui.runOnIdle { assertEquals(1,requests.count { it.operation=="cancel" });assertEquals(1,backs) }
    }
    @Test fun systemCancelledOneShotRequiresNewTimingBeforeRescheduling() {
        cancelled(buildJsonObject { put("type","at");put("at","2026-10-01T10:00:00Z") });prepare();render()
        ui.onNodeWithText("恢复").performScrollTo().performClick()
        ui.onNodeWithText("完成").assertExists()
        ui.runOnIdle { assertFalse(requests.any { it.operation=="resume" || it.operation=="update" }) }
    }
    @Test fun activeExecutionDisablesDefinitionAndRunButStillAllowsRename() {
        latest=ScheduledTask(original.machine,JsonObject(original.json+("active_run" to buildJsonObject { put("id","active");put("status","running") })))
        prepare();render()
        ui.onNodeWithText("任务名").performScrollTo().assertHasClickAction()
        for(label in listOf("什么时候","编辑","在哪运行")) ui.onNodeWithText(label).performScrollTo().assertIsNotEnabled()
        ui.onNodeWithText("立即运行一次").performScrollTo().assertIsNotEnabled()
        ui.onNodeWithText("完成后通知").performScrollTo().assertIsEnabled().performClick()
        ui.runOnIdle {
            val sent=requests.single { it.operation=="update" }
            assertEquals("agent_only",Json.parseToJsonElement(sent.specJson.toStringUtf8()).jsonObject.text("notify"))
            assertEquals(3L,sent.expectedRevision)
        }
    }
    @Test fun pendingManualRequestDisablesAnotherRun() {
        latest=ScheduledTask(original.machine,JsonObject(original.json+("manual_request" to buildJsonObject { put("request_id","queued") })))
        prepare();render();ui.onNodeWithText("立即运行一次").performScrollTo().assertIsNotEnabled()
    }
    @Test fun endedTasksCollapseAndRememberExpansionOnThisDevice() {
        latest=ScheduledTask(original.machine,JsonObject(original.json+("status" to JsonPrimitive("completed"))))
        prepare()
        val context=androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>()
        context.getSharedPreferences("schedule-display",0).edit().clear().commit()
        var showing by androidx.compose.runtime.mutableStateOf(true)
        var newTasks=0
        ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalRemote provides remote,LocalNav provides Nav({},{},{},{},{},{},{},{},{_,_->},{},{},{},newTask={newTasks++})) { if(showing) SchedulesScreen() } } }
        ui.onNodeWithText(original.title).assertDoesNotExist()
        ui.onNodeWithContentDescription("新任务").performClick()
        ui.runOnIdle { assertEquals(1,newTasks) }
        ui.onNodeWithText("已结束").performClick()
        ui.onNodeWithText(original.title).assertExists()
        ui.runOnIdle { showing=false };ui.waitForIdle();ui.runOnIdle { showing=true }
        ui.onNodeWithText(original.title).assertExists()
        context.getSharedPreferences("schedule-display",0).edit().clear().commit()
    }
    @Test fun detailLimitsHistoryPreviewAndExpandsAllRecords() {
        val runs=buildJsonArray { (1..7).forEach { n -> add(buildJsonObject { put("id","run-$n");put("started_at","2026-10-24T10:0$n:00Z");put("status","completed");put("notified",true);put("notification_text","记录 $n") }) } }
        latest=ScheduledTask(original.machine,JsonObject(original.json+("runs" to runs)))
        prepare();render()
        ui.onNodeWithText("记录 1").assertDoesNotExist()
        ui.onNodeWithText("查看全部 7 次").performScrollTo().performClick()
        ui.onNodeWithText("记录 1").assertExists()
    }
    @Test fun offlineDetailDisablesEditsAndExplainsWhenTheyReturn() {
        prepare();render()
        ui.runOnIdle { val state=remote.state.value;remote.showForTest(state.copy(conn=ConnState.OFFLINE)) }
        ui.onAllNodesWithText(SCHEDULE_OFFLINE).assertCountEquals(1)
        for (label in listOf("编辑","什么时候","在哪运行","任务名","立即运行一次","删除任务")) ui.onNodeWithText(label).performScrollTo().assertIsNotEnabled()
        ui.onNodeWithText("连上后才能修改、运行或删除。",substring=true).performScrollTo().assertExists()
    }

    @Test fun missingConversationKeepsRecoveryOnlyInTheActionGroup() {
        latest=ScheduledTask(original.machine,JsonObject(original.json+mapOf("status" to JsonPrimitive("failed"),"reason" to JsonPrimitive("execution_session_missing"),"spec" to JsonObject(original.spec+("target" to buildJsonObject { put("type","resume_session");put("session","gone") })))))
        prepare();render()
        ui.onAllNodesWithText("恢复").assertCountEquals(1)
        ui.onNodeWithText("恢复").performScrollTo().performClick()
        ui.runOnIdle { assertEquals(1,requests.count { it.operation=="resume" }) }
    }

}
