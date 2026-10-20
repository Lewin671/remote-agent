package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.safeDrawingPadding
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
 * The phone screens of docs/design/client-v8.html with the mockup's example
 * data, in light and dark, saved as PNGs for comparing with the mockup (run
 * on a 304 × 644 dp display, the mockup's phone). Nothing is sent: the
 * Remote only shows fixed state.
 */
class V8Gallery {
    @get:Rule val ui = createComposeRule()

    private val now = System.currentTimeMillis()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val home = "/Users/me"
    private val foo = "$home/Code/foo"
    private var dark by mutableStateOf(false)

    @After fun close() {
        remote.close()
        DockFolds.clear()
        // The terminal shot picks the terminal view, which every later session would open in.
        InstrumentationRegistry.getInstrumentation().targetContext.getSharedPreferences("session-view", 0).edit().clear().commit()
    }

    private fun ts(ms: Long) = Timestamp.newBuilder().setSeconds(ms / 1000).build()
    private fun at(h: Int, m: Int): Long = java.time.LocalDate.now().atTime(h, m).atZone(java.time.ZoneId.systemDefault()).toInstant().toEpochMilli()

    private fun machine(id: String, name: String, online: Boolean = true, claude: Boolean = false) = id to MachineView(
        id, online, if (online) now else now - 2 * 3600_000,
        MachineMeta.newBuilder().setHostname(name).setHome(home).setCapabilities(MachineCapabilities.newBuilder().setApprovalAlways(true))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG))
            .also { if (claude) it.addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG)) }
            .build(),
    )

    private val machines = mapOf(machine("mac", "MacBook", claude = true), machine("vps", "VPS-dev"), machine("imac", "iMac"))
    private val codexModels = ListModelsResponse.newBuilder().setDefaultModel("gpt-6-astra")
        .addModels(ModelInfo.newBuilder().setSlug("gpt-6-astra").setDisplayName("GPT-6-Astra").setDefaultEffort("medium")).build()

    private fun session(
        id: String, machine: String, cwd: String, title: String, status: SessionStatus = SessionStatus.SESSION_STATUS_IDLE,
        agent: Agent = Agent.AGENT_CODEX, approvals: Int = 0, questions: Int = 0, plan: Boolean = false, preview: String = "",
        updated: Long = now, created: Long = now - 3600_000, mode: ApprovalMode = ApprovalMode.APPROVAL_MODE_ASK, detail: String = "",
        context: Long = 0,
    ) = id to SessionView(
        id, machine,
        Session.newBuilder().setId(id).setCwd(cwd).setTitle(title).setStatus(status).setAgent(agent).setPendingApprovals(approvals)
            .setPendingQuestions(questions).setPlanOpen(plan).setPreview(preview).setCreatedAt(ts(created)).setMode(mode).setStatusDetail(detail)
            .setModel(if (agent == Agent.AGENT_CLAUDE) "claude-sonnet-5-5" else "gpt-6-astra").setEffort(if (agent == Agent.AGENT_CLAUDE) "high" else "medium")
            .setContextTokens(context).setContextWindow(if (context > 0) 760_000 else 0)
            .also { if (status == SessionStatus.SESSION_STATUS_EXITED) it.setEndedAt(ts(updated)) }.build(),
        updatedMs = updated,
    )

    private fun prefs(vararg pinned: String) = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1).addAllPinned(pinned.map { ProjectPref.newBuilder().setPath(it).build() }).build())

    private fun events(vararg list: Event.Builder): List<Event> = list.mapIndexed { i, b -> b.setSeq(i + 1L).setTime(ts(now - (list.size - i) * 1000L)).build() }
    private fun user(text: String) = Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText(text))
    private fun agent(text: String) = Event.newBuilder().setAgentMessage(AgentMessage.newBuilder().setText(text))
    private fun thought(text: String) = Event.newBuilder().setReasoning(Reasoning.newBuilder().setText(text))
    private fun call(title: String, kind: ToolKind = ToolKind.TOOL_KIND_COMMAND, status: String = "completed", exit: Int? = null, output: String = "", item: String = "Bash") =
        Event.newBuilder().setToolCall(ToolCall.newBuilder().setTitle(title).setCallId(title).setKind(kind).setStatus(status).setItemType(item).setOutput(output).also { if (exit != null) it.setExitCode(exit) })
    private fun started(title: String, callId: String = title) = Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setTitle(title).setCallId(callId).setKind(ToolKind.TOOL_KIND_COMMAND).setItemType("Bash").setInput(title))
    private fun turn() = Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance())
    private fun turnEnd(ms: Long) = Event.newBuilder().setTurnEnded(TurnEnded.newBuilder().setDurationMs(ms))
    private fun approval(id: String, command: String, desc: String) = Event.newBuilder().setApprovalRequested(
        ApprovalRequested.newBuilder().setApprovalId(id).setCommand(command).setDescription(desc).setToolName("Bash").setDeadline(ts(now + 8 * 60_000 + 30_000)),
    )
    private fun resolved(id: String) = Event.newBuilder().setApprovalResolved(ApprovalResolved.newBuilder().setApprovalId(id).setDecision(ApprovalDecision.APPROVAL_DECISION_ALLOW))

    /** Shows [content] full screen with [state] and [history], then saves it light and dark as [name]. */
    private fun shoot(name: String, state: RemoteState, history: Map<String, List<Event>> = emptyMap(), then: (() -> Unit)? = null, content: @Composable androidx.compose.foundation.layout.ColumnScope.() -> Unit) {
        // Online means reached and synced, or the screens take the state for stale (RemoteState.unlinked).
        val shown = if (state.conn == ConnState.ONLINE) state.copy(link = io.github.lewin671.remoteagent.core.Link.ONLINE, synced = true) else state
        remote.showForTest(shown, history, mapOf(
            ("mac" to Agent.AGENT_CODEX) to codexModels,
            ("vps" to Agent.AGENT_CODEX) to codexModels,
            ("imac" to Agent.AGENT_CODEX) to codexModels,
        ), history.mapValues { (sid, list) -> liveOf(state.sessions[sid], list) })
        ui.setContent {
            RaTheme(if (dark) DarkPalette else LightPalette) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    Column(Modifier.fillMaxSize().background(pal.card).safeDrawingPadding()) { content() }
                }
            }
        }
        then?.invoke()
        save("$name-light")
        dark = true
        save("$name-dark")
    }

    /** What the computer would report as waiting now: the pending approvals, questions and inputs, and an open plan. */
    private fun liveOf(s: SessionView?, list: List<Event>): io.github.lewin671.remoteagent.proto.LiveSnapshot {
        val d = io.github.lewin671.remoteagent.core.derive(list, s?.info?.agent ?: Agent.AGENT_CODEX)
        val b = io.github.lewin671.remoteagent.proto.LiveSnapshot.newBuilder().setRunning(d.running)
        s?.info?.let { b.setSession(it) }
        list.filter {
            (it.hasApprovalRequested() && it.approvalRequested.approvalId in d.approvals) ||
                (it.hasQuestionAsked() && it.questionAsked.questionId in d.questions) ||
                (it.hasInputState() && d.inputs[it.inputState.requestId] in setOf(InputStatus.INPUT_STATUS_QUEUED, InputStatus.INPUT_STATUS_HELD_BY_CLI))
        }
            .forEach { b.addActions(it) }
        val plan = list.lastOrNull { it.hasPlanProposed() }
        if (s?.info?.planOpen == true && plan != null)
            b.setPlanEventSeq(plan.seq).setPlanScreen(plan.planProposed.text).setPlanScreenHash(com.google.protobuf.ByteString.copyFrom(ByteArray(32)))
        return b.build()
    }

    private fun save(name: String) {
        ui.waitForIdle()
        Thread.sleep(600)
        val i = InstrumentationRegistry.getInstrumentation()
        val bmp = i.uiAutomation.takeScreenshot() ?: return
        val dir = File(i.targetContext.getExternalFilesDir(null), "v8").apply { mkdirs() }
        File(dir, "$name.png").outputStream().use { bmp.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun homeState(attention: Boolean) = RemoteState(
        machines = machines, conn = ConnState.ONLINE, loaded = true, prefs = prefs(foo),
        sessions = mapOf(
            session("run", "mac", foo, "修复登录超时", SessionStatus.SESSION_STATUS_RUNNING, preview = "正在执行 go test ./auth/...", created = now - 60_000, updated = now - 20_000),
            session("doc", "mac", foo, "补充接口文档", agent = Agent.AGENT_CLAUDE, preview = "已补充 12 个接口的说明，要生成 OpenAPI 吗？", created = at(8, 12), updated = at(8, 12)),
            session("bill", "imac", "$home/Code/billing", "", created = at(11, 25), updated = at(11, 25)),
            session("blog1", "mac", "$home/Code/blog", "发布秋季文章", SessionStatus.SESSION_STATUS_EXITED, updated = now - 2 * 3600_000),
            session("blog2", "vps", "$home/work/blog", "修复 RSS", SessionStatus.SESSION_STATUS_EXITED, updated = now - 26 * 3600_000),
        ) + if (!attention) emptyMap() else mapOf(
            session("api", "vps", foo, "重构 API 层", approvals = 1, updated = at(9, 31)),
            session("dep", "imac", "$home/Code/bar", "升级依赖", questions = 1, updated = at(9, 20)),
            session("p3", "mac", "$home/Code/shop", "拆分订单服务", plan = true, updated = at(9, 10)),
            session("p4", "vps", "$home/Code/billing", "对账脚本", SessionStatus.SESSION_STATUS_BLOCKED, detail = "Trust this folder?", updated = at(9, 0)),
        ),
    )

    private val apiEvents = events(turn(), user("跑一遍全部测试"), approval("A1", "go test ./... -count=1", "跑一遍全部测试，确认拆包没有破坏行为"))
    private val depEvents = events(
        turn(), user("把依赖都升级到最新的版本"), call("go list -m -u all"), agent("有 12 个依赖可以升级，其中 `pgx` 是 v4 → v5 的大版本。"),
        Event.newBuilder().setQuestionAsked(
            QuestionAsked.newBuilder().setQuestionId("Q1").addQuestions(
                Question.newBuilder().setText("大版本也一起升吗？")
                    .addOptions(QuestionOption.newBuilder().setLabel("只升小版本 (Recommended)").setDescription("改动小，go build 大概率直接通过"))
                    .addOptions(QuestionOption.newBuilder().setLabel("包括 pgx v5").setDescription("要改连接池和事务相关的调用")),
            ).addQuestions(Question.newBuilder().setText("要顺便更新 go.mod 的 Go 版本吗？").addOptions(QuestionOption.newBuilder().setLabel("更新到 1.27")).addOptions(QuestionOption.newBuilder().setLabel("保持不变"))),
        ),
    )

    @Test fun home() {
        shoot("01-home-attention", homeState(true), mapOf("api" to apiEvents, "dep" to depEvents)) {
            Column(Modifier.weight(1f)) { HomeScreen() }
            TabBar("home", 4) {}
        }
    }

    @Test fun homeQuiet() {
        shoot("02-home", homeState(false)) {
            Column(Modifier.weight(1f)) { HomeScreen() }
            TabBar("home", 0) {}
        }
    }

    @Test fun homeMany() {
        val extras = (1..7).map { n -> session("many$n", "mac", "$home/Code/project$n", "处理第 $n 个项目", SessionStatus.SESSION_STATUS_RUNNING, created = now - n * 1000L) }.toMap()
        val st = homeState(false).copy(sessions = homeState(false).sessions + extras)
        shoot("02-home-many", st) { HomeScreen() }
    }

    @Test fun pickManyProjects() {
        val extras = (1..9).map { n -> session("project$n", "mac", "$home/Code/project$n", "项目 $n") }.toMap()
        val st = homeState(false).copy(sessions = homeState(false).sessions + extras)
        shoot("06-pick-many", st, then = { ui.onNodeWithContentDescription("新会话").performClick() }) { HomeScreen() }
    }

    private fun projectState() = RemoteState(
        machines = machines, conn = ConnState.ONLINE, loaded = true, prefs = prefs(foo),
        sessions = mapOf(
            session("api", "vps", foo, "重构 API 层", approvals = 1, preview = "请求执行 go test ./... -count=1", updated = at(9, 31)),
            session("run", "mac", foo, "修复登录超时", SessionStatus.SESSION_STATUS_RUNNING, preview = "正在执行 go test ./auth/...", created = now - 60_000, updated = now - 20_000),
            session("e1", "mac", foo, "补充接口文档", SessionStatus.SESSION_STATUS_EXITED, agent = Agent.AGENT_CLAUDE, preview = "已补充 12 个接口的说明", updated = now - 26 * 3600_000),
            session("e2", "mac", foo, "介绍下这个项目", SessionStatus.SESSION_STATUS_EXITED, preview = "没有按时间失效。模型列表缓存在前端页面的内存里，同一台机器每次页面加载只请求一次。", updated = now - 3 * 86400_000),
        ),
    )

    @Test fun project() {
        shoot("03-project", projectState(), mapOf("api" to apiEvents)) { ProjectScreen(foo, false) }
    }

    @Test fun projectMenu() {
        shoot("04-project-menu", projectState()) { ProjectScreen(foo, false) }
    }

    @Test fun newSettings() {
        shoot("05-project-settings", projectState(), then = {
            ui.onNodeWithTag("project-settings-summary").performClick()
        }) { ProjectScreen(foo, false) }
    }

    @Test fun pickProject() {
        shoot("06-pick-project", homeState(false), then = { ui.onNodeWithContentDescription("新会话").performClick() }) { HomeScreen() }
    }

    private fun sessionState(vararg s: Pair<String, SessionView>, conn: ConnState = ConnState.ONLINE, ms: Map<String, MachineView> = machines) =
        RemoteState(machines = ms, conn = conn, loaded = true, sessions = mapOf(*s))

    private val runEvents = events(
        turn(), user("登录接口偶尔超时，帮我查一下原因并修掉"), thought("先看登录的连接处理。"),
        call("rg -n Login auth"), call("sed -n 40,80p auth/login.go"), call("git log -3 auth"),
        agent("问题在 `auth/login.go:58`：\n\n- 每次登录都新建 Redis 连接\n- 连接没设超时，池子耗尽后请求挂住\n\n我改成复用连接池并加上 `2s` 超时。"),
        call("auth/login.go", ToolKind.TOOL_KIND_FILE_CHANGE), started("go test ./auth/..."),
    )

    @Test fun sessionRunning() {
        shoot("10-session-running", sessionState(session("run", "mac", foo, "修复登录超时", SessionStatus.SESSION_STATUS_RUNNING, context = 87_000)), mapOf("run" to runEvents)) { SessionScreen("run") }
    }

    @Test fun queueSendMenu() {
        Outbox.draft("queue-menu").value = "改完顺便跑一下 lint"
        try {
            shoot("10-queue-send-menu", sessionState(session("queue-menu", "mac", foo, "修复登录超时", SessionStatus.SESSION_STATUS_RUNNING)),
                mapOf("queue-menu" to events(turn(), user("修复登录超时"))),
                then = {
                    ui.onNodeWithContentDescription("发送").performTouchInput { longClick() }
                    ui.onNodeWithText("这一轮结束后再发").assertIsDisplayed()
                }) { SessionScreen("queue-menu") }
        } finally { Outbox.draft("queue-menu").value = "" }
    }

    @Test fun sessionMenu() {
        shoot("11-session-menu", sessionState(session("run", "mac", foo, "修复登录超时", SessionStatus.SESSION_STATUS_IDLE, context = 87_000)).copy(prefs = prefs(foo)), mapOf("run" to runEvents.dropLast(1)),
            then = {
                ui.onNodeWithContentDescription("更多").performClick()
                ui.onNodeWithText("项目设置").assertIsDisplayed()
            }) { SessionScreen("run") }
        ui.onNodeWithText("项目设置").performClick()
        ui.onNodeWithText("完成").assertIsDisplayed()
    }

    private val apiSession = session("api", "vps", foo, "重构 API 层", approvals = 1)
    private val apiHistory = events(
        turn(), user("把 handler 拆到单独的包里"), call("internal/handler", ToolKind.TOOL_KIND_FILE_CHANGE),
        approval("A0", "mkdir -p internal/handler", "新建目录"), resolved("A0"), call("mkdir -p internal/handler"),
        agent("拆分完成，跑一下测试确认没有破坏现有行为。"), approval("A1", "go test ./... -count=1", "跑一遍全部测试，确认拆包没有破坏行为"),
    )

    @Test fun sessionApproval() {
        shoot("12-session-approval", sessionState(apiSession), mapOf("api" to apiHistory), then = { ui.onNodeWithText("收起").performClick() }) { SessionScreen("api") }
    }

    @Test fun sessionApprovalOpen() {
        shoot("13-session-approval-open", sessionState(apiSession), mapOf("api" to apiHistory), ) { SessionScreen("api") }
    }

    /** A command of [n] lines, some long enough to wrap. */
    private fun longCommand(n: Int) = (1..n).joinToString("\n") { i ->
        if (i % 7 == 0) "  --label migration-step-$i --description \"把第 $i 步的旧数据迁移到新的表结构，并保留回滚所需的快照\" \\"
        else "psql \"\$DATABASE_URL\" -c 'UPDATE orders SET state = ''done'' WHERE id = $i;' \\"
    }
    private val longDesc = "迁移全部订单数据到新表结构。这条命令会逐条更新订单状态，执行时间可能较长；如果中途失败，需要用快照回滚后再重新执行。请确认数据库连接指向测试环境而不是生产环境。"

    @Test fun sessionApprovalLong() {
        val ev = events(turn(), user("迁移订单表"), approval("L1", longCommand(40), longDesc), approval("L2", "ls", "看看目录"))
        shoot("13-session-approval-long", sessionState(session("long", "vps", foo, "迁移订单表", approvals = 2)), mapOf("long" to ev)) { SessionScreen("long") }
    }

    @Test fun sessionPlanLong() {
        val plan = (1..30).joinToString("\n") { "$it. 第 $it 步：把 `inventory` 模块里和库存扣减相关的第 $it 段逻辑迁到新服务，并补上对应的测试" }
        val ev = events(turn(), user("先出个方案"), agent("方案如下。"), Event.newBuilder().setPlanProposed(PlanProposed.newBuilder().setText(plan)))
        shoot("16-session-plan-long", sessionState(session("planlong", "imac", "$home/Code/shop", "拆分订单服务", plan = true, mode = ApprovalMode.APPROVAL_MODE_PLAN, agent = Agent.AGENT_CLAUDE)), mapOf("planlong" to ev)) { SessionScreen("planlong") }
    }

    @Test fun homeApprovalLong() {
        val st = homeState(true)
        val ev = events(turn(), user("迁移订单表"), approval("A1", longCommand(20), longDesc))
        shoot("01-home-approval-long", st, mapOf("api" to ev, "dep" to depEvents)) { HomeScreen() }
    }

    @Test fun homeApprovalLongOpen() {
        val st = homeState(true)
        val ev = events(turn(), user("迁移订单表"), approval("A1", longCommand(20), longDesc))
        shoot("01-home-approval-long-open", st, mapOf("api" to ev, "dep" to depEvents), then = { ui.onNodeWithText("展开", substring = true).performClick() }) { HomeScreen() }
    }

    @Test fun approvalPage() {
        shoot("14-approval-page", sessionState(apiSession), mapOf("api" to apiHistory)) { ApprovalPage("api", "A1") }
    }

    @Test fun sessionQuestion() {
        shoot("15-session-question", sessionState(session("dep", "imac", "$home/Code/bar", "升级依赖", questions = 1)), mapOf("dep" to depEvents), ) { SessionScreen("dep") }
    }

    @Test fun sessionPlan() {
        val ev = events(turn(), user("先出个方案，把订单服务里的库存逻辑拆出去"), call("读取 14 个文件", ToolKind.TOOL_KIND_OTHER, item = "Read"), agent("方案如下，批准后开始改。"),
            Event.newBuilder().setPlanProposed(PlanProposed.newBuilder().setText("1. 新建 `inventory` 包，迁移库存扣减和回滚\n2. 订单服务改为调用接口，保留旧实现做开关\n3. 补充集成测试，覆盖并发扣减\n4. 灰度开关上线")))
        shoot("16-session-plan", sessionState(session("plan", "imac", "$home/Code/shop", "拆分订单服务", plan = true, mode = ApprovalMode.APPROVAL_MODE_PLAN)), mapOf("plan" to ev), ) { SessionScreen("plan") }
    }

    @Test fun sessionEnded() {
        val ev = events(turn(), user("缓存是多久失效"), agent("没有按时间失效。模型列表缓存在前端页面的内存里，**同一台机器每次页面加载只请求一次**；刷新或重新打开页面会重新获取。"), turnEnd(12_000))
        shoot("17-session-ended", sessionState(session("ended", "mac", "$home/Code/remote-agent", "介绍下这个项目", SessionStatus.SESSION_STATUS_EXITED)), mapOf("ended" to ev)) { SessionScreen("ended") }
    }

    @Test fun sessionOffline() {
        val ev = events(turn(), user("把 3 月的对账结果导出成 CSV"), call("python scripts/export.py --month 2026-03"), agent("已导出到 `out/2026-03.csv`，共 1,284 行。"))
        shoot("18-session-offline", sessionState(session("off", "imac", "$home/Code/billing", "对账脚本"), ms = machines + machine("imac", "iMac", online = false)), mapOf("off" to ev)) { SessionScreen("off") }
    }

    @Test fun sessionTool() {
        val ev = events(turn(), user("跑一遍库存集成测试"), agent("测试需要启动数据库，可能要等一会儿。"), started("go test ./internal/inventory/... -count=1"))
        shoot("19-session-tool", sessionState(session("tool", "vps", "$home/Code/shop", "验证库存回滚", SessionStatus.SESSION_STATUS_RUNNING, agent = Agent.AGENT_CLAUDE, context = 320_000)), mapOf("tool" to ev)) { SessionScreen("tool") }
    }

    @Test fun sessionToolWaitingApproval() {
        val ev = events(turn(), user("跑一遍测试"), started("go test ./..."), approval("A1", "go test ./...", "运行测试"))
        shoot("19-session-tool-approval", sessionState(session("tool-approval", "vps", foo, "等待测试审批", SessionStatus.SESSION_STATUS_RUNNING, approvals = 1)),
            mapOf("tool-approval" to ev)) { SessionScreen("tool-approval") }
    }

    @Test fun sessionBackgroundGeneric() {
        val ev = events(turn(), user("后台处理"), Event.newBuilder().setToolCall(ToolCall.newBuilder()
            .setCallId("generic").setTitle("ToolCall").setItemType("ToolCall").setKind(ToolKind.TOOL_KIND_OTHER)
            .setBackground(ToolBackground.TOOL_BACKGROUND_REQUESTED).setStatus("running")))
        shoot("19-session-background-generic", sessionState(session("background-generic", "vps", foo, "后台处理")),
            mapOf("background-generic" to ev)) { SessionScreen("background-generic") }
    }

    @Test fun pendingBubbles() {
        val ev = events(turn(), user("请先检查构建"),
            Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("q1").setStatus(InputStatus.INPUT_STATUS_QUEUED).setMode(SendMode.SEND_MODE_QUEUE).setText("等这一轮结束后再检查所有 Android 界面和长路径 /Users/me/Code/a-very-long-project-name")),
            Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("s1").setStatus(InputStatus.INPUT_STATUS_QUEUED).setMode(SendMode.SEND_MODE_STEER).setText("现在插入这一轮")),
            Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("s1").setStatus(InputStatus.INPUT_STATUS_HELD_BY_CLI)),
            Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("q1").setStatus(InputStatus.INPUT_STATUS_QUEUED).setPaused(true)))
        shoot("20-pending-bubbles", sessionState(session("pending", "mac", foo, "检查所有界面", SessionStatus.SESSION_STATUS_RUNNING, agent = Agent.AGENT_CLAUDE)), mapOf("pending" to ev)) { SessionScreen("pending") }
        ui.onNodeWithText("已暂停").assertIsDisplayed()
        ui.onNodeWithText("插入这一轮").assertIsDisplayed()
    }

    @Test fun pendingMany() {
        val ev = events(turn(), user("逐项检查"), *(1..4).map { n ->
            Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("queue$n").setStatus(InputStatus.INPUT_STATUS_QUEUED)
                .setMode(SendMode.SEND_MODE_QUEUE).setText("第 $n 条：检查不同项目的待发消息"))
        }.toTypedArray())
        shoot("20-pending-many", sessionState(session("many", "mac", foo, "多个待发消息", SessionStatus.SESSION_STATUS_RUNNING)), mapOf("many" to ev)) { SessionScreen("many") }
        ui.onNodeWithText("还有 2 条排队 展开").assertIsDisplayed()
    }

    @Test fun modeSheet() {
        shoot("21-mode-sheet", sessionState(session("run", "mac", foo, "修复登录超时", mode = ApprovalMode.APPROVAL_MODE_AUTO)), mapOf("run" to runEvents.dropLast(1)), then = {
            ui.onNodeWithContentDescription("更多").performClick()
            ui.onNodeWithText("审批模式").performClick()
        }) { SessionScreen("run") }
    }

    @Test fun fullAccess() {
        shoot("22-full-access", sessionState(session("run", "mac", foo, "修复登录超时", mode = ApprovalMode.APPROVAL_MODE_FULL_ACCESS, context = 690_000)), mapOf("run" to runEvents.dropLast(1))) { SessionScreen("run") }
    }

    private fun quotaMachine(): Pair<String, MachineView> {
        val (id, m) = machine("mac", "MacBook", claude = true)
        val q = m.meta!!.toBuilder()
            .addQuotas(AgentQuota.newBuilder().setAgent(Agent.AGENT_CODEX).setState(QuotaState.QUOTA_STATE_OK).setPlan("Pro Lite").setObservedMs(now - 60_000)
                .addWindows(QuotaWindow.newBuilder().setName("primary").setWindowMinutes(300).setUsedPercent(62.0).setResetsAtMs(now + 3 * 3600_000))
                .addWindows(QuotaWindow.newBuilder().setName("secondary").setWindowMinutes(10080).setUsedPercent(34.0).setResetsAtMs(now + 3 * 86400_000)))
            .addQuotas(AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).setObservedMs(now - 120_000)
                .addWindows(QuotaWindow.newBuilder().setName("five_hour").setUsedPercent(92.0).setResetsAtMs(now + 2 * 3600_000))
                .addWindows(QuotaWindow.newBuilder().setName("seven_day")))
            .build()
        return id to m.copy(meta = q)
    }

    private fun accountState() = homeState(false).copy(
        machines = machines + quotaMachine() + machine("imac", "iMac", online = false),
        devices = mapOf(
            "2222222222222222" to DeviceView("2222222222222222", DeviceKind.DEVICE_KIND_CLIENT, now - 86400_000, DeviceMeta.newBuilder().setName("").setDescription("Android 16 · Pixel").build()),
            "b71c00000000b71c" to DeviceView("b71c00000000b71c", DeviceKind.DEVICE_KIND_CLIENT, now - 3600_000, DeviceMeta.newBuilder().setName("我的手机").build()),
        ),
    )

    @Test fun settings() {
        shoot("30-settings", accountState()) {
            Column(Modifier.weight(1f)) { SettingsScreen() }
            TabBar("settings", 0) {}
        }
    }

    @Test fun accountSecurityAuthority() {
        val state = accountState().copy(isAuthority = true, authorityDeviceId = remote.account.deviceId, leaseEndMs = now + 48 * 3600_000)
        shoot("50-security-authority", state) { AccountSecurityContent(state) }
    }

    @Test fun accountSecurityMember() {
        val state = accountState().copy(authorityDeviceId = "b71c00000000b71c", leaseEndMs = now + 48 * 3600_000)
        shoot("51-security-member", state) { AccountSecurityContent(state) }
    }

    @Test fun accountSecurityPending() {
        val state = accountState().copy(isAuthority = true, authorityDeviceId = remote.account.deviceId, leaseEndMs = now + 48 * 3600_000, recoveryPending = true, recoveryReadyMs = now + 24 * 3600_000)
        shoot("52-security-pending", state) { AccountSecurityContent(state, pending = true) }
    }

    @Test fun devices() {
        shoot("31-devices", accountState()) { DevicesScreen() }
    }

    @Test fun device() {
        shoot("32-device", accountState()) { DeviceScreen("mac") }
    }

    @Test fun quota() {
        shoot("33-quota", accountState()) { QuotaScreen("mac") }
    }

    @Test fun client() {
        shoot("34-client", accountState()) { ClientScreen("b71c00000000b71c") }
    }

    @Test fun manage() {
        val st = homeState(false).copy(prefs = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1)
            .addPinned(ProjectPref.newBuilder().setPath(foo)).addHidden(ProjectPref.newBuilder().setPath("$home/Code/demo")).build()),
            sessions = homeState(false).sessions + mapOf(session("demo", "mac", "$home/Code/demo", "演示", SessionStatus.SESSION_STATUS_EXITED)))
        shoot("35-manage", st) { ManageProjectsScreen() }
    }

    @Test fun scopeSheet() {
        shoot("02-scope", homeState(true), then = { ui.onNodeWithTag("scope-title").performClick() }) { HomeScreen() }
    }

    @Test fun manageMenu() {
        val st = homeState(true).copy(prefs = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1).addPinned(ProjectPref.newBuilder().setPath(foo)).build()))
        shoot("35-manage-menu", st, then = { ui.onNodeWithContentDescription("管理 billing").performClick() }) { ManageProjectsScreen() }
    }

    @Test fun manageRemove() {
        shoot("35-manage-remove", homeState(true), then = {
            ui.onNodeWithContentDescription("管理 billing").performClick()
            ui.onNodeWithText("移除项目…").performClick()
        }) { ManageProjectsScreen() }
    }

    @Test fun connCheck() {
        shoot("30-conn-check", accountState(), then = {
            ui.onNodeWithText("连接检测").performScrollTo().performClick()
            // Until the check is over: 再测一次 is enabled again.
            ui.waitUntil(20_000) { ui.onAllNodesWithText("检测中…").fetchSemanticsNodes().isEmpty() }
        }) { Column(Modifier.weight(1f)) { SettingsScreen() } }
    }

    private val newer = io.github.lewin671.remoteagent.core.Release(
        "0.29.0", 82, io.github.lewin671.remoteagent.core.PROTOCOL_VERSION, "remote-agent-v0.29.0.apk", "0".repeat(64), 5_976_883,
        "项目列表在范围菜单、选项目和项目管理三处统一成一种；新增连接检测。协议版本不变，电脑上的 remote-agent 不用升级。",
    )

    private fun update(
        name: String,
        state: io.github.lewin671.remoteagent.update.UpdateState,
        check: io.github.lewin671.remoteagent.update.UpdateCheck = io.github.lewin671.remoteagent.update.UpdateCheck(),
        home: Boolean = false,
    ) {
        io.github.lewin671.remoteagent.update.Updater.showForTest(state, check)
        try {
            if (home) shoot(name, homeState(false)) { HomeScreen() }
            else shoot(name, accountState()) { UpdateSheet {} }
        } finally {
            io.github.lewin671.remoteagent.update.Updater.showForTest(io.github.lewin671.remoteagent.update.UpdateState.Unknown)
        }
    }

    @Test fun updateHome() = update("40-update-home", io.github.lewin671.remoteagent.update.UpdateState.Available(newer), home = true)

    @Test fun updateAvailable() = update("40-update-available", io.github.lewin671.remoteagent.update.UpdateState.Available(newer))

    @Test fun updateDownloading() = update("40-update-downloading", io.github.lewin671.remoteagent.update.UpdateState.Downloading(newer, 2_097_152))

    @Test fun updateFailed() = update("40-update-failed", io.github.lewin671.remoteagent.update.UpdateState.DownloadFailed(newer.copy(protocol = 99), "连接超时"))

    @Test fun updateLatest() = update("40-update-latest", io.github.lewin671.remoteagent.update.UpdateState.Latest(0))

    // Rechecking while a release is already known (client-v42, #update-recheck). The one where the look
    // finds a newer release is updateAvailable with another version on it, so it has no screen of its own.

    @Test fun updateRechecking() = update(
        "41-update-rechecking",
        io.github.lewin671.remoteagent.update.UpdateState.Available(newer),
        check = io.github.lewin671.remoteagent.update.UpdateCheck(running = true),
    )

    @Test fun updateUpToDate() = update(
        "41-update-uptodate",
        io.github.lewin671.remoteagent.update.UpdateState.Ready(newer, File("remote-agent-v0.29.0.apk")),
        check = io.github.lewin671.remoteagent.update.UpdateCheck(upToDate = true),
    )

    @Test fun updateRecheckFailed() = update(
        "41-update-recheck-failed",
        io.github.lewin671.remoteagent.update.UpdateState.Available(newer),
        check = io.github.lewin671.remoteagent.update.UpdateCheck(error = "连不上发布地址"),
    )

    @Test fun pair() {
        shoot("36-join", RemoteState()) { JoinScreen(io.github.lewin671.remoteagent.RaApp.instance) }
    }

    @Test fun terminal() {
        shoot("37-terminal", sessionState(session("run", "mac", foo, "修复登录超时", SessionStatus.SESSION_STATUS_RUNNING)), mapOf("run" to runEvents),
            then = { ui.onNodeWithText("终端").performClick() }) { SessionScreen("run") }
    }

    @Test fun hiddenProject() {
        val st = projectState().copy(prefs = PrefsObject(1, ProjectPrefs.newBuilder().setSchema(1).addHidden(ProjectPref.newBuilder().setPath(foo)).build()))
        shoot("38-hidden-project", st) { ProjectScreen(foo, false) }
    }

    @Test fun question() {
        val ev = events(turn(), user("梳理登录流程"), Event.newBuilder().setQuestionAsked(QuestionAsked.newBuilder().setQuestionId("Q9")
            .addQuestions(Question.newBuilder().setText("先别急着改。")).addQuestions(Question.newBuilder().setMultiSelect(true).setText("上线前希望覆盖哪些部署和恢复场景？可选择多个，我会分别准备验证步骤。")
                .addOptions(QuestionOption.newBuilder().setLabel("服务重启").setDescription("验证连接恢复与事件补齐"))
                .addOptions(QuestionOption.newBuilder().setLabel("机器离线").setDescription("验证排队反馈和恢复路径"))
                .addOptions(QuestionOption.newBuilder().setLabel("网络短暂中断").setDescription("不重复发送原指令"))
                .addOptions(QuestionOption.newBuilder().setLabel("返回终端手动操作")))))
        QuestionPicks.index["Q9"] = 1
        QuestionPicks.multi["Q9#1"] = setOf(0, 2)
        shoot("39-question-page", sessionState(session("q", "mac", foo, "梳理登录流程", agent = Agent.AGENT_CLAUDE, questions = 1)), mapOf("q" to ev)) { QuestionPage("q", "Q9") }
    }
}
