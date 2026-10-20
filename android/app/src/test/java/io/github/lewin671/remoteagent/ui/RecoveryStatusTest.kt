package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse.Phase
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class RecoveryStatusTest {
    @get:Rule val ui = createComposeRule()

    @Test
    fun statusCanBeCheckedAgainAfterWaitingAndAFailedCompletionToDiscoverAVeto() {
        var phase by mutableStateOf<Phase?>(null)
        var error by mutableStateOf("")
        var next = Phase.PHASE_PENDING
        var refreshes = 0
        var restarts = 0
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column {
                    RecoveryStatus(
                        phase,
                        false,
                        error,
                        onRefresh = {
                            phase = next
                            error = ""
                            refreshes++
                        },
                        onComplete = { error = "恢复尚不能完成" },
                        onRestart = { restarts++ },
                    )
                }
            }
        }
        ui.onNodeWithText("查看状态").performClick()
        ui.onNodeWithText("还在 48 小时等待期内。").assertIsDisplayed()
        ui.onNodeWithText("查看状态").assertIsDisplayed().performClick()
        ui.onNodeWithText("完成恢复").performClick()
        ui.onNodeWithText("恢复尚不能完成").assertIsDisplayed()
        next = Phase.PHASE_REJECTED
        ui.onNodeWithText("查看状态").performClick()
        ui.onNodeWithText("这次恢复被原管理端否决了。").assertIsDisplayed()
        ui.onNodeWithText("完成恢复").assertDoesNotExist()
        ui.onNodeWithText("放弃这次恢复，输入新恢复码").performClick()
        ui.runOnIdle {
            assertEquals(3, refreshes)
            assertEquals(1, restarts)
        }
    }
}
