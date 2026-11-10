package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Density
import io.github.lewin671.remoteagent.core.HomeScope
import io.github.lewin671.remoteagent.core.Link
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.longClick
import androidx.test.platform.app.InstrumentationRegistry
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.PrefsObject
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.ApprovalResolved
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.DefaultModeSource
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.PlanProposed
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.ProjectPref
import io.github.lewin671.remoteagent.proto.Question
import io.github.lewin671.remoteagent.proto.QuestionAsked
import io.github.lewin671.remoteagent.proto.QuestionOption
import io.github.lewin671.remoteagent.proto.Reasoning
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolBackground
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnEnded
import io.github.lewin671.remoteagent.proto.TurnStarted
import io.github.lewin671.remoteagent.proto.UserMessage
import io.github.lewin671.remoteagent.proto.MachineCapabilities
import io.github.lewin671.remoteagent.proto.ListModelsResponse
import io.github.lewin671.remoteagent.proto.ModelInfo
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.AgentQuota
import io.github.lewin671.remoteagent.proto.QuotaState
import io.github.lewin671.remoteagent.proto.QuotaWindow
import io.github.lewin671.remoteagent.core.DeviceView
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.DeviceMeta
import org.junit.After
import org.junit.Rule
import org.junit.Test
import java.io.File

/**
 * The home page of docs/design/client-v58.html (#home, #link) with the
 * mockup's example data, light and dark and at twice the font size, saved
 * as PNGs under the app's external files, v58/, for comparing with the
 * mockup. Nothing is sent: the Remote only shows fixed state.
 */
class V58HomeGallery {
    @get:Rule val ui = createComposeRule()

    private val now = System.currentTimeMillis()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val home = "/Users/me"
    private val foo = "$home/Code/foo"
    private var dark by mutableStateOf(false)
    private var font by mutableStateOf(1f)

    @After fun close() {
        remote.close()
        HomeScopes.clear(InstrumentationRegistry.getInstrumentation().targetContext)
    }

    private fun ts(ms: Long) = Timestamp.newBuilder().setSeconds(ms / 1000).build()
    private fun at(h: Int, m: Int): Long = java.time.LocalDate.now().atTime(h, m).atZone(java.time.ZoneId.systemDefault()).toInstant().toEpochMilli()

