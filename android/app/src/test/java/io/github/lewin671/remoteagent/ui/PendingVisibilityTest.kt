package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import org.junit.*
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class PendingVisibilityTest {
    @get:Rule(order = 0) val appFixture = io.github.lewin671.remoteagent.RaAppTestRule(preparePendingStore = true)
    @get:Rule(order = 1) val ui = createComposeRule()
    @get:Rule(order = 2) val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test-client"), Api.defaultHttp())
    private val session = SessionView("s", "m", Session.newBuilder().setId("s").setCwd("/example/demo").setAgent(Agent.AGENT_CODEX).setStatus(SessionStatus.SESSION_STATUS_RUNNING).build())
    private val state = RemoteState(conn = io.github.lewin671.remoteagent.core.ConnState.ONLINE, loaded = true, machines = mapOf("m" to MachineView("m", true, 0)), sessions = mapOf("s" to session))

    @After fun close() { remote.close(); Outbox.clear(); ImageDrafts.clear() }

    private fun vmWithTray(item: SessionTrayItemView?, paused: Boolean = false): SessionViewModel {
        val tl = SessionTimelineView.newBuilder().setLoaded(true)
        for (i in 1..12) {
            tl.addItems(
                SessionTimelineItem.newBuilder()
                    .setId("e$i").setSeq(i.toLong())
                    .setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown("Reply $i\nA second line of history.")),
            )
        }
        val composer = SessionComposerView.newBuilder().setEnabled(true).setHasActiveTurn(true)
        if (item != null) {
            val gKind = if (paused) SessionTrayGroupKind.SESSION_TRAY_GROUP_KIND_PAUSED else SessionTrayGroupKind.SESSION_TRAY_GROUP_KIND_QUEUE
            val gKey = if (paused) UiMessages.queueGroupPaused().key else UiMessages.queueGroupQueue().key
            composer.setPendingTray(
                SessionPendingTrayView.newBuilder()
                    .setTotalCount(1)
                    .addOrderedItems(item)
                    .addGroups(
                        SessionTrayGroupView.newBuilder()
                            .setKind(gKind)
                            .setTone(SessionTrayTone.SESSION_TRAY_TONE_NEUTRAL)
                            .setTitle(Text.newBuilder().setKey(gKey))
                            .setHeaderText(Text.newBuilder().setKey(gKey))
                            .setTotalCount(1)
                            .addItems(item),
                    ),
            )
        }
        return SessionViewModel.newBuilder()
            .setSessionId("s")
            .setHeader(SessionHeaderView.newBuilder().setFound(true).setMachineId("m").setComputerOnline(true).setAgent(Agent.AGENT_CODEX).setAgentName("Codex"))
            .setComposer(composer)
            .setTimeline(tl)
            .build()
    }

    @Test fun dockedTrayAndGrowingActionsStayInShortViewport() {
        val baseVm = vmWithTray(null)
        remote.showForTest(state, sessions = mapOf("s" to baseVm))
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})) {
                    val vm by remote.watchSession("s").collectAsState(initial = baseVm)
                    Box(Modifier.height(360.dp)) {
                        ChatPane("s", state, vm, Modifier, {}, {}, {})
                    }
                }
            }
        }
        ui.waitUntil(5000) { ui.onAllNodesWithText("Reply 12", substring = true).fetchSemanticsNodes().isNotEmpty() }
        ui.waitForIdle()
        val queued = SessionTrayItemView.newBuilder()
            .setRequestId("queued")
            .setGroup(SessionTrayGroupKind.SESSION_TRAY_GROUP_KIND_QUEUE)
            .setTone(SessionTrayTone.SESSION_TRAY_TONE_NEUTRAL)
            .setActivity(SessionTrayRowActivity.SESSION_TRAY_ROW_ACTIVITY_IDLE)
            .setQueueNumber(1)
            .setText("Queued message line")
            .setMode(SendMode.SEND_MODE_QUEUE)
            .setCanCancel(true)
            .setCanPromote(true)
            .setCanRestore(true)
            .build()
        ui.runOnIdle { remote.showForTest(state, sessions = mapOf("s" to vmWithTray(queued, paused = false))) }
        ui.waitForIdle()
        ui.onNodeWithText("Queued message line", substring = true).assertIsDisplayed()
        ui.onNodeWithContentDescription("取消").assertIsDisplayed()
        val paused = queued.toBuilder()
            .setGroup(SessionTrayGroupKind.SESSION_TRAY_GROUP_KIND_PAUSED)
            .setQueueNumber(0)
            .setCanResume(true)
            .setCanPromote(false)
            .build()
        ui.runOnIdle { remote.showForTest(state, sessions = mapOf("s" to vmWithTray(paused, paused = true))) }
        ui.waitForIdle()
        ui.onNodeWithText("发送", substring = false).assertIsDisplayed()
        ui.onNodeWithContentDescription("取消").assertIsDisplayed()
    }
}
