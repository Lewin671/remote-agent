package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.Context
import android.content.ContextWrapper
import io.github.lewin671.remoteagent.RaApp
import org.robolectric.RuntimeEnvironment
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
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
        val state = RemoteState(conn = ConnState.ONLINE, loaded = true, synced = true, prefs = PrefsObject.EMPTY, machines = ids.associateWith { machine(it) })
        val models = ListModelsResponse.newBuilder().setDefaultModel("first")
            .addModels(ModelInfo.newBuilder().setSlug("first").setDisplayName("First"))
            .addModels(ModelInfo.newBuilder().setSlug("second").setDisplayName("Second").setDefaultEffort("high"))
            .build()
        remote.showForTest(state, modelLists = ids.associate { (it to Agent.AGENT_CODEX) to models })
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    val current by remote.state.collectAsState()
                    NewSessionBox(current, NewConfig(ids.first(), path, Agent.AGENT_CODEX), "demo", focus = false, settled = current.loaded, onProject = {})
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
    @After fun close() { remote.close(); Drafts.clear(); ImageDrafts.clear(); PrefsChanges.clear() }

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

    private fun selectSecondModel() {
        ui.waitUntil(5000) { ui.onAllNodesWithContentDescription("模型：First").fetchSemanticsNodes().isNotEmpty() }
        ui.onNodeWithTag("new-model").performClick()
        ui.onNodeWithText("Second").performClick()
    }

    @Test fun selectingModelThenTappingTextSavesAndUpdatesChip() {
        show("a")
        val field = ui.onNode(hasSetTextAction())
        field.performClick()
        val box = ui.onNodeWithTag("new-composer").fetchSemanticsNode().boundsInRoot
        selectSecondModel()
        field.performTouchInput { click() }
        ui.onNodeWithTag("new-model-popover").assertDoesNotExist()
        ui.onNodeWithTag("new-model").assertContentDescriptionEquals("模型：Second")
        field.assertIsFocused()
        Assert.assertEquals(box, ui.onNodeWithTag("new-composer").fetchSemanticsNode().boundsInRoot)
        ui.runOnIdle {
            val config = Drafts.newSession(path, null).config.value!!
            Assert.assertEquals("second", config.model)
            Assert.assertEquals("high", config.effort)
            Assert.assertNotNull(PrefsChanges.states[path])
        }
    }

    @Test fun machineChangeCancelsPendingModelWithoutSavingToEitherMachine() {
        show("a", "b")
        selectSecondModel()
        ui.runOnIdle { Drafts.newSession(path, null).config.choose(NewConfig("b", path, Agent.AGENT_CODEX)) }
        ui.onNodeWithTag("new-model-popover").assertDoesNotExist()
        ui.runOnIdle {
            Assert.assertEquals("b", Drafts.newSession(path, null).config.value!!.machineId)
            Assert.assertEquals("", Drafts.newSession(path, null).config.value!!.model)
            Assert.assertNull(PrefsChanges.states[path])
        }
    }

    @Test fun agentChangeCancelsPendingModel() {
        show("a")
        selectSecondModel()
        ui.runOnIdle { Drafts.newSession(path, null).config.choose(NewConfig("a", path, Agent.AGENT_CLAUDE)) }
        ui.onNodeWithTag("new-model-popover").assertDoesNotExist()
        ui.runOnIdle {
            Assert.assertEquals(Agent.AGENT_CLAUDE, Drafts.newSession(path, null).config.value!!.agent)
            Assert.assertEquals("", Drafts.newSession(path, null).config.value!!.model)
            Assert.assertNull(PrefsChanges.states[path])
        }
    }

    private fun closingBlockedEditDoesNotSave(change: (RemoteState) -> RemoteState) {
        show("a")
        selectSecondModel()
        ui.runOnIdle { remote.showForTest(change(remote.state.value)) }
        ui.onNode(hasSetTextAction()).performTouchInput { click() }
        ui.onNodeWithTag("new-model-popover").assertDoesNotExist()
        ui.runOnIdle {
            Assert.assertEquals("", Drafts.newSession(path, null).config.value!!.model)
            Assert.assertNull(PrefsChanges.states[path])
        }
    }

    @Test fun blockedPreferencesPreventDismissalSave() = closingBlockedEditDoesNotSave { it.copy(conn = ConnState.OFFLINE) }
    @Test fun unsettledSettingsPreventDismissalSave() = closingBlockedEditDoesNotSave { it.copy(loaded = false) }

    @Test fun doneAndNativeDismissalCommitOnlyOnceAndCancellationDoesNotCommit() {
        var saves = 0
        val picker = ComposerPicker { saves++ }
        picker.toggle("model")
        picker.close() // 完成
        picker.dismissOutside() // queued native outside event
        picker.close() // underlying page DOWN
        Assert.assertEquals(1, saves)
        picker.toggle("model")
        picker.cancel() // destination changed
        picker.dismissOutside()
        Assert.assertEquals(1, saves)
        picker.toggle("model")
        picker.toggle("model") // re-tap
        Assert.assertEquals(2, saves)
    }

    @Test fun oneComputerHasNoSecondLine() {
        show("a")
        ui.onNodeWithTag("new-project", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-machine", useUnmergedTree = true).assertDoesNotExist()
    }
}
