// Account quotas on a machine (architecture 账户额度): each CLI's login
// account apart, what is not known said as such and never as 0%, and old
// numbers marked as the last record.
import Foundation
import RAProto

public enum QuotaTone: Sendable { case normal, warn, full, stale }

/// One window as shown: 5 小时 · 已用 62% · 4 小时 29 分后重置 · 今天 14:10, or why it cannot be trusted.
public struct QuotaRow: Equatable, Sendable {
    public var label: String
    public var used: String
    public var percent: Double?
    public var reset: String
    public var tone: QuotaTone
    public var note: String

    public init(label: String, used: String, percent: Double?, reset: String, tone: QuotaTone, note: String) {
        self.label = label
        self.used = used
        self.percent = percent
        self.reset = reset
        self.tone = tone
        self.note = note
    }
}

/// What a CLI's quota group shows instead of, or besides, its windows.
public enum QuotaStatus: Sendable { case ok, waiting, failed, unsupported }

public struct QuotaGroup: Equatable, Sendable {
    public var agent: PBAgent
    /// Codex · Pro Lite; with several accounts, which one.
    public var title: String
    public var status: QuotaStatus
    public var rows: [QuotaRow]
    /// 最近收到 9:40, or empty.
    public var received: String
    /// The daemon's words for a failure or no support.
    public var detail: String
    /// The numbers are a last record: the machine is offline, or reading failed.
    public var lastRecord: Bool

    public init(agent: PBAgent, title: String, status: QuotaStatus, rows: [QuotaRow], received: String, detail: String, lastRecord: Bool) {
        self.agent = agent
        self.title = title
        self.status = status
        self.rows = rows
        self.received = received
        self.detail = detail
        self.lastRecord = lastRecord
    }
}

public func windowLabel(minutes: Int64) -> String {
    if minutes >= 24 * 60 && minutes % (24 * 60) == 0 { return "\(minutes / (24 * 60)) 天" }
    // Math.round: halves go up.
    if minutes >= 60 { return "\(jvmRound(Double(minutes) / 60.0)) 小时" }
    return "\(minutes) 分钟"
}

private func nameLabel(_ w: PBQuotaWindow) -> String {
    if w.windowMinutes > 0 { return windowLabel(minutes: w.windowMinutes) }
    switch w.name {
    case "five_hour", "primary": return "5 小时"
    case "seven_day", "secondary": return "7 天"
    default: return w.name
    }
}

/// The clock quota times are written in, wherever the client is.
private let beijing = TimeZone(identifier: "Asia/Shanghai") ?? TimeZone(secondsFromGMT: 8 * 3600)!

/// When a reading arrived, in Beijing like the reset times beside it.
private func clockText(_ ms: Int64, now: Int64) -> String {
    let t = LocalDay(ms: ms, zone: beijing)
    let today = LocalDay(ms: now, zone: beijing)
    switch t.epochDay - today.epochDay {
    case 0: return t.hhmm
    case -1: return "昨天 \(t.hhmm)"
    case 1...: return "\(weekdayNames[t.weekdayIndex]) \(t.hhmm)"
    default: return "\(t.month)月\(t.day)日 \(t.hhmm)"
    }
}

/// 3 小时 39 分后重置 · 今天 13:20: how long is left, rounded down, then the
/// moment. The day, weekday and clock are Beijing's wherever the client is;
/// the page says so once. Same text as the web.
public func resetText(resetMs: Int64, now: Int64) -> String {
    let minutes = (resetMs - now) / 60_000
    let left: String
    if minutes >= 24 * 60 {
        left = "\(minutes / (24 * 60)) 天 \(minutes % (24 * 60) / 60) 小时后重置"
    } else if minutes >= 60 {
        left = "\(minutes / 60) 小时 \(minutes % 60) 分后重置"
    } else if minutes >= 1 {
        left = "\(minutes) 分钟后重置"
    } else {
        left = "即将重置"
    }
    let t = LocalDay(ms: resetMs, zone: beijing)
    let day = t.sameDay(LocalDay(ms: now, zone: beijing)) ? "今天" : "\(t.month)月\(t.day)日 \(weekdayNames[t.weekdayIndex])"
    return "\(left) · \(day) \(t.hhmm)"
}

/// One window: used share, reset time, and whether the reading can still be trusted.
public func quotaRow(_ w: PBQuotaWindow, now: Int64, lastRecord: Bool) -> QuotaRow {
    let label = nameLabel(w)
    if !w.hasUsedPercent {
        return QuotaRow(label: label, used: "未获取", percent: nil, reset: "", tone: .normal, note: "没有收到这项额度数据")
    }
    let pct = w.usedPercent
    let used = pct >= 100 ? "已用尽" : "已用 \(jvmInt(pct.rounded(.down)))%"
    let passed = w.resetsAtMs >= 1 && w.resetsAtMs <= now
    let reset = w.resetsAtMs > 0 && !passed ? resetText(resetMs: w.resetsAtMs, now: now) : ""
    if passed {
        return QuotaRow(label: label, used: "等待更新", percent: pct, reset: "", tone: .stale, note: "上次\(used)；重置时间已过，不能据此确认额度已经恢复")
    }
    if lastRecord { return QuotaRow(label: label, used: used, percent: pct, reset: reset, tone: .stale, note: "") }
    return QuotaRow(label: label, used: used, percent: pct, reset: reset, tone: pct >= 100 ? .full : pct >= 80 ? .warn : .normal, note: "")
}

