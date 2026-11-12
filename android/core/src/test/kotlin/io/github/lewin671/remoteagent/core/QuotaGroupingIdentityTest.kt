package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class QuotaGroupingIdentityTest {
    @Test fun translatedSectionNamesCannotMergeDifferentQuotaPools() {
        val now = 1_790_000_000_000L
        val entries = listOf("gemini-5h", "gemini-weekly", "gemini-argon-wtu-weekly", "3p-5h", "3p-weekly")
        val quota = AgentQuota.newBuilder().setAgent(Agent.AGENT_ANTIGRAVITY).setObservedMs(now).setState(QuotaState.QUOTA_STATE_OK)
        entries.forEachIndexed { i, name -> quota.addWindows(QuotaWindow.newBuilder().setName(name).setUsedPercent((i + 1).toDouble()).setResetsAtMs(now + 3600_000)) }
        val machine = MachineView("computer", true, now, MachineMeta.newBuilder()
            .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_ANTIGRAVITY)).addQuotas(quota).build())
        // Deliberately collide every translated title with the untranslated Gemini brand.
        val group = quotaGroups(machine, now, copy = { "Gemini" }).single()
        assertEquals(2, group.sections.size)
        assertEquals(listOf(listOf(1.0, 2.0, 3.0), listOf(4.0, 5.0)), group.sections.map { it.rows.map { row -> row.percent } })
    }
}
