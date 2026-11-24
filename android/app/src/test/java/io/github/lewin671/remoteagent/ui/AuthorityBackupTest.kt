package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.DeviceMeta
import com.google.protobuf.ByteString
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.RobolectricTestRunner

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class AuthorityBackupTest {
    @get:Rule val ui = createComposeRule()
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})

    private fun show(content: @Composable () -> Unit) {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav) { content() }
            }
        }
    }

    @Test
    fun handoffOfferRequiresBackupConfirmationAndFailureRetainsTheCode() {
        var attempts = 0
        var offerPublished by mutableStateOf(false)
        show {
                HandoffPreparationContent(
                    hasHandoffOffer = offerPublished,
                    prepareCode = { "synthetic-offline-code" },
                ) {
                    attempts++
                    if (attempts == 1) throw IllegalStateException("save failed")
                    offerPublished = true
                }
        }
        ui.onNodeWithText("我已保存，通知管理端").assertDoesNotExist()
        ui.onNodeWithText("生成新的恢复码").performClick()
        ui.runOnIdle { assertEquals(0, attempts) }
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        ui.onNodeWithText("我已保存，通知管理端").performScrollTo().performClick()
        ui.onNodeWithText("save failed").assertExists()
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        ui.onNodeWithText("恢复码已确认保存，等待当前管理端批准；可以重新打开此页查看。").assertDoesNotExist()
        ui.onNodeWithText("我已保存，通知管理端").performScrollTo().performClick()
        ui.onNodeWithText("恢复码已确认保存，等待当前管理端批准；可以重新打开此页查看。").assertExists()
        ui.runOnIdle { assertEquals(2, attempts) }
    }

    @Test
    fun rotationRequiresBackupConfirmationAndFailedPublicationDoesNotNavigateAway() {
        var attempts = 0
        var backs = 0
        show {
                RotateRecoveryContent(io.github.lewin671.remoteagent.core.proto.SyncStateView.newBuilder().setIsManager(true).build(), { "synthetic-offline-code" }, {
                    attempts++
                    if (attempts == 1) throw IllegalStateException("save failed")
                }, { backs++ })
        }
        ui.onNodeWithText("我已保存，发布换码").assertDoesNotExist()
        ui.onNodeWithText("生成新的恢复码").performClick()
        ui.runOnIdle { assertEquals(0, attempts) }
        ui.onNodeWithText("我已保存，发布换码").performScrollTo().performClick()
        ui.onNodeWithText("save failed").assertExists()
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        ui.runOnIdle { assertEquals(0, backs) }
        ui.onNodeWithText("我已保存，发布换码").performScrollTo().performClick()
        ui.runOnIdle { assertEquals(1, backs) }
    }
}
