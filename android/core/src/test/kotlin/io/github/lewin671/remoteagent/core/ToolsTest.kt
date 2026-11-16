package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ToolKind
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ToolsTest {
    @Test
    fun waitingForApprovalReadsSharedCoreFlag() {
        val waiting = RunningTool("rm -rf build", "x", 100_000, seq = 2, awaitingApproval = true)
        val idle = RunningTool("ls", "y", 100_000, seq = 3, awaitingApproval = false)
        val d = Derived(tools = mutableListOf(waiting, idle))
        assertTrue(awaitingApproval(waiting, d))
        assertFalse(awaitingApproval(idle, d))
    }

    @Test
    fun theTimerUsesTheDaemonsClockAndFreezesWhileOffline() {
        val t = RunningTool("go test", "", since = 1_000, seq = 1)
        assertMessageEquals(131_000L, toolElapsed(t, now = 132_000))
        assertMessageEquals(59_000L, toolElapsed(t, now = 132_000, frozenAt = 60_000))
        assertNull(toolElapsed(RunningTool("x", "", since = 0), now = 5_000))
        assertMessageEquals("2:11", clock(131_000))
        assertMessageEquals("1:02:03", clock(3_723_000))
    }

    @Test
    fun labelsNameClaudeToolsAndCodexKinds() {
        assertMessageEquals("Bash", toolLabel(ToolKind.TOOL_KIND_COMMAND, "Bash", Agent.AGENT_CLAUDE))
        assertMessageEquals(messageTextForTest(UiMessages.toolsCommand()), toolLabel(ToolKind.TOOL_KIND_COMMAND, "CommandExecution", Agent.AGENT_CODEX))
    }
}
