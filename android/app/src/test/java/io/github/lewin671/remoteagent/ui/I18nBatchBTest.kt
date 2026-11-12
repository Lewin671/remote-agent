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
    @get:Rule(order = 0) val appFixture = io.github.lewin671.remoteagent.RaAppTestRule()
    @get:Rule(order = 1) val ui = createComposeRule()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "preview-client"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private var language by mutableStateOf(locale)
    private val stamp = System.currentTimeMillis()
    private val session = SessionView("s", "m", Session.newBuilder().setId("s").setCwd("/example/demo").setTitle("Example task")
        .setAgent(Agent.AGENT_CLAUDE).setMode(ApprovalMode.APPROVAL_MODE_ASK).setStatus(SessionStatus.SESSION_STATUS_IDLE)
        .setModel("example-model").setCreatedAt(Timestamp.newBuilder().setSeconds(stamp / 1000)).build())
    private val machine = MachineView("m", true, stamp, MachineMeta.newBuilder().setHostname("Example computer").setProtocolVersion(PROTOCOL_VERSION)
        .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).build())
    private val state = RemoteState(conn = ConnState.ONLINE, link = Link.ONLINE, loaded = true, synced = true, isAuthority = true,
        prefs = PrefsObject.EMPTY, machines = mapOf("m" to machine), sessions = mapOf("s" to session))
    private val approval = ApprovalRequested.newBuilder().setApprovalId("a").setCommand("git diff --stat")
        .setDescription("Inspect the proposed changes").setDeadline(Timestamp.newBuilder().setSeconds(stamp / 1000 + 480)).build()
    private val question = QuestionAsked.newBuilder().setQuestionId("q").addQuestions(Question.newBuilder().setId("first")
        .setText("Which implementation should we use?").setMultiSelect(true)
        .addOptions(QuestionOption.newBuilder().setLabel("Small change (Recommended)").setDescription("Keep the public interface"))
        .addOptions(QuestionOption.newBuilder().setLabel("Broader cleanup").setDescription("Update the implementation"))).build()
    private val plan = "## Proposed plan\n\n1. Inspect the implementation\n2. Apply a focused change\n3. Verify behavior"
    private fun event(seq: Long, block: Event.Builder.() -> Unit) = Event.newBuilder().setSeq(seq).setSessionId("s").apply(block).build()
    private val history get() = listOf(
        event(1) { userMessage = UserMessage.newBuilder().setText("Keep this original input unchanged").build() },
        event(2) { agentMessage = AgentMessage.newBuilder().setText("The original CLI reply stays unchanged.").build() },
        event(3) { toolCall = ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("git status --short").setStatus("completed").build() },
        event(4) { toolCall = ToolCall.newBuilder().setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("git diff --stat").setStatus("completed").build() },
        event(5) { turnEnded = TurnEnded.newBuilder().setOutcome(TurnOutcome.TURN_OUTCOME_COMPLETED).setDurationMs(72_000).build() },
    )
    @Before fun prepare() {
        remote.showForTest(state, events = mapOf("s" to history), modelLists = mapOf(("m" to Agent.AGENT_CLAUDE) to
            ListModelsResponse.newBuilder().setDefaultModel("example-model").addModels(ModelInfo.newBuilder().setSlug("example-model").setDisplayName("Example model").addEfforts("high")).build()))
    }
    @After fun close() { remote.close(); Outbox.clear(); ImageDrafts.clear(); Drafts.clear(); Plans.clear(); Answers.clear(); Resolves.clear(); DockFolds.clear() }
    private fun text(message: UiMessage) = messageTextForTest(message, language)
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
        val ended = session.copy(info = session.info!!.toBuilder().setStatus(SessionStatus.SESSION_STATUS_EXITED).setStatusDetail("stopped").build())
        remote.showForTest(state.copy(sessions = mapOf("s" to ended)), events = mapOf("s" to history))
        show { SessionScreen("s") }; shot("session-recovery")
    }
    @Test fun composerMenus() {
        show { Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.Bottom) {
            ComposeBox("Preserve draft text", {}, text(UiMessages.composerMessage()), true, {}, {}, onFile = {}, onQueue = {})
        } }
        shot("composer")
        ui.onNodeWithContentDescription(text(UiMessages.composerSend())).performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.OnLongClick) { it() }
        shot("send-menu")
    }
    @Test fun attachmentMenu() {
        show { Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.Bottom) {
            ComposeBox("", {}, text(UiMessages.composerMessage()), false, {}, {}, onFile = {})
        } }
        ui.onNodeWithContentDescription(text(UiMessages.newSessionIosAttach())).performClick(); shot("attachment-menu")
    }
    @Test fun historyRows() {
        val events = history + event(6) { reasoning = Reasoning.newBuilder().setText("Original reasoning text").build() } +
            event(7) { planProposed = PlanProposed.newBuilder().setText(plan).build() } +
            event(8) { settingsChanged = SettingsChanged.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_PLAN).build() }
        val rows = buildChat(events, derive(events), "Example computer", copy = ::text)
        show { Column(Modifier.fillMaxSize()) { rows.forEach { ChatItemView(it, agentName(Agent.AGENT_CLAUDE, copy = ::messageTextForTest), "/example/demo", "/example") } } }
        shot("history")
    }
    @Test fun queuedMessages() {
        val queued = event(6) { inputState = InputState.newBuilder().setRequestId("queued").setText("A pending message with original content")
            .setStatus(InputStatus.INPUT_STATUS_QUEUED).setMode(SendMode.SEND_MODE_QUEUE).setPaused(true).build() }
        remote.showForTest(state, events = mapOf("s" to history + queued))
        show { SessionScreen("s") }; shot("queue")
        ui.onNodeWithText(text(UiMessages.queuePaused())).assertExists()
    }
    @Test fun approvalDockAndPage() {
        remote.showForTest(state, events = mapOf("s" to history + event(6) { approvalRequested = approval }), live = mapOf("s" to LiveSnapshot.newBuilder().setSession(session.info).addActions(event(6) { approvalRequested = approval }).build()))
        show { Column { ApprovalDock(session, approval, 2) } }; shot("approval-dock")
    }
    @Test fun approvalPage() {
        remote.showForTest(state, events = mapOf("s" to history + event(6) { approvalRequested = approval }), live = mapOf("s" to LiveSnapshot.newBuilder().setSession(session.info).addActions(event(6) { approvalRequested = approval }).build()))
        show { ApprovalPage("s", "a") }; ui.waitUntil(5000) { ui.onAllNodesWithText("git diff --stat", substring = true).fetchSemanticsNodes().isNotEmpty() }; shot("approval-page")
    }
    @Test fun questionDock() {
        remote.showForTest(state, events = mapOf("s" to history + event(6) { questionAsked = question }), live = mapOf("s" to LiveSnapshot.newBuilder().setSession(session.info).addActions(event(6) { questionAsked = question }).build()))
        show { QuestionDock(session, question, 1) {} }; shot("question-dock")
    }
    @Test fun questionPage() {
        remote.showForTest(state, events = mapOf("s" to history + event(6) { questionAsked = question }), live = mapOf("s" to LiveSnapshot.newBuilder().setSession(session.info).addActions(event(6) { questionAsked = question }).build()))
        show { QuestionPage("s", "q") }; ui.waitUntil(5000) { ui.onAllNodesWithText("Which implementation should we use?", substring = true).fetchSemanticsNodes().isNotEmpty() }; shot("question-page")
    }
    @Test fun planDock() {
        show { PlanDock(session, plan, 1) {} }; shot("plan-dock")
    }
    @Test fun planPage() {
        remote.showForTest(state.copy(sessions = mapOf("s" to session.copy(info = session.info!!.toBuilder().setPlanOpen(true).build()))), events = mapOf("s" to history + event(6) { planProposed = PlanProposed.newBuilder().setText(plan).build() }), live = mapOf("s" to LiveSnapshot.newBuilder().setSession(session.info!!.toBuilder().setPlanOpen(true)).setPlanEventSeq(6).setPlanScreenHash(com.google.protobuf.ByteString.copyFrom(ByteArray(32))).setPlanScreen(plan).build()))
        show { PlanPage("s") }; ui.waitUntil(5000) { ui.onAllNodesWithText("Proposed plan", substring = true).fetchSemanticsNodes().isNotEmpty() }; shot("plan-page")
    }
    @Suppress("UNCHECKED_CAST")
    @Test fun modelAndModeSheets() {
        val response = ListModelsResponse.newBuilder().setDefaultModel("example-model").addModels(ModelInfo.newBuilder().setSlug("example-model").setDisplayName("Example model").addAllEfforts(listOf("low", "high"))).build()
        val inFlight = kotlinx.coroutines.CompletableDeferred<ListModelsResponse>()
        val cache = Remote::class.java.getDeclaredField("models").apply { isAccessible = true }.get(remote) as MutableMap<String, kotlinx.coroutines.Deferred<ListModelsResponse>>
        cache["m/${Agent.AGENT_CLAUDE.number}"] = inFlight
        show { ModelSheet(state, session, false) {} }; ui.runOnIdle { inFlight.complete(response) }; ui.waitUntil(5000) { ui.onAllNodesWithText("Example model", substring = true).fetchSemanticsNodes().isNotEmpty() }; shot("session-settings") }
    @Test fun modeSheet() { show { ModeSheet(session) {} }; shot("approval-mode") }
    @Test fun renameDialog() { show { RenameDialog(session) {} }; shot("rename") }
    @Test fun attachmentRows() {
        show { Column(Modifier.padding(16.dp)) {
            FileChip("example.patch", 2048, PendingImage.State.Uploading(.42f), {}, {})
            FileChip("notes.txt", 320, PendingImage.State.Failed("Raw transport diagnostic"), {}, {})
            Text(uiText(FILES_AS_THEY_ARE))
            GeneratedCard(ChatItem.Generated("generated", 1, "Original image prompt", listOf(ImageRef.newBuilder().setImageId("image").setWidth(640).setHeight(360).build()))) {}
        } }; shot("attachments")
    }
    @Test fun imageViewer() {
        val bitmap = Bitmap.createBitmap(240, 160, Bitmap.Config.ARGB_8888).apply { eraseColor(android.graphics.Color.rgb(100, 150, 180)) }
        val bytes = java.io.ByteArrayOutputStream().apply { bitmap.compress(Bitmap.CompressFormat.PNG, 100, this) }.toByteArray()
        val image = ImageRef.newBuilder().setImageId("image").setPath("/example/demo/example.png")
            .setPreview(com.google.protobuf.ByteString.copyFrom(bytes)).setPreviewWidth(240).setPreviewHeight(160)
            .setWidth(240).setHeight(160).setSync(ImageSync.IMAGE_SYNC_NAME_ONLY).setSource(ImageSource.IMAGE_SOURCE_USER).build()
        remote.showForTest(state, events = mapOf("s" to history + event(6) { userMessage = UserMessage.newBuilder().addImages(image).build() }))
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
        show { Column { ChatPane(state, session, Status.IDLE, history, derive(history), true, null, null, Modifier.weight(1f), {})
            ComposeBox(draft.value, { draft.value = it }, text(UiMessages.composerMessage()), true, {}, {}) } }
        ui.waitForIdle()
        ui.runOnIdle { language = if (locale == "en") "zh-CN" else "en" }
        ui.onAllNodes(hasSetTextAction()).onLast().assertTextEquals(draft.value)
        ui.onAllNodesWithContentDescription(text(UiMessages.composerSend())).onLast().assertExists()
        ui.waitUntil(5000) { ui.onAllNodesWithText(text(UiMessages.sessionTurnDuration(duration(72_000, ::text)))).fetchSemanticsNodes().isNotEmpty() }
    }
    @Test fun quantitiesAndSourceIdentity() {
        for (count in listOf(0, 1, 2, 1000)) {
            val rendered = text(UiMessages.toolsCompletedCommands(count)); Assert.assertTrue(rendered.contains(count.toString()))
            if (locale == "en") Assert.assertEquals("Ran $count ${if (count == 1) "command" else "commands"}", rendered)
            Assert.assertTrue(text(UiMessages.queueMore(count)).contains(count.toString()))
            Assert.assertTrue(text(UiMessages.imagePosition(count, 1)).contains(count.toString()))
            val restore = text(UiMessages.imageAndroidRestoreLimit(count, 1, 9))
            if (locale == "en") Assert.assertTrue(restore.contains("Returning $count ${if (count == 1) "image would" else "images would"}"))
        }
        val label = text(UiMessages.sessionOriginSelf())
        Assert.assertEquals(label, messageSource(ChatSource(label), false, "computer", ::text))
        val other = DeviceView("other", DeviceKind.DEVICE_KIND_CLIENT, 0, DeviceMeta.newBuilder().setName(label).build())
        val origin = Origin.newBuilder().setKind(OriginKind.ORIGIN_KIND_CLIENT).setDeviceId(other.id).build()
        val namedSource = chatSource(origin, state.copy(devices = mapOf(other.id to other)), "me", "computer", ::text)!!
        Assert.assertFalse(namedSource.self)
        Assert.assertEquals(label, messageSource(namedSource, false, "computer", ::text))
        val closed = RemoteTerminal.State.Closed(UiMessages.terminalAndroidClosed().messageText())
        Assert.assertEquals(text(UiMessages.terminalAndroidClosed()), closed.reason.resolve(::text))
        Assert.assertNull(messageSource(ChatSource(label, true), true, "computer", ::text))
        Assert.assertEquals("Unchanged CLI text", "Unchanged CLI text".messageText().resolve(::text))
    }
}
