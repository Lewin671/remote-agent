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

/** One window as shown: 5 小时 · 已用 62% · 14:10 重置, or why it cannot be trusted. */
data class QuotaRow(val label: String, val used: String, val percent: Double?, val reset: String, val tone: QuotaTone, val note: String)

/** What a CLI's quota group shows instead of, or besides, its windows. */
enum class QuotaStatus { OK, WAITING, FAILED, UNSUPPORTED }

data class QuotaGroup(
    val agent: Agent,
    /** Codex · Pro Lite; with several accounts, which one. */
    val title: String,
    val status: QuotaStatus,
    val rows: List<QuotaRow>,
    /** 最近收到 9:40, or empty. */
    val received: String,
    /** The daemon's words for a failure or no support. */
    val detail: String,
    /** The numbers are a last record: the machine is offline, or reading failed. */
    val lastRecord: Boolean,
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

private fun clockText(ms: Long, now: Long, zone: ZoneId): String {
    val t = Instant.ofEpochMilli(ms).atZone(zone)
    val today = Instant.ofEpochMilli(now).atZone(zone).toLocalDate()
    val hm = "${t.hour}:${"%02d".format(t.minute)}"
    return when (t.toLocalDate()) {
        today -> hm
        today.minusDays(1) -> "昨天 $hm"
        else -> if (t.toLocalDate().isAfter(today)) "${weekdays[t.dayOfWeek.value - 1]} $hm" else "${t.monthValue}月${t.dayOfMonth}日 $hm"
    }
}

/** One window: used share, reset time, and whether the reading can still be trusted. */
fun quotaRow(w: QuotaWindow, now: Long, lastRecord: Boolean, zone: ZoneId = ZoneId.systemDefault()): QuotaRow {
    val label = nameLabel(w)
    if (!w.hasUsedPercent()) return QuotaRow(label, "未获取", null, "", QuotaTone.NORMAL, "没有收到这项额度数据")
    val pct = w.usedPercent
    val used = if (pct >= 100) "已用尽" else "已用 ${kotlin.math.floor(pct).toInt()}%"
    val passed = w.resetsAtMs in 1..now
    val reset = if (w.resetsAtMs > 0 && !passed) "${clockText(w.resetsAtMs, now, zone)} 重置" else ""
    if (passed) return QuotaRow(label, "等待更新", pct, "", QuotaTone.STALE, "上次$used；重置时间已过，不能据此确认额度已经恢复")
    if (lastRecord) return QuotaRow(label, used, pct, reset, QuotaTone.STALE, "")
    return QuotaRow(label, used, pct, reset, if (pct >= 100) QuotaTone.FULL else if (pct >= 80) QuotaTone.WARN else QuotaTone.NORMAL, "")
}

/**
 * Each installed CLI's quota on [m], one group per account the daemon could
 * tell apart. A daemon from before quotas reports Codex's windows in the
 * machine metadata only; a CLI without an entry is not supported there.
 */
fun quotaGroups(m: MachineView, now: Long, zone: ZoneId = ZoneId.systemDefault()): List<QuotaGroup> {
    val meta = m.meta
    val out = mutableListOf<QuotaGroup>()
    for (agent in machineAgents(m)) {
        val entries = meta?.quotasList.orEmpty().filter { effectiveAgent(it.agent) == agent }
        if (entries.isEmpty()) {
            if (agent == Agent.AGENT_CODEX && meta != null && meta.quotasCount == 0) {
                // An older daemon: Codex's windows in the machine metadata.
                val ws = listOfNotNull(
                    meta.primaryLimit.takeIf { meta.hasPrimaryLimit() }?.let { QuotaWindow.newBuilder().setName("primary").setUsedPercent(it.usedPercent).setResetsAtMs(it.resetsAt * 1000).setWindowMinutes(it.windowMinutes).build() },
                    meta.secondaryLimit.takeIf { meta.hasSecondaryLimit() }?.let { QuotaWindow.newBuilder().setName("secondary").setUsedPercent(it.usedPercent).setResetsAtMs(it.resetsAt * 1000).setWindowMinutes(it.windowMinutes).build() },
                )
                val title = agentName(agent) + meta.planType.takeIf { it.isNotEmpty() }?.let { " · ${planName(it)}" }.orEmpty()
                out += QuotaGroup(agent, title, if (ws.isEmpty()) QuotaStatus.WAITING else QuotaStatus.OK, if (ws.isEmpty()) emptyList() else completeWindows(agent, ws).map { quotaRow(it, now, !m.online, zone) }, "", "", !m.online && ws.isNotEmpty())
            } else {
                out += QuotaGroup(agent, agentName(agent), QuotaStatus.UNSUPPORTED, emptyList(), "", "", false)
            }
            continue
        }
        for (q in entries) out += group(agent, q, entries.size > 1, m.online, now, zone)
    }
    return out
}

/** A plan as people call it: Pro Lite for prolite, Max for claude_max. */
fun planName(plan: String): String = when (plan.lowercase()) {
    "prolite" -> "Pro Lite"
    "claude_max" -> "Max"
    "claude_pro" -> "Pro"
    "claude_team" -> "Team"
    "claude_enterprise" -> "Enterprise"
    else -> plan.replace(Regex("^claude_", RegexOption.IGNORE_CASE), "").split(Regex("[_\\s]+")).filter { it.isNotEmpty() }.joinToString(" ") { it.replaceFirstChar(Char::uppercase) }
}

/**
 * The windows the account has, the CLI's usual ones first. One it does not
 * report is left out: a plan with only a weekly window is not shown a second
 * 7 天 that never arrives.
 */
private fun completeWindows(agent: Agent, windows: List<QuotaWindow>): List<QuotaWindow> {
    val expected = if (agent == Agent.AGENT_CLAUDE) listOf("five_hour", "seven_day") else listOf("primary", "secondary")
    val byName = windows.associateBy { it.name }
    return expected.mapNotNull { byName[it] } + windows.filter { it.name !in expected }
}

private fun group(agent: Agent, q: AgentQuota, several: Boolean, online: Boolean, now: Long, zone: ZoneId): QuotaGroup {
    val status = when (q.state) {
        QuotaState.QUOTA_STATE_OK -> QuotaStatus.OK
        QuotaState.QUOTA_STATE_FAILED -> QuotaStatus.FAILED
        QuotaState.QUOTA_STATE_UNSUPPORTED -> QuotaStatus.UNSUPPORTED
        else -> QuotaStatus.WAITING
    }
    val lastRecord = !online || status == QuotaStatus.FAILED
    val which = when {
        !several -> ""
        q.account.isNotEmpty() -> " · 账户 ${q.account}"
        q.sourceSessionId.isNotEmpty() -> " · 会话 ${q.sourceSessionId.takeLast(4)}"
        else -> ""
    }
    val title = agentName(agent) + (if (q.plan.isNotEmpty()) " · ${planName(q.plan)}" else "") + which
    val rows = if (status == QuotaStatus.UNSUPPORTED || status == QuotaStatus.WAITING || q.windowsCount == 0) emptyList() else completeWindows(agent, q.windowsList).map { quotaRow(it, now, lastRecord, zone) }
    val received = if (q.observedMs > 0) (if (status == QuotaStatus.FAILED) "最近成功收到 " else "最近收到 ") + clockText(q.observedMs, now, zone) else ""
    return QuotaGroup(agent, title, status, rows, received, q.detail, lastRecord && rows.isNotEmpty())
}

/** The warning above a group: a window used up, or close to it; nothing switches by itself. */
fun quotaBanner(g: QuotaGroup): String {
    val got = g.rows.filter { it.percent != null && it.tone != QuotaTone.STALE }
    got.firstOrNull { it.tone == QuotaTone.FULL }?.let { return "${it.label}额度已用尽" }
    got.firstOrNull { it.tone == QuotaTone.WARN }?.let { return "${it.label}额度即将用尽" }
    return ""
}

/** The device page's line for a CLI: its windows in short, or its state. */
fun quotaLine(g: QuotaGroup): String = when (g.status) {
    QuotaStatus.UNSUPPORTED -> "暂不支持读取额度"
    QuotaStatus.WAITING -> "暂未获取额度"
    else -> g.rows.filter { it.used != "未获取" }.joinToString(" · ") { "${it.label} ${it.used}" }.ifEmpty { "暂未获取额度" } + if (g.lastRecord) "（上次记录）" else ""
}
