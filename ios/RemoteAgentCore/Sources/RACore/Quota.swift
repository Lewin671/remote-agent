// Account quotas on a machine (architecture 账户额度): each CLI's login
// account apart, what is not known said as such and never as 0%, and old
// numbers marked as the last record.
import Foundation
import RAProto

public enum QuotaTone: Sendable { case normal, warn, full, stale }

/// One window as shown: 5 小时 · 剩 38% · 4 小时 29 分后重置 · 今天 14:10, or why it cannot be trusted.
public struct QuotaRow: Equatable, Sendable {
    public var label: String
    public var windowName: String
    /// What is left: 剩 38%, 已用尽, 上次剩 38%, 等待更新, 未获取 (design-system §6 「电脑卡与额度」).
    public var left: String
    public var percent: Double?
    public var reset: String
    public var tone: QuotaTone
    public var note: String

    public init(label: String, windowName: String = "", left: String, percent: Double?, reset: String, tone: QuotaTone, note: String) {
        self.label = label
        self.windowName = windowName.isEmpty ? label : windowName
        self.left = left
        self.percent = percent
        self.reset = reset
        self.tone = tone
        self.note = note
    }
}

/// What a CLI's quota group shows instead of, or besides, its windows.
public enum QuotaStatus: Sendable { case ok, waiting, failed, unsupported }

public struct QuotaSubgroup: Equatable, Sendable {
    public var title: String
    public var rows: [QuotaRow]

    public init(title: String, rows: [QuotaRow]) {
        self.title = title
        self.rows = rows
    }
}

public struct QuotaGroup: Equatable, Sendable {
    public var agent: PBAgent
    /// Codex · Pro Lite; with several accounts, which one.
    public var title: String
    public var status: QuotaStatus
    public var rows: [QuotaRow]
    public var subgroups: [QuotaSubgroup]
    /// 最近收到 9:40, or empty.
    public var received: String
    /// The daemon's words for a failure or no support.
    public var detail: String
    /// Why the observation cannot establish current availability.
    public var freshness: String = ""
    /// Offline, failed, or too old to treat as current.
    public var lastRecord: Bool

    public init(agent: PBAgent, title: String, status: QuotaStatus, rows: [QuotaRow], subgroups: [QuotaSubgroup] = [], received: String, detail: String, lastRecord: Bool) {
        self.agent = agent
        self.title = title
        self.status = status
        self.rows = rows
        self.subgroups = subgroups
        self.received = received
        self.detail = detail
        self.lastRecord = lastRecord
    }
}

public func windowLabel(minutes: Int64) -> String {
    if minutes >= 24 * 60 && minutes % (24 * 60) == 0 { return L10n.quotaDays(count: Int(minutes / (24 * 60))) }
    // Math.round: halves go up.
    if minutes >= 60 { return L10n.quotaHr(count: Int(jvmRound(Double(minutes) / 60.0))) }
    return L10n.quotaMin(count: Int(minutes))
}

private func nameLabel(_ w: PBQuotaWindow) -> String {
    if w.windowMinutes > 0 { return windowLabel(minutes: w.windowMinutes) }
    switch w.name {
    case "five_hour", "primary": return L10n.quotaHr(count: 5)
    case "seven_day", "secondary": return L10n.quotaDays(count: 7)
    default: return w.name
    }
}

/// The clock quota times are written in, wherever the client is.
private let beijing = TimeZone(identifier: "Asia/Shanghai") ?? TimeZone(secondsFromGMT: 8 * 3600)!

/// When a reading arrived, in Beijing like the reset times beside it.
private func received(_ ms: Int64, now: Int64, failed: Bool) -> String {
    guard ms > 0 else { return "" }
    let t = LocalDay(ms: ms, zone: beijing)
    if t.sameDay(LocalDay(ms: now, zone: beijing)) {
        return failed ? L10n.quotaReceivedSuccess(time: t.hhmm) : L10n.quotaReceived(time: t.hhmm)
    }
    return failed ? L10n.quotaReceivedSuccessDate(day: t.day, month: t.month, time: t.hhmm) : L10n.quotaReceivedDate(day: t.day, month: t.month, time: t.hhmm)
}

public func quotaFreshness(_ ms: Int64, now: Int64) -> String {
    guard ms > 0 else { return L10n.quotaUnknownTime() }
    let minutes = max(0, now - ms) / 60_000
    if minutes < 30 { return "" }
    return minutes < 60 ? L10n.quotaOldMinutes(count: Int(minutes)) : L10n.quotaOldHours(count: Int(minutes / 60))
}

