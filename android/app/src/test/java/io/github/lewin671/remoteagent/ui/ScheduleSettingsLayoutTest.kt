package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ScheduleSettingsLayoutTest {
    @get:Rule val ui = createComposeRule()

    @Test fun valuesShareATrailingEdgeAndLongValuesEllipsizeAtBothFontSizes() {
        var fontScale by mutableStateOf(1f)
        val long = "Claude Code · 自动 · claude-haiku-5-5-with-an-extra-long-model-name"
        ui.setContent {
            val density = LocalDensity.current
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale)) {
                    Column(Modifier.width(360.dp)) {
                        ScheduleSetting("什么时候", "每天 09:36") {}
                        ScheduleSetting("在哪运行", "project · Claude Code") {}
                        ScheduleSetting("权限与模型", long) {}
                    }
                }
            }
        }
        for (scale in listOf(1f, 2f)) {
            ui.runOnIdle { fontScale = scale }
            val values = listOf("每天 09:36", "project · Claude Code", long)
            val edges = values.map { ui.onNodeWithText(it, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot.right }
            assertEquals(edges[0], edges[1], 1f)
            assertEquals(edges[0], edges[2], 1f)
            for (value in values) {
                val results = mutableListOf<TextLayoutResult>()
                ui.onNodeWithText(value, useUnmergedTree = true).performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(results) }
                assertEquals(1, results.single().lineCount)
                if (value == long) assertTrue(results.single().isLineEllipsized(0))
            }
        }
    }

    @Test fun cliIsACompactSettingThatSwitchesAndDisappearsForOneAgent() {
        var agents by mutableStateOf(listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE))
        var selected by mutableStateOf(Agent.AGENT_CLAUDE)
        ui.setContent { RaTheme(ThemeMode.LIGHT) {
            Column(Modifier.width(360.dp)) { ScheduleAgentSetting(agents, selected, true) { selected = it } }
        } }
        ui.onNodeWithText("CLI").assertIsDisplayed()
        ui.onNodeWithText("Claude Code").assertIsSelected()
        ui.onNodeWithText("Codex").performClick().assertIsSelected()
        assertEquals(Agent.AGENT_CODEX, selected)
        val label = ui.onNodeWithText("CLI").fetchSemanticsNode().boundsInRoot
        val codex = ui.onNodeWithText("Codex").fetchSemanticsNode().boundsInRoot
        assertTrue(codex.left > label.right)
        ui.runOnIdle { agents = listOf(Agent.AGENT_CLAUDE) }
        ui.onNodeWithText("CLI").assertDoesNotExist()
        ui.onNodeWithText("Claude Code").assertDoesNotExist()
    }

    @Test fun frozenCliSettingCannotChangeTheAgent() {
        var selected = Agent.AGENT_CLAUDE
        ui.setContent { RaTheme(ThemeMode.LIGHT) {
            ScheduleAgentSetting(listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE), selected, false) { selected = it }
        } }
        ui.onNodeWithText("Codex").assertIsNotEnabled().performClick()
        assertEquals(Agent.AGENT_CLAUDE, selected)
    }
}
