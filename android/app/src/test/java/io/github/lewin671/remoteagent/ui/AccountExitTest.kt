package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class AccountExitTest {
    @get:Rule val ui = createComposeRule()

    @Test
    fun soleAuthorityMustConfirmKeyLossAndNewPendingRequiresAnotherConfirmation() {
        var pending = false
        var exits = 0
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column { AccountExitAction(RemoteState(isAuthority = true), { pending }, { exits++ }) }
            }
        }
        ui.onNodeWithText("退出并删除本机密钥").performClick()
        ui.onNodeWithText(authorityExitAdvice(true, false)).assertExists()
        ui.runOnIdle { pending = true }
        ui.onNodeWithText("退出").performClick()
        ui.onNodeWithText(authorityExitAdvice(true, true)).assertExists()
        ui.runOnIdle { assertEquals(0, exits) }
        ui.onNodeWithText("退出").performClick()
        ui.runOnIdle { assertEquals(1, exits) }
    }

    @Test
    fun failedPendingReadDoesNotOfferAnOrdinaryLogout() {
        var exits = 0
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column {
                    AccountExitAction(RemoteState(), { throw IllegalStateException("read failed") }, { exits++ })
                }
            }
        }
        ui.onNodeWithText("退出并删除本机密钥").performClick()
        ui.onNodeWithText("退出前没能读取安全操作状态：read failed").assertExists()
        ui.onNodeWithText("退出").assertDoesNotExist()
        ui.runOnIdle { assertEquals(0, exits) }
    }

    @Test
    fun ordinaryMemberCanCancelWithoutDeletingKeys() {
        var exits = 0
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column { AccountExitAction(RemoteState(), { false }, { exits++ }) }
            }
        }
        ui.onNodeWithText("退出并删除本机密钥").performClick()
        ui.onNodeWithText(authorityExitAdvice(false, false)).assertExists()
        ui.onNodeWithText("取消").performClick()
        ui.runOnIdle { assertEquals(0, exits) }
    }
}
