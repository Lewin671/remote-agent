package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.Tone
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
        ui.setContent { RaTheme(io.github.lewin671.remoteagent.data.ThemeMode.LIGHT) { TaskRow("Task", "每小时 · 新开对话", "今天 3 次都没有通知", "明天 8:00", Tone.IDLE) { clicked = true } } }
        ui.onNodeWithText("每小时 · 新开对话").assertIsDisplayed()
        ui.onNodeWithText("今天 3 次都没有通知").assertIsDisplayed()
        ui.onNodeWithText("Task").performClick()
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

}
