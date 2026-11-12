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
        assertMessageEquals(listOf("codex", "claude", "agy"), AGENTS.map { it.id })
        assertMessageEquals(
            listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE, Agent.AGENT_ANTIGRAVITY),
            AGENTS.map { it.agent },
        )
        assertMessageEquals(Agent.AGENT_CODEX, DEFAULT_AGENT)
    }

    @Test
    fun looksUpAgentInfoByEnum() {
        val codex = agentInfo(Agent.AGENT_CODEX)
        assertMessageEquals(QuotaWindowDescriptor("5 小时"), codex.quotaWindow("primary"))
        assertMessageEquals(QuotaWindowDescriptor("7 天"), codex.quotaWindow("secondary"))
        assertTrue(codex.planClearContext)
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.dockAndroidPlanKeep()), codex.planKeepLabel)
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.approvalApproveExecute()), codex.planApproveLabel)
        assertNull(codex.planNote)
        assertFalse(codex.standardContextOption)
        assertFalse(codex.autoMayBeUnavailable)
        assertFalse(codex.reconcilesUnclaimedInput)
        assertFalse(codex.sessionNeedsModelList)
        assertTrue(codex.switchesModelInSession)
        assertTrue(codex.forks)

        val claude = agentInfo(Agent.AGENT_CLAUDE)
        assertMessageEquals(QuotaWindowDescriptor("5 小时"), claude.quotaWindow("five_hour"))
        assertMessageEquals(QuotaWindowDescriptor("7 天"), claude.quotaWindow("seven_day"))
        assertFalse(claude.planClearContext)
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.modesPlanEdit()), claude.planKeepLabel)
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.approvalApprove()), claude.planApproveLabel)
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.dockClaudePlanNote()), claude.planNote)
        assertTrue(claude.standardContextOption)
        assertTrue(claude.autoMayBeUnavailable)
        assertTrue(claude.reconcilesUnclaimedInput)
        assertTrue(claude.sessionNeedsModelList)
        assertTrue(claude.switchesModelInSession)
        assertTrue(claude.forks)

        val agy = agentInfo(Agent.AGENT_ANTIGRAVITY)
        assertMessageEquals(QuotaWindowDescriptor("Gemini · 5 小时", "Gemini"), agy.quotaWindow("gemini-5h"))
        assertMessageEquals(QuotaWindowDescriptor("Gemini · 7 天", "Gemini"), agy.quotaWindow("gemini-weekly"))
        assertMessageEquals(QuotaWindowDescriptor("Gemini 4 Argon · 7 天", "Gemini"), agy.quotaWindow("gemini-argon-wtu-weekly"))
        assertMessageEquals(QuotaWindowDescriptor("第三方模型 · 5 小时", "第三方模型（Claude、GPT-OSS）"), agy.quotaWindow("3p-5h"))
        assertMessageEquals(QuotaWindowDescriptor("第三方模型 · 7 天", "第三方模型（Claude、GPT-OSS）"), agy.quotaWindow("3p-weekly"))
        assertMessageEquals(QuotaWindowDescriptor("custom", "custom"), agy.quotaWindow("custom"))
        assertFalse(agy.toolLabelFromItemType)
        assertFalse(agy.planClearContext)
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.modesPlanEdit()), agy.planKeepLabel)
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.approvalApprove()), agy.planApproveLabel)
        assertMessageEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.dockAgyPlanNote()), agy.planNote)
        assertFalse(agy.standardContextOption)
        assertTrue(agy.autoMayBeUnavailable)
        assertFalse(agy.reconcilesUnclaimedInput)
        assertTrue(agy.sessionNeedsModelList)
        assertFalse(agy.switchesModelInSession)
        assertFalse(agy.forks)
    }

    @Test
    fun mapsUnspecifiedAndNullToCodex() {
        assertMessageEquals("codex", agentInfo(Agent.AGENT_UNSPECIFIED).id)
        assertMessageEquals("codex", agentInfo(null).id)
        assertMessageEquals("Codex", agentName(Agent.AGENT_UNSPECIFIED))
        assertMessageEquals("Codex", agentName(null))
        assertMessageEquals("Codex", agentName())
    }

    @Test
    fun mapsUnrecognizedAgentValuesToUnknownEntryAndNeverCodex() {
        val unknown = agentInfo(Agent.UNRECOGNIZED)
        assertMessageEquals(Agent.UNRECOGNIZED, unknown.agent)
        assertMessageEquals("", unknown.id)
        assertMessageEquals("未知 CLI", unknown.name)
        assertMessageEquals(emptyList<String>(), unknown.quotaWindows)
        assertMessageEquals(QuotaWindowDescriptor("primary"), unknown.quotaWindow("primary"))
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
        assertMessageEquals("any-slug", unknown.modelLabel("any-slug"))
        assertMessageEquals("any-plan", unknown.planName("any-plan"))
        assertMessageEquals("未知 CLI", agentName(Agent.UNRECOGNIZED))

        // Retains unrecognized agent on roundtrip through agentInfo
        val roundtrip = agentInfo(unknown.agent)
        assertMessageEquals("未知 CLI", roundtrip.name)
        assertMessageEquals(Agent.UNRECOGNIZED, roundtrip.agent)
    }

    @Test
    fun looksUpAgentsByTargetId() {
        assertMessageEquals(Agent.AGENT_CODEX, agentById("codex").agent)
        assertMessageEquals(Agent.AGENT_CLAUDE, agentById("claude").agent)
        assertMessageEquals(Agent.AGENT_ANTIGRAVITY, agentById("agy").agent)
        assertMessageEquals("", agentById("").id)
        assertMessageEquals("", agentById("unknown").id)
        assertMessageEquals("", agentById(null).id)
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
        assertMessageEquals("gpt-6-sol", defaults.model)
        assertMessageEquals("medium", defaults.effort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_AUTO, defaults.mode)

        val updated = withDefaults(base, Agent.AGENT_CODEX, model = "gpt-6.1", effort = "high")
        assertMessageEquals("gpt-6.1", updated.defaultModel)
        assertMessageEquals("high", updated.defaultEffort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_AUTO, updated.defaultMode)

        val updatedDefaults = defaultsFor(updated, Agent.AGENT_CODEX)
        assertMessageEquals("gpt-6.1", updatedDefaults.model)
        assertMessageEquals("high", updatedDefaults.effort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_AUTO, updatedDefaults.mode)
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
        assertMessageEquals("claude-sonnet-5-5", defaults.model)
        assertMessageEquals("low", defaults.effort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_ASK, defaults.mode)

        val updated = withDefaults(base, Agent.AGENT_CLAUDE, model = "claude-opus-9")
        assertMessageEquals("claude-opus-9", updated.claude.model)
        assertMessageEquals("low", updated.claude.effort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_ASK, updated.claude.mode)

        val updatedDefaults = defaultsFor(updated, Agent.AGENT_CLAUDE)
        assertMessageEquals("claude-opus-9", updatedDefaults.model)
        assertMessageEquals("low", updatedDefaults.effort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_ASK, updatedDefaults.mode)
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
        assertMessageEquals("gemini-3.1-pro-high", defaults.model)
        assertMessageEquals("high", defaults.effort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_ASK, defaults.mode)

        val updated = withDefaults(base, Agent.AGENT_ANTIGRAVITY, model = "gemini-3.8-flash")
        assertMessageEquals("gemini-3.8-flash", updated.antigravity.model)
        assertMessageEquals("high", updated.antigravity.effort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_ASK, updated.antigravity.mode)

        val updatedDefaults = defaultsFor(updated, Agent.AGENT_ANTIGRAVITY)
        assertMessageEquals("gemini-3.8-flash", updatedDefaults.model)
        assertMessageEquals("high", updatedDefaults.effort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_ASK, updatedDefaults.mode)
    }

    @Test
    fun preservesSettingsWhenUpdatingUnknownAgentDefaults() {
        val base = DaemonSettings.newBuilder().setDefaultModel("m").build()
        assertMessageEquals(base, withDefaults(base, Agent.UNRECOGNIZED, model = "x"))
        val defaults = defaultsFor(base, Agent.UNRECOGNIZED)
        assertMessageEquals("", defaults.model)
        assertMessageEquals("", defaults.effort)
        assertMessageEquals(ApprovalMode.APPROVAL_MODE_UNSPECIFIED, defaults.mode)
    }

    @Test
    fun synthesizesMachineVersionsFromLegacyCodexVersionAndModernAgentsList() {
        val legacy = MachineMeta.newBuilder()
            .setCodexVersion("1.2.3")
            .setCodexVerified(true)
            .build()
        val legacyVersions = machineVersions(legacy)
        assertMessageEquals(1, legacyVersions.size)
        assertMessageEquals(Agent.AGENT_CODEX, legacyVersions[0].agent)
        assertMessageEquals("1.2.3", legacyVersions[0].version)
        assertTrue(legacyVersions[0].verified)

        val modern = MachineMeta.newBuilder()
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).setVersion("2.0.0"))
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE).setVersion("3.0.0"))
            .build()
        val modernVersions = machineVersions(modern)
        assertMessageEquals(2, modernVersions.size)
        assertMessageEquals(Agent.AGENT_CODEX, modernVersions[0].agent)
        assertMessageEquals("2.0.0", modernVersions[0].version)
        assertMessageEquals(Agent.AGENT_CLAUDE, modernVersions[1].agent)
        assertMessageEquals("3.0.0", modernVersions[1].version)
    }
}
