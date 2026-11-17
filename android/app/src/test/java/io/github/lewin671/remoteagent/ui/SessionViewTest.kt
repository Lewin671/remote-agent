package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToIndex
import io.github.lewin671.remoteagent.RaAppTestRule
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Link
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.coreTextToMessageText
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.robolectric.annotation.Config

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class SessionViewTest {
    @get:Rule(order = 0) val appFixture = RaAppTestRule(preparePendingStore = true)
    @get:Rule(order = 1) val sessionMessages = SessionMessageContextRule()
    @get:Rule(order = 2) val ui = createComposeRule()
    @get:Rule(order = 3) val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }

    @Test
    fun aPlanOfMoreLinesThanItsPreviewCanBeOpenedHoweverItLaysOut() {
        assertEquals(true, planFolds((1..7).joinToString("\n") { "$it. 步骤" }, 6))
        // One long paragraph is a single line to count: whether it is cut is the layout's to say.
        assertEquals(false, planFolds("先把筛选条件抽成一个 hook，".repeat(40), 6))
    }

    @Test
    fun coreSessionGoldensRenderDockComposerTimelineAndWindowedHistory() {
        val approval = ScreenGoldenLoader.sessionView("session_approval_flow", "pendingApprovalDock")
        val question = ScreenGoldenLoader.sessionView("session_question_plan", "questionStep0")
        val plan = ScreenGoldenLoader.sessionView("session_question_plan", "planDockOpen")
        val queueRetry = ScreenGoldenLoader.sessionView("session_queue_retry", "queuedPendingAndFailedOutgoing")
        val longTail = ScreenGoldenLoader.sessionView("session_long_window", "tail10")
        val cliErr = ScreenGoldenLoader.sessionView("session_cli_error_turn", "cliErrorTurns")
        val folded = ScreenGoldenLoader.sessionView("session_scheduled_fold", "foldedQuietRuns")

        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test"), Api.defaultHttp())
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        try {
            val st = RemoteState(
                conn = ConnState.ONLINE,
                link = Link.ONLINE,
                loaded = true,
                synced = true,
                machines = mapOf("m1" to MachineView("m1", true, 0)),
            )
            remote.showForTest(st, sessions = mapOf(approval.sessionId to approval))
            ui.setContent {
                RaTheme(ThemeMode.LIGHT) {
                    CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                        Box(Modifier.fillMaxSize()) {
                            SessionScreen(approval.sessionId)
                        }
                    }
                }
            }
            ui.waitForIdle()
            ui.onNodeWithText("Approval test").assertIsDisplayed()
            ui.onNodeWithText("允许").assertIsDisplayed()

            ui.runOnIdle { remote.showForTest(st, sessions = mapOf(approval.sessionId to question)) }
            ui.waitForIdle()
            ui.onNodeWithText(question.dock.question.question.questionText, substring = true).assertIsDisplayed()

            ui.runOnIdle { remote.showForTest(st, sessions = mapOf(approval.sessionId to plan)) }
            ui.waitForIdle()
            ui.onAllNodesWithText(plan.dock.plan.markdown.lines().first().trimStart('#', ' '), substring = true)[0].assertIsDisplayed()

            ui.runOnIdle { remote.showForTest(st, sessions = mapOf(approval.sessionId to queueRetry)) }
            ui.waitForIdle()
            if (queueRetry.composer.pendingInputsCount > 0) {
                ui.onNodeWithText(queueRetry.composer.getPendingInputs(0).text, substring = true).assertIsDisplayed()
            }

            ui.runOnIdle { remote.showForTest(st, sessions = mapOf(approval.sessionId to longTail)) }
            ui.waitForIdle()
            if (longTail.timeline.canLoadEarlier) {
                ui.onNode(hasScrollToIndexAction()).performScrollToIndex(0)
                ui.onNodeWithText("更早").assertIsDisplayed()
            }

            ui.runOnIdle { remote.showForTest(st, sessions = mapOf(approval.sessionId to cliErr)) }
            ui.waitForIdle()
            val errLine = cliErr.timeline.itemsList.first { it.hasErrorLine() }
            assertEquals(true, errLine.errorLine.rawReason.isNotEmpty() || errLine.errorLine.hasText())

            ui.runOnIdle { remote.showForTest(st, sessions = mapOf(approval.sessionId to folded)) }
            ui.waitForIdle()
            val quietIdx = folded.timeline.itemsList.indexOfFirst { it.hasQuietScheduledGroup() } +
                if (folded.timeline.hasEarlier || folded.timeline.canLoadEarlier || folded.timeline.backfilling) 1 else 0
            ui.onNode(hasScrollToIndexAction()).performScrollToIndex(quietIdx)
            val quiet = folded.timeline.itemsList.first { it.hasQuietScheduledGroup() }
            val quietLabel = coreTextToMessageText(quiet.quietScheduledGroup.title).resolve(::messageTextForTest)
            ui.onNodeWithText(quietLabel, substring = true).performClick()
        } finally {
            remote.close()
        }
    }
}
