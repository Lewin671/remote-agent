package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.ApprovalResolved
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.Origin
import io.github.lewin671.remoteagent.proto.OriginKind
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.SettingsChanged
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnContext
import io.github.lewin671.remoteagent.proto.TurnStarted
import io.github.lewin671.remoteagent.proto.UserMessage
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId
import java.time.ZonedDateTime

class ModelTest {
    @Test fun markdownTablePreviewMatchesSharedVectors() {
        val vectors = kotlinx.serialization.json.Json.parseToJsonElement(
            java.io.File(System.getProperty("previewTableVectors")).readText()) as kotlinx.serialization.json.JsonArray
        for (vector in vectors) {
            val fields = vector as kotlinx.serialization.json.JsonObject
            fun value(key: String) = (fields.getValue(key) as kotlinx.serialization.json.JsonPrimitive).content
            org.junit.Assert.assertEquals(value("input"), value("expected"), plain(value("input")))
        }
    }

    private fun ev(seq: Long, b: Event.Builder) = b.setSeq(seq).build()

    @Test
    fun statusFollowsThePriorityList() {
        val online = MachineView("m", online = true, lastSeenMs = 0)
        val offline = online.copy(online = false)
        fun s(b: Session.Builder.() -> Unit) = SessionView("s", "m", Session.newBuilder().apply(b).build())
        assertMessageEquals(Status.EXITED, statusOf(s { status = SessionStatus.SESSION_STATUS_EXITED; pendingApprovals = 1 }, offline))
        assertMessageEquals(Status.OFFLINE, statusOf(s { status = SessionStatus.SESSION_STATUS_RUNNING; pendingApprovals = 1 }, offline))
        assertMessageEquals(Status.APPROVAL, statusOf(s { pendingApprovals = 1; pendingQuestions = 1; planOpen = true }, online))
        assertMessageEquals(Status.QUESTION, statusOf(s { pendingQuestions = 1; planOpen = true }, online))
        assertMessageEquals(Status.PLAN, statusOf(s { planOpen = true; status = SessionStatus.SESSION_STATUS_BLOCKED }, online))
        assertMessageEquals(Status.POPUP, statusOf(s { status = SessionStatus.SESSION_STATUS_BLOCKED }, online))
        assertMessageEquals(Status.RUNNING, statusOf(s { status = SessionStatus.SESSION_STATUS_RUNNING }, online))
        assertMessageEquals(Status.IDLE, statusOf(s { status = SessionStatus.SESSION_STATUS_IDLE }, online))
        assertMessageEquals(Status.LOADING, statusOf(SessionView("s", "m"), online))
    }

    @Test
    fun renameStartsFromTheTitleAndSendsOnlyAChange() {
        fun s(b: Session.Builder.() -> Unit) = SessionView("s", "m", Session.newBuilder().apply(b).build())
        assertMessageEquals("登录接口偶尔超时", renameInitial(s { title = " 登录接口偶尔超时 " }))
        // The "<folder> · <start time>" placeholder is not a title to edit.
        assertMessageEquals("", renameInitial(s { cwd = "/Users/me/Code/billing" }))
        assertMessageEquals("", renameInitial(SessionView("s", "m")))

        // 60 code points, not UTF-16 units: an emoji counts once and is never cut in half.
        assertMessageEquals("字".repeat(60), titleInput("字".repeat(61)))
        assertMessageEquals("😀".repeat(60), titleInput("😀".repeat(61)))
        assertMessageEquals("a".repeat(59) + "😀", titleInput("a".repeat(59) + "😀" + "b"))
        assertMessageEquals("第一行 第二行", titleInput("第一行\r\n第二行"))

        assertMessageEquals(null, renameTitle("  登录超时 ", "登录超时"))
        assertMessageEquals("登录超时：限流和重试", renameTitle(" 登录超时：限流和重试\u3000", "登录超时"))
        // Emptied: back to the automatic title; nothing to do when there was none.
        assertMessageEquals("", renameTitle("  ", "登录超时"))
        assertMessageEquals(null, renameTitle("", ""))
    }

