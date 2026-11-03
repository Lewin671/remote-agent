package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentDefaults
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.MachineMeta
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AgentsTest {
    @Test
    fun listsKnownClisInStableOrder() {
        assertEquals(listOf("codex", "claude"), AGENTS.map { it.id })
        assertEquals(listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE), AGENTS.map { it.agent })
        assertEquals(Agent.AGENT_CODEX, DEFAULT_AGENT)
    }

    @Test
    fun looksUpAgentInfoByEnum() {
        val codex = agentInfo(Agent.AGENT_CODEX)
        assertEquals("codex", codex.id)
        assertEquals("Codex", codex.name)
        assertEquals(listOf("primary", "secondary"), codex.quotaWindows)
        assertTrue(codex.planClearContext)
        assertFalse(codex.planApprovalReturnsToAsk)
        assertFalse(codex.standardContextOption)
        assertFalse(codex.reconcilesUnclaimedInput)
        assertFalse(codex.sessionNeedsModelList)
        assertEquals("GPT-6-Sol", codex.modelLabel("gpt-6-sol"))
        assertEquals("Max", codex.planName("claude_max"))
        assertEquals("Pro Lite", codex.planName("prolite"))
        assertEquals("设置", codex.sheetTitle("设置"))

        val claude = agentInfo(Agent.AGENT_CLAUDE)
        assertEquals("claude", claude.id)
        assertEquals("Claude Code", claude.name)
        assertEquals(listOf("five_hour", "seven_day"), claude.quotaWindows)
        assertFalse(claude.planClearContext)
        assertTrue(claude.planApprovalReturnsToAsk)
        assertTrue(claude.standardContextOption)
        assertTrue(claude.reconcilesUnclaimedInput)
        assertTrue(claude.sessionNeedsModelList)
        assertEquals("claude-sonnet-5-5", claude.modelLabel("claude-sonnet-5-5"))
        assertEquals("Max", claude.planName("claude_max"))
        assertEquals("Pro Lite", claude.planName("prolite"))
        assertEquals("Claude Code · 设置", claude.sheetTitle("设置"))
    }

    @Test
    fun mapsUnspecifiedAndNullToCodex() {
        assertEquals("codex", agentInfo(Agent.AGENT_UNSPECIFIED).id)
        assertEquals("codex", agentInfo(null).id)
        assertEquals("Codex", agentName(Agent.AGENT_UNSPECIFIED))
        assertEquals("Codex", agentName(null))
        assertEquals("Codex", agentName())
    }

    @Test
    fun mapsUnrecognizedAgentValuesToUnknownEntryAndNeverCodex() {
        val unknown = agentInfo(Agent.UNRECOGNIZED)
        assertEquals("", unknown.id)
        assertEquals("未知 CLI", unknown.name)
        assertEquals(emptyList<String>(), unknown.quotaWindows)
        assertFalse(unknown.planClearContext)
        assertFalse(unknown.planApprovalReturnsToAsk)
        assertFalse(unknown.standardContextOption)
        assertNull(unknown.trustFolderHint)
        assertNull(unknown.usageHint)
        assertFalse(unknown.reconcilesUnclaimedInput)
        assertFalse(unknown.sessionNeedsModelList)
        assertEquals("any-slug", unknown.modelLabel("any-slug"))
        assertEquals("any-plan", unknown.planName("any-plan"))
        assertEquals("设置", unknown.sheetTitle("设置"))
        assertEquals("未知 CLI", agentName(Agent.UNRECOGNIZED))
    }

    @Test
    fun looksUpAgentsByTargetId() {
        assertEquals(Agent.AGENT_CODEX, agentById("codex").agent)
        assertEquals(Agent.AGENT_CLAUDE, agentById("claude").agent)
        assertEquals("", agentById("").id)
        assertEquals("", agentById("unknown").id)
        assertEquals("", agentById(null).id)
    }

    @Test
    fun checksWhetherAnAgentEnumIsRecognized() {
        assertTrue(isKnownAgent(Agent.AGENT_CODEX))
        assertTrue(isKnownAgent(Agent.AGENT_CLAUDE))
        assertFalse(isKnownAgent(Agent.AGENT_UNSPECIFIED))
        assertFalse(isKnownAgent(Agent.UNRECOGNIZED))
        assertFalse(isKnownAgent(null))
    }

    @Test
    fun roundTripsDefaultsAndWithDefaultsForCodex() {
        val base = DaemonSettings.newBuilder()
            .setDefaultModel("gpt-6-sol")
            .setDefaultEffort("medium")
            .setDefaultMode(ApprovalMode.APPROVAL_MODE_AUTO)
            .build()

        val defaults = defaultsFor(base, Agent.AGENT_CODEX)
        assertEquals("gpt-6-sol", defaults.model)
        assertEquals("medium", defaults.effort)
        assertEquals(ApprovalMode.APPROVAL_MODE_AUTO, defaults.mode)

        val updated = withDefaults(base, Agent.AGENT_CODEX, model = "gpt-6.1", effort = "high")
        assertEquals("gpt-6.1", updated.defaultModel)
        assertEquals("high", updated.defaultEffort)
        assertEquals(ApprovalMode.APPROVAL_MODE_AUTO, updated.defaultMode)

        val updatedDefaults = defaultsFor(updated, Agent.AGENT_CODEX)
        assertEquals("gpt-6.1", updatedDefaults.model)
        assertEquals("high", updatedDefaults.effort)
        assertEquals(ApprovalMode.APPROVAL_MODE_AUTO, updatedDefaults.mode)
    }

    @Test
    fun roundTripsDefaultsAndWithDefaultsForClaude() {
        val base = DaemonSettings.newBuilder()
            .setClaude(
                AgentDefaults.newBuilder()
                    .setModel("claude-sonnet-5-5")
                    .setEffort("low")
                    .setMode(ApprovalMode.APPROVAL_MODE_ASK)
            )
            .build()

        val defaults = defaultsFor(base, Agent.AGENT_CLAUDE)
        assertEquals("claude-sonnet-5-5", defaults.model)
        assertEquals("low", defaults.effort)
        assertEquals(ApprovalMode.APPROVAL_MODE_ASK, defaults.mode)

        val updated = withDefaults(base, Agent.AGENT_CLAUDE, model = "claude-opus-9")
        assertEquals("claude-opus-9", updated.claude.model)
        assertEquals("low", updated.claude.effort)
        assertEquals(ApprovalMode.APPROVAL_MODE_ASK, updated.claude.mode)

        val updatedDefaults = defaultsFor(updated, Agent.AGENT_CLAUDE)
        assertEquals("claude-opus-9", updatedDefaults.model)
        assertEquals("low", updatedDefaults.effort)
        assertEquals(ApprovalMode.APPROVAL_MODE_ASK, updatedDefaults.mode)
    }

    @Test
    fun preservesSettingsWhenUpdatingUnknownAgentDefaults() {
        val base = DaemonSettings.newBuilder().setDefaultModel("m").build()
        assertEquals(base, withDefaults(base, Agent.UNRECOGNIZED, model = "x"))
        val defaults = defaultsFor(base, Agent.UNRECOGNIZED)
        assertEquals("", defaults.model)
        assertEquals("", defaults.effort)
        assertEquals(ApprovalMode.APPROVAL_MODE_UNSPECIFIED, defaults.mode)
    }

    @Test
    fun synthesizesMachineVersionsFromLegacyCodexVersionAndModernAgentsList() {
        val legacy = MachineMeta.newBuilder()
            .setCodexVersion("1.2.3")
            .setCodexVerified(true)
            .build()
        val legacyVersions = machineVersions(legacy)
        assertEquals(1, legacyVersions.size)
        assertEquals(Agent.AGENT_CODEX, legacyVersions[0].agent)
        assertEquals("1.2.3", legacyVersions[0].version)
        assertTrue(legacyVersions[0].verified)

        val modern = MachineMeta.newBuilder()
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setVersion("2.0.0"))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setVersion("3.0.0"))
            .build()
        val modernVersions = machineVersions(modern)
        assertEquals(2, modernVersions.size)
        assertEquals(Agent.AGENT_CODEX, modernVersions[0].agent)
        assertEquals("2.0.0", modernVersions[0].version)
        assertEquals(Agent.AGENT_CLAUDE, modernVersions[1].agent)
        assertEquals("3.0.0", modernVersions[1].version)
    }
}