/// 3 小时 39 分后重置 · 今天 13:20: how long is left, rounded down, then the
/// moment. The day, weekday and clock are Beijing's wherever the client is;
/// the page says so once. Same text as the web.
public func resetText(resetMs: Int64, now: Int64) -> String {
    let minutes = (resetMs - now) / 60_000
    let left: String
    if minutes >= 24 * 60 {
        left = L10n.quotaResetsInDaysHr(count: Int(minutes / (24 * 60)), detail: Int(minutes % (24 * 60) / 60))
    } else if minutes >= 60 {
        left = L10n.quotaResetsInHrMin(count: Int(minutes / 60), detail: String(describing: minutes % 60))
    } else if minutes >= 1 {
        left = L10n.quotaResetsInMin(count: Int(minutes))
    } else {
        left = L10n.quotaResettingSoon()
    }
    let t = LocalDay(ms: resetMs, zone: beijing)
    let day = t.sameDay(LocalDay(ms: now, zone: beijing)) ? L10n.timeToday() : L10n.timeIosDateTime(day: Int(t.day), month: Int(t.month), time: String(describing: weekdayNames[t.weekdayIndex]))
    return "\(left) · \(day) \(t.hhmm)"
}

/// What is left of a window used `pct` percent: 剩 38%, or 已用尽.
public func quotaLeft(_ pct: Double) -> String {
    pct >= 100 ? L10n.quotaExhaustedQuota() : L10n.quotaLeft(value: String(describing: 100 - jvmInt(pct.rounded(.down))))
}

/// A CLI's line on a computer's card in settings: its tightest window's
/// 剩 N% and how long until it resets, the share left for the thin bar, and
/// whether that is little enough to look at (20 % or less left); or, with
/// no numbers, what stands in their place and no bar.
public struct QuotaCardLine: Equatable, Sendable {
    public var text: String
    public var reset: String
    /// What is left, 0–1; nil draws no bar.
    public var left: Double?
    public var low: Bool
    public var stale: Bool
}

public func quotaCardLine(_ groups: [QuotaGroup]) -> QuotaCardLine {
    let rows = groups.compactMap(quotaSummaryRow)
    guard let r = rows.max(by: { ($0.percent ?? -1) < ($1.percent ?? -1) }), let pct = r.percent else {
        let word = groups.first.map(quotaLine) ?? L10n.quotaQuotaNotAvailableYet()
        return QuotaCardLine(text: word, reset: "", left: nil, low: false, stale: false)
    }
    let reset = r.reset.components(separatedBy: " · ").first ?? ""
    let left = max(0, min(100, 100 - pct)) / 100
    return QuotaCardLine(text: r.left, reset: reset, left: left, low: r.tone != .stale && left <= 0.2, stale: r.tone == .stale)
}

/// One window: what is left, reset time, and whether the reading can still be trusted.
public func quotaRow(_ w: PBQuotaWindow, now: Int64, lastRecord: Bool) -> QuotaRow {
    quotaRow(w, agent: .codex, now: now, lastRecord: lastRecord)
}