    @Test
    fun forkNeedsAnIdleOrEndedSessionWithAMessage() {
        assertMessageEquals(null, forkBlocked(Status.IDLE, running = false, sent = true))
        assertMessageEquals(null, forkBlocked(Status.EXITED, running = false, sent = true))
        for (st in listOf(Status.RUNNING, Status.APPROVAL, Status.QUESTION)) assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionForkAfterTurn()), forkBlocked(st, running = false, sent = true))
        // The events show a turn before the session's status does.
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionForkAfterTurn()), forkBlocked(Status.IDLE, running = true, sent = true))
        for (st in listOf(Status.PLAN, Status.POPUP)) assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionForkAfterPending()), forkBlocked(st, running = false, sent = true))
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionForkAfterStart()), forkBlocked(Status.STARTING, running = false, sent = false))
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionForkAfterMessage()), forkBlocked(Status.IDLE, running = false, sent = false))
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionForkAfterMessage()), forkBlocked(Status.EXITED, running = false, sent = false))
        // What it waits for comes before the missing message.
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionForkAfterTurn()), forkBlocked(Status.RUNNING, running = true, sent = false))

        fun s(b: Session.Builder.() -> Unit) = SessionView("s", "m", Session.newBuilder().apply(b).build())
        val message = listOf(ev(1, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("hi"))))
        assertTrue(sentMessage(s { title = "登录接口偶尔超时" }, emptyList()))
        assertTrue(sentMessage(s {}, message))
        assertMessageEquals(false, sentMessage(s { cwd = "/Users/me/Code/foo" }, listOf(ev(1, Event.newBuilder().setTurnContext(TurnContext.newBuilder().setModel("m"))))))
        assertMessageEquals(false, sentMessage(SessionView("s", "m"), emptyList()))

        assertTrue(forked(s { forkedFrom = "a1" }))
        assertMessageEquals(false, forked(s {}))
        assertMessageEquals(false, forked(SessionView("s", "m")))

        val made = io.github.lewin671.remoteagent.proto.CommandResult.newBuilder().setForkSession(
            io.github.lewin671.remoteagent.proto.ForkSessionResponse.newBuilder().setSession(Session.newBuilder().setId("new")),
        ).build()
        assertMessageEquals("new", forkedSession(made))
        assertMessageEquals(null, forkedSession(io.github.lewin671.remoteagent.proto.CommandResult.getDefaultInstance()))
        assertMessageEquals(null, forkedSession(null))
    }

    @Test
    fun aForkCarriesNothingPendingFromItsSource() {
        // The copy ends in the middle of things only if the daemon's own trimming missed something: nothing of it runs here.
        val d = derive(
            listOf(
                ev(1, Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance())),
                ev(2, Event.newBuilder().setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("a"))),
                ev(3, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setCallId("c").setTitle("go test"))),
                ev(4, Event.newBuilder().setSessionForked(io.github.lewin671.remoteagent.proto.SessionForked.newBuilder().setSourceSessionId("src").setSourceTitle("登录超时"))),
            ),
        )
        assertMessageEquals(false, d.running)
        assertTrue(d.approvals.isEmpty())
        assertTrue(d.tools.isEmpty())
        assertMessageEquals(setOf(3L), d.unfinished)
    }

    @Test
    fun derivesPendingItemsAndInputs() {
        val d = derive(
            listOf(
                ev(1, Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance())),
                ev(2, Event.newBuilder().setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("a1").setCommand("go test"))),
                ev(3, Event.newBuilder().setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("a2"))),
                ev(4, Event.newBuilder().setApprovalResolved(ApprovalResolved.newBuilder().setApprovalId("a1"))),
                ev(5, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("r1").setStatus(InputStatus.INPUT_STATUS_SUBMITTED))),
                ev(6, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("from the phone"))),
                ev(7, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("typed locally"))),
            ),
        )
        assertMessageEquals(listOf("a2"), d.approvals.keys.toList())
        assertTrue(d.resolved.containsKey("a1"))
        assertMessageEquals(InputStatus.INPUT_STATUS_SUBMITTED, d.inputs["r1"])
        assertMessageEquals(setOf(7L), d.typedLocally)
        assertTrue(d.running)
    }

    @Test
    fun pairsToolsById() {
        val d = derive(
            listOf(
                ev(1, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setTitle("a").setCallId("x"))),
                ev(2, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setTitle("b").setCallId("y"))),
                ev(3, Event.newBuilder().setToolCall(ToolCall.newBuilder().setCallId("y"))),
            ),
        )
        assertMessageEquals(listOf("a"), d.tools.map { it.title })
    }

    @Test
    fun notesModeChangesMadeInTheTerminal() {
        val d = derive(
            listOf(
                ev(1, Event.newBuilder().setTurnContext(TurnContext.newBuilder().setModel("m").setEffort("low").setApprovalPolicy("on-request").setApprovalsReviewer("user"))),
                ev(2, Event.newBuilder().setTurnContext(TurnContext.newBuilder().setModel("m").setEffort("low").setApprovalPolicy("on-request").setApprovalsReviewer("auto_review"))),
            ),
        )
        assertMessageEquals(listOf(TerminalChange.ModeChanged(Mode.AUTO, "AUTO")), d.terminalChanges[2])
    }

    private fun changed(model: String = "", effort: String = "") =
        Event.newBuilder().setSettingsChanged(SettingsChanged.newBuilder().setModel(model).setEffort(effort))

    // The web's "notes a model changed in the terminal after a switch from here".
    @Test
    fun notesAModelChangedInTheTerminalAfterASwitchFromHere() {
        fun context(model: String, effort: String) =
            Event.newBuilder().setTurnContext(TurnContext.newBuilder().setModel(model).setEffort(effort).setApprovalPolicy("on-request").setApprovalsReviewer("user"))
        val d = derive(
            listOf(
                ev(1, context("gpt-6-astra", "medium")),
                ev(2, changed("gpt-6-sol", "high")),
                // Changed again at the computer before the next turn.
                ev(3, context("gpt-6-luna", "low")),
                ev(4, changed("gpt-6-sol", "high")),
                ev(5, context("gpt-6-sol", "high")), // as chosen here: nothing to note
            ),
        )
        assertMessageEquals(mapOf(3L to listOf(TerminalChange.ModelChanged("gpt-6-luna", "low"))), d.terminalChanges)
    }

    // The web's "notes a switch back made in the terminal after a switch from here".
    @Test
    fun notesASwitchBackMadeInTheTerminalAfterASwitchFromHere() {
        fun context(model: String) =
            Event.newBuilder().setTurnContext(TurnContext.newBuilder().setModel(model).setEffort("medium").setApprovalPolicy("on-request").setApprovalsReviewer("user"))
        val d = derive(
            listOf(
                ev(1, context("gpt-6-astra")),
                ev(2, changed("gpt-6-sol", "medium")),
                ev(3, context("gpt-6-astra")), // switched back at the computer before the next turn
                ev(4, context("gpt-6-sol")), // and to Sol again there
            ),
        )
        assertMessageEquals(mapOf(3L to listOf(TerminalChange.ModelChanged("gpt-6-astra", "medium")), 4L to listOf(TerminalChange.ModelChanged("gpt-6-sol", "medium"))), d.terminalChanges)
    }

    private fun claudeContext(permission: String, model: String = "claude-haiku-4-5-20251001") =
        Event.newBuilder().setTurnContext(TurnContext.newBuilder().setPermissionMode(permission).setModel(model))

    // The web's "notes Claude Code mode changes made in the terminal, not model aliases set here".
    @Test
    fun notesClaudeCodeModesOutsideTheFourAndNotAModelAliasSetHere() {
        val d = derive(
            listOf(
                ev(1, claudeContext("default")),
                ev(2, claudeContext("acceptEdits", "")), // Shift+Tab in the terminal, read off the screen
                ev(3, changed("sonnet")), // switched here, by alias
                ev(4, claudeContext("acceptEdits", "claude-sonnet-5-5")),
                ev(5, claudeContext("plan", "claude-sonnet-5-5")),
                ev(6, claudeContext("default", "claude-sonnet-5-5")),
            ),
            Agent.AGENT_CLAUDE,
        )
        assertMessageEquals(mapOf(2L to listOf(TerminalChange.ModeChanged(null, "accept edits")), 5L to listOf(TerminalChange.ModeChanged(Mode.PLAN, "PLAN")), 6L to listOf(TerminalChange.ModeChanged(Mode.ASK, "ASK"))), d.terminalChanges)
    }

    // The web's "notes nothing when a mode-only context carries the switched model".
    @Test
    fun notesNothingWhenAModeOnlyContextCarriesTheSwitchedModel() {
        val d = derive(
            listOf(
                ev(1, claudeContext("default")),
                ev(2, changed("sonnet", "medium")),
                ev(3, claudeContext("acceptEdits", "claude-sonnet-5-5")), // mode changed before the next reply
                ev(4, claudeContext("acceptEdits", "claude-sonnet-5-5")),
                ev(5, claudeContext("acceptEdits", "claude-opus-5-5")), // then changed at the computer
                ev(6, claudeContext("default", "claude-opus-5-5")), // and the mode back
            ),
            Agent.AGENT_CLAUDE,
        )
        assertMessageEquals(mapOf(3L to listOf(TerminalChange.ModeChanged(null, "accept edits")), 5L to listOf(TerminalChange.ModelChanged("claude-opus-5-5", "")), 6L to listOf(TerminalChange.ModeChanged(Mode.ASK, "ASK"))), d.terminalChanges)
    }

    @Test
    fun aModelIsTheOneChosenBySlugOrAlias() {
        assertTrue(modelIs("gpt-6-sol", "gpt-6-sol"))
        assertTrue(modelIs("claude-Sonnet-5-5", "sonnet"))
        assertFalse(modelIs("claude-opus-5-5", "sonnet"))
        assertFalse(modelIs("", "sonnet"))
        assertFalse(modelIs("sonnet", ""))
    }

    @Test
    fun saysWhichSideToUpdateWhenProtocolVersionsDiffer() {
        fun m(v: Int) = MachineView("m", true, 0, MachineMeta.newBuilder().setHostname("mac.local").setProtocolVersion(v).build())
        assertMessageEquals(null, protocolMismatch(m(PROTOCOL_VERSION)))
        assertMessageEquals("mac 上的 remote-agent 版本较旧，请在电脑上更新后再试", protocolMismatch(m(PROTOCOL_VERSION - 1)))
        assertMessageEquals("这个 App 版本较旧，请更新后再试", protocolMismatch(m(PROTOCOL_VERSION + 1)))
        // Not read yet: the daemon answers for itself.
        assertMessageEquals(null, protocolMismatch(MachineView("m", true, 0)))
        assertMessageEquals(null, protocolMismatch(null))
    }

    @Test
    fun namesAgentsAndTimes() {
        assertMessageEquals("Codex", agentName(Agent.AGENT_UNSPECIFIED))
        assertMessageEquals("Claude Code", agentName(Agent.AGENT_CLAUDE))
        assertMessageEquals(listOf(Agent.AGENT_CODEX), machineAgents(MachineView("m", true, 0, MachineMeta.getDefaultInstance())))
        val both = MachineMeta.newBuilder()
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).build()
        assertMessageEquals(listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE), machineAgents(MachineView("m", true, 0, both)))
        val zone = ZoneId.of("Asia/Shanghai")
        val now = ZonedDateTime.of(2026, 9, 29, 14, 0, 0, 0, zone).toInstant().toEpochMilli()
        assertMessageEquals("2 小时前", ago(now - 2 * 3600_000L, now, zone))
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.timeYesterday()), ago(now - 24 * 3600_000L, now, zone))
        assertMessageEquals("", ago(0, now, zone))
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.timeJustNow()), whenText(now - 10_000, now, zone))
        assertMessageEquals("3 分钟前", whenText(now - 180_000, now, zone))
        assertMessageEquals("9:05", whenText(now - (4 * 3600 + 55 * 60) * 1000L, now, zone))
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.timeYesterday()), whenText(now - 24 * 3600_000L, now, zone))
        assertMessageEquals("9月27日", whenText(now - 48 * 3600_000L, now, zone))
        assertMessageEquals("foo", projectName("/Users/me/Code/foo/"))
        assertMessageEquals("修 bug", plain("修 `bug`"))
    }

    @Test
    fun followsClaudeCodeModes() {
        fun s(b: Session.Builder.() -> Unit) = SessionView("s", "m", Session.newBuilder().apply(b).build())
        assertMessageEquals(Mode.AUTO, modeLabel(s { mode = io.github.lewin671.remoteagent.proto.ApprovalMode.APPROVAL_MODE_AUTO })?.mode)
        val own = modeLabel(s { permissionMode = "acceptEdits" })!!
        assertMessageEquals("acceptEdits", own.text)
        assertMessageEquals(null, own.mode)
        assertMessageEquals(Mode.PLAN, modeLabel(s { collaborationMode = "plan"; approvalPolicy = "on-request" })?.mode)
        // The list says which models come without the auto mode; a model it
        // does not know gives no answer.
        val list = listOf(
            io.github.lewin671.remoteagent.proto.ModelInfo.newBuilder().setSlug("claude-haiku-4-5-20251001").setAutoUnavailable(true).build(),
            io.github.lewin671.remoteagent.proto.ModelInfo.newBuilder().setSlug("claude-sonnet-5-5").build(),
        )
        assertTrue(autoUnavailable(list, "claude-haiku-4-5-20251001"))
        assertFalse(autoUnavailable(list, "claude-sonnet-5-5"))
        assertFalse(autoUnavailable(list, "haiku"))
        assertFalse(autoUnavailable(null, "claude-haiku-4-5-20251001"))

        val allNoAutoList = listOf(
            io.github.lewin671.remoteagent.proto.ModelInfo.newBuilder().setSlug("gemini-3.1-pro-high").setAutoUnavailable(true).build(),
            io.github.lewin671.remoteagent.proto.ModelInfo.newBuilder().setSlug("gemini-3.8-flash").setAutoUnavailable(true).build(),
        )
        assertTrue(allAutoUnavailable(allNoAutoList))
        assertTrue(autoUnavailable(allNoAutoList, "gemini-3.1-pro-high"))
        assertTrue(autoUnavailable(allNoAutoList, "other-model"))
        assertFalse(allAutoUnavailable(list))
        assertFalse(allAutoUnavailable(emptyList()))
        assertFalse(allAutoUnavailable(null))

        assertMessageEquals("GPT-6-Sol", modelLabel(emptyList(), "gpt-6-sol", Agent.AGENT_CODEX))
        assertMessageEquals("gpt-6-sol", modelLabel(null, "gpt-6-sol", Agent.AGENT_CODEX))
        assertMessageEquals("claude-sonnet-5-5", modelLabel(list, "claude-sonnet-5-5", Agent.AGENT_CLAUDE))
        val models = listOf("opus", "sonnet", "haiku")
        assertMessageEquals("haiku", findModel(models, "claude-haiku-4-5-20251001") { it })
        assertMessageEquals("sonnet", findModel(models, "sonnet") { it })
        assertMessageEquals(null, findModel(models, "gpt-6-luna") { it })
    }

    @Test
    fun aLongModelListShowsThreeAndThePickedOne() {
        val few = listOf("a", "b", "c", "d", "e")
        assertMessageEquals(few, shownModels(few, null, false))
        val many = few + listOf("f", "g")
        assertMessageEquals(listOf("a", "b", "c"), shownModels(many, "b", false))
        assertMessageEquals(listOf("a", "b", "c", "g"), shownModels(many, "g", false))
        assertMessageEquals(many, shownModels(many, "g", true))
    }

    @Test
    fun aLateConfirmationIsForTheMessageRecordedAfterIt() {
        val client = Origin.newBuilder().setKind(OriginKind.ORIGIN_KIND_CLIENT).setDeviceId("x")
        val events = listOf(
            ev(1, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("a").setStatus(InputStatus.INPUT_STATUS_QUEUED).setText("sent from the phone"))),
            ev(2, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("a").setStatus(InputStatus.INPUT_STATUS_UNCONFIRMED))),
            ev(3, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("typed at the computer"))),
            ev(4, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("a").setStatus(InputStatus.INPUT_STATUS_SUBMITTED))),
            ev(5, Event.newBuilder().setOrigin(client).setUserMessage(UserMessage.newBuilder().setText("sent from the phone").setRequestId("a"))),
            ev(6, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("typed later"))),
        )
        assertMessageEquals(setOf(3L, 6L), derive(events, Agent.AGENT_CLAUDE).typedLocally)
        assertMessageEquals(setOf(3L, 6L), derive(events, Agent.AGENT_CODEX).typedLocally)
        assertMessageEquals(InputStatus.INPUT_STATUS_SUBMITTED, derive(events, Agent.AGENT_CLAUDE).inputs["a"])
    }

    @Test
    fun claudeCodeRecordsTheMessageBeforeItsState() {
        val events = listOf(
            ev(1, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("hi"))),
            ev(2, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("r").setStatus(InputStatus.INPUT_STATUS_SUBMITTED))),
        )
        assertMessageEquals(emptySet<Long>(), derive(events, Agent.AGENT_CLAUDE).typedLocally)
        assertMessageEquals(setOf(1L), derive(events, Agent.AGENT_CODEX).typedLocally)
    }

    @Test
    fun warnsAboutClaudeCodesTrustDialogOnly() {
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.dockTrustFolder()), popupHint(Agent.AGENT_CLAUDE, "trust folder"))
        assertMessageEquals("", popupHint(Agent.AGENT_CODEX, "trust folder"))
        assertMessageEquals("", popupHint(Agent.AGENT_UNSPECIFIED, "trust folder"))
        assertMessageEquals("", popupHint(Agent.AGENT_CLAUDE, "hooks review"))
    }

    @Test
    fun readsTheDaemonsDefaultModePerCli() {
        val s = io.github.lewin671.remoteagent.proto.DaemonSettings.newBuilder()
            .setDefaultMode(io.github.lewin671.remoteagent.proto.ApprovalMode.APPROVAL_MODE_FULL_ACCESS)
            .setClaude(io.github.lewin671.remoteagent.proto.AgentDefaults.newBuilder().setMode(io.github.lewin671.remoteagent.proto.ApprovalMode.APPROVAL_MODE_AUTO))
            .build()
        assertMessageEquals(Mode.FULL, defaultMode(s, Agent.AGENT_CODEX))
        assertMessageEquals(Mode.FULL, defaultMode(s, Agent.AGENT_UNSPECIFIED))
        assertMessageEquals(Mode.AUTO, defaultMode(s, Agent.AGENT_CLAUDE))
        assertMessageEquals(null, defaultMode(io.github.lewin671.remoteagent.proto.DaemonSettings.getDefaultInstance(), Agent.AGENT_CODEX))
    }
}
