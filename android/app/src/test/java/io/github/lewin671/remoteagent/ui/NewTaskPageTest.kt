package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.showForTest

import android.app.Application
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertContentDescriptionEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsFocused
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.assertTextContains
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.click
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.PEER_PROTOCOL_VERSION
import io.github.lewin671.remoteagent.core.PrefsObject
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.ModelOptionView
import io.github.lewin671.remoteagent.core.proto.NewSessionModelPanelView
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.ListModelsResponse
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.ModelInfo
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.ProjectSummary
import org.junit.After
import org.junit.Assert
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** The new task's page (design-system §6 新任务): where in the title, everything else in the box at the bottom. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class NewTaskPageTest {
    @get:Rule(order = 0) val appFixture = io.github.lewin671.remoteagent.RaAppTestRule(preparePendingStore = true)
    @get:Rule(order = 1) val ui = createComposeRule()
    @get:Rule(order = 2) val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }
    private val remote = Remote(Account("http://127.0.0.1:1", "test-client"), Api.defaultHttp(), dispatcher = kotlinx.coroutines.Dispatchers.Unconfined)
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val path = "/example/demo"

    private fun machine(id: String) = MachineView(
        id, true, 0,
        MachineMeta.newBuilder().setHostname("host-$id").setProtocolVersion(PEER_PROTOCOL_VERSION)
            .addProjects(ProjectSummary.newBuilder().setPath(path))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE))
            .build(),
    )

    private val models = ListModelsResponse.newBuilder().setDefaultModel("first")
        .addModels(ModelInfo.newBuilder().setSlug("first").setDisplayName("First").setDefaultEffort("medium").addAllEfforts(listOf("low", "medium", "high")))
        .addModels(ModelInfo.newBuilder().setSlug("second").setDisplayName("Second").setDefaultEffort("high").addAllEfforts(listOf("low", "high")))
        .addModels(ModelInfo.newBuilder().setSlug("third").setDisplayName("Third").setDefaultEffort("low").addEfforts("low"))
        .addModels(ModelInfo.newBuilder().setSlug("fourth").setDisplayName("Fourth"))
        .addModels(ModelInfo.newBuilder().setSlug("fifth").setDisplayName("Fifth"))
        .addModels(ModelInfo.newBuilder().setSlug("sixth").setDisplayName("Sixth").setDefaultEffort("ultra").addAllEfforts(listOf("low", "medium", "high", "xhigh", "max", "ultra")))
        .build()

    private fun show(vararg ids: String) {
        val state = RemoteState(
            conn = ConnState.ONLINE,
            loaded = true,
            synced = true,
            prefs = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1).build()),
            machines = ids.associateWith { machine(it) },
        )
        remote.showForTest(
            state,
            modelLists = ids.flatMap { id ->
                listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE).map { (id to it) to models }
            }.toMap(),
        )
        remote.coreHost.openNewSession(path)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    val vm by remote.newSessionView.collectAsState()
                    NewSessionBox(vm, focus = false, onProject = {})
                }
            }
        }
        ui.waitUntil(5000) {
            ui.mainClock.advanceTimeByFrame()
            ui.waitForIdle()
            !remote.coreHost.snapshotAttachmentDraft(io.github.lewin671.remoteagent.core.newSessionDraftScope(path)).restoring
        }
        ui.waitForIdle()
    }

    @After fun close() { remote.close(); Drafts.clear() }

    @Test fun theTitleSaysWhereAndTheBoxHoldsTheRest() {
        show("a", "b")
        ui.onNodeWithTag("new-project", useUnmergedTree = true).assertExists()
        ui.onNodeWithText("demo", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-machine", useUnmergedTree = true).assertExists()
        ui.onNodeWithText(messageTextForTest(UiMessages.newSessionPrompt()), useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-agent", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-model", useUnmergedTree = true).assertExists()
        ui.onNodeWithContentDescription(messageTextForTest(UiMessages.newSessionAndroidStart())).assertExists()
        ui.onNodeWithText(messageTextForTest(UiMessages.homeNewTask())).assertDoesNotExist()
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
        ui.onNodeWithTag("new-agent").assertContentDescriptionEquals(messageTextForTest(UiMessages.newSessionCliLabel("Claude Code")))
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

    @Test fun machineChangeClosesPopoverAndUpdatesMachine() {
        show("a", "b")
        ui.onNodeWithTag("new-model").performClick()
        ui.onNodeWithTag("new-model-popover").assertExists()
        ui.runOnIdle { remote.coreHost.selectNewSessionMachine("b") }
        ui.waitUntil(2000) { remote.newSessionView.value.config.machineId == "b" }
        ui.waitForIdle()
        ui.onNodeWithTag("new-model-popover").assertDoesNotExist()
        ui.runOnIdle {
            Assert.assertEquals("b", remote.newSessionView.value.config.machineId)
            Assert.assertEquals("first", remote.newSessionView.value.config.model)
        }
    }

    @Test fun doneAndNativeDismissalCommitOnlyOnceAndCancellationDoesNotCommit() {
        var saves = 0
        val picker = ComposerPicker { saves++ }
        picker.toggle("model")
        picker.close()
        picker.dismissOutside()
        picker.close()
        Assert.assertEquals(1, saves)
        picker.toggle("model")
        picker.cancel()
        picker.dismissOutside()
        Assert.assertEquals(1, saves)
        picker.toggle("model")
        picker.toggle("model")
        Assert.assertEquals(2, saves)
    }

    @Test fun effortAndModelApplyAndAllModelsIsASeparatePage() {
        show("a")
        ui.runOnIdle {
            Assert.assertTrue(
                "models=${remote.newSessionView.value.modelPanel.modelsCount} key=${remote.newSessionView.value.projectKey} cfg=${remote.newSessionView.value.config.machineId}/${remote.newSessionView.value.config.agent} err=${remote.newSessionView.value.modelPanel.modelsError.key}:${remote.newSessionView.value.modelPanel.modelsError.literal}",
                remote.newSessionView.value.modelPanel.modelsCount > 0,
            )
        }
        ui.onNodeWithTag("new-model").performClick()
        ui.onNodeWithTag("new-model").assertIsSelected()
        ui.onNodeWithText("low").performClick()
        ui.waitUntil(2000) { remote.newSessionView.value.config.effort == "low" }
        ui.onNodeWithTag("new-model").assertContentDescriptionEquals("模型：First，推理强度：low")
        ui.onNodeWithText("Second").performClick()
        ui.onNodeWithTag("new-model").assertContentDescriptionEquals("模型：Second，推理强度：high")
        ui.onNodeWithTag("new-model-popover").assertExists()
        ui.onNodeWithTag("new-model-show-all").performScrollTo().performClick()
        ui.onNodeWithTag("new-model-all").assertExists()
        ui.onNodeWithTag("new-model-main").assertDoesNotExist()
        ui.onNodeWithTag("new-model-back").performClick()
        ui.onNodeWithTag("new-model-main").assertExists()
        ui.onNodeWithTag("new-model-show-all").performScrollTo().performClick()
        ui.onNodeWithText("Sixth").performScrollTo().performClick()
        ui.onNodeWithTag("new-model-main").assertExists()
        ui.onNodeWithTag("new-model-all").assertDoesNotExist()
        ui.onNodeWithTag("new-model").assertContentDescriptionEquals("模型：Sixth，推理强度：ultra")
        ui.onNodeWithText("low").performClick()
        ui.onNodeWithTag("new-model").assertContentDescriptionEquals("模型：Sixth，推理强度：low")
        ui.onNodeWithText(messageTextForTest(UiMessages.commonDone())).performClick()
        ui.onNodeWithTag("new-model-popover").assertDoesNotExist()
        ui.runOnIdle {
            Assert.assertEquals("sixth", remote.newSessionView.value.config.model)
            Assert.assertEquals("low", remote.newSessionView.value.config.effort)
        }
    }

    @org.robolectric.annotation.GraphicsMode(org.robolectric.annotation.GraphicsMode.Mode.NATIVE)
    @Test fun longSummaryEllipsizesOnlyModelKeepingWholeEffort() {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Box(Modifier.width(200.dp)) {
                    NewTaskModelLabel("A very long model display name that cannot fit", "medium", "Codex · ")
                }
            }
        }
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

    private fun toolRow(width: Int, model: String) {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                NewTaskToolsRow(Agent.AGENT_CLAUDE, "Claude Code", model, "medium", "", false, {}, {}, Modifier.width(width.dp))
            }
        }
    }

    private fun modelTextLayout(): TextLayoutResult {
        val results = mutableListOf<TextLayoutResult>()
        ui.onNodeWithTag("new-model-name", useUnmergedTree = true)
            .performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(results) }
        return results.single()
    }

    @org.robolectric.annotation.GraphicsMode(org.robolectric.annotation.GraphicsMode.Mode.NATIVE)
    @Test fun toolRowKeepsFullCliAndSummaryWhenBothFit() {
        toolRow(340, "Opus 5.5")
        ui.onNodeWithTag("new-agent").assertTextContains("Claude Code")
        Assert.assertFalse(modelTextLayout().isLineEllipsized(0))
    }

    @org.robolectric.annotation.GraphicsMode(org.robolectric.annotation.GraphicsMode.Mode.NATIVE)
    @Test fun toolRowCollapsesCliBeforeTruncatingModel() {
        toolRow(260, "Opus 5.5")
        ui.onNodeWithTag("new-agent").assertContentDescriptionEquals(messageTextForTest(UiMessages.newSessionCliLabel("Claude Code")))
            .assert(hasText("Claude Code").not())
        Assert.assertFalse(modelTextLayout().isLineEllipsized(0))
        ui.onNodeWithTag("new-model-effort", useUnmergedTree = true).assertTextEquals(" · medium")
    }

    @org.robolectric.annotation.GraphicsMode(org.robolectric.annotation.GraphicsMode.Mode.NATIVE)
    @Test fun toolRowTruncatesLongModelAfterCollapsingCliKeepingUsefulPrefixAndEffort() {
        toolRow(220, "Opus 5.5 with a very long model name")
        ui.onNodeWithTag("new-agent").assertContentDescriptionEquals(messageTextForTest(UiMessages.newSessionCliLabel("Claude Code")))
            .assert(hasText("Claude Code").not())
        val layout = modelTextLayout()
        Assert.assertTrue(layout.isLineEllipsized(0))
        Assert.assertTrue(layout.getLineEnd(0, visibleEnd = true) >= 6)
        val effort = mutableListOf<TextLayoutResult>()
        ui.onNodeWithTag("new-model-effort", useUnmergedTree = true)
            .performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(effort) }
        Assert.assertTrue(effort.single().getLineRight(0) <= effort.single().size.width)
        Assert.assertFalse(effort.single().isLineEllipsized(0))
    }

    @Test fun pinsOnlyWithSpaceForTwoModelRowsAndPadding() {
        Assert.assertTrue(shouldPinModelControls(228, 132, 44, 8))
        Assert.assertFalse(shouldPinModelControls(227, 132, 44, 8))
        Assert.assertFalse(shouldPinModelControls(228, 132, 52, 8))
    }

    @Test fun effortAndContextStayFixedWhileModelListScrolls() {
        val panel = NewSessionModelPanelView.newBuilder()
            .setPanelEnabled(true)
            .setDefaultModelLabel(CoreText.newBuilder().setKey(UiMessages.modelsDefault().key))
            .addAllModels(
                models.modelsList.map { m ->
                    ModelOptionView.newBuilder().setSlug(m.slug).setDisplayName(m.displayName).setChecked(m.slug == "first").build()
                },
            )
            .addAllEffortChoices(listOf("low", "medium", "high"))
            .setSelectedEffort("medium")
            .setSupportsContextWindow(true)
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column(Modifier.width(360.dp).height(320.dp)) {
                    NewTaskModelPanel(panel, "test computer", "first", { _, _, _ -> }, {}, {})
                }
            }
        }
        val controls = ui.onNodeWithTag("new-model-controls")
        val before = controls.fetchSemanticsNode().boundsInRoot
        val header = ui.onNodeWithText("模型").fetchSemanticsNode().boundsInRoot
        ui.onNodeWithTag("new-model-show-all").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText(messageTextForTest(UiMessages.modelsEffort())).assertIsDisplayed()
        ui.onNodeWithText("上下文窗口").assertIsDisplayed()
        ui.onNodeWithText("1M").assertIsDisplayed()
        ui.onNodeWithText(messageTextForTest(UiMessages.modelsStandardWindow())).assertIsDisplayed()
        Assert.assertEquals(before, controls.fetchSemanticsNode().boundsInRoot)
        Assert.assertEquals(header, ui.onNodeWithText("模型").fetchSemanticsNode().boundsInRoot)
    }

    @Test fun oneComputerHasNoSecondLine() {
        show("a")
        ui.onNodeWithTag("new-project", useUnmergedTree = true).assertExists()
        ui.onNodeWithTag("new-machine", useUnmergedTree = true).assertDoesNotExist()
    }
}
