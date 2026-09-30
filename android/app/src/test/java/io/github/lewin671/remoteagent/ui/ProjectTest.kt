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

    @Test
    fun aDraftKeepsItsConfigurationWhenTheProjectChanges() {
        val first = NewConfig("m1", "/work/app", Agent.AGENT_CODEX, "gpt-a")
        val moved = NewConfig("m2", "/work/app", Agent.AGENT_CLAUDE)
        val d = DraftConfig(first)
        // Nothing written yet: it follows the project.
        d.follow(moved, drafting = false)
        assertEquals(moved, d.value)
        d.follow(first, drafting = false)
        // Once a draft is begun, an update does not retarget it.
        d.follow(moved, drafting = true)
        assertEquals(first, d.value)
        // Sent and cleared, it follows again.
        d.follow(moved, drafting = false)
        assertEquals(moved, d.value)
        // A choice stays even with nothing written.
        val mine = NewConfig("m1", "/work/app", Agent.AGENT_CODEX, mode = Mode.ASK)
        d.choose(mine)
        d.follow(first, drafting = false)
        assertEquals(mine, d.value)
    }

    @Test
    fun aReopenedDraftKeepsWhereItWasGoing() {
        val suggested = NewConfig("m1", "/work/reopen", Agent.AGENT_CODEX)
        val first = Drafts.newSession("/work/reopen", suggested)
        val mine = NewConfig("m2", "/work/reopen", Agent.AGENT_CLAUDE)
        first.config.choose(mine)
        first.text.value = "修一下登录"
        // Back out and in again, with the project now suggesting something else.
        val again = Drafts.newSession("/work/reopen", NewConfig("m3", "/work/reopen", Agent.AGENT_CODEX))
        assertEquals("修一下登录", again.text.value)
        assertEquals(mine, again.config.value)
    }
}

class SwitchTest {
    private val c = NewConfig("mac", "/p", io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX, "gpt-6-astra", "medium", io.github.lewin671.remoteagent.core.Mode.AUTO)

    @org.junit.Test
    fun anotherCliDropsTheModelAndKeepsASharedMode() {
        val s = switchAgent(c, io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE)
        org.junit.Assert.assertEquals("", s.model)
        org.junit.Assert.assertEquals("", s.effort)
        org.junit.Assert.assertEquals(io.github.lewin671.remoteagent.core.Mode.AUTO, s.mode)
        org.junit.Assert.assertEquals("mac", s.machineId)
        // Full access is never carried over: the machine's default decides and is confirmed.
        val full = switchAgent(c.copy(mode = io.github.lewin671.remoteagent.core.Mode.FULL), io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE)
        org.junit.Assert.assertEquals(null, full.mode)
    }

    @org.junit.Test
    fun anotherMachineKeepsTheCliOnlyWhenItHasIt() {
        val claude = c.copy(agent = io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE)
        val s = switchMachine(claude, "vps", listOf(io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX))
        org.junit.Assert.assertEquals(io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX, s.agent)
        org.junit.Assert.assertEquals("", s.model)
        val kept = switchMachine(claude, "imac", listOf(io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX, io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE))
        org.junit.Assert.assertEquals(io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE, kept.agent)
    }

    @org.junit.Test
    fun theConfigurationIsTwoLines() {
        val state = io.github.lewin671.remoteagent.core.RemoteState()
        val (a, b) = configLines(state, c, null)
        org.junit.Assert.assertTrue(a.endsWith("自动"))
        org.junit.Assert.assertEquals("gpt-6-astra · medium", b)
    }
}
