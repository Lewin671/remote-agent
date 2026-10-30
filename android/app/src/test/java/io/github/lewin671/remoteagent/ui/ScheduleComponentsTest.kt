package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import kotlinx.serialization.json.*
import java.time.Instant
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ScheduleComponentsTest {
    @get:Rule val ui = createComposeRule()
    @Test fun taskRowOpensItsDetailAndShowsAllThreeLines() {
        var clicked = false
        val fixture=Json.parseToJsonElement(java.io.File(System.getProperty("scheduleClients")).readText()).jsonObject
        val task=ScheduledTask("computer",fixture["tasks"]!!.jsonArray.first { it.jsonObject.text("id")=="quiet" }.jsonObject)
        val state=RemoteState(conn=ConnState.ONLINE,machines=mapOf("computer" to MachineView("computer",true,0,MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).build())))
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { TaskRow(task,state,Instant.parse(fixture.text("now"))) { clicked = true } } }
        ui.onNodeWithText("每小时 · project · Claude Code").assertIsDisplayed()
        ui.onNodeWithText("今天 2 次，都没有通知").assertIsDisplayed()
        ui.onNodeWithText(task.title).performClick()
        assertTrue(clicked)
    }
    @Test fun attentionReasonUsesMutedTextRatherThanTheOrangeStatusColor() {
        val fixture=Json.parseToJsonElement(java.io.File(System.getProperty("scheduleClients")).readText()).jsonObject
        val task=ScheduledTask("computer",fixture["tasks"]!!.jsonArray.first { it.jsonObject.text("id")=="attention" }.jsonObject)
        val state=RemoteState(conn=ConnState.ONLINE,machines=mapOf("computer" to MachineView("computer",true,0,MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).build())))
        var muted=androidx.compose.ui.graphics.Color.Unspecified
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { muted=pal.muted; TaskRow(task,state,Instant.parse(fixture.text("now"))) {} } }
        val layouts=mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        ui.onNodeWithText(scheduleReason(task.reason), useUnmergedTree = true).performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertEquals(muted,layouts.single().layoutInput.style.color)
    }
    @Test fun tabBadgeCountsOnlyAttentionTasksAndNavigates() {
        var selected = ""
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { TabBar("home", 4, 2) { selected = it } } }
        ui.onNodeWithText("2").assertExists()
        ui.onNodeWithText("定时").performClick()
        assertEquals("schedules", selected)
    }
    @Test fun quietTurnsExpandToTheirOriginalText() {
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ChatItemView(ChatItem.QuietScheduled("quiet", listOf(ChatItem.Agent("e1", "Original result")), listOf(1_800_000_000_000), 3), "Codex") } }
        ui.onNodeWithText("Original result").assertDoesNotExist()
        ui.onNodeWithText("定时检查", substring = true).performClick()
        ui.onNodeWithText("Original result").assertExists()
    }
    @Test fun cutoffPickerReformatsTheDateWhenTimezoneChanges() {
        val original=Json.parseToJsonElement("""{"type":"calendar","time":"08:00","timezone":"Asia/Shanghai","until":"2027-10-30T00:15:29Z"}""").jsonObject
        var saved:JsonObject?=null
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleTimeSheet(original,{saved=it},{}) } }
        ui.onNodeWithText("2027-10-30",useUnmergedTree=true).assertExists()
        ui.onNode(hasSetTextAction()).performTextReplacement("America/Los_Angeles")
        ui.onNodeWithText("2027-10-29",useUnmergedTree=true).assertExists()
        ui.onNodeWithText("完成").performScrollTo().performClick()
        assertNotNull("完成应提交转换后的截止时间",saved)
        assertEquals(original["until"],saved?.get("until"))
    }
    @Test fun completingAnUnchangedTimeSheetPreservesAgentTriggerAndExactDeadline() {
        val original = kotlinx.serialization.json.Json.parseToJsonElement("""{"type":"after_turn","delay_seconds":90,"repeat":true,"until":"2027-01-01T18:00:00Z"}""") as kotlinx.serialization.json.JsonObject
        var saved: kotlinx.serialization.json.JsonObject? = null
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleTimeSheet(original, { saved = it }, {}) } }
        ui.onNodeWithText("完成").performClick()
        assertEquals(original, saved)
    }

    @Test fun turnDelayIsPreservedUntilATimeTypeIsExplicitlySelected() {
        val original = kotlinx.serialization.json.Json.parseToJsonElement("""{"type":"after_turn","delay_seconds":90,"repeat":true}""") as kotlinx.serialization.json.JsonObject
        var saved: kotlinx.serialization.json.JsonObject? = null
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleTimeSheet(original, { saved = it }, {}) } }
        ui.onNodeWithText("当前是", substring = true).assertExists()
        ui.onNodeWithText("每天", useUnmergedTree = true).performClick()
        ui.onNodeWithText("完成").performClick()
        assertEquals("calendar", (saved!!["type"] as kotlinx.serialization.json.JsonPrimitive).content)
    }

    @Test fun renameSelectsTheNameAndSameNameSaveStaysLocal() {
        val task = io.github.lewin671.remoteagent.core.ScheduledTask("m", kotlinx.serialization.json.Json.parseToJsonElement("""{"id":"t","status":"scheduled","spec":{"title":"项目早报","message":"检查项目。"}}""") as kotlinx.serialization.json.JsonObject)
        var closed = false
        var saves = 0
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleRenameDialog(task, { closed = true }) { saves++ } } }
        ui.onNode(hasSetTextAction()).assert(SemanticsMatcher.expectValue(androidx.compose.ui.semantics.SemanticsProperties.TextSelectionRange, androidx.compose.ui.text.TextRange(0,task.title.length)))
        ui.onNodeWithText("留空保存会用消息的第一句").assertExists()
        ui.onNodeWithText("保存").performClick()
        ui.runOnIdle { assertTrue(closed); assertEquals(0,saves) }
    }
    @Test fun uncertainRenameCannotBeSubmittedAgain() {
        val task = io.github.lewin671.remoteagent.core.ScheduledTask("m", kotlinx.serialization.json.Json.parseToJsonElement("""{"id":"t","status":"scheduled","spec":{"title":"项目早报","message":"检查项目。"}}""") as kotlinx.serialization.json.JsonObject)
        var saves = 0
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleRenameDialog(task, {}) { saves++; throw io.github.lewin671.remoteagent.core.CommandException(io.github.lewin671.remoteagent.core.CommandException.UNKNOWN,"结果未确认") } } }
        ui.onNode(hasSetTextAction()).performTextReplacement("新任务名")
        ui.onNodeWithText("保存").performClick()
        ui.onNodeWithText("保存").assertIsNotEnabled().performClick()
        ui.runOnIdle { assertEquals(1,saves) }
    }

    @Test fun weeklySelectionIsVisibleAndCannotBeEmptyOnCompletion() {
        var saved: JsonObject? = null
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleTimeSheet(buildJsonObject { put("type","calendar");put("time","08:00");put("timezone","UTC");put("weekdays",buildJsonArray { add(1) }) }, {saved=it}, {}) } }
        ui.onNodeWithText("一").assertIsToggleable().assertIsOn().performClick()
        ui.onNodeWithText("完成").assertIsNotEnabled()
        ui.onNodeWithText("五").performClick().assertIsOn()
        ui.onNodeWithText("完成").performClick()
        assertEquals(listOf(5), saved!!["weekdays"]!!.jsonArray.map { it.jsonPrimitive.int })
    }
    @Test fun intervalHasNoTimezoneAndSwitchingTypesKeepsSelections() {
        var saved: JsonObject? = null
        val initial=buildJsonObject { put("type","calendar");put("time","13:24");put("timezone","UTC");put("max_runs",8) }
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleTimeSheet(initial,{saved=it},{}) } }
        ui.onNodeWithText("每隔").performClick()
        ui.onNodeWithText("时区").assertDoesNotExist()
        ui.onNodeWithText(SCHEDULE_INTERVAL_HINT).assertExists()
        ui.onNodeWithText("一次").performClick();ui.onNodeWithText("每周").performClick();ui.onNodeWithText("每天").performClick()
        ui.onNodeWithText("13:24",useUnmergedTree=true).assertExists()
        ui.onNodeWithText("完成").performClick()
        assertEquals(8L,saved!!.long("max_runs"))
    }
    @Test fun descriptionFoldsInPlaceAndEditorConfirmsDiscard() {
        val long=(1..12).joinToString("\n") { "第 $it 行，检查项目并报告结果。" }
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleDescription(long) } }
        ui.onNodeWithText("展开全文").performClick();ui.onNodeWithText("收起").assertExists()
    }
    @Test fun editorRejectsEmptyOrOversizeAndCancelConfirmsChanges() {
        var closed=false
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleMessageEditor("原文",false,"",{closed=true},{}) } }
        ui.onNode(hasSetTextAction()).performTextReplacement("")
        ui.onNodeWithText("保存").assertIsNotEnabled()
        ui.onNode(hasSetTextAction()).performTextReplacement("中".repeat(10923))
        ui.onNodeWithText("太长了，最多 32 KB").assertExists();ui.onNodeWithText("保存").assertIsNotEnabled()
        ui.onNodeWithText("取消").performClick();ui.onNodeWithText("放弃修改？").assertExists()
        assertFalse(closed)
        ui.onNodeWithText("放弃",useUnmergedTree=true).performClick();assertTrue(closed)
    }
    @Test fun scheduledChatFoldsOnlyScheduledMessages() {
        val long=(1..12).joinToString("\n") { "描述第 $it 行" }
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { MeBubble(long,caption="定时任务 · 08:00",foldScheduled=true) } }
        ui.onNodeWithText("定时任务 · 08:00 · 展开全文").performClick()
        ui.onNodeWithText("定时任务 · 08:00 · 收起").assertExists()
    }

    @Test fun offlineEditorCanCancelWhileSavingIsDisabled() {
        var closed=false
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { ScheduleMessageEditor("原文",false,"",{closed=true},{},writable=false) } }
        ui.onNodeWithText("保存").assertIsNotEnabled()
        ui.onNodeWithText("取消").assertIsEnabled().performClick()
        assertTrue(closed)
    }

}
