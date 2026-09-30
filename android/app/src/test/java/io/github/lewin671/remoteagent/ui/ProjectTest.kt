package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentDefaults
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.DaemonSettings
import org.junit.Assert.assertEquals
import org.junit.Test

class ProjectTest {
    @Test
    fun aNewSessionCannotBeStartedInPlan() {
        // Both CLIs reject starting in plan; full access is only a confirmed default.
        assertEquals(listOf(Mode.ASK, Mode.AUTO), newSessionModes)
    }

    private val codexFull = DaemonSettings.newBuilder().setDefaultMode(ApprovalMode.APPROVAL_MODE_FULL_ACCESS).build()
    private val claudeFull = DaemonSettings.newBuilder()
        .setDefaultMode(ApprovalMode.APPROVAL_MODE_ASK)
        .setClaude(AgentDefaults.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_FULL_ACCESS)).build()

    @Test
    fun aFullAccessDefaultNeedsConfirming() {
        assertEquals(LaunchMode.ConfirmFull, launchMode(null, codexFull, Agent.AGENT_CODEX))
        assertEquals(LaunchMode.ConfirmFull, launchMode(null, claudeFull, Agent.AGENT_CLAUDE))
        // Each CLI has its own default.
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_ASK), launchMode(null, claudeFull, Agent.AGENT_CODEX))
        assertEquals("默认：完全放行", modeLabel(null, codexFull, Agent.AGENT_CODEX))
    }

    @Test
    fun aDefaultStartWaitsForTheMachinesDefault() {
        // Until it is read, a start could be in full access without anyone knowing.
        assertEquals(LaunchMode.Unknown, launchMode(null, null, Agent.AGENT_CODEX))
        // A chosen mode needs no default.
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_AUTO), launchMode(Mode.AUTO, null, Agent.AGENT_CODEX))
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_ASK), launchMode(Mode.ASK, codexFull, Agent.AGENT_CODEX))
        // No daemon default: the CLI's own configuration decides.
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED), launchMode(null, DaemonSettings.getDefaultInstance(), Agent.AGENT_CODEX))
    }
}
