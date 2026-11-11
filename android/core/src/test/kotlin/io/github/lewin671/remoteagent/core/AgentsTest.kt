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
        assertEquals(listOf("codex", "claude", "agy"), AGENTS.map { it.id })
        assertEquals(
            listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE, Agent.AGENT_ANTIGRAVITY),
            AGENTS.map { it.agent },
        )
        assertEquals(Agent.AGENT_CODEX, DEFAULT_AGENT)
    }

    @Test
    fun looksUpAgentInfoByEnum() {
        val codex = agentInfo(Agent.AGENT_CODEX)
        assertEquals(QuotaWindowDescriptor("5 小时"), codex.quotaWindow("primary"))
        assertEquals(QuotaWindowDescriptor("7 天"), codex.quotaWindow("secondary"))
        assertTrue(codex.planClearContext)
        assertEquals("继续规划…", codex.planKeepLabel)
        assertEquals("批准执行", codex.planApproveLabel)
        assertNull(codex.planNote)
        assertFalse(codex.standardContextOption)
        assertFalse(codex.autoMayBeUnavailable)
        assertFalse(codex.reconcilesUnclaimedInput)
        assertFalse(codex.sessionNeedsModelList)
        assertTrue(codex.switchesModelInSession)
        assertTrue(codex.forks)

        val claude = agentInfo(Agent.AGENT_CLAUDE)
        assertEquals(QuotaWindowDescriptor("5 小时"), claude.quotaWindow("five_hour"))
        assertEquals(QuotaWindowDescriptor("7 天"), claude.quotaWindow("seven_day"))
        assertFalse(claude.planClearContext)
        assertEquals("修改计划…", claude.planKeepLabel)
        assertEquals("批准", claude.planApproveLabel)
        assertEquals("批准后使用询问模式", claude.planNote)
        assertTrue(claude.standardContextOption)
        assertTrue(claude.autoMayBeUnavailable)
        assertTrue(claude.reconcilesUnclaimedInput)
        assertTrue(claude.sessionNeedsModelList)
        assertTrue(claude.switchesModelInSession)
        assertTrue(claude.forks)

        val agy = agentInfo(Agent.AGENT_ANTIGRAVITY)
        assertEquals(QuotaWindowDescriptor("Gemini · 5 小时", "Gemini"), agy.quotaWindow("gemini-5h"))
        assertEquals(QuotaWindowDescriptor("Gemini · 7 天", "Gemini"), agy.quotaWindow("gemini-weekly"))
        assertEquals(QuotaWindowDescriptor("Gemini 4 Argon · 7 天", "Gemini"), agy.quotaWindow("gemini-argon-wtu-weekly"))
        assertEquals(QuotaWindowDescriptor("第三方模型 · 5 小时", "第三方模型（Claude、GPT-OSS）"), agy.quotaWindow("3p-5h"))
        assertEquals(QuotaWindowDescriptor("第三方模型 · 7 天", "第三方模型（Claude、GPT-OSS）"), agy.quotaWindow("3p-weekly"))
        assertEquals(QuotaWindowDescriptor("custom", "custom"), agy.quotaWindow("custom"))
        assertFalse(agy.toolLabelFromItemType)
        assertFalse(agy.planClearContext)
        assertEquals("修改计划…", agy.planKeepLabel)
        assertEquals("批准", agy.planApproveLabel)
        assertEquals("批准后回到默认模式（询问）", agy.planNote)
        assertFalse(agy.standardContextOption)
        assertTrue(agy.autoMayBeUnavailable)
        assertFalse(agy.reconcilesUnclaimedInput)
        assertTrue(agy.sessionNeedsModelList)
        assertFalse(agy.switchesModelInSession)
        assertFalse(agy.forks)
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
        assertEquals(Agent.UNRECOGNIZED, unknown.agent)
        assertEquals("", unknown.id)
        assertEquals("未知 CLI", unknown.name)
        assertEquals(emptyList<String>(), unknown.quotaWindows)
        assertEquals(QuotaWindowDescriptor("primary"), unknown.quotaWindow("primary"))
        assertFalse(unknown.planClearContext)
        assertNull(unknown.planNote)
        assertFalse(unknown.standardContextOption)
        assertFalse(unknown.autoMayBeUnavailable)
        assertNull(unknown.trustFolderHint)
        assertNull(unknown.usageHint)
        assertFalse(unknown.reconcilesUnclaimedInput)
        assertFalse(unknown.sessionNeedsModelList)
        assertFalse(unknown.switchesModelInSession)
        assertFalse(unknown.forks)
        assertEquals("any-slug", unknown.modelLabel("any-slug"))
        assertEquals("any-plan", unknown.planName("any-plan"))
        assertEquals("未知 CLI", agentName(Agent.UNRECOGNIZED))

        // Retains unrecognized agent on roundtrip through agentInfo
        val roundtrip = agentInfo(unknown.agent)
        assertEquals("未知 CLI", roundtrip.name)
        assertEquals(Agent.UNRECOGNIZED, roundtrip.agent)
    }

    @Test
    fun looksUpAgentsByTargetId() {
        assertEquals(Agent.AGENT_CODEX, agentById("codex").agent)
        assertEquals(Agent.AGENT_CLAUDE, agentById("claude").agent)
        assertEquals(Agent.AGENT_ANTIGRAVITY, agentById("agy").agent)
        assertEquals("", agentById("").id)
        assertEquals("", agentById("unknown").id)
        assertEquals("", agentById(null).id)
    }

    @Test
    fun checksWhetherAnAgentEnumIsRecognized() {
        assertTrue(isKnownAgent(Agent.AGENT_CODEX))
        assertTrue(isKnownAgent(Agent.AGENT_CLAUDE))
        assertTrue(isKnownAgent(Agent.AGENT_ANTIGRAVITY))
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
    fun roundTripsDefaultsAndWithDefaultsForAntigravity() {
        val base = DaemonSettings.newBuilder()
            .setAntigravity(
                AgentDefaults.newBuilder()
                    .setModel("gemini-3.1-pro-high")
                    .setEffort("high")
                    .setMode(ApprovalMode.APPROVAL_MODE_ASK),
            )
            .build()

        val defaults = defaultsFor(base, Agent.AGENT_ANTIGRAVITY)
        assertEquals("gemini-3.1-pro-high", defaults.model)
        assertEquals("high", defaults.effort)
        assertEquals(ApprovalMode.APPROVAL_MODE_ASK, defaults.mode)

        val updated = withDefaults(base, Agent.AGENT_ANTIGRAVITY, model = "gemini-3.8-flash")
        assertEquals("gemini-3.8-flash", updated.antigravity.model)
        assertEquals("high", updated.antigravity.effort)
        assertEquals(ApprovalMode.APPROVAL_MODE_ASK, updated.antigravity.mode)

        val updatedDefaults = defaultsFor(updated, Agent.AGENT_ANTIGRAVITY)
        assertEquals("gemini-3.8-flash", updatedDefaults.model)
        assertEquals("high", updatedDefaults.effort)
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
