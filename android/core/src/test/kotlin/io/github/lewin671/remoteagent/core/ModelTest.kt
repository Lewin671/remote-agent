package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.ApprovalResolved
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnContext
import io.github.lewin671.remoteagent.proto.TurnStarted
import io.github.lewin671.remoteagent.proto.UserMessage
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId
import java.time.ZonedDateTime

class ModelTest {
    private fun ev(seq: Long, b: Event.Builder) = b.setSeq(seq).build()

    @Test
    fun statusFollowsThePriorityList() {
        val online = MachineView("m", online = true, lastSeenMs = 0)
        val offline = online.copy(online = false)
        fun s(b: Session.Builder.() -> Unit) = SessionView("s", "m", Session.newBuilder().apply(b).build())
        assertEquals(Status.EXITED, statusOf(s { status = SessionStatus.SESSION_STATUS_EXITED; pendingApprovals = 1 }, offline))
        assertEquals(Status.OFFLINE, statusOf(s { status = SessionStatus.SESSION_STATUS_RUNNING; pendingApprovals = 1 }, offline))
        assertEquals(Status.APPROVAL, statusOf(s { pendingApprovals = 1; pendingQuestions = 1; planOpen = true }, online))
        assertEquals(Status.QUESTION, statusOf(s { pendingQuestions = 1; planOpen = true }, online))
        assertEquals(Status.PLAN, statusOf(s { planOpen = true; status = SessionStatus.SESSION_STATUS_BLOCKED }, online))
        assertEquals(Status.POPUP, statusOf(s { status = SessionStatus.SESSION_STATUS_BLOCKED }, online))
        assertEquals(Status.RUNNING, statusOf(s { status = SessionStatus.SESSION_STATUS_RUNNING }, online))
        assertEquals(Status.IDLE, statusOf(s { status = SessionStatus.SESSION_STATUS_IDLE }, online))
        assertEquals(Status.LOADING, statusOf(SessionView("s", "m"), online))
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
        assertEquals(listOf("a2"), d.approvals.keys.toList())
        assertTrue(d.resolved.containsKey("a1"))
        assertEquals(InputStatus.INPUT_STATUS_SUBMITTED, d.inputs["r1"])
        assertEquals(setOf(7L), d.typedLocally)
        assertTrue(d.running)
    }

    @Test
    fun pairsToolsByIdWhenGiven() {
        val d = derive(
            listOf(
                ev(1, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setTitle("a").setCallId("x"))),
                ev(2, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setTitle("b").setCallId("y"))),
                ev(3, Event.newBuilder().setToolCall(ToolCall.newBuilder().setCallId("y"))),
            ),
        )
        assertEquals(listOf("a"), d.tools.map { it.title })
        val codex = derive(
            listOf(
                ev(1, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setTitle("a"))),
                ev(2, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setTitle("b"))),
                ev(3, Event.newBuilder().setToolCall(ToolCall.newBuilder().setCallId("call_1"))),
            ),
        )
        assertEquals(listOf("b"), codex.tools.map { it.title })
    }

    @Test
    fun notesModeChangesMadeInTheTerminal() {
        val d = derive(
            listOf(
                ev(1, Event.newBuilder().setTurnContext(TurnContext.newBuilder().setModel("m").setEffort("low").setApprovalPolicy("on-request").setApprovalsReviewer("user"))),
                ev(2, Event.newBuilder().setTurnContext(TurnContext.newBuilder().setModel("m").setEffort("low").setApprovalPolicy("on-request").setApprovalsReviewer("auto_review"))),
            ),
        )
        assertEquals("模式切到了 自动", d.terminalChanges[2])
    }

    @Test
    fun namesAgentsAndTimes() {
        assertEquals("Codex", agentName(Agent.AGENT_UNSPECIFIED))
        assertEquals("Claude Code", agentName(Agent.AGENT_CLAUDE))
        assertEquals(listOf(Agent.AGENT_CODEX), machineAgents(MachineView("m", true, 0, MachineMeta.getDefaultInstance())))
        val both = MachineMeta.newBuilder()
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).build()
        assertEquals(listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE), machineAgents(MachineView("m", true, 0, both)))
        val zone = ZoneId.of("Asia/Shanghai")
        val now = ZonedDateTime.of(2026, 9, 29, 14, 0, 0, 0, zone).toInstant().toEpochMilli()
        assertEquals("刚刚", whenText(now - 10_000, now, zone))
        assertEquals("3 分钟前", whenText(now - 180_000, now, zone))
        assertEquals("9:05", whenText(now - (4 * 3600 + 55 * 60) * 1000L, now, zone))
        assertEquals("昨天", whenText(now - 24 * 3600_000L, now, zone))
        assertEquals("9月27日", whenText(now - 48 * 3600_000L, now, zone))
        assertEquals("foo", projectName("/Users/me/Code/foo/"))
        assertEquals("修 bug", plain("修 `bug`"))
    }

    @Test
    fun followsClaudeCodeModes() {
        fun s(b: Session.Builder.() -> Unit) = SessionView("s", "m", Session.newBuilder().apply(b).build())
        assertEquals(Mode.AUTO, modeLabel(s { mode = io.github.lewin671.remoteagent.proto.ApprovalMode.APPROVAL_MODE_AUTO })?.mode)
        val own = modeLabel(s { permissionMode = "acceptEdits" })!!
        assertEquals("acceptEdits", own.text)
        assertEquals(null, own.mode)
        assertEquals(Mode.PLAN, modeLabel(s { collaborationMode = "plan"; approvalPolicy = "on-request" })?.mode)
        assertTrue(noAutoMode(s { agent = Agent.AGENT_CLAUDE; model = "claude-haiku-4-5-20251001" }))
        val models = listOf("opus", "sonnet", "haiku")
        assertEquals("haiku", findModel(models, "claude-haiku-4-5-20251001") { it })
        assertEquals("sonnet", findModel(models, "sonnet") { it })
        assertEquals(null, findModel(models, "gpt-6-luna") { it })
    }

    @Test
    fun claudeCodeRecordsTheMessageBeforeItsState() {
        val events = listOf(
            ev(1, Event.newBuilder().setUserMessage(UserMessage.newBuilder().setText("hi"))),
            ev(2, Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("r").setStatus(InputStatus.INPUT_STATUS_SUBMITTED))),
        )
        assertEquals(emptySet<Long>(), derive(events, Agent.AGENT_CLAUDE).typedLocally)
        assertEquals(setOf(1L), derive(events, Agent.AGENT_CODEX).typedLocally)
    }
}