public func quotaRow(_ w: PBQuotaWindow, agent: PBAgent, now: Int64, lastRecord: Bool) -> QuotaRow {
    let desc = agentInfo(agent).quotaWindow(w.name)
    let known = (agentInfo(agent).expectedQuotaWindows ?? [])
    let useMinutes = desc.group == nil && known.contains(w.name)
    let label = (useMinutes && w.windowMinutes > 0) ? windowLabel(minutes: w.windowMinutes) : desc.label
    let windowName = w.windowMinutes > 0 ? windowLabel(minutes: w.windowMinutes) : label
    if !w.hasUsedPercent {
        return QuotaRow(label: label, windowName: windowName, left: L10n.quotaUnavailable(), percent: nil, reset: "", tone: .normal, note: L10n.quotaNoDataReceivedForThisQuota())
    }
    let pct = w.usedPercent
    let left = quotaLeft(pct)
    let passed = w.resetsAtMs >= 1 && w.resetsAtMs <= now
    let reset = w.resetsAtMs > 0 && !passed ? resetText(resetMs: w.resetsAtMs, now: now) : ""
    if passed {
        return QuotaRow(label: label, windowName: windowName, left: L10n.quotaAwaitingUpdate(), percent: pct, reset: "", tone: .stale, note: L10n.quotaPreviouslyResetTimePassedButQuotaRecoveryIsUnconfirmed(value: String(describing: left)))
    }
    if lastRecord { return QuotaRow(label: label, windowName: windowName, left: L10n.quotaPreviously(value: String(describing: left)), percent: pct, reset: reset, tone: .stale, note: "") }
    return QuotaRow(label: label, windowName: windowName, left: left, percent: pct, reset: reset, tone: pct >= 100 ? .full : pct >= 80 ? .warn : .normal, note: "")
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

/// A plan as people call it: Pro Lite for prolite, Max for claude_max.
public func planName(_ plan: String) -> String {
    normalizePlanName(plan)
}

/// The windows the account has, the CLI's usual ones first. One it does not
/// report is left out: a plan with only a weekly window is not shown a second
/// 7 天 that never arrives.
private func completeWindows(agent: PBAgent, _ windows: [PBQuotaWindow]) -> [PBQuotaWindow] {
    let expected = (agentInfo(agent).expectedQuotaWindows ?? [])
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
    let freshness = quotaFreshness(q.observedMs, now: now)
    let lastRecord = !online || status == .failed || !freshness.isEmpty
    let which: String
    if !several {
        which = ""
    } else if !q.account.isEmpty {
        which = L10n.quotaAccount(value: String(describing: q.account))
    } else if !q.sourceSessionID.isEmpty {
        which = L10n.quotaSession(value: String(describing: q.sourceSessionID.jvmTakeLast(4)))
    } else {
        which = ""
    }
    let title = agentName(agent) + (q.plan.isEmpty ? "" : " · \(agentInfo(agent).planName(q.plan))") + which

    var rows = [QuotaRow]()
    var groupOrder = [QuotaWindowGroup]()
    var groupRows = [QuotaWindowGroup: [QuotaRow]]()

    if status != .unsupported && status != .waiting && !q.windows.isEmpty {
        let ordered = completeWindows(agent: agent, q.windows)
        for w in ordered {
            let row = quotaRow(w, agent: agent, now: now, lastRecord: lastRecord)
            rows.append(row)
            let desc = agentInfo(agent).quotaWindow(w.name)
            if let g = desc.group {
                if groupRows[g] == nil {
                    groupOrder.append(g)
                    groupRows[g] = []
                }
                groupRows[g]?.append(row)
            }
        }
    }

    let subgroups: [QuotaSubgroup]
    if !groupOrder.isEmpty {
        subgroups = groupOrder.enumerated().map { idx, g in
            let subgroupTitle = idx == 0 ? "\(title) · \(g.title)" : g.title
            return QuotaSubgroup(title: subgroupTitle, rows: groupRows[g] ?? [])
        }
    } else {
        subgroups = []
    }

    let received = received(q.observedMs, now: now, failed: status == .failed)
    var result = QuotaGroup(agent: agent, title: title, status: status, rows: rows, subgroups: subgroups, received: received, detail: q.detail, lastRecord: lastRecord && !rows.isEmpty)
    result.freshness = freshness
    return result
}

/// The warning above a group: a window used up, or close to it; nothing switches by itself.
public func quotaBanner(_ g: QuotaGroup) -> String {
    let got = g.rows.filter { $0.percent != nil && $0.tone != .stale }
    if let r = got.first(where: { $0.tone == .full }) { return L10n.quotaExhausted(name: agentName(g.agent), window: r.label) }
    if let r = got.first(where: { $0.tone == .warn }) { return L10n.quotaWarning(name: agentName(g.agent), window: r.label) }
    return ""
}

/// The device page's line for a CLI: its most-used window, or its state.
public func quotaLine(_ g: QuotaGroup) -> String {
    switch g.status {
    case .unsupported: return L10n.quotaQuotaLookupIsNotSupportedYet()
    case .waiting: return L10n.quotaQuotaNotAvailableYet()
    default:
        guard let tightest = quotaSummaryRow(g) else {
            return g.lastRecord ? L10n.quotaUnavailablePrevious() : L10n.quotaQuotaNotAvailableYet()
        }
        let win = tightest.windowName.ifEmpty(tightest.label)
        return "\(tightest.left) · \(win)"
    }
}

/// The summary uses the first most-used reported window, preserving daemon order on ties.
public func quotaSummaryRow(_ g: QuotaGroup) -> QuotaRow? {
    guard g.status == .ok || g.status == .failed else { return nil }
    return g.rows.filter { $0.percent != nil }.reduce(nil) { best, row in
        guard let best else { return row }
        return (row.percent ?? -1) > (best.percent ?? -1) ? row : best
    }
}
