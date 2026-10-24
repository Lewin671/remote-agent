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
        ui.onNodeWithText("今天 2 次都没有通知").assertIsDisplayed()
        ui.onNodeWithText(task.title).performClick()
        assertTrue(clicked)
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

}
