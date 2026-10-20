package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

class MoreMenuLayoutTest {
    @get:Rule val ui = createComposeRule()

    @Test fun longValuesAndDisabledReasonsFitAtTwoHundredPercentInBothThemes() {
        var dark by mutableStateOf(false)
        val model = "GPT-6-Astra-with-a-very-long-model-name · medium"
        ui.setContent {
            val density = LocalDensity.current
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalDensity provides Density(density.density, 2f)) {
                    Column(Modifier.width(320.dp).testTag("frame").padding(16.dp)) {
                        ActionSheetRow(SheetAction("模型", value = model) {}) {}
                        ActionSheetRow(SheetAction("分叉会话", enabled = false, note = "这一轮结束后可以分叉") {}) {}
                        ActionSheetRow(SheetAction("结束会话…", danger = true) {}) {}
                    }
                }
            }
        }
        for (theme in listOf(false, true)) {
            ui.runOnIdle { dark = theme }
            val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
            val label = ui.onNodeWithText("模型", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val value = ui.onNodeWithText(model, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            assertTrue("value overlaps label", value.left >= label.right)
            assertTrue("value outside frame", value.right <= frame.right)
            ui.onNodeWithText("分叉会话").assertIsNotEnabled()
            ui.onNodeWithText("这一轮结束后可以分叉").assertIsDisplayed()
            ui.onNodeWithText("结束会话…").assertIsDisplayed().assertIsEnabled()
        }
    }
}
