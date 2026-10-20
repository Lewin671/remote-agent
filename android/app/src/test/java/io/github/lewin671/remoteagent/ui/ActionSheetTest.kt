package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ActionSheetTest {
    @get:Rule val ui = createComposeRule()

    @Test fun menuIsAListWithoutASeparateCancelAndClosesBeforeAnAction() {
        val calls = mutableListOf<String>()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ActionSheet(
                    info = { Column { Text("Codex · foo · MacBook"); Text("~/Code/foo") } },
                    actions = listOf(
                        SheetAction("审批模式", value = "询问") { calls += "mode" },
                        SheetAction("分叉会话", enabled = false, note = "这一轮结束后可以分叉") {},
                        SheetAction("中断本轮", danger = true) {},
                    ),
                    onDismiss = { calls += "close" },
                )
            }
        }
        ui.onNodeWithText("取消").assertDoesNotExist()
        ui.onNodeWithText("询问").assertIsDisplayed()
        ui.onNodeWithText("分叉会话").assertIsNotEnabled()
        ui.onNodeWithText("这一轮结束后可以分叉").assertIsDisplayed()
        ui.onNodeWithText("审批模式").performClick()
        ui.runOnIdle { assertEquals(listOf("close", "mode"), calls) }
    }

    @Test fun anInFlightForkLocksItsRowAndKeepsTheProgress() {
        var clicked = false
        ui.setContent {
            RaTheme(ThemeMode.DARK) {
                ActionSheetRow(SheetAction("正在分叉…", busy = true, note = "等待电脑回复", keepOpen = true) {}) { clicked = true }
            }
        }
        ui.onNodeWithText("正在分叉…").assertIsNotEnabled()
        ui.onNodeWithText("等待电脑回复").assertIsDisplayed()
        ui.runOnIdle { assertFalse(clicked) }
    }
}
