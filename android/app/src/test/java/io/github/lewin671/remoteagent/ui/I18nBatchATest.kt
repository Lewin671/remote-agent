package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.graphics.Bitmap
import android.graphics.Canvas
import android.view.inspector.WindowInspector
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import org.junit.*
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File

/** Stopped-engine previews: no account, CLI, emulator or device is contacted. */
@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class, qualifiers = "w390dp-h844dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class I18nBatchATest(private val locale: String) {
    companion object {
        @JvmStatic @ParameterizedRobolectricTestRunner.Parameters(name = "{0}")
        fun locales() = listOf(arrayOf("zh-CN"), arrayOf("en"))
    }
    @get:Rule(order = 0) val appFixture = io.github.lewin671.remoteagent.RaAppTestRule(preparePendingStore = true)
    @get:Rule(order = 1) val ui = createComposeRule()
    @get:Rule(order = 2) val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }
    private val remote = Remote(Account("http://127.0.0.1:1", "preview-client"), Api.defaultHttp(), dispatcher = kotlinx.coroutines.Dispatchers.Unconfined)
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val path = "/example/demo"
    private var language by mutableStateOf(locale)
    private val models = ListModelsResponse.newBuilder().setDefaultModel("first")
        .addModels(ModelInfo.newBuilder().setSlug("first").setDisplayName("Example Sonnet").addAllEfforts(listOf("low", "medium", "high")))
        .addModels(ModelInfo.newBuilder().setSlug("second").setDisplayName("Example Opus").addAllEfforts(listOf("low", "high")))
        .addModels(ModelInfo.newBuilder().setSlug("third").setDisplayName("Example Haiku").setContextWindow(200_000).addEfforts("low"))
        .addAllModels((4..6).map { ModelInfo.newBuilder().setSlug("model-$it").setDisplayName("Example model $it").build() }).build()
    private fun machine(id: String) = MachineView(id, true, 0, MachineMeta.newBuilder().setHostname("Computer $id").setProtocolVersion(PEER_PROTOCOL_VERSION)
        .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX)).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).build())
    private val stamp = System.currentTimeMillis()
    private fun session(id: String, folder: String = path, pending: Boolean = false, ended: Boolean = false) = SessionView(id, "a",
        Session.newBuilder().setCwd(folder).setTitle("Example task $id").setAgent(Agent.AGENT_CLAUDE).setPreview("User and CLI content stays unchanged")
            .setPendingQuestions(if (pending) 1 else 0).setStatus(if (ended) SessionStatus.SESSION_STATUS_EXITED else SessionStatus.SESSION_STATUS_RUNNING)
            .setCreatedAt(Timestamp.newBuilder().setSeconds((stamp - 60_000) / 1000))
            .apply { if (ended) setEndedAt(Timestamp.newBuilder().setSeconds((stamp - 120_000) / 1000)) }.build(), updatedMs = stamp - 120_000)
    private val state get() = RemoteState(conn = ConnState.ONLINE, link = Link.ONLINE, loaded = true, synced = true, isAuthority = true,
        prefs = PrefsObject.EMPTY, machines = listOf("a", "b").associateWith(::machine),
        sessions = listOf(session("question", pending = true), session("running"), session("ended", ended = true), session("other", "/example/notes")).associateBy { it.id })
    @Before fun prepare() {
        remote.showForTest(state, modelLists = listOf("a", "b").flatMap { id -> listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE).map { (id to it) to models } }.toMap())
        remote.coreHost.openManageProjects()
        remote.coreHost.openNewSession(path)
    }
    @After fun close() { remote.close(); Drafts.clear(); ImageDrafts.clear(); Starts.clear(); HomeScopes.clear(RuntimeEnvironment.getApplication()) }
    private fun text(message: UiMessage) = messageTextForTest(message, language)
    private fun show(content: @Composable () -> Unit) = ui.setContent {
        RaTheme(ThemeMode.LIGHT) {
            CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalMessageLocale provides language) {
                Box(Modifier.fillMaxSize()) { content() }
            }
        }
    }
    private fun shot(name: String) {
        ui.waitForIdle()
        val output = System.getenv("RA_I18N_SHOTS") ?: return
        val file = File(output, "$locale-$name.png"); file.parentFile!!.mkdirs()
        ui.mainClock.advanceTimeBy(600)
        ui.runOnIdle {
            val view = WindowInspector.getGlobalWindowViews().last()
            val bitmap = Bitmap.createBitmap(view.width, view.height, Bitmap.Config.ARGB_8888)
            view.draw(Canvas(bitmap))
            file.outputStream().use { Assert.assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }
    private fun composer() = show {
        val vm by remote.newSessionView.collectAsState()
        val st by remote.state.collectAsState()
        NewSessionBox(vm, st, focus = false, onProject = {})
    }
    @Test fun home() { show { HomeScreen() }; shot("home") }
    @Test fun scope() { show { HomeScreen() }; ui.onNodeWithTag("scope-title").performClick(); shot("scope") }
    @Test fun manage() { show { ManageProjectsScreen() }; shot("projects") }
    @Test fun menuAndRemoval() {
        show { ManageProjectsScreen() }
        ui.onNodeWithContentDescription(text(UiMessages.projectsManageNamed("demo"))).performClick(); shot("project-menu")
        ui.onNodeWithText(text(UiMessages.projectsRemoveAction())).performClick(); shot("project-remove")
    }
    @Test fun newTaskAndCli() { composer(); shot("new-task"); ui.onNodeWithTag("new-agent").performClick(); shot("cli") }
    @Test fun modelContextUsesStableValues() {
        composer(); ui.onNodeWithTag("new-model").performClick(); shot("models")
        ui.onNodeWithText(text(UiMessages.modelsStandardWindow())).performClick()
        ui.onNodeWithText(text(UiMessages.commonDone())).performClick()
        ui.runOnIdle { Assert.assertTrue(remote.newSessionView.value.config.standardContext) }
        ui.onNodeWithTag("new-model").performClick(); ui.onNodeWithText(text(UiMessages.modelsExtendedWindow())).performClick()
        ui.onNodeWithText(text(UiMessages.commonDone())).performClick()
        ui.runOnIdle { Assert.assertFalse(remote.newSessionView.value.config.standardContext) }
    }
    @Test fun allModels() {
        composer(); ui.onNodeWithTag("new-model").performClick()
        ui.onNodeWithTag("new-model-show-all").performClick(); shot("all-models")
    }
    @Test fun projectSettings() { show { CoreProjectSettingsSheet(path, onClose = {}) }; shot("project-settings") }
    @Test fun projectModels() { show { CoreProjectSettingsSheet(path, open = "model", onClose = {}) }; shot("project-models") }
    @Test fun machineChoice() { show { CoreProjectSettingsSheet(path, open = "machine", onClose = {}) }; shot("choose-machine") }
    @Test fun projectChoice() { show { PickProjectSheet(remote.homeView.collectAsState().value.sidebarProjects, {}) }; shot("choose-project") }
    @Test fun directoryOffline() { show { Column { CoreDirPickerBody(io.github.lewin671.remoteagent.core.proto.DirPickerView.newBuilder().setOpen(true).build(), onChosen = { _, _ -> }) } }; shot("new-project") }
    @Test fun emptyAndConnection() {
        show { HomeScreen() }; ui.runOnIdle { remote.showForTest(state.copy(sessions = emptyMap(), machines = emptyMap())) }; shot("empty")
        ui.runOnIdle { remote.showForTest(state.copy(link = Link.NO_NETWORK)) }; shot("connection")
    }
    @Test fun languageChangeKeepsDraftAndConfig() {
        composer(); val raw = "保留原始输入 / original input"
        ui.onNode(hasSetTextAction()).performTextInput(raw)
        val before = remote.newSessionView.value.config
        ui.runOnIdle { language = if (locale == "en") "zh-CN" else "en" }
        ui.onNode(hasSetTextAction()).assertTextEquals(raw)
        ui.onNodeWithContentDescription(text(UiMessages.newSessionAndroidStart())).assertExists()
        ui.runOnIdle { Assert.assertEquals(before, remote.newSessionView.value.config) }
    }
    @Test fun pluralResourcesAndRawText() {
        for (count in listOf(0, 1, 2, 1000)) {
            val value = text(UiMessages.homeWaitingCount(count)); Assert.assertTrue(value.contains(count.toString()))
            if (locale == "en") Assert.assertEquals("$count ${if (count == 1) "item needs" else "items need"} you", value)
        }
        val raw = "中文 CLI 原文"; Assert.assertEquals(raw, raw.messageText().resolve { text(it) })
    }
}
