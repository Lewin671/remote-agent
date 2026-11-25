package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.proto.ExitConfirmStep
import io.github.lewin671.remoteagent.core.proto.ExitConfirmView
import io.github.lewin671.remoteagent.core.proto.ExitIntentKind
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
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
    @get:org.junit.Rule val batchCResources = SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()

    @Test
    fun rendersCoreExitConfirmViewAndDispatchesRequestAndConfirm() {
        var requests = 0
        val confirmations = mutableListOf<Boolean>()
        var vm by mutableStateOf(ExitConfirmView.getDefaultInstance())
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column {
                    AccountExitActionContent(
                        exitView = vm,
                        onRequestExit = {
                            requests++
                            vm = ExitConfirmView.newBuilder()
                                .setIntent(ExitIntentKind.EXIT_INTENT_KIND_LEAVE_ACCOUNT)
                                .setStep(ExitConfirmStep.EXIT_CONFIRM_STEP_AWAITING_CONFIRM)
                                .setWarningBody(CoreText.newBuilder().setKey("security.this_device_is_the_only_managing_client_transfer_management"))
                                .build()
                        },
                        onConfirmExit = { confirmations += it },
                    )
                }
            }
        }
        ui.onNodeWithText("退出并删除本机密钥…").performClick()
        assertEquals(1, requests)
        ui.onNodeWithText("本机是唯一管理端，建议先交接；退出会删除管理密钥，没有恢复码备份将丢失管理权。").assertExists()
        ui.onNodeWithText("此浏览器", substring = true).assertDoesNotExist()
        ui.onNodeWithText("退出").performClick()
        assertEquals(listOf(true), confirmations)
    }

    @Test
    fun failedExitCheckRendersErrorNoticeWithoutConfirmDialog() {
        val vm = ExitConfirmView.newBuilder()
            .setStep(ExitConfirmStep.EXIT_CONFIRM_STEP_FAILED)
            .setErrorText(CoreText.newBuilder().setLiteral("read failed"))
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column {
                    AccountExitActionContent(
                        exitView = vm,
                        onRequestExit = {},
                        onConfirmExit = {},
                    )
                }
            }
        }
        ui.onNodeWithText("read failed").assertExists()
        ui.onNodeWithText("退出").assertDoesNotExist()
    }

    @Test
    fun cancellingDialogDispatchesConfirmFalse() {
        var lastConfirm: Boolean? = null
        val vm = ExitConfirmView.newBuilder()
            .setIntent(ExitIntentKind.EXIT_INTENT_KIND_LEAVE_ACCOUNT)
            .setStep(ExitConfirmStep.EXIT_CONFIRM_STEP_AWAITING_CONFIRM)
            .setWarningBody(CoreText.newBuilder().setKey("security.exiting_deletes_local_keys_you_will_need_to_pair"))
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column {
                    AccountExitActionContent(
                        exitView = vm,
                        onRequestExit = {},
                        onConfirmExit = { lastConfirm = it },
                    )
                }
            }
        }
        ui.onNodeWithText("取消").performClick()
        assertEquals(false, lastConfirm)
    }

    @Test
    fun confirmingExitDialogDrivesRealCorePastAwaitingConfirmWithoutCancel() {
        val host = io.github.lewin671.remoteagent.core.CoreHost(workerDispatcher = kotlinx.coroutines.Dispatchers.Unconfined)
        val store = io.github.lewin671.remoteagent.core.MemorySecurityStoreV4()
        host.onSecurityEffect = { eff ->
            if (eff.effectCase == io.github.lewin671.remoteagent.core.proto.Effect.EffectCase.SECURITY_STORAGE) {
                host.sendEvent { setSecurityStorageResult(store.executeStorage(eff.securityStorage)) }
                true
            } else {
                false
            }
        }
        try {
            host.exitRequest(ExitIntentKind.EXIT_INTENT_KIND_SETUP_RESET, "https://relay.example.com")
            assertEquals(ExitConfirmStep.EXIT_CONFIRM_STEP_AWAITING_CONFIRM, host.exitConfirmView.value.step)
            ui.setContent {
                RaTheme(ThemeMode.LIGHT) {
                    ExitConfirmDialog(host)
                }
            }
            ui.onNodeWithText("确认退出").assertExists().performClick()
            ui.waitForIdle()
            assertEquals(ExitConfirmStep.EXIT_CONFIRM_STEP_EXECUTING, host.exitConfirmView.value.step)
            ui.onNodeWithText("确认退出").assertDoesNotExist()
        } finally {
            host.close()
        }
    }
}
