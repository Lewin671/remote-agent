package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.Context
import android.content.ContextWrapper
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.material3.Surface
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CoreHost
import io.github.lewin671.remoteagent.core.proto.AccountSecurityViewModel
import io.github.lewin671.remoteagent.core.proto.ExitConfirmStep
import io.github.lewin671.remoteagent.core.proto.ExitConfirmView
import io.github.lewin671.remoteagent.core.proto.JoinPairingStep
import io.github.lewin671.remoteagent.core.proto.JoinPairingView
import io.github.lewin671.remoteagent.core.proto.OnboardingMode
import io.github.lewin671.remoteagent.core.proto.OnboardingViewModel
import io.github.lewin671.remoteagent.core.proto.RemovedDeviceView
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

/** MainActivity's fresh-install shell: theme and Surface, without LocalNav or LocalRemote. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class UnauthenticatedScreensTest {
    @get:Rule val ui = createComposeRule()
    private val hosts = mutableListOf<CoreHost>()

    @After
    fun tearDown() {
        hosts.forEach { it.close() }
        hosts.clear()
    }

    private fun newHost(): CoreHost = CoreHost().also { hosts += it }

    private fun app(): RaApp =
        RaApp().also {
            ContextWrapper::class
                .java
                .getDeclaredMethod("attachBaseContext", Context::class.java)
                .apply { isAccessible = true }
                .invoke(it, RuntimeEnvironment.getApplication())
        }

    private fun show(content: @androidx.compose.runtime.Composable () -> Unit) {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Surface(Modifier.fillMaxSize(), color = pal.paper) { content() }
            }
        }
    }

    private fun assertSetupHeaderBelowSystemBar(
        title: String,
        screen: @androidx.compose.runtime.Composable () -> Unit,
    ) {
        show { AppSafeArea(WindowInsets(top = 48.dp, bottom = 24.dp)) { screen() } }
        val header = ui.onAllNodesWithText(title)[0].getUnclippedBoundsInRoot()
        assertTrue("$title overlaps the status bar: $header", header.top >= 48.dp)
        val back = ui.onNodeWithContentDescription("返回").getUnclippedBoundsInRoot()
        assertTrue("back target overlaps the status bar: $back", back.top >= 48.dp)
    }

    @Test
    fun activityInsetsAreConsumedExactlyOnceForNestedPages() {
        val host = newHost()
        val insets = WindowInsets(top = 48.dp, bottom = 24.dp)
        show {
            AppSafeArea(insets) {
                androidx.compose.foundation.layout.Box(Modifier.windowInsetsPadding(insets)) {
                    CreateAccountScreen(host) {}
                }
            }
        }
        val top = ui.onAllNodesWithText("创建账户")[0].getUnclippedBoundsInRoot().top
        assertTrue("nested page applied system insets twice: $top", top >= 48.dp && top < 96.dp)
    }

    @Test
    fun creationHeaderRespectsActivityInsets() {
        val host = newHost()
        assertSetupHeaderBelowSystemBar("创建账户") { CreateAccountScreen(host) {} }
    }

    @Test
    fun recoveryHeaderRespectsActivityInsets() {
        val host = newHost()
        assertSetupHeaderBelowSystemBar("用恢复码找回管理权") { RecoverScreen(host) {} }
    }

    @Test
    fun refusedStartupHasAnExplicitConfirmedStartOverAction() {
        var retries = 0
        var discarded = 0
        val exitState = mutableStateOf(ExitConfirmView.getDefaultInstance())
        show {
            AccountLoadFailureContent(
                message = "批准已过期",
                exitView = exitState.value,
                onRetry = { retries++ },
                onRequestReset = {
                    exitState.value = ExitConfirmView.newBuilder()
                        .setIntent(io.github.lewin671.remoteagent.core.proto.ExitIntentKind.EXIT_INTENT_KIND_SETUP_RESET)
                        .setStep(ExitConfirmStep.EXIT_CONFIRM_STEP_AWAITING_CONFIRM)
                        .setWarningBody(CoreText.newBuilder().setKey("security.the_local_account_unfinished_registration_and_security_operations_will"))
                        .build()
                },
                onConfirmExit = { if (it) discarded++ },
            )
        }
        ui.onNodeWithText("重试").performClick()
        assertEquals(1, retries)
        ui.onNodeWithText("退出并重新开始").performClick()
        assertEquals(0, discarded)
        ui.onNodeWithText("确认退出").performClick()
        assertEquals(1, discarded)
    }

    @Test
    fun scannerPermissionScreenNeedsOnlyTheActivityLocals() {
        val host = newHost()
        host.showViewsForTest(
            onboarding = OnboardingViewModel.newBuilder()
                .setMode(OnboardingMode.ONBOARDING_MODE_JOIN)
                .setCanBack(true)
                .setJoin(JoinPairingView.newBuilder().setStep(JoinPairingStep.JOIN_PAIRING_STEP_FORM).setServerEditable(true))
                .build(),
        )
        show { JoinScreen(app(), host) }
        ui.onNodeWithText("扫描二维码").performScrollTo().performClick()
        ui.onNodeWithText("需要相机权限才能扫码；也可以返回手动输入配对码。").assertExists()
        ui.onNodeWithContentDescription("取消").performClick()
        ui.onNodeWithText("扫描二维码").assertExists()
    }

    @Test
    fun removedScreenDoesNotRequireTheSignedInNavigationProvider() {
        val host = newHost()
        show {
            RemovedScreen(
                view = RemovedDeviceView.newBuilder()
                    .setCanRetryLogin(true)
                    .build(),
                coreHost = host,
                remote = null,
            )
        }
        ui.onNodeWithText("服务器拒绝了这台设备的登录").assertIsDisplayed()
    }

    @Test
    fun joinRendersWithoutSignedInNavigation() {
        val host = newHost()
        show { JoinScreen(app(), host) }
        ui.onNodeWithText("创建账户").assertExists()
    }

    @Test
    fun joinSaysWhereTheCodeComesFrom() {
        val host = newHost()
        host.showViewsForTest(
            onboarding = OnboardingViewModel.newBuilder()
                .setMode(OnboardingMode.ONBOARDING_MODE_JOIN)
                .setCanBack(true)
                .setJoin(JoinPairingView.newBuilder().setStep(JoinPairingStep.JOIN_PAIRING_STEP_FORM).setServerEditable(true))
                .build(),
        )
        show { JoinScreen(app(), host) }
        ui.onNodeWithText("在管理端打开「设置 › 添加设备」，扫它显示的二维码，或把 8 位配对码填在这里。").assertExists()
    }

    @Test
    fun rotateRecoveryHasOnlyTheTopBarBack() {
        var backs = 0
        val vm = AccountSecurityViewModel.newBuilder().setIsManager(true).setCanPrepareRotation(true).build()
        show { RotateRecoveryContent(vm = vm, onPrepare = {}, onPublish = {}, onBack = { backs++ }) }
        ui.onNodeWithText("生成新的恢复码").assertIsDisplayed()
        ui.onNodeWithText("返回").assertDoesNotExist()
        ui.onNodeWithContentDescription("返回").performClick()
        assertEquals(1, backs)
    }

    @Test
    fun pendingRegistrationOpensJoinWithEnabledRetry() {
        val host = newHost()
        val vm = OnboardingViewModel.newBuilder()
            .setMode(OnboardingMode.ONBOARDING_MODE_JOIN)
            .setJoin(
                JoinPairingView.newBuilder()
                    .setStep(JoinPairingStep.JOIN_PAIRING_STEP_FORM)
                    .setPendingRegistration(true)
                    .setServerInput("https://ra.example.com"),
            )
            .build()
        show { OnboardingContent(vm = vm, coreHost = host) }
        ui.onNodeWithText("重试").assertIsEnabled()
    }

    @Test
    fun accountSecurityHidesAbsentPendingOpRow() {
        val vm = AccountSecurityViewModel.newBuilder()
            .setIsManager(true)
            .setHasPendingOp(false)
            .build()
        show {
            androidx.compose.runtime.CompositionLocalProvider(
                LocalNav provides Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {}),
            ) {
                AccountSecurityContent(vm = vm)
            }
        }
        ui.onNodeWithText("未完成的安全操作").assertDoesNotExist()
        ui.onNodeWithText("本机").assertExists()
    }

    @Test
    fun pendingSecurityOperationPageRendersDiscardFromViewModel() {
        var discardedWith: Boolean? = null
        val vm = AccountSecurityViewModel.newBuilder()
            .setHasPendingOp(true)
            .setPendingKind(io.github.lewin671.remoteagent.core.proto.PendingSecurityOpKind.PENDING_SECURITY_OP_KIND_STATE_CHANGE)
            .setCanContinuePending(true)
            .setCanDiscardPending(true)
            .build()
        show {
            PendingSecurityOperationPage(
                vm = vm,
                onDiscard = { discardedWith = it },
            )
        }
        ui.onNodeWithText("继续原操作").assertExists()
        ui.onNodeWithText("放弃未签名的操作").assertExists().performClick()
        assertEquals(false, discardedWith)
    }

    @Test
    fun pendingSecurityOperationPageHidesDiscardWhenCoreDisallowsIt() {
        val vm = AccountSecurityViewModel.newBuilder()
            .setHasPendingOp(true)
            .setCanContinuePending(true)
            .setCanDiscardPending(false)
            .build()
        show { PendingSecurityOperationPage(vm = vm) }
        ui.onNodeWithText("继续原操作").assertExists()
        ui.onNodeWithText("放弃未签名的操作").assertDoesNotExist()
    }

    @Test
    fun pairingCodeDigitByDigitInputPreservesOrderAndCursor() {
        val host = CoreHost(workerDispatcher = Dispatchers.Unconfined).also { hosts += it }
        host.onboardingSetMode(OnboardingMode.ONBOARDING_MODE_JOIN)
        show { JoinScreen(app(), host) }

        val codeField = ui.onNodeWithTag("join-pairing-code")
        for (digit in "10546926") {
            codeField.performTextInput(digit.toString())
        }
        ui.waitForIdle()
        assertEquals("1054 6926", host.onboardingView.value.join.formattedCode)
        codeField.assertTextEquals("1054 6926")

        // Server input accepts edits and stays empty when cleared (does not snap back to "https://").
        val serverField = ui.onNodeWithTag("join-server-input")
        serverField.performTextInput("http://127.0.0.1:18080")
        ui.waitForIdle()
        assertEquals("http://127.0.0.1:18080", host.onboardingView.value.join.serverInput)
        serverField.performTextClearance()
        ui.waitForIdle()
        assertEquals("", host.onboardingView.value.join.serverInput)

        // Direct cursor mapping check for mid-code edit and backspace over the Core-inserted space.
        assertEquals(TextRange(6), mapPairingCodeSelection("10546", TextRange(5), "1054 6"))
        assertEquals(TextRange(4), mapPairingCodeSelection("10546", TextRange(4), "1054 6"))
        assertEquals(TextRange(3), mapPairingCodeSelection("10854 6926", TextRange(3), "1085 4692"))
    }

    @Test
    fun rapidAsyncTypingIntoPairingCodeAndServerDropsNoCharacters() {
        val scheduler = kotlinx.coroutines.test.TestCoroutineScheduler()
        val host = CoreHost(workerDispatcher = kotlinx.coroutines.test.StandardTestDispatcher(scheduler)).also { hosts += it }
        host.onboardingSetMode(OnboardingMode.ONBOARDING_MODE_JOIN)
        scheduler.advanceUntilIdle()
        show { JoinScreen(app(), host) }

        val codeField = ui.onNodeWithTag("join-pairing-code")
        for (digit in "32127933") {
            codeField.performTextInput(digit.toString())
        }
        val serverField = ui.onNodeWithTag("join-server-input")
        for (ch in "http://127.0.0.1:63867") {
            serverField.performTextInput(ch.toString())
        }
        scheduler.advanceUntilIdle()
        ui.waitForIdle()
        assertEquals("3212 7933", host.onboardingView.value.join.formattedCode)
        codeField.assertTextEquals("3212 7933")
        assertEquals("http://127.0.0.1:63867", host.onboardingView.value.join.serverInput)
        serverField.assertTextEquals("服务器", "http://127.0.0.1:63867")
    }
}
