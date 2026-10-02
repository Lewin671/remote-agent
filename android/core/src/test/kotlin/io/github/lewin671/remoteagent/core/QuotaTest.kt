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
import java.time.ZoneId

class QuotaTest {
    private val zone = ZoneId.of("Asia/Shanghai")
    private val now = 1_790_000_000_000L
    private fun machine(online: Boolean, vararg q: AgentQuota) = MachineView(
        "m", online, 0,
        MachineMeta.newBuilder().addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX)).addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE)).addAllQuotas(q.toList()).build(),
    )
    private fun w(name: String, pct: Double?, resetIn: Long = 3_600_000) =
        QuotaWindow.newBuilder().setName(name).setResetsAtMs(now + resetIn).apply { if (pct != null) usedPercent = pct }.build()

    @Test
    fun claudeCodesQuotaIsShownWhenTheDaemonReadsIt() {
        val m = machine(
            true,
            AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).setPlan("claude_max").setObservedMs(now - 60_000)
                .addWindows(w("five_hour", 92.0)).addWindows(w("seven_day", null)).build(),
        )
        val g = quotaGroups(m, now, zone).single { it.agent == Agent.AGENT_CLAUDE }
        assertEquals("Claude Code · Max", g.title)
        assertEquals("5 小时额度即将用尽", quotaBanner(g))
        assertEquals(QuotaStatus.OK, g.status)
        assertEquals("已用 92%", g.rows[0].used)
        assertEquals(QuotaTone.WARN, g.rows[0].tone)
        // A window without a number is unknown, never 0%.
        assertEquals("未获取", g.rows[1].used)
        assertEquals(null, g.rows[1].percent)
        // Codex has no entry: this daemon does not read it.
        assertEquals(QuotaStatus.UNSUPPORTED, quotaGroups(m, now, zone).single { it.agent == Agent.AGENT_CODEX }.status)
    }

    @Test
    fun waitingFailedAndOfflineAreSaidAsSuch() {
        val waiting = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_WAITING).build()
        assertEquals("暂未获取额度", quotaLine(quotaGroups(machine(true, waiting), now, zone).single { it.agent == Agent.AGENT_CLAUDE }))
        val failed = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_FAILED).setDetail("状态栏输入读不出来")
            .setObservedMs(now - 3_600_000).addWindows(w("five_hour", 28.0)).build()
        val g = quotaGroups(machine(true, failed), now, zone).single { it.agent == Agent.AGENT_CLAUDE }
        assertTrue(g.lastRecord)
        assertEquals("已用 28%", g.rows[0].used)
        assertEquals(QuotaTone.STALE, g.rows[0].tone)
        assertEquals("5 小时 已用 28%（上次记录）", quotaLine(g))
        assertTrue(g.received.startsWith("最近成功收到"))
        val ok = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).addWindows(w("five_hour", 28.0)).build()
        assertEquals(QuotaTone.STALE, quotaGroups(machine(false, ok), now, zone).single { it.agent == Agent.AGENT_CLAUDE }.rows[0].tone)
    }

    @Test
    fun aPassedResetOnlyWaitsForAnUpdate() {
        val q = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).addWindows(w("five_hour", 100.0, resetIn = -60_000)).build()
        val r = quotaGroups(machine(true, q), now, zone).single { it.agent == Agent.AGENT_CLAUDE }.rows[0]
        assertEquals("等待更新", r.used)
        assertTrue(r.note.contains("不能据此确认额度已经恢复"))
    }

    @Test
    fun accountsThatCannotBeToldApartStayApart() {
        val a = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).setSourceSessionId("aaaa1111bbbb").addWindows(w("five_hour", 10.0)).build()
        val b = AgentQuota.newBuilder().setAgent(Agent.AGENT_CLAUDE).setState(QuotaState.QUOTA_STATE_OK).setAccount("f00dbeef99").addWindows(w("five_hour", 50.0)).build()
        val gs = quotaGroups(machine(true, a, b), now, zone).filter { it.agent == Agent.AGENT_CLAUDE }
        assertEquals(listOf("Claude Code · 会话 bbbb", "Claude Code · 账户 f00dbeef99"), gs.map { it.title })
    }

    @Test
    fun absentWindowsAreLeftOutAndReportedDurationsAreKept() {
        for ((agent, name) in listOf(Agent.AGENT_CLAUDE to "five_hour", Agent.AGENT_CODEX to "primary")) {
            val window = w(name, 99.6).toBuilder().setWindowMinutes(120).build()
            val q = AgentQuota.newBuilder().setAgent(agent).setState(QuotaState.QUOTA_STATE_OK).addWindows(window).build()
            val g = quotaGroups(machine(true, q), now, zone).single { it.agent == agent }
            assertEquals("2 小时", g.rows[0].label)
            assertEquals("已用 99%", g.rows[0].used)
            assertEquals(QuotaTone.WARN, g.rows[0].tone)
            assertEquals("2 小时额度即将用尽", quotaBanner(g))
            // A window the account does not have is not shown as missing.
            assertEquals(1, g.rows.size)
        }
        assertEquals("Pro Lite", planName("prolite"))
        assertEquals("Team Plus", planName("Team Plus"))
    }

}
