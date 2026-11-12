package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.Context
import android.content.ContextWrapper
import io.github.lewin671.remoteagent.RaApp
import org.robolectric.RuntimeEnvironment
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.text.TextLayoutResult
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

    private val models = ListModelsResponse.newBuilder().setDefaultModel("first")
        .addModels(ModelInfo.newBuilder().setSlug("first").setDisplayName("First").setDefaultEffort("medium").addAllEfforts(listOf("low", "medium", "high")))
        .addModels(ModelInfo.newBuilder().setSlug("second").setDisplayName("Second").setDefaultEffort("high").addAllEfforts(listOf("low", "high")))
        .addModels(ModelInfo.newBuilder().setSlug("third").setDisplayName("Third").setDefaultEffort("low").addEfforts("low"))
        .addModels(ModelInfo.newBuilder().setSlug("fourth").setDisplayName("Fourth"))
        .addModels(ModelInfo.newBuilder().setSlug("fifth").setDisplayName("Fifth"))
        .addModels(ModelInfo.newBuilder().setSlug("sixth").setDisplayName("Sixth").setDefaultEffort("ultra").addAllEfforts(listOf("low", "medium", "high", "xhigh", "max", "ultra")))
        .build()

    private fun show(vararg ids: String) {
        val state = RemoteState(conn = ConnState.ONLINE, loaded = true, synced = true, prefs = PrefsObject.EMPTY, machines = ids.associateWith { machine(it) })
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
        ui.waitUntil(5000) { ui.onAllNodesWithContentDescription("模型：First，推理强度：medium").fetchSemanticsNodes().isNotEmpty() }
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
        ui.onNodeWithTag("new-model").assertContentDescriptionEquals("模型：Second，推理强度：high")
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

    @Test fun effortAndModelApplyImmediatelyAndAllModelsIsASeparatePage() {
        show("a")
        ui.waitUntil(5000) { ui.onAllNodesWithContentDescription("模型：First，推理强度：medium").fetchSemanticsNodes().isNotEmpty() }
        ui.onNodeWithTag("new-model").performClick()
        ui.onNodeWithTag("new-model").assertIsSelected()
        ui.onNodeWithText("low").performClick()
        ui.onNodeWithTag("new-model").assertContentDescriptionEquals("模型：First，推理强度：low")
        ui.onNodeWithText("Second").performClick()
        ui.onNodeWithTag("new-model").assertContentDescriptionEquals("模型：Second，推理强度：low")
        ui.onNodeWithTag("new-model-popover").assertExists()
        ui.runOnIdle { Assert.assertNull(PrefsChanges.states[path]) }
        ui.onNodeWithTag("new-model-show-all").performScrollTo().performClick()
        ui.onNodeWithTag("new-model-all").assertExists()
        ui.onNodeWithTag("new-model-main").assertDoesNotExist()
        ui.onNodeWithTag("new-model-back").performClick()
        ui.onNodeWithTag("new-model-main").assertExists()
        ui.onNodeWithTag("new-model-show-all").performScrollTo().performClick()
        ui.onNodeWithText("Sixth").performScrollTo().performClick()
        ui.onNodeWithTag("new-model-main").assertExists()
        ui.onNodeWithTag("new-model-all").assertDoesNotExist()
        ui.onNodeWithTag("new-model").assertContentDescriptionEquals("模型：Sixth，推理强度：low")
        ui.onNodeWithText("完成").performClick()
        ui.onNodeWithTag("new-model-popover").assertDoesNotExist()
        ui.runOnIdle {
            Assert.assertEquals("sixth", Drafts.newSession(path, null).config.value!!.model)
            Assert.assertEquals("low", Drafts.newSession(path, null).config.value!!.effort)
            Assert.assertNotNull(PrefsChanges.states[path])
        }
    }

    @Test fun summaryUsesEffectiveEffortAndOmitsFixedMissingAndUnknownEfforts() {
        val c = NewConfig("a", path, Agent.AGENT_CODEX)
        fun summary(config: NewConfig = c, single: Boolean = false) = newTaskModelSummary(config, models, null, true, single)
        Assert.assertEquals("First · medium", summary().label)
        Assert.assertEquals("Codex · First · high", summary(c.copy(effort = "high"), true).label)
        Assert.assertEquals("Third", summary(c.copy(model = "third")).label)
        Assert.assertEquals("Fourth", summary(c.copy(model = "fourth")).label)
        Assert.assertEquals("custom", summary(c.copy(model = "custom", effort = "high")).label)
        Assert.assertEquals("读取设置…", newTaskModelSummary(c, null, null, false, false).label)
        Assert.assertEquals("默认模型", newTaskModelSummary(c, null, null, true, false).label)
        val settings = DaemonSettings.newBuilder().setDefaultModel("second").setDefaultEffort("low").build()
        Assert.assertEquals("Second · low", newTaskModelSummary(c, models, settings, true, false).label)
        Assert.assertEquals("custom", newTaskModelSummary(c, models, settings.toBuilder().setDefaultModel("custom").build(), true, false).label)
    }

    @Test fun changingModelKeepsSupportedEffortOtherwiseUsesNewDefault() {
        val c = NewConfig("a", path, Agent.AGENT_CODEX)
        Assert.assertEquals("high", chooseNewTaskModel(c, "second", models, null).effort)
        Assert.assertEquals("low", chooseNewTaskModel(c.copy(effort = "low"), "second", models, null).effort)
        Assert.assertEquals("medium", chooseNewTaskModel(c.copy(model = "sixth", effort = "ultra"), "", models, null).effort)
        Assert.assertEquals("", chooseNewTaskModel(c, "fourth", models, null).effort)
        Assert.assertEquals("low", chooseNewTaskModel(c, "third", models, null).effort)
    }

    @org.robolectric.annotation.GraphicsMode(org.robolectric.annotation.GraphicsMode.Mode.NATIVE)
    @Test fun longSummaryEllipsizesOnlyModelKeepingWholeEffort() {
        ui.setContent { RaTheme(ThemeMode.LIGHT) {
            Box(Modifier.width(200.dp)) { NewTaskModelLabel(NewTaskModelSummary("A very long model display name that cannot fit", "medium", "Codex · ")) }
        } }
        fun layout(tag: String): TextLayoutResult {
            val results = mutableListOf<TextLayoutResult>()
            ui.onNodeWithTag(tag).performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(results) }
            return results.single()
        }
        Assert.assertTrue(layout("new-model-name").isLineEllipsized(0))
        val effortLayout = layout("new-model-effort")
        Assert.assertTrue(effortLayout.getLineRight(0) <= effortLayout.size.width)
        Assert.assertFalse(layout("new-model-effort").isLineEllipsized(0))
        ui.onNodeWithTag("new-model-effort").assertTextEquals(" · medium")
    }

    @Test fun oneComputerHasNoSecondLine() {
        show("a")
        ui.onNodeWithTag("new-project", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-machine", useUnmergedTree = true).assertDoesNotExist()
    }
}
