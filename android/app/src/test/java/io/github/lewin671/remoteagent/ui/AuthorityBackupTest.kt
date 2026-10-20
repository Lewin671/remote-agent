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
        val pub = ByteArray(32) { 7 }
        var attempts = 0
        var state by mutableStateOf(RemoteState())
        show {
                HandoffPreparationContent(state, "self", { "synthetic-offline-code" to pub }) {
                    attempts++
                    if (attempts == 1) throw IllegalStateException("save failed")
                    state = state.copy(devices = mapOf("self" to DeviceView("self", DeviceKind.DEVICE_KIND_CLIENT, 0,
                        DeviceMeta.newBuilder().setHandoffRecoveryEd(ByteString.copyFrom(it)).build())))
                }
        }
        ui.onNodeWithText("我已离线保存，通知当前管理端").assertDoesNotExist()
        ui.onNodeWithText("生成新的恢复码").performClick()
        ui.runOnIdle { assertEquals(0, attempts) }
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        ui.onNodeWithText("我已离线保存，通知当前管理端").performClick()
        ui.onNodeWithText("save failed").assertExists()
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        ui.onNodeWithText("恢复码已确认保存。等待当前管理端批准；可以关闭后重新打开此页查看。").assertDoesNotExist()
        ui.onNodeWithText("我已离线保存，通知当前管理端").performClick()
        ui.onNodeWithText("恢复码已确认保存。等待当前管理端批准；可以关闭后重新打开此页查看。").assertExists()
        ui.runOnIdle { assertEquals(2, attempts) }
    }

    @Test
    fun rotationRequiresBackupConfirmationAndFailedPublicationDoesNotNavigateAway() {
        val signer = SigningKey(ByteArray(32) { 1 })
        val member = Authority.newMember(signer, ByteArray(32) { 2 }, 2u)
        val state = Authority.genesis("11111111111111111111111111111111", ByteArray(32) { 3 }, member,
            SigningKey(ByteArray(32) { 4 }).publicKey, ByteArray(32) { 5 }, signer, 1_700_000_000_000)
        val prepared = PreparedRecovery("synthetic-offline-code", state, ByteArray(32) { 4 })
        var attempts = 0
        var backs = 0
        show {
                RotateRecoveryContent(RemoteState(isAuthority = true), { prepared }, {
                    attempts++
                    if (attempts == 1) throw IllegalStateException("save failed")
                }, { backs++ })
        }
        ui.onNodeWithText("我已保存，发布新码").assertDoesNotExist()
        ui.onNodeWithText("生成新的恢复码").performClick()
        ui.runOnIdle { assertEquals(0, attempts) }
        ui.onNodeWithText("我已保存，发布新码").performClick()
        ui.onNodeWithText("save failed").assertExists()
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        ui.runOnIdle { assertEquals(0, backs) }
        ui.onNodeWithText("我已保存，发布新码").performClick()
        ui.runOnIdle { assertEquals(1, backs) }
    }
}
