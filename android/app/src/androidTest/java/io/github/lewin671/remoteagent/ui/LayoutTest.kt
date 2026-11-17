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
                    Column(Modifier.testTag("frame").width(320.dp).background(pal.paper)) {
                        GLabel("本客户端的通知")
                        Group(rows = listOf { Text("等我处理时", Modifier.padding(14.dp), color = pal.ink, style = T.s15) })
                        PushNotice(io.github.lewin671.remoteagent.push.PushStatus(problem, "重试")) { retries++ }
                    }
                }
            }
        }
        for (theme in listOf(false, true)) {
            ui.runOnIdle { dark = theme }
            ui.onNodeWithText(problem, substring = true).assertIsDisplayed()
            ui.onNodeWithText("仅影响 App 在后台时的系统通知，不影响聊天和审批", substring = true).assertIsDisplayed()
            val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
            val note = ui.onNodeWithText(problem, substring = true).fetchSemanticsNode().boundsInRoot
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
        val vm = sessionFixtureVm("s", session())
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, 2f)) {
                    ModeSheet("s", vm) {}
                }
            }
        }
        ui.onNodeWithText("所有操作都不审批").performScrollTo().assertIsDisplayed()
    }
    @Test fun narrowComposerPreservesControlsAtTwoHundredPercentFont() {
        val vm = sessionFixtureVm("s", session())
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(ThemeMode.DARK) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, 2f)) {
                    Column(Modifier.testTag("frame").width(320.dp).background(pal.paper)) {
                        Segmented(listOf("聊天", "终端"), 0, {}, dot = 1)
                        Composer("s", state, vm, {}, {})
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
        ui.setContent { RaTheme(ThemeMode.LIGHT) { Segmented(listOf("聊天", "终端"), selected, { selected = it }, enabled = { it == 0 || enabled }) } }
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
                    Column(Modifier.testTag("frame").width(320.dp).height(640.dp).background(pal.paper)) {
                        GroupTitle("今天", date = true)
                        EndedRow(
                            io.github.lewin671.remoteagent.core.proto.EndedRowView.newBuilder()
                                .setSessionId("s")
                                .setMachineId(machineId)
                                .setTitle(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("test"))
                                .setRight(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("12:00"))
                                .build(),
                            onClick = {},
                        )
                        NewSessionBox(
                            io.github.lewin671.remoteagent.core.proto.NewSessionViewModel.newBuilder()
                                .setProjectKey(key)
                                .setProjectName(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("remote-agent-with-a-very-long-project-name"))
                                .setConfig(io.github.lewin671.remoteagent.core.proto.NewSessionConfig.newBuilder().setMachineId(machineId).setModel("long-model-name"))
                                .setModelLabel(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("long-model-name"))
                                .build(),
                            state,
                            focus = false,
                        )
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
                    NewSettingsSheet(state.copy(conn = ConnState.ONLINE, prefs = PrefsObject.EMPTY), key, "remote-agent", machineId, Agent.AGENT_CODEX, onDone = {}, onClose = {}, scheduling = true)
                }
            }
        }
        ui.onNodeWithText("电脑").assertIsDisplayed()
        ui.onNodeWithText("权限").performScrollTo().assertIsDisplayed()
    }
    private fun approval(lines: Int) = ApprovalRequested.newBuilder().setApprovalId("A$lines")
        .setCommand((1..lines).joinToString("\n") { "echo step-$it" }).setDescription("说明文字".repeat(30)).build()

    @Test fun aWaitingItemOpensWithOneHeadLineAndFoldsByItsId() {
        DockFolds.clear()
        val a = approval(40)
        val vm = sessionFixtureVm(
            "s",
            session(),
            events = listOf(Event.newBuilder().setSeq(1).setApprovalRequested(a).build()),
            live = LiveSnapshot.newBuilder().setSession(session().info).addActions(Event.newBuilder().setSeq(1).setApprovalRequested(a)).build(),
        )
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, 1.3f)) {
                    Column(Modifier.testTag("frame").width(320.dp).height(640.dp).background(pal.paper)) {
                        Spacer(Modifier.weight(1f))
                        CompositionLocalProvider(LocalDockMax provides 384.dp) { ApprovalDock("s", vm) {} }
                    }
                }
            }
        }
        // Open by itself: one head line, the command before the description, the buttons in view.
        ui.onNodeWithText("收起", substring = true).assertIsDisplayed()
        ui.onAllNodesWithText("审批", substring = true).assertCountEquals(1)
        ui.onNodeWithText("允许").assertIsDisplayed()
        val command = ui.onNodeWithText("echo step-1", substring = true).fetchSemanticsNode().positionInRoot
        val description = ui.onNodeWithText("说明文字", substring = true).fetchSemanticsNode().positionInRoot
        assertTrue("command before description", command.y < description.y)
        // The body scrolls inside about 60%; the last line of the command is reachable.
        ui.onNodeWithText("说明文字", substring = true).performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("收起", substring = true).performClick()
        ui.onNodeWithText("处理", substring = true).assertIsDisplayed()
        ui.onNodeWithText("允许").assertDoesNotExist()
        ui.runOnIdle { assertTrue(!DockFolds.isOpen("s", "${vm.dock.kind.name}:${vm.dock.itemId}")) }
        DockFolds.clear()
    }

    @Test fun aPlanOpensItsPageFromItsHeadLine() {
        DockFolds.clear()
        var full = 0
        val markdown = "# 计划：在 run-P 目录创建 5 个 beta 前缀文件\n\n## Context\n\n用户要求用前缀 beta 创建 5 个文本文件。\n\n## 步骤\n\n" +
            (1..30).joinToString("\n") { "${it}. 第 $it 步" }
        val planSession = SessionView("s", machineId, session().info!!.toBuilder().setPlanOpen(true).build())
        val vm = sessionFixtureVm(
            "s",
            planSession,
            events = listOf(Event.newBuilder().setSessionId("s").setSeq(1).setPlanProposed(PlanProposed.newBuilder().setText(markdown)).build()),
            live = LiveSnapshot.newBuilder().setSession(planSession.info).setPlanEventSeq(1).setPlanScreen(markdown).build(),
        )
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.testTag("frame").width(360.dp).height(640.dp).background(pal.paper)) {
                        Spacer(Modifier.weight(1f))
                        PlanDock("s", vm) { full++ }
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

    /** The home's dark card for [a], online with the computer there unless [unlinked]. */
    private fun showCard(a: ApprovalRequested, dark: Boolean = false, unlinked: Boolean = false) {
        val online = RemoteState(machines = mapOf(machineId to MachineView(machineId, true, 0)), conn = ConnState.ONLINE, link = Link.ONLINE, synced = true, loaded = true)
        val st = if (unlinked) online.copy(conn = ConnState.OFFLINE, link = Link.NO_NETWORK) else online
        val card = io.github.lewin671.remoteagent.core.proto.AttentionCardView.newBuilder()
            .setSessionId("s")
            .setMachineId(machineId)
            .setApprovalId(a.approvalId)
            .setTitle(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("Approval"))
            .setKindText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("请求运行命令"))
            .setWhere(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("remote-agent"))
            .setBody(a.command)
            .setToolName(a.toolName)
            .setIsCommand(a.command.isNotEmpty())
            .setTruncated(false)
            .apply { if (unlinked) setTime(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey(UiMessages.listStateUnlinked().key)) }
            .build()
        ui.setContent {
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.testTag("frame").width(360.dp).background(pal.paper).padding(16.dp)) {
                        ApprovalCard(card, st, onOpen = {})
                    }
                }
            }
        }
    }

    @Test fun aCardCutsACommandAtWholeLinesAndOffersNoAllowUntilItIsOpen() {
        showCard(approval(20))
        ui.onNodeWithText("还有 15 行没显示").assertIsDisplayed()
        ui.onNodeWithText("允许").assertDoesNotExist()
        // Five whole lines, the last ending in …, so the cut shows.
        val cut = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        ui.onNodeWithTag("command", useUnmergedTree = true)
            .performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult) { it(cut) }
        assertEquals(CARD_LINES, cut.single().lineCount)
        assertTrue("last line ellipsized", cut.single().isLineEllipsized(CARD_LINES - 1))
        screenshot("card-command-cut-light")
        ui.onNodeWithText("看完整命令").performClick()
        ui.onNodeWithText("共 20 行").assertIsDisplayed()
        ui.onNodeWithText("允许").assertIsDisplayed().assertIsEnabled()
        ui.onNodeWithText("收起").performClick()
        ui.onNodeWithText("还有 15 行没显示").assertIsDisplayed()
    }

    @Test fun aShortCommandCanBeAllowedAndDenyingAsksForAReasonInPlace() {
        showCard(approval(2), dark = true)
        ui.onNodeWithText("没显示", substring = true).assertDoesNotExist()
        ui.onNodeWithText("允许").assertIsDisplayed().assertIsEnabled()
        ui.onNodeWithText("拒绝").performClick()
        ui.onNodeWithContentDescription("拒绝的理由").assertIsDisplayed()
        ui.onNodeWithText("取消").assertIsDisplayed()
        ui.onNodeWithText("允许").assertDoesNotExist()
        screenshot("card-deny-dark")
        ui.onNodeWithText("取消").performClick()
        ui.onNodeWithText("允许").assertIsDisplayed()
    }

    @Test fun outOfReachTheCardStaysButCannotBeDecided() {
        showCard(approval(2), unlinked = true)
        ui.onNodeWithText("未连接").assertIsDisplayed()
        ui.onNodeWithText("允许").assertIsNotEnabled()
        ui.onNodeWithText("拒绝").assertIsNotEnabled()
    }

    @Test fun theDockKeepsToTheChatWidthOnATablet() {
        val s = session()
        val a = approval(1)
        val appVm = sessionFixtureVm("s", s, events = listOf(Event.newBuilder().setSeq(1).setApprovalRequested(a).build()),
            live = LiveSnapshot.newBuilder().setSession(s.info).addActions(Event.newBuilder().setSeq(1).setApprovalRequested(a)).build())
        val planVm = sessionFixtureVm("s", SessionView("s", machineId, s.info!!.toBuilder().setPlanOpen(true).build()),
            events = listOf(Event.newBuilder().setSeq(1).setPlanProposed(PlanProposed.newBuilder().setText("## Plan\n1. step")).build()))
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.testTag("frame").requiredWidth(1000.dp).background(pal.paper)) {
                        ApprovalDock("s", appVm) {}
                        PlanDock("s", planVm) {}
                    }
                }
            }
        }
        val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
        with(ui.density) {
            for (label in listOf("允许", "批准执行")) {
                val b = ui.onNodeWithText(label).fetchSemanticsNode().boundsInRoot
                assertTrue("$label ${(b.right - frame.left).toDp()}", (b.right - frame.left).toDp() <= (1000.dp + ChatMax) / 2)
                assertTrue("$label ${(b.left - frame.left).toDp()}", (b.left - frame.left).toDp() >= (1000.dp - ChatMax) / 2)
            }
        }
    }

    @Test fun wideSettingsKeepAReadableColumnAndACompactThemePicker() {
        ui.setContent {
            RaTheme(ThemeMode.DARK) {
                // Wider than the emulator's screen: laid out, if not all drawn.
                Column(Modifier.testTag("frame").requiredWidth(1000.dp).background(pal.gbg)) {
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
    @Test fun machineCardKeepsStatusAndQuotaUnderTheNameWithLargeFont() {
        val name = "A very long workstation name that must leave room for status"
        var dark by mutableStateOf(false)
        var online by mutableStateOf(true)
        fun cardVm(isOnline: Boolean): io.github.lewin671.remoteagent.core.proto.SettingsViewModel {
            val row = io.github.lewin671.remoteagent.core.proto.QuotaRowView.newBuilder()
                .setHasPercent(true)
                .setRemainingPercent(38)
                .setRemainingText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("剩 38%"))
                .setResetText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral("63 小时 5 分钟后重置"))
                .build()
            val card = io.github.lewin671.remoteagent.core.proto.MachineListCardView.newBuilder()
                .setMachineId(machineId)
                .setName(name)
                .setOnline(isOnline)
                .setStatusText(
                    io.github.lewin671.remoteagent.core.proto.Text.newBuilder()
                        .setLiteral(if (isOnline) "在线" else "2 小时前在线"),
                )
                .addQuotaSummaries(
                    io.github.lewin671.remoteagent.core.proto.QuotaCliSection.newBuilder()
                        .setAgent(Agent.AGENT_CODEX)
                        .setAgentName("Codex")
                        .setSummaryRow(row),
                )
                .build()
            return io.github.lewin671.remoteagent.core.proto.SettingsViewModel.newBuilder()
                .addMachines(card)
                .setCanAddDevice(true)
                .build()
        }
        ui.setContent {
            val density = LocalDensity.current
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(density.density, 2f)) {
                    Column(Modifier.width(320.dp).testTag("frame")) {
                        MachinesGroup(cardVm(online))
                    }
                }
            }
        }
        for (theme in listOf(false, true)) {
            ui.runOnIdle { dark = theme }
            for (connected in listOf(true, false)) {
                ui.runOnIdle { online = connected }
                val status = ui.onNodeWithText(if (connected) "在线" else "2 小时前在线", useUnmergedTree = true).assertIsDisplayed().fetchSemanticsNode().boundsInRoot
                val title = ui.onNodeWithText(name, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                val frame = ui.onNodeWithTag("frame").fetchSemanticsNode().boundsInRoot
                assertTrue("status overlaps title", status.top >= title.bottom)
                assertTrue("title outside card", title.right <= frame.right)
                assertTrue("status outside card", status.right <= frame.right)
                if (connected) {
                    val quota = ui.onNodeWithText("剩 38%", useUnmergedTree = true).assertIsDisplayed().fetchSemanticsNode().boundsInRoot
                    assertTrue("quota above status", quota.top >= status.bottom)
                    assertTrue("quota outside card", quota.right <= frame.right)
                    // A long reset time goes under the bar, so it never squeezes the CLI's name.
                    val reset = ui.onNodeWithText("重置", substring = true, useUnmergedTree = true).assertIsDisplayed().fetchSemanticsNode().boundsInRoot
                    assertTrue("reset beside the name", reset.top >= quota.bottom)
                } else {
                    ui.onNodeWithText("剩 38%", useUnmergedTree = true).assertDoesNotExist()
                }
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
