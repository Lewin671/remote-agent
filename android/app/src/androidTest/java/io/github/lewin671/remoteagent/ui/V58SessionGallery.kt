package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.test.platform.app.InstrumentationRegistry
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Link
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
import io.github.lewin671.remoteagent.proto.DefaultModeSource
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.MachineCapabilities
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.PlanProposed
import io.github.lewin671.remoteagent.proto.ProjectPref
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.Question
import io.github.lewin671.remoteagent.proto.QuestionAsked
import io.github.lewin671.remoteagent.proto.QuestionOption
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnEnded
import io.github.lewin671.remoteagent.proto.TurnStarted
import io.github.lewin671.remoteagent.proto.UserMessage
import org.junit.After
import org.junit.Rule
import org.junit.Test
import java.io.File

/**
 * The session of docs/design/client-v58.html (#conversation, #reply,
 * #tools, #files, #boundaries) with the mockup's example data, light and
 * dark and some at twice the font size, saved as PNGs under the app's
 * external files, v58/session-*, for comparing with the mockup. Nothing is
 * sent: the Remote only shows fixed state.
 */
class V58SessionGallery {
    @get:Rule val ui = createComposeRule()

    private val now = System.currentTimeMillis()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val home = "/Users/me"
    private val foo = "$home/Code/foo"
    private var dark by mutableStateOf(false)
    private var font by mutableStateOf(1f)

    @org.junit.Before fun chat() {
        // Every session opens in the view last picked on this device.
        InstrumentationRegistry.getInstrumentation().targetContext.getSharedPreferences("session-view", android.content.Context.MODE_PRIVATE).edit().clear().commit()
        ImageDrafts.forSession("s").clear()
    }

    @After fun close() {
        remote.close()
        DockFolds.clear()
        ImageDrafts.forSession("s").clear()
        HomeScopes.clear(InstrumentationRegistry.getInstrumentation().targetContext)
    }

    private fun ts(ms: Long) = Timestamp.newBuilder().setSeconds(ms / 1000).build()

