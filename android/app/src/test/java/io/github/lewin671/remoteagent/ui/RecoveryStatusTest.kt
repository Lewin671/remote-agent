package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.proto.EmergencyRecoveryView
import io.github.lewin671.remoteagent.core.proto.SecurityRecoveryPhase
import io.github.lewin671.remoteagent.data.ThemeMode
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
        var rec by mutableStateOf(
            EmergencyRecoveryView.newBuilder()
                .setPhase(SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_SUBMITTED_WAITING)
                .setCanSubmitOrRefresh(true)
                .setCanComplete(false)
                .build(),
        )
        var error by mutableStateOf("")
        var step = 0
        var refreshes = 0
        var restarts = 0
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column {
                    RecoveryStatus(
                        rec = rec,
                        error = error,
                        onRefresh = {
                            step++
                            error = ""
                            refreshes++
                            rec = when (step) {
                                1 -> EmergencyRecoveryView.newBuilder()
                                    .setPhase(SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_READY_TO_COMPLETE)
                                    .setCanSubmitOrRefresh(true)
                                    .setCanComplete(true)
                                    .build()
                                else -> EmergencyRecoveryView.newBuilder()
                                    .setPhase(SecurityRecoveryPhase.SECURITY_RECOVERY_PHASE_REJECTED)
                                    .setCanSubmitOrRefresh(false)
                                    .setCanComplete(false)
                                    .setCanDiscard(true)
                                    .build()
                            }
                        },
                        onComplete = { error = "恢复尚不能完成" },
                        onRestart = { restarts++ },
                    )
                }
            }
        }
        ui.onNodeWithText("还在 48 小时等待期内。").assertIsDisplayed()
        ui.onNodeWithText("完成恢复").assertIsNotEnabled()
        ui.onNodeWithText("查看状态").assertIsEnabled().performClick()
        ui.onNodeWithText("已经可以完成恢复。").assertIsDisplayed()
        ui.onNodeWithText("完成恢复").assertIsEnabled().performClick()
        ui.onNodeWithText("恢复尚不能完成").assertIsDisplayed()
        ui.onNodeWithText("查看状态").assertIsEnabled().performClick()
        ui.onNodeWithText("这次恢复被原管理端否决了。").assertIsDisplayed()
        ui.onNodeWithText("完成恢复").assertDoesNotExist()
        ui.onNodeWithText("放弃这次恢复，输入新恢复码").assertIsEnabled().performClick()
        ui.runOnIdle {
            assertEquals(2, refreshes)
            assertEquals(1, restarts)
        }
    }
}
