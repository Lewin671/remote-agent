// Account quotas on a machine (architecture 账户额度): each CLI's login
// account apart, what is not known said as such and never as 0%, and old
// numbers marked as the last record.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentQuota
import io.github.lewin671.remoteagent.proto.QuotaState
import io.github.lewin671.remoteagent.proto.QuotaWindow
import java.time.Instant
import java.time.ZoneId

enum class QuotaTone { NORMAL, WARN, FULL, STALE }

/** One window as shown: 5 小时 · 剩 38% · 4 小时 29 分后重置 · 今天 14:10, or why it cannot be trusted. */
data class QuotaRow(
    val label: String,
    /** What is left: 剩 38%, 已用尽, 上次剩 38%, 等待更新, 未获取. */
    val left: String,
    val percent: Double?,
    val reset: String,
    val tone: QuotaTone,
    val note: String,
    val windowName: String = label,
)

/** What a CLI's quota group shows instead of, or besides, its windows. */
enum class QuotaStatus { OK, WAITING, FAILED, UNSUPPORTED }

data class QuotaSection(
    val title: String,
    val rows: List<QuotaRow>,
)

data class QuotaGroup(
    val agent: Agent,
    /** Codex · Pro Lite; with several accounts, which one. */
    val title: String,
    val status: QuotaStatus,
    val rows: List<QuotaRow>,
    /** 最近收到 9:40, or null. */
    val received: UiMessage?,
    /** The daemon's words for a failure or no support. */
    val detail: String,
    /** Offline, failed, or too old to treat as current. */
    val lastRecord: Boolean,
    val sections: List<QuotaSection> = emptyList(),
    val freshness: UiMessage? = null,
)

private val weekdays = listOf("周一", "周二", "周三", "周四", "周五", "周六", "周日")

fun windowLabel(minutes: Long): String = when {
    minutes >= 24 * 60 && minutes % (24 * 60) == 0L -> "${minutes / (24 * 60)} 天"
    minutes >= 60 -> "${Math.round(minutes / 60.0)} 小时"
    else -> "$minutes 分钟"
}

private fun nameLabel(w: QuotaWindow): String = when {
    w.windowMinutes > 0 -> windowLabel(w.windowMinutes)
    w.name == "five_hour" || w.name == "primary" -> "5 小时"
    w.name == "seven_day" || w.name == "secondary" -> "7 天"
    else -> w.name
}

private val beijing: ZoneId = ZoneId.of("Asia/Shanghai")

/** When a reading arrived, in Beijing like the reset times beside it. */
private fun received(ms: Long, now: Long, failed: Boolean): UiMessage? {
    if (ms <= 0) return null
    val t = Instant.ofEpochMilli(ms).atZone(beijing)
    val hm = "${t.hour}:${"%02d".format(t.minute)}"
    return if (t.toLocalDate() == Instant.ofEpochMilli(now).atZone(beijing).toLocalDate()) {
        if (failed) UiMessages.quotaReceivedSuccess(hm) else UiMessages.quotaReceived(hm)
    } else {
        if (failed) UiMessages.quotaReceivedSuccessDate(t.dayOfMonth, t.monthValue, hm) else UiMessages.quotaReceivedDate(t.dayOfMonth, t.monthValue, hm)
    }
}

fun quotaFreshness(ms: Long, now: Long): UiMessage? {
    if (ms <= 0) return UiMessages.quotaUnknownTime()
    val minutes = ((now - ms).coerceAtLeast(0) / 60_000).coerceAtMost(Int.MAX_VALUE.toLong()).toInt()
    return when {
        minutes < 30 -> null
        minutes < 60 -> UiMessages.quotaOldMinutes(minutes)
        else -> UiMessages.quotaOldHours(minutes / 60)
    }
}

/**
 * 3 小时 39 分后重置 · 今天 13:20: how long is left, rounded down, then the
 * moment. The day, weekday and clock are Beijing's wherever the client is;
 * the page says so once. Same text as the web.
 */
fun resetText(resetMs: Long, now: Long): String {
    val minutes = (resetMs - now) / 60_000
    val left = when {
        minutes >= 24 * 60 -> "${minutes / (24 * 60)} 天 ${minutes % (24 * 60) / 60} 小时后重置"
        minutes >= 60 -> "${minutes / 60} 小时 ${minutes % 60} 分后重置"
        minutes >= 1 -> "$minutes 分钟后重置"
        else -> "即将重置"
    }
    val t = Instant.ofEpochMilli(resetMs).atZone(beijing)
    val day = if (t.toLocalDate() == Instant.ofEpochMilli(now).atZone(beijing).toLocalDate()) "今天" else "${t.monthValue}月${t.dayOfMonth}日 ${weekdays[t.dayOfWeek.value - 1]}"
    return "$left · $day ${t.hour}:${"%02d".format(t.minute)}"
}

/** One window: what is left, reset time, and whether the reading can still be trusted. */
fun quotaRow(
    w: QuotaWindow,
    now: Long,
    lastRecord: Boolean,
    fallbackLabel: String = nameLabel(w),
    useWindowMinutes: Boolean = true,
): QuotaRow {
    val windowName = if (w.windowMinutes > 0) windowLabel(w.windowMinutes) else fallbackLabel
    val label = if (useWindowMinutes && w.windowMinutes > 0) windowName else fallbackLabel
    if (!w.hasUsedPercent()) return QuotaRow(label, "未获取", null, "", QuotaTone.NORMAL, "没有收到这项额度数据", windowName)
    val pct = w.usedPercent
    val left = if (pct >= 100) "已用尽" else "剩 ${100 - kotlin.math.floor(pct).toInt()}%"
    val passed = w.resetsAtMs in 1..now
    val reset = if (w.resetsAtMs > 0 && !passed) resetText(w.resetsAtMs, now) else ""
    if (passed) return QuotaRow(label, "等待更新", pct, "", QuotaTone.STALE, "上次$left；重置时间已过，不能据此确认额度已经恢复", windowName)
    if (lastRecord) return QuotaRow(label, "上次$left", pct, reset, QuotaTone.STALE, "", windowName)
    return QuotaRow(label, left, pct, reset, if (pct >= 100) QuotaTone.FULL else if (pct >= 80) QuotaTone.WARN else QuotaTone.NORMAL, "", windowName)
}

