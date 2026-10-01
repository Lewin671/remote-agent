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
import androidx.compose.ui.test.performClick
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
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnEnded
import io.github.lewin671.remoteagent.proto.TurnStarted
import io.github.lewin671.remoteagent.proto.UserMessage
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
 * The phone screens of docs/design/client-v7.html with the mockup's example
 * data, in light and dark, saved as PNGs for comparing with the mockup (run
 * on a 304 × 644 dp display, the mockup's phone). Nothing is sent: the
 * Remote only shows fixed state.
 */
class V7Gallery {
    @get:Rule val ui = createComposeRule()

    private val now = System.currentTimeMillis()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val home = "/Users/me"
    private val foo = "$home/Code/foo"
    private var dark by mutableStateOf(false)

    @After fun close() { remote.close() }

    private fun ts(ms: Long) = Timestamp.newBuilder().setSeconds(ms / 1000).build()
    private fun at(h: Int, m: Int): Long = java.time.LocalDate.now().atTime(h, m).atZone(java.time.ZoneId.systemDefault()).toInstant().toEpochMilli()

    private fun machine(id: String, name: String, online: Boolean = true, claude: Boolean = false) = id to MachineView(
        id, online, if (online) now else now - 2 * 3600_000,
        MachineMeta.newBuilder().setHostname(name).setHome(home)
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG))
            .also { if (claude) it.addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK).setDefaultModeSource(DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG)) }
            .build(),
    )

    private val machines = mapOf(machine("mac", "MacBook", claude = true), machine("vps", "VPS-dev"), machine("imac", "iMac"))

    private fun session(
        id: String, machine: String, cwd: String, title: String, status: SessionStatus = SessionStatus.SESSION_STATUS_IDLE,
        agent: Agent = Agent.AGENT_CODEX, approvals: Int = 0, questions: Int = 0, plan: Boolean = false, preview: String = "",
        updated: Long = now, created: Long = now - 3600_000, mode: ApprovalMode = ApprovalMode.APPROVAL_MODE_ASK, detail: String = "",
        context: Long = 0,
    ) = id to SessionView(
        id, machine,
        Session.newBuilder().setId(id).setCwd(cwd).setTitle(title).setStatus(status).setAgent(agent).setPendingApprovals(approvals)
            .setPendingQuestions(questions).setPlanOpen(plan).setPreview(preview).setCreatedAt(ts(created)).setMode(mode).setStatusDetail(detail)
            .setModel(if (agent == Agent.AGENT_CLAUDE) "sonnet" else "gpt-6-astra").setEffort(if (agent == Agent.AGENT_CLAUDE) "high" else "medium")
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
        remote.showForTest(state, history)
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

    private fun save(name: String) {
        ui.waitForIdle()
        Thread.sleep(600)
        val i = InstrumentationRegistry.getInstrumentation()
        val bmp = i.uiAutomation.takeScreenshot() ?: return
        val dir = File(i.targetContext.getExternalFilesDir(null), "v7").apply { mkdirs() }
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
        shoot("05-new-settings", projectState(), then = {
            ui.onNodeWithText("新会话设置 ›").performClick()
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

    @Test fun sessionMenu() {
        shoot("11-session-menu", sessionState(session("run", "mac", foo, "修复登录超时", SessionStatus.SESSION_STATUS_IDLE, context = 87_000)), mapOf("run" to runEvents.dropLast(1)),
            then = { ui.onNodeWithText("⋯").performClick() }) { SessionScreen("run") }
    }

    private val apiSession = session("api", "vps", foo, "重构 API 层", approvals = 1)
    private val apiHistory = events(
        turn(), user("把 handler 拆到单独的包里"), call("internal/handler", ToolKind.TOOL_KIND_FILE_CHANGE),
        approval("A0", "mkdir -p internal/handler", "新建目录"), resolved("A0"), call("mkdir -p internal/handler"),
        agent("拆分完成，跑一下测试确认没有破坏现有行为。"), approval("A1", "go test ./... -count=1", "跑一遍全部测试，确认拆包没有破坏行为"),
    )

    @Test fun sessionApproval() {
        shoot("12-session-approval", sessionState(apiSession), mapOf("api" to apiHistory)) { SessionScreen("api") }
    }

    @Test fun sessionApprovalOpen() {
        shoot("13-session-approval-open", sessionState(apiSession), mapOf("api" to apiHistory), then = { ui.onNodeWithText("处理").performClick() }) { SessionScreen("api") }
    }

    @Test fun approvalPage() {
        shoot("14-approval-page", sessionState(apiSession), mapOf("api" to apiHistory)) { ApprovalPage("api", "A1") }
    }

    @Test fun sessionQuestion() {
        shoot("15-session-question", sessionState(session("dep", "imac", "$home/Code/bar", "升级依赖", questions = 1)), mapOf("dep" to depEvents), then = { ui.onNodeWithText("处理").performClick() }) { SessionScreen("dep") }
    }

    @Test fun sessionPlan() {
        val ev = events(turn(), user("先出个方案，把订单服务里的库存逻辑拆出去"), call("读取 14 个文件", ToolKind.TOOL_KIND_OTHER, item = "Read"), agent("方案如下，批准后开始改。"),
            Event.newBuilder().setPlanProposed(PlanProposed.newBuilder().setText("1. 新建 `inventory` 包，迁移库存扣减和回滚\n2. 订单服务改为调用接口，保留旧实现做开关\n3. 补充集成测试，覆盖并发扣减\n4. 灰度开关上线")))
        shoot("16-session-plan", sessionState(session("plan", "imac", "$home/Code/shop", "拆分订单服务", plan = true, mode = ApprovalMode.APPROVAL_MODE_PLAN)), mapOf("plan" to ev), then = { ui.onNodeWithText("处理").performClick() }) { SessionScreen("plan") }
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
        shoot("19-session-tool", sessionState(session("tool", "vps", "$home/Code/shop", "验证库存回滚", SessionStatus.SESSION_STATUS_RUNNING, agent = Agent.AGENT_CLAUDE, context = 320_000)), mapOf("tool" to ev),
            then = { ui.onAllNodesWithText("详情")[0].performClick() }) { SessionScreen("tool") }
    }

    @Test fun toolPage() {
        val ev = events(turn(), user("跑一遍库存集成测试"), started("go test ./internal/inventory/... -count=1"))
        shoot("20-tool-page", sessionState(session("tool", "vps", "$home/Code/shop", "验证库存回滚", SessionStatus.SESSION_STATUS_RUNNING, agent = Agent.AGENT_CLAUDE)), mapOf("tool" to ev)) { ToolPage("tool", 3) }
    }

    @Test fun modeSheet() {
        shoot("21-mode-sheet", sessionState(session("run", "mac", foo, "修复登录超时", mode = ApprovalMode.APPROVAL_MODE_AUTO)), mapOf("run" to runEvents.dropLast(1)), then = {
            ui.onNodeWithText("⋯").performClick()
            ui.onNodeWithText("审批模式：自动").performClick()
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
