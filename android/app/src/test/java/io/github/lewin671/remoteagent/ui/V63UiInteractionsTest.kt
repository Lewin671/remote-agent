package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.showForTest

import android.app.Application
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsFocused
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.click
import io.github.lewin671.remoteagent.RaAppTestRule
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Link
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.proto.HomeQuestionCardView
import io.github.lewin671.remoteagent.core.proto.HomeQuestionOptionView
import io.github.lewin671.remoteagent.core.proto.SessionCommandState
import io.github.lewin671.remoteagent.core.proto.SessionCommandStatus
import io.github.lewin671.remoteagent.core.proto.SessionDockKind
import io.github.lewin671.remoteagent.core.proto.SessionDockView
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.core.proto.SessionDirectChipView
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class V63UiInteractionsTest {
    @get:Rule(order = 0) val appFixture = RaAppTestRule(preparePendingStore = true)
    @get:Rule(order = 1) val sessionMessages = SessionMessageContextRule()
    @get:Rule(order = 2) val ui = createComposeRule()
    @get:Rule(order = 3) val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }

    private fun coreLit(s: String) = CoreText.newBuilder().setLiteral(s).build()

    private fun sampleQuestion(canAnswer: Boolean = true, offlineNote: String = "") =
        HomeQuestionCardView.newBuilder()
            .setSessionId("s-q1")
            .setMachineId("m1")
            .setQuestionId("q1")
            .setObservedEventSeq(42L)
            .setBadgeText(coreLit("需要选择"))
            .setTimeText(coreLit("刚刚"))
            .setTitle(coreLit("确认方案"))
            .setWhere(coreLit("Mac Studio · api"))
            .setHeader("实现方式")
            .setQuestionText("选择缓存失效策略？")
            .addOptions(
                HomeQuestionOptionView.newBuilder()
                    .setOptionNum(1)
                    .setLabel("LRU")
                    .setDescription("默认推荐")
                    .setRecommended(true),
            )
            .addOptions(
                HomeQuestionOptionView.newBuilder()
                    .setOptionNum(2)
                    .setLabel("FIFO")
                    .setDescription("按写入顺序"),
            )
            .setCanAnswer(canAnswer)
            .apply { if (offlineNote.isNotEmpty()) setOfflineNote(coreLit(offlineNote)) }
            .build()

    @After
    fun cleanDrafts() {
        Drafts.clear()
    }

    @Test
    fun homeQuestionSlotHandlesTerminalOfflineFailureRetryAndElsewhereExit() {
        val remote = Remote(Account("http://127.0.0.1:1", "test"), Api.defaultHttp())
        var terminalOpened = ""
        val cardState = mutableStateOf<HomeQuestionCardView?>(sampleQuestion(canAnswer = false, offlineNote = "Mac Studio 离线，无法作答"))
        val baseState = RemoteState(conn = ConnState.ONLINE, link = Link.ONLINE, loaded = true, synced = true)
        val baseVm = SessionViewModel.newBuilder().setSessionId("s-q1").build()
        try {
            remote.showForTest(baseState, sessions = mapOf("s-q1" to baseVm))
            ui.setContent {
                RaTheme(ThemeMode.LIGHT) {
                    CompositionLocalProvider(LocalRemote provides remote) {
                        Column(Modifier.verticalScroll(rememberScrollState())) {
                            HomeQuestionSlot(
                                question = cardState.value,
                                onOpenSession = {},
                                onOpenTerminal = { terminalOpened = it },
                            )
                        }
                    }
                }
            }
            ui.waitForIdle()
            ui.onNodeWithText("选择缓存失效策略？").assertIsDisplayed()
            ui.onNodeWithText("LRU · 推荐").assertIsDisplayed()
            ui.onNodeWithText("Mac Studio 离线，无法作答").performScrollTo().assertIsDisplayed()

            // Offline option cannot submit; terminal fallback link works.
            ui.onNodeWithText("去终端回答").performScrollTo().performClick()
            ui.waitForIdle()
            assertEquals("s-q1", terminalOpened)

            // Bring card online and submit option 1, then simulate backend failure -> Retry.
            cardState.value = sampleQuestion(canAnswer = true)
            ui.waitForIdle()
            ui.mainClock.autoAdvance = false
            ui.onNodeWithText("LRU · 推荐").performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.OnClick)
            ui.mainClock.advanceTimeBy(160)
            ui.waitForIdle()

            ui.mainClock.autoAdvance = true
            val busyVm = baseVm.toBuilder()
                .setDock(
                    SessionDockView.newBuilder()
                        .setHasItem(true)
                        .setKind(SessionDockKind.SESSION_DOCK_KIND_QUESTION)
                        .setItemId("q1")
                        .setCommand(
                            SessionCommandState.newBuilder()
                                .setBusy(true)
                                .setStatus(SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING),
                        ),
                )
                .build()
            remote.showForTest(baseState, sessions = mapOf("s-q1" to busyVm))
            ui.waitForIdle()

            val failedVm = busyVm.toBuilder()
                .setDock(
                    busyVm.dock.toBuilder()
                        .setCommand(
                            SessionCommandState.newBuilder()
                                .setBusy(false)
                                .setStatus(SessionCommandStatus.SESSION_COMMAND_STATUS_FAILED)
                                .setErrorText(coreLit("网络超时")),
                        ),
                )
                .build()
            remote.showForTest(baseState, sessions = mapOf("s-q1" to failedVm))
            ui.waitUntil(3000) { ui.onAllNodesWithText("网络超时", substring = true).fetchSemanticsNodes().isNotEmpty() }
            ui.onNodeWithText("网络超时", substring = true).performScrollTo().assertIsDisplayed()
            ui.onNodeWithText("重试").performScrollTo().assertIsDisplayed()

            // External disappearance during error shows "已在另一端回答" for 900ms, then exits after 160ms.
            ui.mainClock.autoAdvance = false
            ui.runOnIdle { cardState.value = null }
            ui.waitForIdle()
            ui.mainClock.advanceTimeBy(50)
            ui.waitForIdle()
            ui.onNodeWithText("已在另一端回答").assertExists()
            ui.mainClock.advanceTimeBy(950)
            ui.waitForIdle()
            ui.mainClock.advanceTimeBy(250)
            ui.waitForIdle()
            ui.onAllNodesWithText("已在另一端回答").assertCountEquals(0)
        } finally {
            remote.close()
        }
    }

    @Test
    fun homeQuestionSlotShowsDeliveredLineWhenAnsweredLocally() {
        val remote = Remote(Account("http://127.0.0.1:1", "test"), Api.defaultHttp())
        val cardState = mutableStateOf<HomeQuestionCardView?>(sampleQuestion(canAnswer = true))
        val baseState = RemoteState(conn = ConnState.ONLINE, link = Link.ONLINE, loaded = true, synced = true)
        val baseVm = SessionViewModel.newBuilder().setSessionId("s-q1").build()
        try {
            remote.showForTest(baseState, sessions = mapOf("s-q1" to baseVm))
            ui.setContent {
                RaTheme(ThemeMode.LIGHT) {
                    CompositionLocalProvider(LocalRemote provides remote) {
                        Column(Modifier.verticalScroll(rememberScrollState())) {
                            HomeQuestionSlot(
                                question = cardState.value,
                                onOpenSession = {},
                                onOpenTerminal = {},
                            )
                        }
                    }
                }
            }
            ui.waitForIdle()
            ui.mainClock.autoAdvance = false
            ui.onNodeWithText("FIFO").performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.OnClick)
            ui.mainClock.advanceTimeBy(50)
            ui.waitForIdle()
            // Disappears from pending list after local answer succeeds -> "已回答 · FIFO"
            cardState.value = null
            ui.mainClock.advanceTimeBy(50)
            ui.waitForIdle()
            ui.onNodeWithText("已回答 · FIFO").assertIsDisplayed()
            ui.mainClock.advanceTimeBy(950)
            ui.waitForIdle()
            ui.mainClock.advanceTimeBy(250)
            ui.waitForIdle()
            ui.onAllNodesWithText("已回答 · FIFO").assertCountEquals(0)
        } finally {
            remote.close()
        }
    }

    @Test
    fun composeBoxRunningDraftTogglesModePreservesInputAndDemotesSend() {
        var text by mutableStateOf("check logs")
        var demoted by mutableStateOf(false)
        var sends = 0
        var queues = 0
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                ComposeBox(
                    text,
                    { text = it },
                    "消息",
                    true,
                    { sends++ },
                    {},
                    onQueue = { queues++ },
                    demoted = demoted,
                )
            }
        }
        val field = ui.onNode(hasSetTextAction())
        field.performTouchInput { click() }
        field.assertIsFocused()

        val sendAfter = ui.onNodeWithText("做完再发")
        sendAfter.assertIsDisplayed()
        field.performTextInput(" now")
        ui.waitForIdle()
        assertEquals("check logs now", text)

        sendAfter.performClick()
        demoted = true
        ui.waitForIdle()
        ui.onNodeWithContentDescription("发送").performClick()
        assertEquals(1, sends)
        assertEquals(1, queues)
    }

    @Test
    fun approvalButtonsShowBusyDenyRetryDraftAndScrollToBottomGate() {
        Drafts.clear()
        val replyState = mutableStateOf(
            ApprovalReply(
                cmdState = null,
                chosenAllow = null,
                message = "",
                always = false,
                send = { _, _, _ -> },
            ),
        )
        var denying by mutableStateOf(false)
        var scrollNeeded by mutableStateOf(true)
        var scrolledToBottom = 0
        var allowClicked = 0
        val baseReply = ApprovalReply(
            cmdState = null,
            chosenAllow = null,
            message = "",
            always = false,
            send = { allow, _, _ -> if (allow) allowClicked++ },
        )
        replyState.value = baseReply

        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                DarkCard {
                    ApprovalButtons(
                        approvalId = "a-test",
                        d = replyState.value,
                        denying = denying,
                        onDenying = { denying = it },
                        locked = false,
                        truncated = false,
                        always = false,
                        alwaysLabel = "总是允许",
                        scrollToBottomNeeded = scrollNeeded,
                        onScrollToBottom = { scrolledToBottom++ },
                    )
                }
            }
        }

        // Scroll-to-bottom gate blocks allow until scrolled to bottom.
        ui.onNodeWithText("往下看完整命令").assertIsDisplayed().performClick()
        assertEquals(1, scrolledToBottom)
        assertEquals(0, allowClicked)

        ui.runOnUiThread { scrollNeeded = false }
        ui.waitForIdle()
        ui.onNodeWithText("允许").assertIsDisplayed()

        // Enter deny mode and preserve draft reason through busy + failed transitions.
        ui.onNodeWithText("拒绝").performClick()
        ui.onNode(hasSetTextAction()).performTextInput("危险命令")
        assertEquals("危险命令", Drafts.of("deny:a-test").value)

        ui.runOnUiThread {
            replyState.value = ApprovalReply(
                cmdState = SessionCommandState.newBuilder()
                    .setBusy(true)
                    .setStatus(SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING)
                    .build(),
                chosenAllow = false,
                message = "危险命令",
                always = false,
                send = { _, _, _ -> },
            )
        }
        ui.waitForIdle()
        ui.onNodeWithText("正在拒绝…").assertIsDisplayed()
        ui.onNodeWithText("危险命令").assertIsDisplayed()
        ui.onNodeWithText("取消").assertIsNotEnabled()

        ui.runOnUiThread {
            replyState.value = ApprovalReply(
                cmdState = SessionCommandState.newBuilder()
                    .setBusy(false)
                    .setStatus(SessionCommandStatus.SESSION_COMMAND_STATUS_FAILED)
                    .setErrorText(coreLit("发送失败"))
                    .build(),
                chosenAllow = false,
                message = "危险命令",
                always = false,
                send = { _, _, _ -> },
            )
        }
        ui.waitForIdle()
        ui.onNodeWithText("重试拒绝").assertIsDisplayed()
        ui.onNodeWithText("发送失败", substring = true).assertIsDisplayed()
        assertEquals("危险命令", Drafts.of("deny:a-test").value)
    }

    @Test
    fun sessionTopBarDirectBadgeOpensPopoverSurvivesRelayFallbackAndNavigatesToConnection() {
        val baseVm = ScreenGoldenLoader.sessionView("session_approval_flow", "pendingApprovalDock")
        val state = RemoteState(
            conn = ConnState.ONLINE,
            link = Link.ONLINE,
            loaded = true,
            synced = true,
            machines = mapOf("m1" to MachineView("m1", online = true, lastSeenMs = 0L)),
        )
        val remote = Remote(Account("http://127.0.0.1:1", "test"), Api.defaultHttp())
        var connectionOpened = ""
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {}, deviceConnection = { connectionOpened = it })
        val directChip = SessionDirectChipView.newBuilder()
            .setVisible(true)
            .setActive(true)
            .setReadDirect(true)
            .setFileDirect(true)
            .setLabelText(coreLit("直连"))
            .setPopoverTitle(coreLit("已通过直连连接到电脑 · 14 ms"))
            .setPopoverBody(coreLit("加载与文件走直连；发消息和审批同时经直连和中继发出，以先到的为准。"))
            .setPopoverFoot(coreLit("直连稳定"))
            .build()
        val directHeader = baseVm.header.toBuilder()
            .setMachineId("m1")
            .setComputerOnline(true)
            .setComputerUnlinked(false)
            .setDirectChip(directChip)
            .build()
        val directVm = baseVm.toBuilder().setHeader(directHeader).build()
        val relayHeader = baseVm.header.toBuilder()
            .setMachineId("m1")
            .setComputerOnline(true)
            .setComputerUnlinked(false)
            .clearDirectChip()
            .build()
        val relayVm = baseVm.toBuilder().setHeader(relayHeader).build()
        try {
            remote.showForTest(state, sessions = mapOf(directVm.sessionId to directVm))
            ui.setContent {
                RaTheme(ThemeMode.LIGHT) {
                    CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                        Box(Modifier.fillMaxSize()) {
                            SessionScreen(directVm.sessionId)
                        }
                    }
                }
            }
            ui.waitForIdle()
            val badge = ui.onNodeWithContentDescription("直连已生效，点按查看连接详情")
            badge.assertIsDisplayed().performClick()
            ui.onNodeWithText("加载与文件走直连；发消息和审批同时经直连和中继发出，以先到的为准。").assertIsDisplayed()

            // Mid-open drop to relay: popover remains open and reflects relay fallback copy.
            ui.runOnIdle { remote.showForTest(state, sessions = mapOf(relayVm.sessionId to relayVm)) }
            ui.waitForIdle()
            ui.onNodeWithText("已切回中继").assertIsDisplayed()

            ui.onNodeWithText("连接设置").performClick()
            assertEquals("m1", connectionOpened)
        } finally {
            remote.close()
        }
    }

    @Test
    fun dirtySheetConfirmsDiscardOnDismissIncludingDynamicDirtyTransition() {
        var dirty by mutableStateOf(false)
        var closes = 0
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Sheet(onClose = { closes++ }, dirty = dirty) {
                    Text("表单内容")
                }
            }
        }
        ui.waitForIdle()
        dirty = true
        ui.waitForIdle()

        val scrimMatcher = hasClickAction().and(
            SemanticsMatcher("sheet scrim") { node ->
                node.config.getOrNull(SemanticsProperties.ContentDescription)?.any { desc ->
                    desc.contains("sheet", ignoreCase = true) ||
                        desc.contains("关闭") ||
                        desc.contains("close", ignoreCase = true)
                } == true
            },
        )
        ui.onNode(scrimMatcher).performClick()
        ui.waitForIdle()
        ui.onNodeWithText("放弃还没保存的修改？").assertIsDisplayed()
        ui.onNodeWithText("继续编辑").performClick()
        assertEquals(0, closes)
        ui.onAllNodesWithText("放弃还没保存的修改？").assertCountEquals(0)
        ui.onNode(scrimMatcher).performClick()
        ui.waitForIdle()
        ui.onNodeWithText("放弃修改").performClick()
        assertEquals(1, closes)
    }
}
