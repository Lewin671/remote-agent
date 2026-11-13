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
    private val state = RemoteState(conn = ConnState.ONLINE, loaded = true, machines = mapOf("m" to MachineView("m", true, 0)), sessions = mapOf("s" to session))
    private val history = (1..12).map { Event.newBuilder().setSeq(it.toLong()).setAgentMessage(AgentMessage.newBuilder().setText("Reply $it\nA second line of history.")).build() }

    @After fun close() { remote.close(); Outbox.clear(); ImageDrafts.clear() }

    @Test fun queuedRowAndItsGrowingActionsStayInShortViewport() {
        remote.showForTest(state, events = mapOf("s" to history))
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})) {
                    val current by remote.events("s").collectAsState()
                    // The available chat height above a real keyboard, without testing fake IME insets.
                    Box(Modifier.height(320.dp)) {
                        ChatPane(state, session, Status.RUNNING, current.events, derive(current.events), true, null, null, Modifier, {})
                    }
                }
            }
        }
        ui.waitUntil(5000) { ui.onAllNodesWithText("Reply 12", substring = true).fetchSemanticsNodes().isNotEmpty() }
        ui.waitForIdle()
        val queued = Event.newBuilder().setSeq(13).setInputState(InputState.newBuilder().setRequestId("queued").setText("Queued message\nline two\nline three\nline four").setStatus(InputStatus.INPUT_STATUS_QUEUED).setMode(SendMode.SEND_MODE_QUEUE)).build()
        ui.runOnIdle { remote.showForTest(state, events = mapOf("s" to history + queued)) }
        ui.waitForIdle()
        fun trace(label: String) {
            println("$label " + ui.onRoot().printToString())
        }
        trace("queued layout")
        ui.onNodeWithText("Queued message", substring = true).assertIsDisplayed()
        ui.onNodeWithText("取消").assertIsDisplayed()
        val paused = queued.toBuilder().setInputState(queued.inputState.toBuilder().setPaused(true)).build()
        ui.runOnIdle { remote.showForTest(state, events = mapOf("s" to history + paused)) }
        ui.waitForIdle()
        trace("paused layout")
        ui.onNodeWithText("发送", substring = false).assertIsDisplayed()
        ui.onNodeWithText("取消").assertIsDisplayed()
    }
}
