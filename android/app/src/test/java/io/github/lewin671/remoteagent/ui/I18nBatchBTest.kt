package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.graphics.Bitmap
import android.graphics.Canvas
import android.view.inspector.WindowInspector
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import io.github.lewin671.remoteagent.term.*
import org.junit.*
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File

/** Native UI previews on a stopped engine, using fabricated content and no CLI or device. */
@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class, qualifiers = "w390dp-h844dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class I18nBatchBTest(private val locale: String) {
    companion object {
        @JvmStatic @ParameterizedRobolectricTestRunner.Parameters(name = "{0}")
        fun locales() = listOf(arrayOf("zh-CN"), arrayOf("en"))
    }
    @get:Rule(order = 0) val appFixture = io.github.lewin671.remoteagent.RaAppTestRule(preparePendingStore = true)
    @get:Rule(order = 1) val ui = createComposeRule()
    @get:Rule(order = 2) val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }
    private val remote = Remote(Account("http://127.0.0.1:1", "preview-client"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private var language by mutableStateOf(locale)
    private val stamp = System.currentTimeMillis()
    private val session = SessionView("s", "m", Session.newBuilder().setId("s").setCwd("/example/demo").setTitle("Example task")
        .setAgent(Agent.AGENT_CLAUDE).setMode(ApprovalMode.APPROVAL_MODE_ASK).setStatus(SessionStatus.SESSION_STATUS_IDLE)
        .setModel("example-model").setCreatedAt(Timestamp.newBuilder().setSeconds(stamp / 1000)).build())
    private val machine = MachineView("m", true, stamp, MachineMeta.newBuilder().setHostname("Example computer").setProtocolVersion(PEER_PROTOCOL_VERSION)
        .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).build())
    private val state = RemoteState(conn = io.github.lewin671.remoteagent.core.ConnState.ONLINE, link = Link.ONLINE, loaded = true, synced = true, isAuthority = true,
        prefs = PrefsObject.EMPTY, machines = mapOf("m" to machine), sessions = mapOf("s" to session))
    private fun text(message: UiMessage) = messageTextForTest(message, language)
    private val defaultSessionVm: SessionViewModel get() = sessionVm(history)
    private fun sessionVm(
        items: List<SessionTimelineItem> = history,
        dock: SessionDockView = SessionDockView.getDefaultInstance(),
        pendingTray: SessionPendingTrayView? = null,
        exited: Boolean = false,
        canSwitchModel: Boolean = true,
        modelsLoading: Boolean = false,
        modelChoices: List<SessionModelChoiceView> = emptyList(),
    ): SessionViewModel = SessionViewModel.newBuilder()
        .setSessionId("s")
        .setHeader(
            SessionHeaderView.newBuilder()
                .setFound(true)
                .setSessionsListed(true)
                .setLiveLoaded(true)
                .setTitle(Text.newBuilder().setLiteral("Example task"))
                .setProjectName(Text.newBuilder().setLiteral("demo"))
                .setProjectKey("/example/demo")
                .setCwd("/example/demo")
                .setAgent(Agent.AGENT_CLAUDE)
                .setAgentName("Claude Code")
                .setStatus(if (exited) SessionDisplayStatus.SESSION_DISPLAY_STATUS_ENDED else SessionDisplayStatus.SESSION_DISPLAY_STATUS_IDLE)
                .setStatusText(Text.newBuilder().setKey(if (exited) UiMessages.listStateEnded().key else UiMessages.listStateIdle().key))
                .setModel("example-model")
                .setModelLabel("example-model")
                .setMachineId("m")
                .setComputerName(Text.newBuilder().setLiteral("Example computer"))
                .setComputerOnline(true)
                .setConnState(io.github.lewin671.remoteagent.core.proto.ConnState.CONN_STATE_CONNECTED)
                .apply {
                    if (exited) {
                        setExited(true)
                        setEndedAtMs(stamp)
                        setEndedText(Text.newBuilder().setKey(UiMessages.sessionEnded().key))
                    }
                }
                .setCapabilities(
                    SessionCapabilitiesView.newBuilder()
                        .setCanSend(!exited)
                        .setSupportsImages(true)
                        .setSupportsFiles(true)
                        .setSupportsSendModes(true)
                        .setSupportsCompact(true)
                        .setSupportsFork(true)
                        .setSupportsModeSwitch(true)
                        .setSupportsModelSwitch(true)
                        .setTerminalAvailable(!exited),
                ),
        )
        .setActions(
            SessionActionsView.newBuilder()
                .setCanRename(true)
                .setCanFork(true)
                .setCanEnd(!exited)
                .setCanSwitchMode(!exited)
                .setCanSwitchModel(canSwitchModel)
                .setModelsLoading(modelsLoading)
                .addAllModelChoices(modelChoices)
                .addModeChoices(
                    SessionModeChoiceView.newBuilder()
                        .setMode(ApprovalMode.APPROVAL_MODE_ASK)
                        .setLabel(Text.newBuilder().setKey(UiMessages.modesAsk().key))
                        .setSelected(true)
                        .setAvailable(true),
                ),
        )
        .setDock(dock)
        .setComposer(
            SessionComposerView.newBuilder()
                .setEnabled(!exited)
                .setPlaceholder(Text.newBuilder().setKey(UiMessages.composerMessage().key))
                .apply { if (pendingTray != null) setPendingTray(pendingTray) },
        )
        .setTimeline(SessionTimelineView.newBuilder().setLoaded(true).addAllItems(items))
        .build()

    private fun tl(seq: Long, id: String, b: SessionTimelineItem.Builder.() -> Unit) =
        SessionTimelineItem.newBuilder().setId(id).setSeq(seq).setEndSeq(seq).apply(b).build()

    private val history: List<SessionTimelineItem> get() = listOf(
        tl(1, "e1") { userMessage = SessionUserMessageItem.newBuilder().setText("Keep this original input unchanged").build() },
        tl(2, "e2") { agentMessage = SessionAgentMessageItem.newBuilder().setMarkdown("The original CLI reply stays unchanged.").build() },
        tl(3, "e3") {
            toolGroup = SessionToolGroupItem.newBuilder()
                .setSummary(Text.newBuilder().setKey(UiMessages.toolsCompletedCommands(2).key).addArgs(TextArg.newBuilder().setName("count").setIntValue(2)))
                .setCommandCount(2)
                .addCalls(SessionToolCallItem.newBuilder().setCallId("c1").setToolKind(ToolKind.TOOL_KIND_COMMAND).setTitle("git status --short").setStatus(SessionToolStatus.SESSION_TOOL_STATUS_OK))
                .addCalls(SessionToolCallItem.newBuilder().setCallId("c2").setToolKind(ToolKind.TOOL_KIND_COMMAND).setTitle("git diff --stat").setStatus(SessionToolStatus.SESSION_TOOL_STATUS_OK))
                .build()
        },
        tl(5, "e5") {
            turnEnd = SessionTurnEndItem.newBuilder()
                .setDurationMs(72_000)
                .setLabel(Text.newBuilder().setKey(UiMessages.sessionTurnDuration("{duration}").key).addArgs(TextArg.newBuilder().setName("duration").setStrValue("1m12s")))
                .build()
        },
    )
    @Before fun prepare() {
        remote.showForTest(state, sessions = mapOf("s" to defaultSessionVm))
    }
    @After fun close() { remote.close(); Outbox.clear(); ImageDrafts.clear(); Drafts.clear(); DockFolds.clear(); QuestionMultiPicks.clear() }
    private fun show(content: @Composable () -> Unit) = ui.setContent {
        RaTheme(ThemeMode.LIGHT) {
            CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalMessageLocale provides language) {
                Box(Modifier.fillMaxSize().background(pal.paper)) { content() }
            }
        }
    }
    private fun shot(name: String) {
        ui.waitForIdle()
        val output = System.getenv("RA_I18N_B_SHOTS") ?: return
        ui.mainClock.advanceTimeBy(600)
        ui.runOnIdle {
            val view = WindowInspector.getGlobalWindowViews().last()
            val bitmap = Bitmap.createBitmap(view.width, view.height, Bitmap.Config.ARGB_8888)
            view.draw(Canvas(bitmap))
            val file = File(output, "$locale-$name.png"); file.parentFile!!.mkdirs()
            file.outputStream().use { Assert.assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }
    @Test fun sessionAndMenu() {
        show { SessionScreen("s") }; shot("session")
        ui.onNodeWithContentDescription(text(UiMessages.commonAndroidMore())).performClick(); shot("session-menu")
    }
    @Test fun sessionRecovery() {
        remote.showForTest(state, sessions = mapOf("s" to sessionVm(exited = true)))
        show { SessionScreen("s") }; shot("session-recovery")
    }
    @Test fun composerMenus() {
        show { Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.Bottom) {
            ComposeBox("Preserve draft text", {}, text(UiMessages.composerMessage()), true, {}, {}, onFile = {}, onQueue = {})
        } }
        shot("composer")
        ui.onNodeWithText(text(UiMessages.composerSendAfter())).assertExists()
    }
    @Test fun attachmentMenu() {
        show { Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.Bottom) {
            ComposeBox("", {}, text(UiMessages.composerMessage()), false, {}, {}, onFile = {})
        } }
        ui.onNodeWithContentDescription(text(UiMessages.newSessionIosAttach())).performClick(); shot("attachment-menu")
    }
    private val samplePlanMarkdown = "## Proposed plan\n\n1. Inspect the implementation\n2. Apply a focused change\n3. Verify behavior"
    @Test fun historyRows() {
        val items = history + listOf(
            tl(7, "e7") { plan = SessionPlanItem.newBuilder().setMarkdown(samplePlanMarkdown).build() },
            tl(8, "e8") { note = SessionNoteItem.newBuilder().setKind(SessionNoteKind.SESSION_NOTE_KIND_MODE_CHANGED).setText(Text.newBuilder().setKey(UiMessages.modesPlan().key)).build() },
        )
        show { Column(Modifier.fillMaxSize()) { items.forEach { TimelineItemView(it) } } }
        shot("history")
    }
    @Test fun queuedMessages() {
        val pausedItem = SessionTrayItemView.newBuilder()
            .setRequestId("queued")
            .setGroup(SessionTrayGroupKind.SESSION_TRAY_GROUP_KIND_PAUSED)
            .setTone(SessionTrayTone.SESSION_TRAY_TONE_NEUTRAL)
            .setActivity(SessionTrayRowActivity.SESSION_TRAY_ROW_ACTIVITY_IDLE)
            .setText("A pending message with original content")
            .setMode(SendMode.SEND_MODE_QUEUE)
            .setCanCancel(true)
            .setCanResume(true)
            .build()
        val tray = SessionPendingTrayView.newBuilder()
            .setTotalCount(1)
            .addOrderedItems(pausedItem)
            .addGroups(
                SessionTrayGroupView.newBuilder()
                    .setKind(SessionTrayGroupKind.SESSION_TRAY_GROUP_KIND_PAUSED)
                    .setTone(SessionTrayTone.SESSION_TRAY_TONE_NEUTRAL)
                    .setTitle(Text.newBuilder().setKey(UiMessages.queueGroupPaused().key))
                    .setHeaderText(Text.newBuilder().setKey(UiMessages.queueGroupPaused().key))
                    .setTotalCount(1)
                    .addItems(pausedItem),
            )
            .build()
        remote.showForTest(state, sessions = mapOf("s" to sessionVm(pendingTray = tray)))
        show { SessionScreen("s") }; shot("queue")
        ui.onNodeWithText(text(UiMessages.queueGroupPaused())).assertExists()
    }
    @Test fun approvalDockAndPage() {
        val dock = SessionDockView.newBuilder()
            .setHasItem(true).setKind(SessionDockKind.SESSION_DOCK_KIND_APPROVAL).setItemId("a").setObservedSeq(6)
            .setTitle(Text.newBuilder().setKey(UiMessages.listStateApproval().key))
            .setMeta(Text.newBuilder().setLiteral("git diff --stat"))
            .setApproval(SessionApprovalDockView.newBuilder().setApprovalId("a").setCommandPreview("git diff --stat").setDescription("Inspect the proposed changes").setCanAlwaysAllow(true))
            .build()
        val vm = sessionVm(dock = dock)
        remote.showForTest(state, sessions = mapOf("s" to vm))
        show { Column { SessionDock(vm, {}, {}, {}, {}) } }; shot("approval-dock")
    }
    @Test fun approvalPage() {
        val dock = SessionDockView.newBuilder()
            .setHasItem(true).setKind(SessionDockKind.SESSION_DOCK_KIND_APPROVAL).setItemId("a").setObservedSeq(6)
            .setTitle(Text.newBuilder().setKey(UiMessages.listStateApproval().key))
            .setApproval(SessionApprovalDockView.newBuilder().setApprovalId("a").setCommandPreview("git diff --stat").setBody("git diff --stat").setDescription("Inspect the proposed changes"))
            .build()
        remote.showForTest(state, sessions = mapOf("s" to sessionVm(dock = dock)))
        show { ApprovalPage("s", "a") }; ui.waitUntil(5000) { ui.onAllNodesWithText("git diff --stat", substring = true).fetchSemanticsNodes().isNotEmpty() }; shot("approval-page")
    }
    @Test fun questionDock() {
        val q = SessionQuestionStepView.newBuilder().setQuestionText("Which implementation should we use?").setMultiSelect(true)
            .addOptions(SessionQuestionOptionView.newBuilder().setOptionNum(1).setLabel("Small change (Recommended)").setDescription("Keep the public interface"))
            .addOptions(SessionQuestionOptionView.newBuilder().setOptionNum(2).setLabel("Broader cleanup").setDescription("Update the implementation")).build()
        val dock = SessionDockView.newBuilder()
            .setHasItem(true).setKind(SessionDockKind.SESSION_DOCK_KIND_QUESTION).setItemId("q").setObservedSeq(6)
            .setTitle(Text.newBuilder().setKey(UiMessages.listStateQuestion().key))
            .setQuestion(SessionQuestionDockView.newBuilder().setQuestionId("q").setTotalQuestions(1).setQuestion(q))
            .build()
        val vm = sessionVm(dock = dock)
        remote.showForTest(state, sessions = mapOf("s" to vm))
        show { SessionDock(vm, {}, {}, {}, {}) }; shot("question-dock")
    }
    @Test fun questionPage() {
        val q = SessionQuestionStepView.newBuilder().setQuestionText("Which implementation should we use?").setMultiSelect(true)
            .addOptions(SessionQuestionOptionView.newBuilder().setOptionNum(1).setLabel("Small change (Recommended)").setDescription("Keep the public interface"))
            .addOptions(SessionQuestionOptionView.newBuilder().setOptionNum(2).setLabel("Broader cleanup").setDescription("Update the implementation")).build()
        val dock = SessionDockView.newBuilder()
            .setHasItem(true).setKind(SessionDockKind.SESSION_DOCK_KIND_QUESTION).setItemId("q").setObservedSeq(6)
            .setTitle(Text.newBuilder().setKey(UiMessages.listStateQuestion().key))
            .setQuestion(SessionQuestionDockView.newBuilder().setQuestionId("q").setTotalQuestions(1).setQuestion(q))
            .build()
        remote.showForTest(state, sessions = mapOf("s" to sessionVm(dock = dock)))
        show { QuestionPage("s", "q") }; ui.waitUntil(5000) { ui.onAllNodesWithText("Which implementation should we use?", substring = true).fetchSemanticsNodes().isNotEmpty() }; shot("question-page")
    }
    @Test fun planDock() {
        val dock = SessionDockView.newBuilder()
            .setHasItem(true).setKind(SessionDockKind.SESSION_DOCK_KIND_PLAN).setItemId("plan").setObservedSeq(6)
            .setTitle(Text.newBuilder().setKey(UiMessages.listStatePlan().key))
            .setPlan(SessionPlanDockView.newBuilder().setMarkdown(samplePlanMarkdown).addChoices(SessionPlanChoiceOptionView.newBuilder().setChoice(PlanChoice.PLAN_CHOICE_IMPLEMENT).setLabel(Text.newBuilder().setLiteral("Implement plan"))))
            .build()
        val vm = sessionVm(dock = dock)
        show { SessionDock(vm, {}, {}, {}, {}) }; shot("plan-dock")
    }
    @Test fun planPage() {
        val dock = SessionDockView.newBuilder()
            .setHasItem(true).setKind(SessionDockKind.SESSION_DOCK_KIND_PLAN).setItemId("plan").setObservedSeq(6)
            .setTitle(Text.newBuilder().setKey(UiMessages.listStatePlan().key))
            .setPlan(SessionPlanDockView.newBuilder().setMarkdown(samplePlanMarkdown).addChoices(SessionPlanChoiceOptionView.newBuilder().setChoice(PlanChoice.PLAN_CHOICE_IMPLEMENT).setLabel(Text.newBuilder().setLiteral("Implement plan"))))
            .build()
        remote.showForTest(state, sessions = mapOf("s" to sessionVm(dock = dock)))
        show { PlanPage("s") }; ui.waitUntil(5000) { ui.onAllNodesWithText("Proposed plan", substring = true).fetchSemanticsNodes().isNotEmpty() }; shot("plan-page")
    }
    @Test fun modelAndModeSheets() {
        val choices = listOf(SessionModelChoiceView.newBuilder().setSlug("example-model").setDisplayName("Example model").addEfforts("high").setSelected(true).build())
        remote.showForTest(state, sessions = mapOf("s" to sessionVm(modelsLoading = true)))
        show { val current by remote.watchSession("s").collectAsState(initial = defaultSessionVm); ModelSheet("s", current) {} }
        ui.runOnIdle { remote.showForTest(state, sessions = mapOf("s" to sessionVm(modelChoices = choices))) }
        ui.waitUntil(5000) { ui.onAllNodesWithText("Example model", substring = true).fetchSemanticsNodes().isNotEmpty() }; shot("session-settings")
    }
    @Test fun modeSheet() { show { ModeSheet("s", defaultSessionVm) {} }; shot("approval-mode") }
    @Test fun renameDialog() { show { RenameDialog("s", defaultSessionVm) {} }; shot("rename") }
    @Test fun attachmentRows() {
        show { Column(Modifier.padding(16.dp)) {
            FileChip("example.patch", "2.0 KB", PendingImage.State.Uploading(.42f), {}, {})
            FileChip("notes.txt", "320 B", PendingImage.State.Failed.raw("Raw transport diagnostic"), {}, {})
            Text(uiText(UiMessages.attachmentMetadata()))
            GeneratedCard("generated", listOf(ImageRef.newBuilder().setImageId("image").setWidth(640).setHeight(360).build()), "Original image prompt") {}
        } }; shot("attachments")
    }
    @Test fun imageViewer() {
        val bitmap = Bitmap.createBitmap(240, 160, Bitmap.Config.ARGB_8888).apply { eraseColor(android.graphics.Color.rgb(100, 150, 180)) }
        val bytes = java.io.ByteArrayOutputStream().apply { bitmap.compress(Bitmap.CompressFormat.PNG, 100, this) }.toByteArray()
        val image = ImageRef.newBuilder().setImageId("image").setPath("/example/demo/example.png")
            .setPreview(com.google.protobuf.ByteString.copyFrom(bytes)).setPreviewWidth(240).setPreviewHeight(160)
            .setWidth(240).setHeight(160).setSync(ImageSync.IMAGE_SYNC_NAME_ONLY).setSource(ImageSource.IMAGE_SOURCE_USER).build()
        val imgItem = tl(6, "e6") { userMessage = SessionUserMessageItem.newBuilder().addImages(image).build() }
        val viewerImg = SessionViewerImageView.newBuilder()
            .setViewerIndex(0)
            .setSeq(6)
            .setEventImageIndex(0)
            .setKind(SessionImageSourceKind.SESSION_IMAGE_SOURCE_KIND_USER)
            .setRef(image)
            .setSourceLabel(Text.newBuilder().setKey(UiMessages.imageUserSource().key))
            .setSizeText("240 × 160")
            .setHasDisplayCopy(false)
            .setDisplayName("example.png")
            .setDisplayPath("/example/demo/example.png")
            .setFileExtension("png")
            .setMimeType("image/png")
            .build()
        val vm = sessionVm(items = history + imgItem).toBuilder()
            .setTimeline(
                sessionVm(items = history + imgItem).timeline.toBuilder()
                    .addViewerImages(viewerImg),
            )
            .setImageViewer(
                SessionImageViewerView.newBuilder()
                    .setOpen(true)
                    .setCurrentIndex(0)
                    .setTotalCount(1)
                    .setPositionText(Text.newBuilder().setLiteral("1 / 1"))
                    .setCurrentImage(viewerImg),
            )
            .build()
        remote.showForTest(state, sessions = mapOf("s" to vm))
        show { ImageViewer("s", 6, 0) }; shot("image-viewer")
    }
    @Test fun draftViewer() {
        val draft = ImageDraft("preview-draft", MemoryFiles(), scope = null)
        val bitmap = Bitmap.createBitmap(240, 160, Bitmap.Config.ARGB_8888).apply { eraseColor(android.graphics.Color.rgb(100, 150, 180)) }
        val image = PendingImage("draft", null, retainSource = false).apply {
            thumb = bitmap.asImageBitmap()
            state = PendingImage.State.Uploading(.42f)
        }
        draft.images += image
        show { DraftImageViewer(draft, remote, "m", 0) {} }; shot("draft-viewer")
    }
    @Test fun terminalView() { show { TerminalPane(remote, "s", true) }; shot("terminal") }
    @Test fun terminalReader() {
        show { Column(Modifier.fillMaxSize().background(TermColors.bg)) {
            Text(text(UiMessages.terminalReadonly()), color = TermColors.tool, modifier = Modifier.padding(12.dp))
            Text("$ git diff --stat\n example.txt | 2 +-\n", color = Color.White, modifier = Modifier.weight(1f).padding(12.dp))
            HistoryStatusLine(TerminalHistory.State(disconnected = true), false, {})
            HistoryPill(true, {})
            KeyBar(true, {}, {})
        } }; shot("terminal-reader")
    }
    @Test fun languageChangeKeepsComposerAndRebuildsChat() {
        val draft = mutableStateOf("保留原始输入 / original input")
        show { Column { ChatPane("s", state, defaultSessionVm, Modifier.weight(1f), {}, {}, {})
            ComposeBox(draft.value, { draft.value = it }, text(UiMessages.composerMessage()), true, {}, {}) } }
        ui.waitForIdle()
        ui.runOnIdle { language = if (locale == "en") "zh-CN" else "en" }
        ui.onAllNodes(hasSetTextAction()).onLast().assertTextEquals(draft.value)
        ui.onAllNodesWithContentDescription(text(UiMessages.composerSend())).onLast().assertExists()
        ui.waitUntil(5000) { ui.onAllNodesWithText(text(UiMessages.sessionTurnDuration("1m12s"))).fetchSemanticsNodes().isNotEmpty() }
    }
    @Test fun quantitiesAndSourceIdentity() {
        for (count in listOf(0, 1, 2, 1000)) {
            val rendered = text(UiMessages.toolsCompletedCommands(count)); Assert.assertTrue(rendered.contains(count.toString()))
            if (locale == "en") Assert.assertEquals("Ran $count ${if (count == 1) "command" else "commands"}", rendered)
            Assert.assertTrue(text(UiMessages.queueMoreItems(count)).contains(count.toString()))
            Assert.assertTrue(text(UiMessages.imagePosition(count, 1)).contains(count.toString()))
            val restore = text(UiMessages.imageAndroidRestoreLimit(count, 1, 9))
            if (locale == "en") Assert.assertTrue(restore.contains("Returning $count ${if (count == 1) "image would" else "images would"}"))
        }
        val closed = RemoteTerminal.State.Closed(UiMessages.terminalAndroidClosed().messageText())
        Assert.assertEquals(text(UiMessages.terminalAndroidClosed()), closed.reason.resolve(::text))
        Assert.assertEquals("Unchanged CLI text", "Unchanged CLI text".messageText().resolve(::text))
    }
}
