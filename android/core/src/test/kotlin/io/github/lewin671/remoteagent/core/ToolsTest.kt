package io.github.lewin671.remoteagent.core

import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnEnded
import io.github.lewin671.remoteagent.proto.TurnOutcome
import io.github.lewin671.remoteagent.proto.TurnStarted
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ToolsTest {
    private fun ev(seq: Long, b: Event.Builder, sec: Long = 0) = b.setSeq(seq).apply { if (sec > 0) time = Timestamp.newBuilder().setSeconds(sec).build() }.build()
    private fun start(seq: Long, title: String, id: String = "", input: String = "", digest: String = "", sec: Long = 100) =
        ev(seq, Event.newBuilder().setToolStarted(ToolStarted.newBuilder().setTitle(title).setCallId(id).setInput(input).setInputDigest(digest).setKind(ToolKind.TOOL_KIND_COMMAND).setItemType("Bash")), sec)
    private fun call(seq: Long, title: String, id: String = "", input: String = "") =
        ev(seq, Event.newBuilder().setToolCall(ToolCall.newBuilder().setTitle(title).setCallId(id).setInput(input).setStatus("completed")))
    private val turn = ev(1, Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance()))

    @Test
    fun parallelCallsPairByIdAndKeepTheirStart() {
        val d = derive(listOf(turn, start(2, "go test", "a"), start(3, "go build", "b"), call(4, "go build", "b")), Agent.AGENT_CLAUDE)
        assertEquals(listOf("go test"), d.tools.map { it.title })
        assertEquals(2L, d.tools[0].seq)
        assertEquals(4L, d.completedAt[3])
        assertEquals(100_000L, d.tools[0].since)
    }

    @Test
    fun aTurnEndingBeforeAResultLeavesItUnconfirmedNotDone() {
        val d = derive(
            listOf(turn, start(2, "go test ./..."), ev(3, Event.newBuilder().setTurnEnded(TurnEnded.newBuilder().setOutcome(TurnOutcome.TURN_OUTCOME_ABORTED)))),
        )
        assertTrue(d.tools.isEmpty())
        assertEquals(listOf("go test ./..."), d.background.map { it.tool.title })
        assertFalse(d.background[0].returned)
        assertEquals(setOf(2L), d.unfinished)
        // The next turn moves on; the row stays unconfirmed in the history.
        val d2 = derive(
            listOf(
                turn, start(2, "go test ./..."), ev(3, Event.newBuilder().setTurnEnded(TurnEnded.getDefaultInstance())),
                ev(4, Event.newBuilder().setTurnStarted(TurnStarted.getDefaultInstance())),
            ),
        )
        assertTrue(d2.background.isEmpty())
        assertEquals(setOf(2L), d2.unfinished)
    }

    @Test
    fun aBackgroundRunIsNotASuccess() {
        val input = """{"command":"npm run dev","run_in_background":true}"""
        val d = derive(listOf(turn, start(2, "npm run dev", "x", input), call(3, "npm run dev", "x", input)), Agent.AGENT_CLAUDE)
        assertTrue(d.tools.isEmpty())
        assertEquals(1, d.background.size)
        assertTrue(d.background[0].returned)
        assertFalse(backgroundInput("""{"run_in_background":false}"""))
        // Claude Code backgrounds a long command by itself: only the result says so.
        val auto = derive(
            listOf(turn, start(2, "sleep 45", "y", "sleep 45"), ev(3, Event.newBuilder().setToolCall(ToolCall.newBuilder().setCallId("y").setInput("sleep 45").setOutput("Command running in background with ID: b2wvqs0hj. Output is being written to: /tmp/x")))),
            Agent.AGENT_CLAUDE,
        )
        assertEquals(1, auto.background.size)
    }

    @Test
    fun waitingForApprovalIsNotRunning() {
        val d = derive(
            listOf(
                turn, start(2, "rm -rf build", "x", digest = "d1"),
                ev(3, Event.newBuilder().setApprovalRequested(ApprovalRequested.newBuilder().setApprovalId("a").setInputDigest("d1"))),
            ),
            Agent.AGENT_CLAUDE,
        )
        assertTrue(awaitingApproval(d.tools[0], d))
        val other = derive(listOf(turn, start(2, "ls", "y", digest = "d2")), Agent.AGENT_CLAUDE)
        assertFalse(awaitingApproval(other.tools[0], other))
    }

    @Test
    fun theTimerUsesTheDaemonsClockAndFreezesWhileOffline() {
        val t = RunningTool("go test", "", since = 1_000, seq = 1)
        assertEquals(131_000L, toolElapsed(t, now = 132_000))
        assertEquals(59_000L, toolElapsed(t, now = 132_000, frozenAt = 60_000))
        assertNull(toolElapsed(RunningTool("x", "", since = 0), now = 5_000))
        assertEquals("2:11", clock(131_000))
        assertEquals("1:02:03", clock(3_723_000))
    }

    @Test
    fun labelsNameClaudeToolsAndCodexKinds() {
        assertEquals("Bash", toolLabel(ToolKind.TOOL_KIND_COMMAND, "Bash", Agent.AGENT_CLAUDE))
        assertEquals("命令", toolLabel(ToolKind.TOOL_KIND_COMMAND, "CommandExecution", Agent.AGENT_CODEX))
    }
}
