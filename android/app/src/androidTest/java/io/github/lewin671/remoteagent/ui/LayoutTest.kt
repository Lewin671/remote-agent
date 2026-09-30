package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.assertTrue
import org.junit.After
import org.junit.Rule
import org.junit.Test
import java.io.File

/** Real Compose layout and hit regions on an isolated emulator app; no live account. */
class LayoutTest {
    @get:Rule val ui = createComposeRule()
    private val key = "/Users/me/Code/ai-bot/remote-agent-with-a-very-long-project-name"
    private val machineId = "1111111111111111"
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private fun session(id: String = "s", ended: Boolean = false) = SessionView(id, machineId, Session.newBuilder()
        .setId(id).setCwd(key).setTitle("长标题：核查各端 UI，确保发送和配置按钮在窄屏下都可以操作")
        .setModel("a-very-long-model-name-that-must-not-push-send-offscreen").setEffort("high")
        .setStatus(if (ended) SessionStatus.SESSION_STATUS_EXITED else SessionStatus.SESSION_STATUS_IDLE)
        .setMode(ApprovalMode.APPROVAL_MODE_FULL_ACCESS).setContextTokens(86).setContextWindow(100).build(), updatedMs = System.currentTimeMillis())
    private val state get() = RemoteState(machines = mapOf(machineId to MachineView(machineId, false, 0,
        MachineMeta.newBuilder().setHostname("workstationM1-Pro-with-a-long-name").build())), conn = ConnState.OFFLINE, loaded = true)
    private fun screenshot(name: String) {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val img = ui.onNodeWithTag("frame").captureToImage().asAndroidBitmap()
        File(context.cacheDir, "$name.png").outputStream().use { img.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
    @After fun close() { remote.close() }
    @Test fun largeFontModeSheetCanScrollToAllOptions() {
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, 2f)) {
                    ModeSheet(session()) {}
                }
            }
        }
        ui.onNodeWithText("所有操作都不审批").performScrollTo().assertIsDisplayed()
    }
    @Test fun narrowComposerPreservesSendAndWarningAtLargeFont() {
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(ThemeMode.DARK) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, 1.5f)) {
                    Column(Modifier.testTag("frame").width(320.dp).background(pal.card)) {
                        SessionTabs(0, true, true) {}
                        Composer(state, session(), Derived(), Status.IDLE) {}
                    }
                }
            }
        }
        ui.onNodeWithContentDescription("发送").assertIsDisplayed()
        ui.onNodeWithText("设置… ●").assertIsDisplayed()
        ui.onNodeWithText("完全放行 ▾").assertIsDisplayed()
        val send = ui.onNodeWithContentDescription("发送").fetchSemanticsNode().boundsInRoot
        val settings = ui.onNodeWithText("设置… ●").fetchSemanticsNode().boundsInRoot
        val root = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
        assertTrue(send.right <= root.right && settings.right <= send.left)
        screenshot("composer-dark-large")
    }
    @Test fun terminalTabsExposeSelectionAndDisableEndedTerminal() {
        var selected by mutableIntStateOf(0)
        var enabled by mutableStateOf(true)
        ui.setContent { RaTheme(ThemeMode.LIGHT) { SessionTabs(selected, enabled, false) { selected = it } } }
        ui.onNodeWithText("聊天").assertIsSelected()
        ui.onNodeWithText("终端").performClick().assertIsSelected()
        ui.runOnIdle { selected = 0; enabled = false }
        ui.onNodeWithText("终端").assertIsNotEnabled()
        ui.onNodeWithText("聊天").assertIsSelected()
    }
    @Test fun projectCardKeepsLongConfigurationWithinBounds() {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.testTag("frame").width(320.dp).background(pal.bg)) {
                        SectionHeader("最近结束", 3)
                        SessionRow(state, session(ended = true), System.currentTimeMillis(), false, false)
                        NewSessionCard(state, NewConfig(machineId, key, model = "long-model-name", mode = Mode.ASK), "remote-agent-with-a-very-long-project-name", false)
                    }
                }
            }
        }
        ui.onNodeWithText("更改").assertIsDisplayed().assertHasClickAction()
        ui.onNodeWithContentDescription("开始").assertIsDisplayed().assertIsNotEnabled()
        val root = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
        for (node in ui.onAllNodes(hasClickAction()).fetchSemanticsNodes()) {
            assertTrue("control outside card", node.boundsInRoot.left >= root.left && node.boundsInRoot.right <= root.right)
        }
        screenshot("project-light-narrow")
    }
}
