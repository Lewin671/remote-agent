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
import org.junit.Assert.assertEquals
import org.junit.After
import org.junit.Rule
import org.junit.Test
import java.io.File

/** Real Compose layout and hit regions on an isolated emulator app; no live account. */
class LayoutTest {
    @Test fun optionalPushNoticeFitsLargeFontAndKeepsRetryUsableInBothThemes() {
        var dark by mutableStateOf(false)
        var retries = 0
        val problem = "暂时连不上 Google 推送服务，正在自动重试。请检查网络和 Google Play 服务"
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalDensity provides Density(base.density, 2f)) {
                    Column(Modifier.testTag("frame").width(320.dp).background(pal.soft)) {
                        GLabel("本客户端的通知")
                        Group(rows = listOf { Text("等我处理时", Modifier.padding(14.dp), color = pal.fg, style = T.body) })
                        PushNotice(io.github.lewin671.remoteagent.push.PushStatus(problem, "重试")) { retries++ }
                    }
                }
            }
        }
        for (theme in listOf(false, true)) {
            ui.runOnIdle { dark = theme }
            ui.onNodeWithText(problem).assertIsDisplayed()
            ui.onNodeWithText("仅影响 App 在后台时的系统通知，不影响聊天和审批").assertIsDisplayed()
            val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
            val note = ui.onNodeWithText(problem).fetchSemanticsNode().boundsInRoot
            assertTrue(note.left >= frame.left && note.right <= frame.right)
            ui.onNodeWithText("重试").performClick()
            screenshot(if (theme) "push-notice-dark-large" else "push-notice-light-large")
        }
        ui.runOnIdle { assertEquals(2, retries) }
    }
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
    @Test fun narrowComposerPreservesControlsAtTwoHundredPercentFont() {
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(ThemeMode.DARK) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, 2f)) {
                    Column(Modifier.testTag("frame").width(320.dp).background(pal.card)) {
                        SessionTabs(0, true, true) {}
                        Composer(state, session(), Derived(), Status.IDLE) {}
                    }
                }
            }
        }
        ui.onNodeWithContentDescription("发送").assertIsDisplayed()
        ui.onNodeWithContentDescription("添加图片").assertIsDisplayed()
        val send = ui.onNodeWithContentDescription("发送").fetchSemanticsNode().boundsInRoot
        val menu = ui.onNodeWithContentDescription("添加图片").fetchSemanticsNode().boundsInRoot
        val root = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
        assertTrue(send.right <= root.right && menu.right <= send.left)
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
    @Test fun projectBoxKeepsControlsWithinBounds() {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.testTag("frame").width(320.dp).background(pal.card)) {
                        SectionHeader("最近结束", 3)
                        SessionRow(state, session(ended = true), System.currentTimeMillis())
                        NewSessionBox(state, NewConfig(machineId, key, model = "long-model-name", mode = Mode.ASK), "remote-agent-with-a-very-long-project-name", false)
                    }
                }
            }
        }
        ui.onNodeWithContentDescription("添加图片").assertIsDisplayed().assertHasClickAction()
        ui.onNodeWithContentDescription("开始").assertIsDisplayed().assertIsNotEnabled()
        val root = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
        for (node in ui.onAllNodes(hasClickAction()).fetchSemanticsNodes()) {
            assertTrue("control outside card", node.boundsInRoot.left >= root.left && node.boundsInRoot.right <= root.right)
        }
        screenshot("project-light-narrow")
    }

    @Test fun projectSettingsLabelsStayWholeAtTwoHundredPercentFont() {
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, 2f)) {
                    NewSettingsSheet(state.copy(conn = ConnState.ONLINE, prefs = PrefsObject.EMPTY), key, "remote-agent", machineId, Agent.AGENT_CODEX, onDone = {}, onClose = {})
                }
            }
        }
        ui.onNodeWithText("机器").assertIsDisplayed()
        ui.onNodeWithText("审批").performScrollTo().assertIsDisplayed()
    }
    @Test fun wideSettingsKeepAReadableColumnAndACompactThemePicker() {
        ui.setContent {
            RaTheme(ThemeMode.DARK) {
                // Wider than the emulator's screen: laid out, if not all drawn.
                Column(Modifier.testTag("frame").requiredWidth(1000.dp).background(pal.soft)) {
                    Column(Modifier.fillMaxWidth().readableColumn()) {
                        GLabel("外观")
                        Group(Modifier.testTag("group"), rows = listOf {
                            Row(Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(horizontal = 14.dp), verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
                                Text("主题", Modifier.weight(1f))
                                Box(Modifier.testTag("pick")) { SegPick(listOf("自动", "亮色", "暗色"), 0) {} }
                            }
                        })
                    }
                }
            }
        }
        val d = ui.density
        val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
        val group = ui.onNodeWithTag("group").fetchSemanticsNode().boundsInRoot
        val pick = ui.onNodeWithTag("pick").fetchSemanticsNode().boundsInRoot
        with(d) {
            // 672 dp with the group's 16 dp margins, centred in the frame.
            assertTrue("group ${group.width.toDp()}", kotlin.math.abs(group.width.toDp().value - 672f) < 1f)
            assertTrue(kotlin.math.abs((group.left - frame.left) - (frame.right - group.right)) < 2f)
            assertTrue("pick ${pick.width.toDp()}", pick.width.toDp() < 220.dp && pick.right <= group.right)
        }
        ui.onNodeWithText("自动").assertIsSelected()
    }
}
