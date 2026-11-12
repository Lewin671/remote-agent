package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.AgentQuota
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.QuotaState
import io.github.lewin671.remoteagent.proto.QuotaWindow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class QuotaTest {
    private val now = 1_790_000_000_000L
    private fun machine(online: Boolean, vararg q: AgentQuota) = MachineView(
        "m", online, 0,
        MachineMeta.newBuilder().addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX)).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).addAllQuotas(q.toList()).build(),
    )
    private fun w(name: String, pct: Double?, resetIn: Long = 3_600_000, minutes: Long = 0L, baseNow: Long = now) =
        QuotaWindow.newBuilder().setName(name).setResetsAtMs(baseNow + resetIn).apply {
            if (pct != null) setUsedPercent(pct)
            if (minutes > 0) setWindowMinutes(minutes)
        }.build()

    @Test
    fun timesUseBeijingWhereverTheClientIs() {
        val now = java.time.Instant.parse("2026-10-02T15:30:00Z").toEpochMilli()
        val w = QuotaWindow.newBuilder().setName("seven_day").setUsedPercent(95.0)
            .setResetsAtMs(java.time.Instant.parse("2026-10-02T16:10:00Z").toEpochMilli()).build()
        val m = machine(true, AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE)
            .setState(QuotaState.QUOTA_STATE_OK).setObservedMs(now - 60_000).addWindows(w).build())
        val g = quotaGroups(m, now).single { it.agent == Agent.AGENT_CLAUDE }
        // Still Friday in Beijing, so the reset after midnight is not 今天.
        assertEquals("40 分钟后重置 · 10月3日 周六 0:10", g.rows.single().reset)
        assertEquals("23:29", g.received?.args?.get("time"))
    }

    @Test
    fun resetSaysHowLongIsLeftThenTheMoment() {
        // 9:41 in Beijing on Friday 2 October 2026.
        val now = java.time.Instant.parse("2026-10-02T01:41:00Z").toEpochMilli()
        fun at(utc: String) = resetText(java.time.Instant.parse(utc).toEpochMilli(), now)
        assertEquals("3 小时 39 分后重置 · 今天 13:20", at("2026-10-02T05:20:00Z"))
        assertEquals("6 天 4 小时后重置 · 10月8日 周四 14:10", at("2026-10-08T06:10:00Z"))
        // Rounded down, and a zero part is still written.
        assertEquals("6 天 0 小时后重置 · 10月8日 周四 9:41", at("2026-10-08T01:41:59Z"))
        assertEquals("3 小时 0 分后重置 · 今天 12:41", at("2026-10-02T04:41:30Z"))
        assertEquals("23 小时 59 分后重置 · 10月3日 周六 9:40", at("2026-10-03T01:40:00Z"))
        assertEquals("1 天 0 小时后重置 · 10月3日 周六 9:41", at("2026-10-03T01:41:00Z"))
        assertEquals("1 小时 0 分后重置 · 今天 10:41", at("2026-10-02T02:41:00Z"))
        assertEquals("39 分钟后重置 · 今天 10:20", at("2026-10-02T02:20:00Z"))
        assertEquals("1 分钟后重置 · 今天 9:42", at("2026-10-02T01:42:00Z"))
        assertEquals("即将重置 · 今天 9:41", at("2026-10-02T01:41:59Z"))
        // The hour is not padded; the minute is.
        assertEquals("15 小时 24 分后重置 · 10月3日 周六 1:05", at("2026-10-02T17:05:00Z"))
    }

    @Test
    fun claudeCodesQuotaIsShownWhenTheDaemonReadsIt() {
        val m = machine(
            true,
            AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).setPlan("claude_max").setObservedMs(now - 60_000)
                .addWindows(w("five_hour", 92.0)).addWindows(w("seven_day", null)).build(),
        )
        val g = quotaGroups(m, now).single { it.agent == Agent.AGENT_CLAUDE }
        assertEquals("Claude Code · Max", g.title)
        assertEquals(mapOf("name" to "Claude Code", "window" to "5 小时"), quotaBanner(g)?.args)
        assertEquals(QuotaStatus.OK, g.status)
        assertEquals("剩 8%", g.rows[0].left)
        assertEquals(QuotaTone.WARN, g.rows[0].tone)
        // A window without a number is unknown, never 0%.
        assertEquals("未获取", g.rows[1].left)
        assertEquals(null, g.rows[1].percent)
        // Codex has no entry: this daemon does not read it.
        assertEquals(QuotaStatus.UNSUPPORTED, quotaGroups(m, now).single { it.agent == Agent.AGENT_CODEX }.status)
    }

    @Test
    fun waitingFailedAndOfflineAreSaidAsSuch() {
        val waiting = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_WAITING).build()
        assertEquals("暂未获取额度", quotaLine(quotaGroups(machine(true, waiting), now).single { it.agent == Agent.AGENT_CLAUDE }))
        val failed = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_FAILED).setDetail("状态栏输入读不出来")
            .setObservedMs(now - 3_600_000).addWindows(w("five_hour", 28.0)).build()
        val g = quotaGroups(machine(true, failed), now).single { it.agent == Agent.AGENT_CLAUDE }
        assertTrue(g.lastRecord)
        assertEquals("上次剩 72%", g.rows[0].left)
        assertEquals(QuotaTone.STALE, g.rows[0].tone)
        assertEquals("上次剩 72% · 5 小时", quotaLine(g))
        assertEquals("quota.received_success", g.received?.key)
        val ok = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).addWindows(w("five_hour", 28.0)).build()
        assertEquals(QuotaTone.STALE, quotaGroups(machine(false, ok), now).single { it.agent == Agent.AGENT_CLAUDE }.rows[0].tone)
    }

    @Test
    fun aPassedResetOnlyWaitsForAnUpdate() {
        val q = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).addWindows(w("five_hour", 100.0, resetIn = -60_000)).build()
        val r = quotaGroups(machine(true, q), now).single { it.agent == Agent.AGENT_CLAUDE }.rows[0]
        assertEquals("等待更新", r.left)
        assertTrue(r.note.contains("不能据此确认额度已经恢复"))
    }

    @Test
    fun accountsThatCannotBeToldApartStayApart() {
        val a = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).setObservedMs(now).setSourceSessionId("aaaa1111bbbb").addWindows(w("five_hour", 10.0)).build()
        val b = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).setObservedMs(now).setAccount("f00dbeef99").addWindows(w("five_hour", 50.0)).build()
        val gs = quotaGroups(machine(true, a, b), now).filter { it.agent == Agent.AGENT_CLAUDE }
        assertEquals(listOf("Claude Code · 会话 bbbb", "Claude Code · 账户 f00dbeef99"), gs.map { it.title })
    }

    @Test
    fun absentWindowsAreLeftOutAndReportedDurationsAreKept() {
        for ((agent, name) in listOf(Agent.AGENT_CLAUDE to "five_hour", Agent.AGENT_CODEX to "primary")) {
            val window = w(name, 99.6).toBuilder().setWindowMinutes(120).build()
            val q = AgentQuota.newBuilder().setAgent(agent).setState(QuotaState.QUOTA_STATE_OK).setObservedMs(now).addWindows(window).build()
            val g = quotaGroups(machine(true, q), now).single { it.agent == agent }
            assertEquals("2 小时", g.rows[0].label)
            assertEquals("剩 1%", g.rows[0].left)
            assertEquals(QuotaTone.WARN, g.rows[0].tone)
            assertEquals(mapOf("name" to agentName(agent), "window" to "2 小时"), quotaBanner(g)?.args)
            // A window the account does not have is not shown as missing.
            assertEquals(1, g.rows.size)
        }
        assertEquals("Pro Lite", planName("prolite"))
        assertEquals("Team Plus", planName("Team Plus"))
    }

    @Test
    fun groupsAntigravityQuotaWindowsByModelFamily() {
        val observedMs = java.time.Instant.parse("2026-10-02T01:41:00Z").toEpochMilli()
        val now = observedMs + 10_000
        val windows = listOf(
            w("gemini-5h", 20.0, 3600_000, 300, baseNow = now),
            w("gemini-weekly", 10.0, 86400_000, 10080, baseNow = now),
            w("gemini-argon-wtu-weekly", 15.0, 86400_000, 10080, baseNow = now),
            w("3p-5h", 40.0, 7200_000, 300, baseNow = now),
            w("3p-weekly", 30.0, 86400_000, 10080, baseNow = now),
            w("custom-bucket", 5.0, 86400_000, 60, baseNow = now),
        )
        val q = AgentQuota.newBuilder()
            .setAgent(Agent.AGENT_ANTIGRAVITY)
            .setState(QuotaState.QUOTA_STATE_OK)
            .setPlan("Google AI Pro")
            .setObservedMs(observedMs)
            .addAllWindows(windows)
            .build()
        val m = MachineView(
            "m", true, 0,
            MachineMeta.newBuilder()
                .addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_ANTIGRAVITY))
                .addQuotas(q)
                .build(),
        )
        val g = quotaGroups(m, now).single { it.agent == Agent.AGENT_ANTIGRAVITY }
        assertEquals("Antigravity · Google AI Pro", g.title)
        assertEquals("9:41", g.received?.args?.get("time"))
        assertEquals(3, g.sections.size)
        assertEquals("Antigravity · Google AI Pro · Gemini", g.sections[0].title)
        assertEquals(listOf("Gemini · 5 小时", "Gemini · 7 天", "Gemini 4 Argon · 7 天"), g.sections[0].rows.map { it.label })
        assertEquals("第三方模型（Claude、GPT-OSS）", g.sections[1].title)
        assertEquals(listOf("第三方模型 · 5 小时", "第三方模型 · 7 天"), g.sections[1].rows.map { it.label })
        assertEquals("custom-bucket", g.sections[2].title)
        assertEquals(listOf("custom-bucket"), g.sections[2].rows.map { it.label })
        assertEquals("剩 60% · 5 小时", quotaLine(g))
    }
    @Test
    fun onlineReadingsAgeAtThirtyMinutesAndEarlierObservationsIncludeDates() {
        val now = java.time.Instant.parse("2026-11-12T08:16:00Z").toEpochMilli()
        val q = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK)
            .addWindows(w("seven_day", 97.0, resetIn = 86_400_000, baseNow = now))
        fun group() = quotaGroups(machine(true, q.build()), now).single { it.agent == Agent.AGENT_CLAUDE }
        q.setObservedMs(now - 1_799_999)
        assertEquals(QuotaTone.WARN, group().rows[0].tone)
        q.setObservedMs(now - 1_800_000)
        assertEquals(QuotaTone.STALE, group().rows[0].tone)
        assertTrue(group().lastRecord)
        assertEquals("quota.old_minutes", group().freshness?.key)
        assertEquals(30, group().freshness?.args?.get("count"))
        assertEquals(null, quotaBanner(group()))
        assertEquals(14, quotaFreshness(now - 14 * 3_600_000, now)?.args?.get("count"))
        q.setObservedMs(java.time.Instant.parse("2026-11-11T15:59:00Z").toEpochMilli())
        assertEquals("quota.received_date", group().received?.key)
        assertEquals(mapOf("day" to 11, "month" to 11, "time" to "23:59"), group().received?.args)
        q.setObservedMs(0)
        assertEquals("quota.unknown_time", group().freshness?.key)
    }

}
