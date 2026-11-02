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
        val fixture=Json.parseToJsonElement(java.io.File(requireNotNull(System.getProperty("scheduleClients"))).readText()).jsonObject
        val task=ScheduledTask("computer",fixture["tasks"]!!.jsonArray.first { it.jsonObject.text("id")=="quiet" }.jsonObject)
        val state=RemoteState(conn=ConnState.ONLINE,machines=mapOf("computer" to MachineView("computer",true,0,MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).build())))
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { TaskRow(task,state,Instant.parse(fixture.text("now"))) { clicked = true } } }
        ui.onNodeWithText("每小时", substring = true).assertIsDisplayed()
        ui.onNodeWithText("今天 2 次，都没有通知").assertIsDisplayed()
        ui.onNodeWithText(task.title).performClick()
        assertTrue(clicked)
    }
    @Test fun newResultRowUsesBlueDotAndTwoLineBodyWithoutUnreadCount() {
        val run = buildJsonObject { put("id", "r"); put("status", "completed"); put("notified", true); put("started_at", "2026-10-24T08:00:00Z"); put("notification_text", "最近摘要".repeat(100)) }
        val task = ScheduledTask("computer", buildJsonObject {
            put("id", "task"); put("status", "scheduled"); put("runs", JsonArray(listOf(run)))
            put("spec", buildJsonObject { put("title", "很长的任务名".repeat(30)) })
        })
        var fg = androidx.compose.ui.graphics.Color.Unspecified
        ui.setContent {
            val density = androidx.compose.ui.platform.LocalDensity.current
            androidx.compose.runtime.CompositionLocalProvider(androidx.compose.ui.platform.LocalDensity provides androidx.compose.ui.unit.Density(density.density, 2f)) {
                RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.DARK) { fg = pal.fg; TaskRow(task, RemoteState(), hasNew = true) {} }
            }
        }
        ui.onNodeWithText("99+").assertDoesNotExist()
        ui.onNodeWithContentDescription("有新结果").assertExists()
        fun layout(text: String): androidx.compose.ui.text.TextLayoutResult {
            val layouts = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
            ui.onNodeWithText(text, useUnmergedTree = true).performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(layouts) }
            return layouts.single()
        }
        val preview = layout(run.text("notification_text"))
        assertEquals(fg, preview.layoutInput.style.color)
        assertEquals(2, preview.layoutInput.maxLines)
        assertTrue(preview.lineCount <= 2)
        assertEquals(T.bodyB.fontWeight, layout(task.title).layoutInput.style.fontWeight)
        assertEquals(1, layout(task.title).lineCount)
        ui.onNodeWithText(SCHEDULE_OFFLINE_ROW, substring = true, useUnmergedTree = true).assertExists()
    }
    @Test fun visitDotsAppearOnlyForTheUnreadRunsCapturedOnEntry() {
        val runs = (1..2).map { ScheduledRun(buildJsonObject { put("id", "r$it"); put("notified", true); put("status", "completed"); put("started_at", "2026-10-24T08:00:00Z") }) }
        val highlighted = androidx.compose.runtime.mutableStateOf(setOf("r1"))
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { androidx.compose.foundation.layout.Column {
            ScheduleRunGroup(RunGroup("runs", runs, false), Instant.parse("2026-10-24T09:00:00Z"), highlighted.value) {}
        } } }
        ui.onAllNodesWithContentDescription("进入时未读", useUnmergedTree = true).assertCountEquals(1)
        ui.runOnIdle { highlighted.value = emptySet() }
        ui.onAllNodesWithContentDescription("进入时未读", useUnmergedTree = true).assertCountEquals(0)
    }
    @Test fun attentionReasonUsesMutedTextRatherThanTheOrangeStatusColor() {
        val fixture=Json.parseToJsonElement(java.io.File(requireNotNull(System.getProperty("scheduleClients"))).readText()).jsonObject
        val task=ScheduledTask("computer",fixture["tasks"]!!.jsonArray.first { it.jsonObject.text("id")=="attention" }.jsonObject)
        val state=RemoteState(conn=ConnState.ONLINE,machines=mapOf("computer" to MachineView("computer",true,0,MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder().setScheduledTasks(true)).build())))
        var muted=androidx.compose.ui.graphics.Color.Unspecified
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { muted=pal.muted; TaskRow(task,state,Instant.parse(fixture.text("now"))) {} } }
        val layouts=mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        ui.onNodeWithText(schedulePreview(task), useUnmergedTree = true).performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertEquals(muted,layouts.single().layoutInput.style.color)
    }
    @Test fun tabBadgeShowsTheSharedScheduleCountAndNavigates() {
        var selected = ""
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { TabBar("home", 4, 2) { selected = it } } }
        ui.onNodeWithText("2").assertExists()
        ui.onNodeWithText("定时").performClick()
        assertEquals("schedules", selected)
    }
    @Test fun tabBadgeCapsAttentionAndOnlyShowsBlueWhenThereIsNoAttention() {
        val attention = androidx.compose.runtime.mutableIntStateOf(120)
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.DARK) { TabBar("schedules", 0, attention.intValue, true) {} } }
        ui.onNodeWithText("99+").assertExists()
        ui.onNodeWithContentDescription("120 件需要处理").assertExists()
        ui.onNodeWithContentDescription("有新结果").assertDoesNotExist()
        ui.runOnIdle { attention.intValue = 0 }
        ui.onNodeWithText("99+").assertDoesNotExist()
        ui.onNodeWithContentDescription("有新结果").assertExists()
    }
    @Test fun rowExposesReadActionAndSwipeInvokesIt() {
        val task = ScheduledTask("m", Json.parseToJsonElement("""{"id":"t","status":"completed","spec":{"title":"测试任务"},"runs":[{"id":"r","notified":true}]}""").jsonObject)
        var marks = 0
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { TaskRow(task, RemoteState(), hasNew = true, onMark = { marks++ }) {} } }
        val actions = ui.onNodeWithText("测试任务").fetchSemanticsNode().config[androidx.compose.ui.semantics.SemanticsActions.CustomActions]
        ui.runOnIdle { assertEquals("标为已读", actions.single().label); actions.single().action() }
        ui.onNodeWithText("测试任务").performTouchInput { swipeLeft() }
        ui.runOnIdle { assertEquals(2, marks) }
    }
    @Test fun markdownPreviewPreservesWholeBlocksAndExpandsTablesAndCode() {
        val text = "开头一段。\n\n" + (1..10).joinToString("\n\n") { "第 $it 段。" } + "\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfmt.Println(1)\n```"
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { androidx.compose.foundation.layout.Column(androidx.compose.ui.Modifier) { ScheduleResultMarkdown(text) } } }
        ui.onNodeWithText("开头一段。").assertExists()
        ui.onNodeWithText("第 10 段。").assertDoesNotExist()
        ui.onNodeWithText("展开全文").performClick()
        ui.onNodeWithText("第 10 段。").assertExists()
        ui.onNodeWithText("fmt.Println(1)", substring = true).assertExists()
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

    @Test fun conversationTabRetainsItsUncappedBadgeAndOriginalSemantics() {
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { TabBar("home", 120, 120) {} } }
        ui.onNodeWithText("120").assertExists()
        ui.onNodeWithText("99+").assertExists()
        ui.onAllNodesWithContentDescription("120 件需要处理").assertCountEquals(1)
    }
    @Test fun conversationSidebarRetainsPlainOrangeText() {
        var orangeInk = androidx.compose.ui.graphics.Color.Unspecified
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) {
            orangeInk = pal.orangeInk; SidebarTabs("home", 120, 120) {}
        } }
        val layouts = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        ui.onNodeWithText("120", useUnmergedTree = true).performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertEquals(orangeInk, layouts.single().layoutInput.style.color)
        ui.onNodeWithText("99+").assertExists()
    }

    @Test fun firstRunningTaskShowsMutedNoResultWithoutRepeatingRunningInItsPreview() {
        val task = ScheduledTask("computer", buildJsonObject {
            put("id", "first"); put("status", "running")
            put("spec", buildJsonObject { put("title", "首次检查"); put("trigger", buildJsonObject { put("type", "interval"); put("interval_seconds", 3600) }) })
            put("active_run", buildJsonObject { put("id", "active"); put("status", "running"); put("started_at", "2026-10-24T08:00:00Z") })
        })
        var muted = androidx.compose.ui.graphics.Color.Unspecified
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { muted = pal.muted; TaskRow(task, RemoteState()) {} } }
        val layouts = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        ui.onNodeWithText("还没有结果", useUnmergedTree = true).performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertEquals(muted, layouts.single().layoutInput.style.color)
        ui.onAllNodesWithText("正在运行", substring = true, useUnmergedTree = true).assertCountEquals(1)
    }

}