    private fun machine(files: Boolean = false, online: Boolean = true) = "mac" to MachineView(
        "mac", online, now,
        MachineMeta.newBuilder().setHostname("MacBook").setHome(home)
            .setCapabilities(MachineCapabilities.newBuilder().setApprovalAlways(true).setFileUpload(files))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG))
            .build(),
    )

    private fun session(
        title: String, status: SessionStatus = SessionStatus.SESSION_STATUS_IDLE, agent: Agent = Agent.AGENT_CODEX,
        approvals: Int = 0, questions: Int = 0, plan: Boolean = false, detail: String = "", context: Long = 0,
        mode: ApprovalMode = ApprovalMode.APPROVAL_MODE_ASK, cwd: String = foo, id: String = "s",
    ) = id to SessionView(
        id, "mac",
        Session.newBuilder().setId(id).setCwd(cwd).setTitle(title).setStatus(status).setAgent(agent).setPendingApprovals(approvals)
            .setPendingQuestions(questions).setPlanOpen(plan).setCreatedAt(ts(now - 3600_000)).setMode(mode).setStatusDetail(detail)
            .setModel(if (agent == Agent.AGENT_CLAUDE) "claude-sonnet-5-5" else "gpt-6-astra").setEffort("medium")
            .setContextTokens(context).setContextWindow(if (context > 0) 100_000 else 0)
            .also { if (status == SessionStatus.SESSION_STATUS_EXITED) it.setEndedAt(ts(now - 60_000)).setStatusDetail("stopped") }.build(),
        updatedMs = now,
    )

    private fun events(vararg list: Event.Builder): List<Event> = list.mapIndexed { i, b -> b.setSessionId("s").setSeq(i + 1L).setTime(ts(now - (list.size - i) * 4000L)).build() }
    private fun user(text: String) = Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText(text))
    private fun agent(text: String) = Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText(text))
    private fun turn() = Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance())
    private fun turnEnd() = Event.newBuilder().setTurnEnded(TurnEnded.newBuilder().setDurationMs(83_000))
    private fun tool(id: String, cmd: String, exit: Int = 0, ms: Long = 4200) = Event.newBuilder().setToolCall(
        ToolCall.newBuilder().setCallId(id).setKind(ToolKind.TOOL_KIND_COMMAND).setTitle(cmd).setItemType("CommandExecution")
            .setStatus(if (exit == 0) "completed" else "failed").setExitCode(exit).setDurationMs(ms),
    )
    private fun started(id: String, cmd: String) = Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setCallId(id).setTitle(cmd).setKind(ToolKind.TOOL_KIND_COMMAND).setItemType("CommandExecution"))
    private fun queued(id: String, text: String) = Event.newBuilder().setInputState(
        InputState.newBuilder().setRequestId(id).setStatus(InputStatus.INPUT_STATUS_QUEUED).setText(text).setMode(SendMode.SEND_MODE_QUEUE),
    )
    private val command = "rm -rf gen/remoteagent && \\\nbuf generate --template buf.gen.yaml && \\\ngo build ./... && \\\ngo test ./internal/... -count=1"
    private fun approval() = Event.newBuilder().setApprovalRequested(
        ApprovalRequested.newBuilder().setApprovalId("A1").setCommand(command).setToolName("Bash").setDeadline(ts(now + 8 * 60_000 - 30_000))
            .setDescription("跑一遍全部测试，确认拆包没有破坏行为。先清掉旧的生成文件再用 buf 重新生成"),
    )
    private fun question() = Event.newBuilder().setQuestionAsked(
        QuestionAsked.newBuilder().setQuestionId("Q1")
            .addQuestions(Question.newBuilder().setText("大版本也一起升吗？")
                .addOptions(QuestionOption.newBuilder().setLabel("只升小版本 (Recommended)").setDescription("风险最小，今天就能合"))
                .addOptions(QuestionOption.newBuilder().setLabel("大版本一起升").setDescription("要改 3 处调用"))
                .addOptions(QuestionOption.newBuilder().setLabel("先列出变化再决定")))
            .addQuestions(Question.newBuilder().setText("升级后要跑哪些测试？").addOptions(QuestionOption.newBuilder().setLabel("全部"))),
    )
    private val plan = "# 拆分库存逻辑\n\n1. 新建 inventory 包，迁移扣减和回滚\n2. 订单服务改为调用接口，涉及 5 个文件\n3. 补集成测试，覆盖并发扣减和超时回滚\n4. 灰度开关 INVENTORY_V2，默认关闭"
    private val reply = "找到原因了：每次登录都会新建一个到 Redis 的连接，高峰期连接数打满后请求排队，超过 `5s` 就超时。\n\n" +
        "## 改了什么\n\n- `auth/login.go`：改为复用连接池，单次请求超时 `2s`\n- `auth/pool.go`：新增连接池，上限 `32`\n\n" +
        "## 关键改动\n\n```go\nfunc (s *Service) Login(ctx context.Context, req *LoginRequest) (*Session, error) {\n    ctx, cancel := context.WithTimeout(ctx, 2*time.Second)\n    defer cancel()\n    return s.verify(ctx, req)\n}\n```\n\n" +
        "| 场景 | 改动前 P99 | 改动后 P99 |\n|---|---:|---:|\n| 50 并发 | 180 ms | 42 ms |\n| 200 并发 | 5.2 s | 61 ms |\n\n还有一处我没动：`auth/refresh.go` 也是每次新建连接，要一起改吗？"

    private fun state(vararg sessions: Pair<String, SessionView>, files: Boolean = false, link: Link = Link.ONLINE, conn: ConnState = ConnState.ONLINE, online: Boolean = true) = RemoteState(
        machines = mapOf(machine(files, online)), conn = conn, loaded = true, link = link, synced = true,
        prefs = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1).addPinned(ProjectPref.newBuilder().setPath(foo)).build()),
        sessions = sessions.toMap(),
    )

    private fun liveOf(s: SessionView?, list: List<Event>): io.github.lewin671.remoteagent.proto.LiveSnapshot {
        val d = io.github.lewin671.remoteagent.core.derive(list, s?.info?.agent ?: Agent.AGENT_CODEX)
        val b = io.github.lewin671.remoteagent.proto.LiveSnapshot.newBuilder().setRunning(d.running)
        s?.info?.let { b.setSession(it) }
        list.filter { (it.hasApprovalRequested() && it.approvalRequested.approvalId in d.approvals) || (it.hasQuestionAsked() && it.questionAsked.questionId in d.questions) }
            .forEach { b.addActions(it) }
        // What the terminal shows of a plan waiting for approval (as LayoutTest's plan).
        list.lastOrNull { it.hasPlanProposed() }?.let {
            b.setPlanEventSeq(it.seq).setPlanScreenHash(com.google.protobuf.ByteString.copyFrom(ByteArray(32)))
                .setPlanScreen("⎿ /plan to preview\n──────\nReady to code?\nHere is Claude's plan:\n──────\n" + it.planProposed.text)
        }
        return b.build()
    }

    /** Shows the session with [state] and [history], then saves it light and dark (and large when [large]) as session-[name]. */
    private fun shoot(name: String, state: RemoteState, history: List<Event>, large: Boolean = false, home: Boolean = false, then: (() -> Unit)? = null) {
        remote.showForTest(state, mapOf("s" to history), emptyMap(), mapOf("s" to liveOf(state.sessions["s"], history)))
        ui.setContent {
            val base = LocalDensity.current
            RaTheme(if (dark) DarkPalette else LightPalette) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav, LocalDensity provides Density(base.density, font)) {
                    if (home) HomeScreen() else SessionScreen("s")
                }
            }
        }
        then?.invoke()
        save("session-$name-light")
        dark = true
        save("session-$name-dark")
        if (large) {
            dark = false
            font = 2f
            save("session-$name-large")
            font = 1f
        }
    }

    private fun save(name: String) {
        ui.waitForIdle()
        Thread.sleep(700)
        val i = InstrumentationRegistry.getInstrumentation()
        val bmp = i.uiAutomation.takeScreenshot() ?: return
        val dir = File(i.targetContext.getExternalFilesDir(null), "v58").apply { mkdirs() }
        File(dir, "$name.png").outputStream().use { bmp.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private val running = events(
        turn(), user("跑一遍库存集成测试，再并行检查一下 lint"), tool("t1", "go vet ./..."), tool("t2", "go build ./..."), tool("t3", "pnpm lint", exit = 1),
        agent("测试需要启动数据库，可能要等一会儿。"), started("t4", "go test ./internal/inventory/... -count=1"), queued("r1", "测试过了就提交"),
    )

    @Test fun chatRunning() = shoot("01-running", state(session("验证库存回滚", SessionStatus.SESSION_STATUS_RUNNING, context = 85_000)), running, large = true)

    @Test fun reply() = shoot("02-reply", state(session("修复登录超时")), events(turn(), user("登录接口偶尔超时，帮我查一下原因并修掉"), tool("t1", "go test ./auth/..."), agent(reply), turnEnd()))

    @Test fun approvalDock() = shoot("03-approval", state(session("重构 API 层", approvals = 1)), events(turn(), user("跑一遍全部测试"), approval()), large = true)

    @Test fun approvalDenying() = shoot("04-approval-deny", state(session("重构 API 层", approvals = 1)), events(turn(), user("跑一遍全部测试"), approval())) {
        ui.onNodeWithText("拒绝").performClick()
    }

    @Test fun approvalFolded() = shoot("05-approval-folded", state(session("重构 API 层", approvals = 1)), events(turn(), user("跑一遍全部测试"), approval())) {
        ui.onNodeWithText("收起", substring = true).performClick()
    }

    @Test fun questionDock() = shoot("06-question", state(session("升级依赖", agent = Agent.AGENT_CLAUDE, questions = 1)), events(turn(), user("把依赖都升级到最新的版本"), question()))

    @Test fun planDock() = shoot(
        "07-plan",
        state(session("拆分库存逻辑", agent = Agent.AGENT_CLAUDE, plan = true, mode = ApprovalMode.APPROVAL_MODE_PLAN)),
        events(turn(), user("评估一下把库存逻辑拆成独立包的方案"), Event.newBuilder().setPlanProposed(PlanProposed.newBuilder().setText(plan))),
    )

    @Test fun popupDock() = shoot("08-popup", state(session("billing · 11:25", SessionStatus.SESSION_STATUS_BLOCKED, detail = "Trust this folder?")), events(turn(), user("看看 billing 的目录结构")))

    @Test fun moreMenu() = shoot("09-more", state(session("修复登录超时", SessionStatus.SESSION_STATUS_RUNNING, context = 85_000, mode = ApprovalMode.APPROVAL_MODE_FULL_ACCESS)), running, large = true) {
        ui.onNodeWithContentDescription("更多").performClick()
    }

    @Test fun attachMenu() = shoot("10-attach", state(session("整理季度资料"), files = true), events(turn(), user("把季度资料整理成一页摘要"), agent("摘要写好了，放在 `docs/q3-summary.md`。还缺原始数据的话可以发给我。"))) {
        ui.onNodeWithContentDescription("添加附件").performClick()
    }

    @Test fun videoInTheBox() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val clip = File(context.filesDir, "clip.mp4")
        val draft = ImageDrafts.forSession("s")
        draft.attach(
            listOf(
                PendingImage("v", if (clip.exists()) android.net.Uri.fromFile(clip).toString() else null, "clip.mp4", clip.length(), retainSource = false),
                PendingImage("d", null, "季度分析-完整版-含补充说明.pdf", 8_600_000, retainSource = false),
            ),
            9,
        )
        // Not connected to the server, so nothing tries to upload them.
        shoot("11-video", state(session("整理季度资料"), files = true, conn = ConnState.OFFLINE), events(turn(), user("把季度资料整理成一页摘要")))
    }

    @Test fun ended() = shoot("12-ended", state(session("拆分 handler 包", SessionStatus.SESSION_STATUS_EXITED)), events(turn(), user("拆一下 handler 包"), agent("拆好了。"), turnEnd()))

    @Test fun reconnecting() = shoot("13-reconnecting", state(session("验证库存回滚"), link = Link.WAITING, conn = ConnState.OFFLINE), events(turn(), user("跑一遍库存集成测试"), agent("开发服务器已经在后台启动。")))

    @Test fun terminal() = shoot("14-terminal", state(session("修复登录超时", SessionStatus.SESSION_STATUS_RUNNING)), running) {
        ui.onNodeWithText("终端").performClick()
    }

    @Test fun scopeMenu() = shoot(
        "15-scope-menu",
        state(
            session("修复登录超时", SessionStatus.SESSION_STATUS_RUNNING),
            session("发布秋季文章", cwd = "$home/Code/blog", id = "b"),
            session("整理读书笔记", cwd = "$home/notes", id = "n"),
        ),
        running, home = true,
    ) {
        ui.onNodeWithTag("scope-title").performClick()
    }
}