/// Each installed CLI's quota on `m`, one group per account the daemon could
/// tell apart. A CLI without an entry is not supported there.
public func quotaGroups(_ m: MachineView, now: Int64) -> [QuotaGroup] {
    var out = [QuotaGroup]()
    for agent in machineAgents(m) {
        let entries = (m.meta?.quotas ?? []).filter { effectiveAgent($0.agent) == agent }
        if entries.isEmpty {
            out.append(QuotaGroup(agent: agent, title: agentName(agent), status: .unsupported, rows: [], received: "", detail: "", lastRecord: false))
            continue
        }
        for q in entries { out.append(group(agent: agent, q, several: entries.count > 1, online: m.online, now: now)) }
    }
    return out
}

private let claudePrefix = Rx("\\Aclaude_", ignoreCase: true)
private let planWordBreak = Rx("[_\(Jre.s)]+")

/// A plan as people call it: Pro Lite for prolite, Max for claude_max.
public func planName(_ plan: String) -> String {
    switch plan.lowercased() {
    case "prolite": return "Pro Lite"
    case "claude_max": return "Max"
    case "claude_pro": return "Pro"
    case "claude_team": return "Team"
    case "claude_enterprise": return "Enterprise"
    default:
        return planWordBreak.split(claudePrefix.replace(plan, with: "")).filter { !$0.isEmpty }.map(capitalized).joined(separator: " ")
    }
}

/// Kotlin's replaceFirstChar(Char::uppercase): the first UTF-16 unit in upper case, an astral character left as it is.
private func capitalized(_ word: String) -> String {
    guard let first = word.unicodeScalars.first, first.value <= 0xFFFF else { return word }
    return first.properties.uppercaseMapping + String(word.unicodeScalars.dropFirst())
}

/// The windows the account has, the CLI's usual ones first. One it does not
/// report is left out: a plan with only a weekly window is not shown a second
/// 7 天 that never arrives.
private func completeWindows(agent: PBAgent, _ windows: [PBQuotaWindow]) -> [PBQuotaWindow] {
    let expected = agent == .claude ? ["five_hour", "seven_day"] : ["primary", "secondary"]
    var byName = [String: PBQuotaWindow]()
    for w in windows { byName[w.name] = w }
    return expected.compactMap { byName[$0] } + windows.filter { !expected.contains($0.name) }
}

private func group(agent: PBAgent, _ q: PBAgentQuota, several: Bool, online: Bool, now: Int64) -> QuotaGroup {
    let status: QuotaStatus
    switch q.state {
    case .ok: status = .ok
    case .failed: status = .failed
    case .unsupported: status = .unsupported
    default: status = .waiting
    }
    let lastRecord = !online || status == .failed
    let which: String
    if !several {
        which = ""
    } else if !q.account.isEmpty {
        which = " · 账户 \(q.account)"
    } else if !q.sourceSessionID.isEmpty {
        which = " · 会话 \(q.sourceSessionID.jvmTakeLast(4))"
    } else {
        which = ""
    }
    let title = agentName(agent) + (q.plan.isEmpty ? "" : " · \(planName(q.plan))") + which
    let rows = status == .unsupported || status == .waiting || q.windows.isEmpty
        ? [] : completeWindows(agent: agent, q.windows).map { quotaRow($0, now: now, lastRecord: lastRecord) }
    let received = q.observedMs > 0 ? (status == .failed ? "最近成功收到 " : "最近收到 ") + clockText(q.observedMs, now: now) : ""
    return QuotaGroup(agent: agent, title: title, status: status, rows: rows, received: received, detail: q.detail, lastRecord: lastRecord && !rows.isEmpty)
}

/// The warning above a group: a window used up, or close to it; nothing switches by itself.
public func quotaBanner(_ g: QuotaGroup) -> String {
    let got = g.rows.filter { $0.percent != nil && $0.tone != .stale }
    if let r = got.first(where: { $0.tone == .full }) { return "\(r.label)额度已用尽" }
    if let r = got.first(where: { $0.tone == .warn }) { return "\(r.label)额度即将用尽" }
    return ""
}

/// The device page's line for a CLI: its windows in short, or its state.
public func quotaLine(_ g: QuotaGroup) -> String {
    switch g.status {
    case .unsupported: return "暂不支持读取额度"
    case .waiting: return "暂未获取额度"
    default:
        return g.rows.filter { $0.used != "未获取" }.map { "\($0.label) \($0.used)" }.joined(separator: " · ").ifEmpty("暂未获取额度")
            + (g.lastRecord ? "（上次记录）" : "")
    }
}
