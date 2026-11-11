package io.github.lewin671.remoteagent.ui

import android.app.Application
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

    @After fun close() = remote.close()

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

    @Test fun oneComputerHasNoSecondLine() {
        show("a")
        ui.onNodeWithTag("new-project", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-machine", useUnmergedTree = true).assertDoesNotExist()
    }
}
