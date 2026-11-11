package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.Context
import android.content.ContextWrapper
import io.github.lewin671.remoteagent.RaApp
import org.robolectric.RuntimeEnvironment
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import org.junit.*
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** The new task's page (design-system §6 新任务): where in the title, everything else in the box at the bottom. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class NewTaskPageTest {
    @get:Rule val ui = createComposeRule()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test-client"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val path = "/example/demo"

    private fun machine(id: String) = MachineView(id, true, 0, MachineMeta.newBuilder().setHostname("host-$id").setProtocolVersion(PROTOCOL_VERSION)
        .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX)).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).build())

    private fun show(vararg ids: String) {
        val state = RemoteState(conn = ConnState.ONLINE, loaded = true, synced = true, machines = ids.associateWith { machine(it) })
        remote.showForTest(state)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    NewSessionBox(state, NewConfig(ids.first(), path, Agent.AGENT_CODEX), "demo", focus = false, onProject = {})
                }
            }
        }
    }

    @Before fun imageStore() {
        // The composer owns an image draft even with no attachments. Set up its store here so
        // this screen's tests also run alone, without relying on another test creating RaApp.
        val app = RaApp()
        ContextWrapper::class.java.getDeclaredMethod("attachBaseContext", Context::class.java)
            .apply { isAccessible = true }.invoke(app, RuntimeEnvironment.getApplication())
        RaApp::class.java.getDeclaredField("instance").apply { isAccessible = true }.set(null, app)
    }
    @After fun close() { remote.close(); Drafts.clear(); ImageDrafts.clear() }

    @Test fun theTitleSaysWhereAndTheBoxHoldsTheRest() {
        show("a", "b")
        ui.onNodeWithTag("new-project", useUnmergedTree = true).assertExists()
        ui.onNodeWithText("demo", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-machine", useUnmergedTree = true).assertExists()
        ui.onNodeWithText("要做什么？", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-agent", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-model", useUnmergedTree = true).assertExists()
        ui.onNodeWithContentDescription("开始").assertExists()
        ui.onNodeWithText("新任务").assertDoesNotExist()
    }

    @Test fun cliMenuKeepsComposerAndTextFocusAndSameChipCloses() {
        show("a")
        val field = ui.onNode(hasSetTextAction())
        field.performClick()
        val box = ui.onNodeWithTag("new-composer").fetchSemanticsNode().boundsInRoot
        ui.onNodeWithTag("new-agent").performTouchInput { click() }
        ui.onNodeWithTag("new-agent-menu").assertExists()
        field.assertIsFocused()
        Assert.assertEquals(box, ui.onNodeWithTag("new-composer").fetchSemanticsNode().boundsInRoot)
        ui.onNodeWithTag("new-agent").performTouchInput { click() }
        ui.onNodeWithTag("new-agent-menu").assertDoesNotExist()
        ui.onNodeWithTag("new-agent").performClick()
        ui.onNodeWithText("Claude Code").performClick()
        ui.onNodeWithTag("new-agent-menu").assertDoesNotExist()
        ui.onNodeWithTag("new-agent").assertContentDescriptionEquals("CLI：Claude Code")
        field.assertIsFocused()
        Assert.assertEquals(box, ui.onNodeWithTag("new-composer").fetchSemanticsNode().boundsInRoot)
    }

    @Test fun modelPopoverKeepsComposerAndDismissesOutside() {
        show("a")
        val field = ui.onNode(hasSetTextAction())
        field.performClick()
        val box = ui.onNodeWithTag("new-composer").fetchSemanticsNode().boundsInRoot
        ui.onNodeWithTag("new-model").performClick()
        ui.onNodeWithTag("new-model-popover").assertExists()
        field.assertIsFocused()
        Assert.assertEquals(box, ui.onNodeWithTag("new-composer").fetchSemanticsNode().boundsInRoot)
        ui.onNodeWithTag("new-model").performTouchInput { click() }
        ui.onNodeWithTag("new-model-popover").assertDoesNotExist()
        ui.onNodeWithTag("new-model").performClick()
        field.performTouchInput { click() }
        ui.onNodeWithTag("new-model-popover").assertDoesNotExist()
        field.assertIsFocused()
    }

    @Test fun oneComputerHasNoSecondLine() {
        show("a")
        ui.onNodeWithTag("new-project", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-machine", useUnmergedTree = true).assertDoesNotExist()
    }
}