/**
 * Each installed CLI's quota on [m], one group per account the daemon could
 * tell apart. A CLI without an entry is not supported there.
 */
fun quotaGroups(m: MachineView, now: Long): List<QuotaGroup> {
    val meta = m.meta
    val out = mutableListOf<QuotaGroup>()
    for (agent in machineAgents(m)) {
        val entries = meta?.quotasList.orEmpty().filter { agentInfo(it.agent).agent == agentInfo(agent).agent }
        if (entries.isEmpty()) {
            out += QuotaGroup(agent, agentName(agent), QuotaStatus.UNSUPPORTED, emptyList(), null, "", false)
            continue
        }
        for (q in entries) out += group(agent, q, entries.size > 1, m.online, now)
    }
    return out
}

/** A plan as people call it: Pro Lite for prolite, Max for claude_max. */
fun planName(plan: String): String = normalizePlanName(plan)

/**
 * The windows the account has, the CLI's usual ones first. One it does not
 * report is left out: a plan with only a weekly window is not shown a second
 * 7 天 that never arrives.
 */
private fun completeWindows(agent: Agent, windows: List<QuotaWindow>): List<QuotaWindow> {
    val expected = agentInfo(agent).quotaWindows
    val byName = windows.associateBy { it.name }
    return expected.mapNotNull { byName[it] } + windows.filter { it.name !in expected }
}

private fun group(agent: Agent, q: AgentQuota, several: Boolean, online: Boolean, now: Long): QuotaGroup {
    val status = when (q.state) {
        QuotaState.QUOTA_STATE_OK -> QuotaStatus.OK
        QuotaState.QUOTA_STATE_FAILED -> QuotaStatus.FAILED
        QuotaState.QUOTA_STATE_UNSUPPORTED -> QuotaStatus.UNSUPPORTED
        else -> QuotaStatus.WAITING
    }
    val freshness = quotaFreshness(q.observedMs, now)
    val lastRecord = !online || status == QuotaStatus.FAILED || freshness != null
    val which = when {
        !several -> ""
        q.account.isNotEmpty() -> " · 账户 ${q.account}"
        q.sourceSessionId.isNotEmpty() -> " · 会话 ${q.sourceSessionId.takeLast(4)}"
        else -> ""
    }
    val prefix = agentName(agent) + (if (q.plan.isNotEmpty()) " · ${agentInfo(agent).planName(q.plan)}" else "") + which
    val staleSuffix = if (lastRecord) " · 上次记录" else ""
    val title = prefix + staleSuffix

    val rows = mutableListOf<QuotaRow>()
    val groupMap = LinkedHashMap<String, MutableList<QuotaRow>>()
    if (status != QuotaStatus.UNSUPPORTED && status != QuotaStatus.WAITING && q.windowsCount > 0) {
        val expected = agentInfo(agent).quotaWindows
        val ordered = completeWindows(agent, q.windowsList)
        for (w in ordered) {
            val desc = agentInfo(agent).quotaWindow(w.name)
            val useMinutes = desc.group == null && w.name in expected
            val row = quotaRow(w, now, lastRecord, desc.label, useMinutes)
            rows += row
            if (desc.group != null) {
                groupMap.getOrPut(desc.group) { mutableListOf() } += row
            }
        }
    }
    val sections = if (groupMap.isNotEmpty()) {
        groupMap.entries.mapIndexed { idx, (g, grows) ->
            QuotaSection(
                title = if (idx == 0) "$prefix · $g$staleSuffix" else g,
                rows = grows,
            )
        }
    } else if (rows.isNotEmpty()) {
        listOf(QuotaSection(title = title, rows = rows))
    } else {
        emptyList()
    }
    val received = received(q.observedMs, now, status == QuotaStatus.FAILED)
    return QuotaGroup(agent, title, status, rows, received, q.detail, lastRecord && rows.isNotEmpty(), sections, freshness)
}

/** The warning above a group: a window used up, or close to it; nothing switches by itself. */
fun quotaBanner(g: QuotaGroup): UiMessage? {
    val got = g.rows.filter { it.percent != null && it.tone != QuotaTone.STALE }
    got.firstOrNull { it.tone == QuotaTone.FULL }?.let { return UiMessages.quotaExhausted(agentName(g.agent), it.label) }
    got.firstOrNull { it.tone == QuotaTone.WARN }?.let { return UiMessages.quotaWarning(agentName(g.agent), it.label) }
    return null
}

/** The device page's line for a CLI: its most-used window, or its state. */
fun quotaLine(g: QuotaGroup): String = when (g.status) {
    QuotaStatus.UNSUPPORTED -> "暂不支持读取额度"
    QuotaStatus.WAITING -> "暂未获取额度"
    else -> {
        val got = g.rows.filter { it.percent != null }
        if (got.isEmpty()) {
            "暂未获取额度" + if (g.lastRecord) "（上次记录）" else ""
        } else {
            val tightest = got.maxByOrNull { it.percent ?: -1.0 } ?: got.first()
            val win = tightest.windowName.ifEmpty { tightest.label }
            "${tightest.left} · $win"
        }
    }
}
