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
import io.github.lewin671.remoteagent.update.UpdateCheck
import io.github.lewin671.remoteagent.update.UpdateState
import io.github.lewin671.remoteagent.update.Updater
import org.junit.Assert.assertTrue
import org.junit.Assert.assertEquals
import org.junit.After
import org.junit.Rule
import org.junit.Test
import java.io.File

/** Real Compose layout and hit regions on an isolated emulator app; no live account. */
class LayoutTest {
    /** The release the update sheet tests are about, as the manifest would describe it. */
    private val newer = Release("0.36.2", 100, PROTOCOL_VERSION, "remote-agent-v0.36.2.apk", "0".repeat(64), 5_976_883, "更新说明")
    private var updaterBefore: Pair<UpdateState, UpdateCheck>? = null

    /** The update sheet as it looks in [state] with [check] as the last look; [close] puts the updater back. */
    private fun showUpdate(state: UpdateState, check: UpdateCheck = UpdateCheck(), dark: Boolean = false, fontScale: Float = 1f) {
        if (updaterBefore == null) updaterBefore = Updater.state.value to Updater.checking.value
        Updater.showForTest(state, check)
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalDensity provides Density(base.density, fontScale)) { UpdateSheet {} }
            }
        }
    }

    @Test fun aKnownUpdateKeepsItsStepAndOffersALightweightCheck() {
        showUpdate(UpdateState.Available(newer))
        ui.onNodeWithText("新版本 0.36.2").assertIsDisplayed()
        ui.onNodeWithText("下载并安装（5.7 MiB）").assertIsDisplayed().assertIsEnabled()
        ui.onNodeWithText("检查更新").assertIsDisplayed().assertIsEnabled()
        // Downloaded and verified: the step is the install, and the check is still there.
        ui.runOnIdle { Updater.showForTest(UpdateState.Ready(newer, File("update.apk"))) }
        ui.onNodeWithText("安装").assertIsDisplayed().assertIsEnabled()
        ui.onNodeWithText("检查更新").assertIsDisplayed().assertIsEnabled()
    }

    @Test fun downloadingLeavesNeitherTheStepNorTheCheckUsable() {
        showUpdate(UpdateState.Downloading(newer, 2_097_152))
        ui.onNodeWithText("正在下载 2.0 MiB / 5.7 MiB").assertIsDisplayed().assertIsNotEnabled()
        ui.onNodeWithText("检查更新").assertIsDisplayed().assertIsNotEnabled()
    }

    @Test fun aLookInFlightKeepsTheReleaseOnScreenAndLocksBothActions() {
        showUpdate(UpdateState.Available(newer), UpdateCheck(running = true))
        ui.onNodeWithText("新版本 0.36.2").assertIsDisplayed()
        ui.onNodeWithText("更新说明").assertIsDisplayed()
        ui.onNodeWithText("下载并安装（5.7 MiB）").assertIsDisplayed().assertIsNotEnabled()
        ui.onNodeWithText("正在检查更新…").assertIsDisplayed().assertIsNotEnabled()
    }

    @Test fun aLookThatFindsTheSameReleaseSaysSoAndKeepsTheApkReady() {
        showUpdate(UpdateState.Ready(newer, File("update.apk")), UpdateCheck(upToDate = true))
        ui.onNodeWithText("已是最新发布版本").assertIsDisplayed()
        ui.onNodeWithText("安装").assertIsDisplayed().assertIsEnabled()
        ui.onNodeWithText("检查更新").assertIsDisplayed().assertIsEnabled()
    }

    @Test fun aLookThatFailsShowsWhyAndCanBeTriedAgain() {
        showUpdate(UpdateState.Available(newer), UpdateCheck(error = "连不上发布地址"))
        ui.onNodeWithText("没能检查更新：连不上发布地址").assertIsDisplayed()
        ui.onNodeWithText("下载并安装（5.7 MiB）").assertIsDisplayed().assertIsEnabled()
        ui.onNodeWithText("检查更新").assertIsDisplayed().assertIsEnabled()
    }

    @Test fun theLongestLineAndTheCheckStillFitAtTwoHundredPercentFontInDark() {
        showUpdate(UpdateState.Ready(newer, File("update.apk")), UpdateCheck(error = "连不上发布地址"), dark = true, fontScale = 2f)
        ui.onNodeWithText("没能检查更新：连不上发布地址").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("安装").performScrollTo().assertIsDisplayed().assertIsEnabled()
        ui.onNodeWithText("检查更新").performScrollTo().assertIsDisplayed().assertIsEnabled()
    }

    @Test fun theVersionSheetKeepsItsOneCheckButtonWhileLooking() {
        showUpdate(UpdateState.Unknown, UpdateCheck(running = true))
        ui.onNodeWithText("正在检查更新…").assertIsDisplayed().assertIsNotEnabled()
        ui.runOnIdle { Updater.showForTest(UpdateState.Latest(0)) }
        ui.onNodeWithText("已是最新版本").assertIsDisplayed()
        ui.onNodeWithText("检查更新").assertIsDisplayed().assertIsEnabled()
    }

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
    @After fun close() {
        remote.close()
        updaterBefore?.let { Updater.showForTest(it.first, it.second) }
        updaterBefore = null
    }
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
    private fun approval(lines: Int) = ApprovalRequested.newBuilder().setApprovalId("A$lines")
        .setCommand((1..lines).joinToString("\n") { "echo step-$it" }).setDescription("说明文字".repeat(30)).build()

    @Test fun aWaitingItemOpensWithOneHeadLineAndFoldsByItsId() {
        DockFolds.clear()
        val a = approval(40)
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, 1.3f)) {
                    Column(Modifier.testTag("frame").width(320.dp).height(640.dp).background(pal.card)) {
                        Spacer(Modifier.weight(1f))
                        CompositionLocalProvider(LocalDockMax provides 384.dp) { ApprovalDock(session(), a, 1) }
                    }
                }
            }
        }
        // Open by itself: one head line, the command before the description, the buttons in view.
        ui.onNodeWithText("收起", substring = true).assertIsDisplayed()
        ui.onAllNodesWithText("请求执行", substring = true).assertCountEquals(1)
        ui.onNodeWithText("允许").assertIsDisplayed()
        val command = ui.onNodeWithText("echo step-1", substring = true).fetchSemanticsNode().positionInRoot
        val description = ui.onNodeWithText("说明文字", substring = true).fetchSemanticsNode().positionInRoot
        assertTrue("command before description", command.y < description.y)
        // The body scrolls inside about 60%; the last line of the command is reachable.
        ui.onNodeWithText("说明文字", substring = true).performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("收起", substring = true).performClick()
        ui.onNodeWithText("处理", substring = true).assertIsDisplayed()
        ui.onNodeWithText("允许").assertDoesNotExist()
        ui.runOnIdle { assertTrue(!DockFolds.isOpen("s", DockItem.Approval(a).id)) }
        DockFolds.clear()
    }

    @Test fun aPlanOpensItsPageFromItsHeadLine() {
        DockFolds.clear()
        var full = 0
        // What the computer says is pending: its terminal's box, and the plan event it names.
        val markdown = "# 计划：在 run-P 目录创建 5 个 beta 前缀文件\n\n## Context\n\n用户要求用前缀 beta 创建 5 个文本文件。\n\n## 步骤\n\n" +
            (1..30).joinToString("\n") { "${it}. 第 $it 步" }
        val events = listOf(
            io.github.lewin671.remoteagent.proto.Event.newBuilder().setSessionId("s").setSeq(1)
                .setPlanProposed(io.github.lewin671.remoteagent.proto.PlanProposed.newBuilder().setText(markdown)).build(),
        )
        val live = io.github.lewin671.remoteagent.proto.LiveSnapshot.newBuilder()
            .setSession(Session.newBuilder().setId("s").setPlanOpen(true)).setPlanEventSeq(1)
            .setPlanScreenHash(com.google.protobuf.ByteString.copyFrom(ByteArray(32)))
            .setPlanScreen("⎿ /plan to preview\n──────\nReady to code?\nHere is Claude's plan:\n──────\n" + markdown)
            .build()
        val plan = io.github.lewin671.remoteagent.core.currentActions(
            io.github.lewin671.remoteagent.core.derive(events), live, events).plan!!.text
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.testTag("frame").width(360.dp).height(640.dp).background(pal.card)) {
                        Spacer(Modifier.weight(1f))
                        PlanDock(session(), plan, 0) { full++ }
                    }
                }
            }
        }
        // The plan's own Markdown, not the terminal's box.
        ui.onNodeWithText("计划：在 run-P 目录创建 5 个 beta 前缀文件", substring = true).assertIsDisplayed()
        ui.onNodeWithText("Ready to code?", substring = true).assertDoesNotExist()
        screenshot("plan-dock-head")
        // The texts themselves: the head line merges them into one button.
        val link = ui.onNodeWithText("展开全文", useUnmergedTree = true).assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        val fold = ui.onNodeWithText("收起", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val label = ui.onNodeWithText("计划待批准", substring = true, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        // One line: the type, then 展开全文, then 收起; not at the end of the scrolling plan.
        assertTrue("same line", kotlin.math.abs(link.center.y - fold.center.y) < 2f && kotlin.math.abs(link.center.y - label.center.y) < 2f)
        assertTrue("before 收起", link.right <= fold.left)
        ui.onNodeWithText("展开全文", substring = true).performClick()
        ui.runOnIdle { assertEquals(1, full) }
        // Still open: the link went to the page, it did not fold the dock.
        ui.onNodeWithText("收起", substring = true).assertIsDisplayed()
        DockFolds.clear()
    }

    @Test fun aCardCutsACommandAtWholeLinesAndOpensItInPlace() {
        var dark by mutableStateOf(false)
        ui.setContent {
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                Column(Modifier.testTag("frame").width(320.dp).background(pal.soft).padding(12.dp)) {
                    CommandPreview((1..20).joinToString("\n") { "echo step-$it" }, "k")
                }
            }
        }
        ui.onNodeWithText("还有 14 行 · 展开", substring = true).assertIsDisplayed()
        // Six whole lines, the last ending in …, so the cut shows.
        val cut = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        ui.onNodeWithText("echo step-1", substring = true)
            .performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(cut) }
        assertEquals(6, cut.single().lineCount)
        assertTrue("last line ellipsized", cut.single().isLineEllipsized(5))
        screenshot("card-command-cut-light")
        ui.runOnIdle { dark = true }
        screenshot("card-command-cut-dark")
        ui.onNodeWithText("还有 14 行 · 展开", substring = true).performClick()
        ui.onNodeWithText("收起", substring = true).assertIsDisplayed()
        ui.onNodeWithText("收起", substring = true).performClick()
        ui.onNodeWithText("还有 14 行 · 展开", substring = true).assertIsDisplayed()
    }

    @Test fun aShortCommandHasNoMoreLine() {
        ui.setContent { RaTheme(ThemeMode.LIGHT) { Column(Modifier.width(320.dp)) { CommandPreview("go test ./...", "k2") } } }
        ui.onNodeWithText("go test ./...").assertIsDisplayed()
        ui.onNodeWithText("展开", substring = true).assertDoesNotExist()
    }

    @Test fun approvalButtonsKeepTo480OnATablet() {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.testTag("frame").requiredWidth(1000.dp).background(pal.card)) {
                        ApprovalButtons(session(), approval(1), compact = false)
                        PlanButtons(session(), full = true)
                    }
                }
            }
        }
        val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
        with(ui.density) {
            for (label in listOf("允许", "批准执行")) {
                val b = ui.onNodeWithText(label).fetchSemanticsNode().boundsInRoot
                assertTrue("$label ${(b.right - frame.left).toDp()}", (b.right - frame.left).toDp() <= 481.dp)
            }
        }
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
    @Test fun machineStatusStaysAtTheTrailingEdgeWithQuotasAndLargeFont() {
        // Keep both readings today in every simulator timezone.
        val now = java.time.LocalDate.now().atTime(12, 0).atZone(java.time.ZoneId.systemDefault()).toInstant().toEpochMilli()
        val name = "A very long workstation name that must leave room for status"
        var dark by mutableStateOf(false)
        var online by mutableStateOf(true)
        val meta = MachineMeta.newBuilder().setHostname(name)
            .addQuotas(AgentQuota.newBuilder().setAgent(Agent.AGENT_CODEX).setState(QuotaState.QUOTA_STATE_OK).setObservedMs(now)
                .addWindows(QuotaWindow.newBuilder().setWindowMinutes(300).setUsedPercent(62.0))).build()
        ui.setContent {
            val density = LocalDensity.current
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(density.density, 2f)) {
                    Column(Modifier.width(320.dp).testTag("frame")) {
                        MachinesGroup(RemoteState(machines = mapOf(machineId to MachineView(machineId, online, now - 7_200_000, meta))), now)
                    }
                }
            }
        }
        for (theme in listOf(false, true)) {
            ui.runOnIdle { dark = theme }
            for (connected in listOf(true, false)) {
                ui.runOnIdle { online = connected }
                val status = ui.onNodeWithText(if (connected) "在线" else "2 小时前", useUnmergedTree = true).assertIsDisplayed().fetchSemanticsNode().boundsInRoot
                val title = ui.onNodeWithText(name, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
                assertTrue("status overlaps title", status.left >= title.right)
                assertTrue("status outside row", status.right <= frame.right)
                assertTrue("status should stay at row end", status.right > frame.left + frame.width * .65f)
            }
        }
    }

    @Test fun tabletHomeListsEveryWaitingAndRunningSession() {
        val sessions = (1..10).map { n ->
            SessionView("wide$n", machineId, Session.newBuilder().setId("wide$n").setCwd(key)
                .setTitle(if (n <= 4) "Waiting $n" else "Running $n")
                .setPendingApprovals(if (n <= 4) 1 else 0).setStatus(SessionStatus.SESSION_STATUS_RUNNING).build())
        }
        remote.showForTest(RemoteState(loaded = true, sessions = sessions.associateBy { it.id }, machines = state.machines))
        ui.setContent {
            val config = android.content.res.Configuration(androidx.compose.ui.platform.LocalConfiguration.current).apply { screenWidthDp = 1000 }
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, androidx.compose.ui.platform.LocalConfiguration provides config) {
                    HomeScreen()
                }
            }
        }
        ui.onNodeWithText("Waiting 4").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("Running 10").performScrollTo().assertIsDisplayed()
        ui.onAllNodes(hasText("还有", substring = true)).assertCountEquals(0)
    }

}
