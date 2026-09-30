package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import org.junit.Assert.assertEquals
import org.junit.Test

class ProjectTest {
    @Test
    fun savedSettingsWinOverLatestSessionAndStayOnOfflineMachine() {
        val saved = io.github.lewin671.remoteagent.proto.ProjectSettings.newBuilder().setPath("/work/app").setMachineId("old")
            .setAgent(Agent.AGENT_CLAUDE).setModel("sonnet").setEffort("high").setMode(ApprovalMode.APPROVAL_MODE_FULL_ACCESS).build()
        val latest = io.github.lewin671.remoteagent.core.SessionView("s", "new",
            io.github.lewin671.remoteagent.proto.Session.newBuilder().setCwd("/work/app").setModel("astra").setAgent(Agent.AGENT_CODEX).build(), updatedMs = 999)
        val state = io.github.lewin671.remoteagent.core.RemoteState(
            prefs = io.github.lewin671.remoteagent.core.PrefsObject(1, io.github.lewin671.remoteagent.proto.ProjectPrefs.newBuilder().addSettings(saved).build()),
            sessions = mapOf(latest.id to latest), machines = mapOf("old" to io.github.lewin671.remoteagent.core.MachineView("old", false, 0)))
        val config = projectConfig(state, "/work/app")!!
        assertEquals("old", config.machineId)
        assertEquals(Mode.FULL, config.mode)
        assertEquals(saved, config.settings(saved.updatedMs))
    }

    @Test fun summaryUsesTheMachineModelDisplayName() {
        val models = io.github.lewin671.remoteagent.proto.ListModelsResponse.newBuilder()
            .addModels(io.github.lewin671.remoteagent.proto.ModelInfo.newBuilder().setSlug("gpt-6-astra").setDisplayName("GPT-6-Astra").setDefaultEffort("medium"))
            .build()
        val text = projectSummary(io.github.lewin671.remoteagent.core.RemoteState(),
            NewConfig("mac", "/work/app", model = "gpt-6-astra", mode = Mode.ASK), models)
        org.junit.Assert.assertTrue(text.contains("GPT-6-Astra · medium"))
    }
    @Test
    fun aNewSessionCannotBeStartedInPlan() {
        assertEquals(listOf(Mode.ASK, Mode.AUTO, Mode.FULL), newSessionModes)
    }

    private fun machine(vararg infos: io.github.lewin671.remoteagent.proto.AgentInfo) =
        io.github.lewin671.remoteagent.core.MachineView("m", true, 0, io.github.lewin671.remoteagent.proto.MachineMeta.newBuilder().addAllAgents(infos.toList()).build())

    private fun info(agent: Agent, mode: ApprovalMode, source: io.github.lewin671.remoteagent.proto.DefaultModeSource, varies: Boolean = false) =
        io.github.lewin671.remoteagent.proto.AgentInfo.newBuilder().setAgent(agent).setDefaultMode(mode).setDefaultModeSource(source).setDefaultModeVaries(varies).build()

    private val cli = io.github.lewin671.remoteagent.proto.DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG

    @Test
    fun theCliDefaultTheMachineReportsIsShownWithoutAConfirmation() {
        // As measured on this machine: Claude Code defaults to bypass, Codex to auto.
        val m = machine(info(Agent.AGENT_CLAUDE, ApprovalMode.APPROVAL_MODE_FULL_ACCESS, cli, varies = true), info(Agent.AGENT_CODEX, ApprovalMode.APPROVAL_MODE_AUTO, cli))
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED), launchMode(null, defaultModeOf(m, Agent.AGENT_CLAUDE)))
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED), launchMode(null, defaultModeOf(m, Agent.AGENT_CODEX)))
        assertEquals("默认：完全放行（项目设置可能不同）", modeLabel(null, defaultModeOf(m, Agent.AGENT_CLAUDE), Agent.AGENT_CLAUDE))
        assertEquals("默认：自动", modeLabel(null, defaultModeOf(m, Agent.AGENT_CODEX), Agent.AGENT_CODEX))
        assertEquals("自动", effectiveModeText(null, defaultModeOf(m, Agent.AGENT_CODEX)))
        // A chosen mode needs no default.
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_ASK), launchMode(Mode.ASK, defaultModeOf(m, Agent.AGENT_CLAUDE)))
    }

    @Test
    fun anUnknownDefaultDoesNotBlockAStart() {
        // An older daemon says nothing: it could be full access.
        val old = machine(io.github.lewin671.remoteagent.proto.AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).build())
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED), launchMode(null, defaultModeOf(old, Agent.AGENT_CODEX)))
        // A mode outside the four (accept edits) is known and not full access.
        val edits = machine(info(Agent.AGENT_CLAUDE, ApprovalMode.APPROVAL_MODE_UNSPECIFIED, cli))
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED), launchMode(null, defaultModeOf(edits, Agent.AGENT_CLAUDE)))
        assertEquals("默认：Claude Code 自己的模式", modeLabel(null, defaultModeOf(edits, Agent.AGENT_CLAUDE), Agent.AGENT_CLAUDE))
    }

    @Test
    fun onlyAStartedSessionTakesTheDraft() {
        // A refusal that asks for confirmation answers nothing: the draft stays.
        org.junit.Assert.assertNull(startedSession(null))
        org.junit.Assert.assertNull(startedSession(io.github.lewin671.remoteagent.proto.CommandResult.getDefaultInstance()))
        val ok = io.github.lewin671.remoteagent.proto.CommandResult.newBuilder().setCreateSession(
            io.github.lewin671.remoteagent.proto.CreateSessionResponse.newBuilder().setSession(io.github.lewin671.remoteagent.proto.Session.newBuilder().setId("s1")),
        ).build()
        assertEquals("s1", startedSession(ok))
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
        // Full access is an explicit project setting and remains selected.
        val full = switchAgent(c.copy(mode = io.github.lewin671.remoteagent.core.Mode.FULL), io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE)
        org.junit.Assert.assertEquals(io.github.lewin671.remoteagent.core.Mode.FULL, full.mode)
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
        val (a, b) = configLines(state, c, DefaultMode(null, false, false))
        org.junit.Assert.assertTrue(a.endsWith("自动"))
        org.junit.Assert.assertEquals("gpt-6-astra · medium", b)
    }
}
