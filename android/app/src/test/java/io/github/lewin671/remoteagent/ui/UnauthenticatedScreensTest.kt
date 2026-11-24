package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.Context
import android.content.ContextWrapper
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.material3.Surface
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.proto.SecurityPendingOpSummary
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
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
        val app = app()
        val insets = WindowInsets(top = 48.dp, bottom = 24.dp)
        show {
            AppSafeArea(insets) {
                androidx.compose.foundation.layout.Box(Modifier.windowInsetsPadding(insets)) {
                    CreateAccountScreen(app) {}
                }
            }
        }
        val top = ui.onAllNodesWithText("创建账户")[0].getUnclippedBoundsInRoot().top
        assertTrue("nested page applied system insets twice: $top", top >= 48.dp && top < 96.dp)
    }

    @Test
    fun creationHeaderRespectsActivityInsets() {
        val app = app()
        assertSetupHeaderBelowSystemBar("创建账户") { CreateAccountScreen(app) {} }
    }

    @Test
    fun recoveryHeaderRespectsActivityInsets() {
        val app = app()
        assertSetupHeaderBelowSystemBar("用恢复码找回管理权") { RecoverScreen(app) {} }
    }

    @Test
    fun refusedStartupHasAnExplicitConfirmedStartOverAction() {
        var retries = 0
        var discarded = 0
        show { AccountLoadFailure("批准已过期", { retries++ }, { discarded++ }) }
        ui.onNodeWithText("重试").performClick()
        org.junit.Assert.assertEquals(1, retries)
        ui.onNodeWithText("退出并重新开始").performClick()
        org.junit.Assert.assertEquals(0, discarded)
        ui.onNodeWithText("确认退出").performClick()
        org.junit.Assert.assertEquals(1, discarded)
    }

    @Test
    fun scannerPermissionScreenNeedsOnlyTheActivityLocals() {
        val app = app()
        show { JoinScreen(app) }
        ui.onNodeWithText("加入已有账户").performClick()
        ui.onNodeWithText("扫描二维码").performScrollTo().performClick()
        ui.onNodeWithText("需要相机权限才能扫码；也可以返回手动输入配对码。").assertExists()
        ui.onNodeWithContentDescription("取消").performClick()
        ui.onNodeWithText("扫描二维码").assertExists()
    }

    @Test
    fun removedScreenDoesNotRequireTheSignedInNavigationProvider() {
        val app = app()
        val remote =
            io.github.lewin671.remoteagent.core.Remote(
                io.github.lewin671.remoteagent.core.Account(
                    "http://127.0.0.1:1",
                    "2222222222222222",
                ),
                io.github.lewin671.remoteagent.core.Api.defaultHttp(),
            )
        try {
            show { RemovedScreen(app, remote) }
            ui.onNodeWithText("服务器拒绝了这台设备的登录").assertIsDisplayed()
        } finally {
            remote.close()
        }
    }

    @Test
    fun noOperationMeansNoResumeActionAndTheVaultIsReadOffMain() {
        val read = java.util.concurrent.atomic.AtomicBoolean(false)
        val readOnMain = java.util.concurrent.atomic.AtomicBoolean(true)
        show {
            PendingSecurityOperation(readPending = {
                readOnMain.set(android.os.Looper.myLooper() == android.os.Looper.getMainLooper())
                read.set(true)
                false
            }, resume = { error("nothing to resume") })
        }
        ui.waitUntil { read.get() }
        ui.waitForIdle()
        org.junit.Assert.assertFalse(readOnMain.get())
        ui.onNodeWithText("核实原安全操作").assertDoesNotExist()
        ui.onNodeWithText("请在管理端查看操作是否已完成。").assertDoesNotExist()
    }

    @Test
    fun theResumeActionGoesAwayAfterThePendingOperationCompletes() {
        val pending = java.util.concurrent.atomic.AtomicBoolean(true)
        val resumes = java.util.concurrent.atomic.AtomicInteger()
        show {
            PendingSecurityOperation(readPending = { pending.get() }, resume = {
                resumes.incrementAndGet()
                pending.set(false)
            })
        }
        ui.waitUntil { ui.onAllNodesWithText("核实原安全操作").fetchSemanticsNodes().isNotEmpty() }
        ui.onNodeWithText("核实原安全操作").performClick()
        ui.waitUntil { ui.onAllNodesWithText("原安全操作已核实完成。").fetchSemanticsNodes().isNotEmpty() }
        org.junit.Assert.assertEquals(1, resumes.get())
        ui.onNodeWithText("核实原安全操作").assertDoesNotExist()
    }

    @Test
    fun anOperationCompletedElsewhereDoesNotProduceAnEmptyResumeMessage() {
        val pending = java.util.concurrent.atomic.AtomicBoolean(true)
        show {
            PendingSecurityOperation(readPending = { pending.get() }, resume = { error("already completed") })
        }
        ui.waitUntil { ui.onAllNodesWithText("核实原安全操作").fetchSemanticsNodes().isNotEmpty() }
        pending.set(false)
        ui.onNodeWithText("核实原安全操作").performClick()
        ui.waitUntil { ui.onAllNodesWithText("核实原安全操作").fetchSemanticsNodes().isEmpty() }
        ui.onNodeWithText("原安全操作已核实完成。").assertDoesNotExist()
    }

    @Test
    fun joinRendersWithoutSignedInNavigation() {
        val app = app()
        show { JoinScreen(app) }
        ui.onNodeWithText("创建账户").assertExists()
    }

    @Test
    fun joinSaysWhereTheCodeComesFrom() {
        val app = app()
        show { JoinScreen(app) }
        ui.onNodeWithText("加入已有账户").performClick()
        // The authority client shows the code and the QR; this device scans or types it, not the reverse.
        ui.onNodeWithText("在管理端打开「设置 › 添加设备」，扫它显示的二维码，或把 8 位配对码填在这里。").assertExists()
    }

    @Test
    fun createAccountFromFreshJoinRendersAndReturns() {
        val app = app()
        show { JoinScreen(app) }
        ui.onNodeWithText("创建账户").performScrollTo().performClick()
        ui.onNodeWithContentDescription("返回").assertIsDisplayed()
        // The top bar's round button is the one way back; no second "返回" text button at the bottom.
        ui.onNodeWithText("返回").assertDoesNotExist()
        ui.onNodeWithContentDescription("返回").performClick()
        ui.onNodeWithText("创建账户").assertExists()
    }

    @Test
    fun recoverFromFreshJoinRendersAndReturns() {
        val app = app()
        show { JoinScreen(app) }
        ui.onNodeWithText("用恢复码找回管理权").performScrollTo().performClick()
        ui.onNodeWithText("用恢复码找回管理权").assertIsDisplayed()
        ui.onNodeWithContentDescription("返回").performClick()
        ui.onNodeWithText("用恢复码找回管理权").assertExists()
    }

    @Test
    fun rotateRecoveryHasOnlyTheTopBarBack() {
        var backs = 0
        show { RotateRecoveryContent(io.github.lewin671.remoteagent.core.proto.SyncStateView.getDefaultInstance(), { error("unused") }, {}, { backs++ }) }
        ui.onNodeWithText("生成新的恢复码").assertIsDisplayed()
        ui.onNodeWithText("返回").assertDoesNotExist()
        ui.onNodeWithContentDescription("返回").performClick()
        org.junit.Assert.assertEquals(1, backs)
    }

    @Test
    fun pendingRegistrationOpensJoinWithEnabledRetryAndCallsResumeOverride() {
        val app = app()
        val resumed = java.util.concurrent.atomic.AtomicInteger()
        val pendingStatus = io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus.newBuilder()
            .setBootstrap(io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION)
            .setServer("https://ra.example.com")
            .build()
        show {
            JoinContent(
                app = app,
                bootstrapOverride = pendingStatus,
                resumeOverride = { resumed.incrementAndGet() },
            )
        }
        ui.onNodeWithText("重试").assertIsEnabled().performClick()
        ui.waitUntil { resumed.get() == 1 }
        org.junit.Assert.assertEquals(1, resumed.get())
    }

    @Test
    fun pendingCreateTransitionToReadyDoesNotResetJoinToEntry() {
        val app = app()
        val pendingCreate = SecurityWorkflowStatus.newBuilder()
            .setBootstrap(io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_CREATE)
            .setServer("https://ra.example.com")
            .build()
        val ready = SecurityWorkflowStatus.newBuilder()
            .setBootstrap(io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_READY)
            .setServer("https://ra.example.com")
            .setDeviceId("dev-1")
            .setAccountId("acct-1")
            .build()
        val statusState = androidx.compose.runtime.mutableStateOf(pendingCreate)
        show {
            JoinContent(
                app = app,
                bootstrapOverride = statusState.value,
            )
        }
        // In pending creation mode: we are on CreateAccountScreen, not the welcome entry screen.
        ui.onNodeWithText("加入已有账户").assertDoesNotExist()

        // Core transitions status from PENDING_CREATE -> READY as continueCreation persists the account.
        // JoinContent must stay on CreateAccountScreen (not drop back to welcome entry and cancel adoption).
        ui.runOnIdle { statusState.value = ready }
        ui.waitForIdle()
        ui.onNodeWithText("加入已有账户").assertDoesNotExist()
        ui.onNodeWithText("用恢复码找回管理权").assertDoesNotExist()
    }

    @Test
    fun accountSecurityIgnoresDefaultUnsetProtobufPendingOp() {
        val unset = SecurityWorkflowStatus.getDefaultInstance()
        assertNotNull(unset.pendingOp)
        assertNull(activePendingOp(unset))
        val withPending = SecurityWorkflowStatus.newBuilder()
            .setPendingOp(SecurityPendingOpSummary.newBuilder().setKind("handoff"))
            .build()
        assertNotNull(activePendingOp(withPending))

        val remote = Remote(Account("http://127.0.0.1:1", "self-1"), Api.defaultHttp())
        try {
            show {
                androidx.compose.runtime.CompositionLocalProvider(
                    LocalRemote provides remote,
                    LocalNav provides Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {}),
                ) {
                    AccountSecurityContent(
                        syncView = io.github.lewin671.remoteagent.core.proto.SyncStateView.newBuilder()
                            .setIsManager(true)
                            .setAuthorityDeviceId("self-1")
                            .build(),
                        pending = activePendingOp(unset) != null,
                    )
                }
            }
            ui.onNodeWithText("未完成的安全操作").assertDoesNotExist()
            ui.onNodeWithText("本机").assertExists()
        } finally {
            remote.close()
        }
    }
}
