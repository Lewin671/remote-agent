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
import io.github.lewin671.remoteagent.data.ThemeMode
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
                Surface(Modifier.fillMaxSize(), color = pal.card) { content() }
            }
        }
    }

    private fun assertSetupHeaderBelowSystemBar(
        title: String,
        screen: @androidx.compose.runtime.Composable () -> Unit,
    ) {
        show { AppSafeArea(WindowInsets(top = 48.dp, bottom = 24.dp)) { screen() } }
        val header = ui.onNodeWithText(title).getUnclippedBoundsInRoot()
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
        val top = ui.onNodeWithText("新建账户").getUnclippedBoundsInRoot().top
        assertTrue("nested page applied system insets twice: $top", top >= 48.dp && top < 96.dp)
    }

    @Test
    fun creationHeaderRespectsActivityInsets() {
        val app = app()
        assertSetupHeaderBelowSystemBar("新建账户") { CreateAccountScreen(app) {} }
    }

    @Test
    fun recoveryHeaderRespectsActivityInsets() {
        val app = app()
        assertSetupHeaderBelowSystemBar("用恢复码找回管理权") { RecoverScreen(app) {} }
    }

    @Test
    fun rebuildHeaderRespectsActivityInsets() {
        val app = app()
        assertSetupHeaderBelowSystemBar("重建安全域") { RebuildSecurityScreen(app, null) {} }
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
        ui.onNodeWithText("取消").performClick()
        ui.onNodeWithText("扫描二维码").assertExists()
    }

    @Test
    fun removedScreenDoesNotRequireTheSignedInNavigationProvider() {
        val app = app()
        val remote =
            io.github.lewin671.remoteagent.core.Remote(
                io.github.lewin671.remoteagent.core.Account(
                    "http://127.0.0.1:1",
                    ByteArray(32),
                    ByteArray(32),
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
        ui.onNodeWithText("新建账户").assertExists()
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
        ui.onNodeWithText("新建账户").performScrollTo().performClick()
        ui.onNodeWithContentDescription("返回").assertIsDisplayed()
        ui.onNodeWithContentDescription("返回").performClick()
        ui.onNodeWithText("新建账户").assertExists()
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
    fun rebuildRendersWithoutSignedInNavigation() {
        val app = app()
        var backs = 0
        show { RebuildSecurityScreen(app, null) { backs++ } }
        ui.onNodeWithText("重建安全域").assertIsDisplayed()
        ui.onNodeWithContentDescription("返回").performClick()
        org.junit.Assert.assertEquals(1, backs)
    }
}