    private fun machine(id: String, name: String, online: Boolean = true, claude: Boolean = false, antigravity: Boolean = false) = id to MachineView(
        id, online, if (online) now else now - 2 * 3600_000,
        MachineMeta.newBuilder().setHostname(name).setHome(home).setCapabilities(MachineCapabilities.newBuilder().setApprovalAlways(true))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG))
            .also { if (claude) it.addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG)) }
            .also { if (antigravity) it.addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_ANTIGRAVITY).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG)) }
            .build(),
    )

    private val machines = mapOf(machine("mac", "MacBook", claude = true, antigravity = true), machine("vps", "VPS-dev"), machine("imac", "iMac"))
    private val codexModels = ListModelsResponse.newBuilder().setDefaultModel("gpt-6-astra")
        .addModels(ModelInfo.newBuilder().setSlug("gpt-6-astra").setDisplayName("GPT-6-Astra").setDefaultEffort("medium")).build()
    private val claudeModels = ListModelsResponse.newBuilder().setDefaultModel("claude-sonnet-5-5")
        .addModels(ModelInfo.newBuilder().setSlug("claude-sonnet-5-5").setDisplayName("Claude 3.5 Sonnet").setDefaultEffort("high")).build()
    private val agyModels = ListModelsResponse.newBuilder().setDefaultModel("gemini-2.5-pro")
        .addModels(ModelInfo.newBuilder().setSlug("gemini-2.5-pro").setDisplayName("Gemini 2.5 Pro"))
        .addModels(ModelInfo.newBuilder().setSlug("gemini-2.5-flash").setDisplayName("Gemini 2.5 Flash"))
        .build()

    private fun session(
        id: String, machine: String, cwd: String, title: String, status: SessionStatus = SessionStatus.SESSION_STATUS_IDLE,
        agent: Agent = Agent.AGENT_CODEX, approvals: Int = 0, questions: Int = 0, plan: Boolean = false, preview: String = "",
        updated: Long = now, created: Long = now - 3600_000, mode: ApprovalMode = ApprovalMode.APPROVAL_MODE_ASK, detail: String = "",
        context: Long = 0,
    ) = id to SessionView(
        id, machine,
        Session.newBuilder().setId(id).setCwd(cwd).setTitle(title).setStatus(status).setAgent(agent).setPendingApprovals(approvals)
            .setPendingQuestions(questions).setPlanOpen(plan).setPreview(preview).setCreatedAt(ts(created)).setMode(mode).setStatusDetail(detail)
            .setModel(when (agent) {
                Agent.AGENT_CLAUDE -> "claude-sonnet-5-5"
                Agent.AGENT_ANTIGRAVITY -> "gemini-2.5-pro"
                else -> "gpt-6-astra"
            }).setEffort(if (agent == Agent.AGENT_CLAUDE) "high" else "medium")
            .setContextTokens(context).setContextWindow(if (context > 0) 760_000 else 0)
            .also { if (status == SessionStatus.SESSION_STATUS_EXITED) it.setEndedAt(ts(updated)) }.build(),
        updatedMs = updated,
    )

    private fun prefs(vararg pinned: String) = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1).addAllPinned(pinned.map { ProjectPref.newBuilder().setPath(it).build() }).build())

    private fun events(vararg list: Event.Builder): List<Event> = list.mapIndexed { i, b -> b.setSeq(i + 1L).setTime(ts(now - (list.size - i) * 1000L)).build() }
    private fun user(text: String) = Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText(text))
    private fun turn() = Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance())

    private fun approval(id: String, command: String, desc: String) = Event.newBuilder().setApprovalRequested(
        ApprovalRequested.newBuilder().setApprovalId(id).setCommand(command).setDescription(desc).setToolName("Bash").setDeadline(ts(now + 8 * 60_000 - 30_000)),
    )
    private fun question(id: String, text: String) = Event.newBuilder().setQuestionAsked(
        QuestionAsked.newBuilder().setQuestionId(id).addQuestions(Question.newBuilder().setText(text).addOptions(QuestionOption.newBuilder().setLabel("是")).addOptions(QuestionOption.newBuilder().setLabel("否"))),
    )

    /** Shows the home page with [state] and [history] in [scope], then saves it light and dark (and large when [large]) as [name]. */
    private fun shoot(name: String, state: RemoteState, history: Map<String, List<Event>> = emptyMap(), scope: HomeScope? = null, large: Boolean = false, then: (() -> Unit)? = null) {
        val shown = if (state.conn == ConnState.ONLINE) state.copy(link = Link.ONLINE, synced = true) else state
        remote.showForTest(shown, history, emptyMap(), history.mapValues { (sid, list) -> liveOf(state.sessions[sid], list) })
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        if (scope != null) HomeScopes.choose(context, remote.account.localKey, scope) else HomeScopes.clear(context)
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(if (dark) DarkPalette else LightPalette) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, font)) {
                    HomeScreen()
                }
            }
        }
        then?.invoke()
        save("$name-light")
        dark = true
        save("$name-dark")
        if (large) {
            dark = false
            font = 2f
            save("$name-large")
            font = 1f
        }
    }

    private fun liveOf(s: SessionView?, list: List<Event>): io.github.lewin671.remoteagent.proto.LiveSnapshot {
        val d = io.github.lewin671.remoteagent.core.derive(list, s?.info?.agent ?: Agent.AGENT_CODEX)
        val b = io.github.lewin671.remoteagent.proto.LiveSnapshot.newBuilder().setRunning(d.running)
        s?.info?.let { b.setSession(it) }
        list.filter { (it.hasApprovalRequested() && it.approvalRequested.approvalId in d.approvals) || (it.hasQuestionAsked() && it.questionAsked.questionId in d.questions) }
            .forEach { b.addActions(it) }
        return b.build()
    }

    private fun save(name: String) {
        ui.waitForIdle()
        Thread.sleep(700)
        val i = InstrumentationRegistry.getInstrumentation()
        val bmp = i.uiAutomation.takeScreenshot() ?: return
        val dir = File(i.targetContext.getExternalFilesDir(null), "v58").apply { mkdirs() }
        File(dir, "$name.png").outputStream().use { bmp.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private val running = mapOf(
        session("run", "mac", foo, "修复登录超时", SessionStatus.SESSION_STATUS_RUNNING, preview = "go test ./auth/...", created = now - 60_000, updated = now - 20_000),
        session("doc", "mac", foo, "补充接口文档", agent = Agent.AGENT_CLAUDE, preview = "已补充 12 个接口的说明，要生成 OpenAPI 吗？", created = at(8, 12), updated = at(8, 12)),
        session("ops", "imac", "$home/Code/billing", "整理部署脚本", agent = Agent.AGENT_ANTIGRAVITY, created = now - 2 * 60_000, updated = at(11, 25)),
    )
    private val ended = mapOf(
        session("e1", "mac", "$home/Code/blog", "发布秋季文章", SessionStatus.SESSION_STATUS_EXITED, updated = now - 2 * 3600_000),
        session("e2", "mac", foo, "拆分 handler 包", SessionStatus.SESSION_STATUS_EXITED, updated = now - 3 * 3600_000),
        session("e3", "vps", "$home/Code/blog", "修复 RSS 日期格式", SessionStatus.SESSION_STATUS_EXITED, updated = now - 26 * 3600_000),
        session("e4", "mac", "$home/notes", "整理读书笔记目录", SessionStatus.SESSION_STATUS_EXITED, updated = now - 30 * 3600_000),
    )
    private fun waiting(extra: Boolean) = mapOf(
        session("api", "vps", foo, "重构 API 层", approvals = 1, updated = now - 50 * 60_000),
        session("dep", "imac", "$home/Code/bar", "升级依赖", questions = 1, updated = now - 40 * 60_000),
        session("p3", "mac", "$home/Code/shop", "重写同步层", plan = true, preview = "共 9 步，预计改 31 个文件", updated = now - 30 * 60_000),
        session("p4", "vps", foo, "梳理登录流程", SessionStatus.SESSION_STATUS_BLOCKED, detail = "Permission Config Editor", updated = now - 20 * 60_000),
    ) + if (!extra) emptyMap() else mapOf(
        session("q2", "mac", foo, "补测试", questions = 1, updated = now - 10 * 60_000),
        session("q3", "mac", foo, "改文案", questions = 1, updated = now - 5 * 60_000),
    )
    private val command = "rm -rf gen/remoteagent && \\\nbuf generate --template buf.gen.yaml && \\\ngo build ./... && \\\ngo test ./internal/... -count=1 \\\n  -run 'Test(Pair|Session|Terminal)' && \\\ngo vet ./... && \\\ngit status --short gen/"
    private val history = mapOf(
        "api" to events(turn(), user("跑一遍全部测试"), approval("A1", command, "跑一遍全部测试，确认拆包没有破坏行为。先清掉旧的生成文件再用 buf 重新生成，编译通过后只跑配对、会话和终端三组测试")),
        "dep" to events(turn(), user("把依赖都升级到最新的版本"), question("Q1", "大版本也一起升吗？")),
        "q2" to events(turn(), question("Q2", "要补集成测试吗？")),
        "q3" to events(turn(), question("Q3", "按哪个文案改？")),
    )
    private fun state(sessions: Map<String, SessionView>, link: Link = Link.ONLINE) = RemoteState(
        machines = machines, conn = if (link == Link.ONLINE) ConnState.ONLINE else ConnState.OFFLINE, loaded = true, prefs = prefs(foo),
        sessions = sessions, link = link, synced = true,
    )

    @Test fun attention() = shoot("01-attention", state(waiting(false) + running + ended), history, large = true)

    @Test fun commandOpen() = shoot("02-command-open", state(waiting(true) + running), history) {
        ui.onNodeWithText("看完整命令").performClick()
    }

    @Test fun denying() = shoot("03-denying", state(waiting(false) + running), history) {
        ui.onNodeWithText("不允许").performClick()
    }

    @Test fun quiet() = shoot("04-running", state(running + ended), large = true)

    @Test fun projectScope() = shoot("05-scope-foo", state(mapOf(waiting(false).entries.first { it.key == "dep" }.toPair()) + running + ended), history, scope = HomeScope.Of(foo))

    @Test fun scopeMenu() = shoot("06-scope-menu", state(running + ended)) {
        ui.onNodeWithTag("scope-title").performClick()
    }

    @Test fun emptyAccount() = shoot("07-empty-account", RemoteState(machines = machines, conn = ConnState.ONLINE, loaded = true, prefs = prefs()))

    @Test fun emptyProject() = shoot(
        "08-empty-project",
        state(running).copy(prefs = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1).addPinned(ProjectPref.newBuilder().setPath(foo))
            .addSettings(io.github.lewin671.remoteagent.proto.ProjectSettings.newBuilder().setPath("$home/Code/ai-bot").setMachineId("mac")).build())),
        scope = HomeScope.Of("$home/Code/ai-bot"),
    )

    @Test fun noNetwork() = shoot("09-no-network", state(waiting(false) + running + ended, Link.NO_NETWORK), history)

    @Test fun reconnecting() = shoot("10-reconnecting", state(waiting(false) + running, Link.WAITING), history)

    @Test fun noServer() = shoot("11-no-server", state(waiting(false) + running + ended, Link.NO_SERVER), history)

    @Test fun noNetworkNothingKept() = shoot("12-no-network-empty", RemoteState(link = Link.NO_NETWORK, conn = ConnState.OFFLINE))

    @Test fun noServerNothingKept() = shoot("13-no-server-empty", RemoteState(link = Link.NO_SERVER, conn = ConnState.OFFLINE, error = "证书无效"))
}
