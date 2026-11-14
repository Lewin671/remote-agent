import Foundation
import RAProto

// What the computer-status screens draw, as plain values: the three rings, the two small cards, the ranking
// rows, the chart's geometry and the home reminder. Nothing here knows SwiftUI. Words come only from the
// `machine_status` messages, numbers and units in the language's own format. State is read from the proto and its
// enums, never from translated text. Android's MachineStatusModel.kt is the reference for every rule.

/// How a figure reads: not at all, orange for 注意, red for 紧张 (ui-spec A5).
public enum MachineTone: Sendable, Equatable { case normal, warn, critical }

public func machineTone(_ pressure: PBResourcePressure) -> MachineTone {
    switch pressure {
    case .warning: .warn
    case .critical: .critical
    default: .normal
    }
}

public enum StatusMetric: Sendable, Equatable, Hashable { case cpu, memory }

public let gibibyte = 1_073_741_824.0

/// Sizes for display: disk in decimal bytes, memory in binary ones, both written GB / MB (ui-rulings 1).
public struct StatusText: Sendable {
    public let locale: String
    public init(locale: String = MessageLocale.current) { self.locale = locale }

    private func formatter(_ decimals: Int) -> NumberFormatter {
        let f = NumberFormatter()
        f.locale = Locale(identifier: locale)
        f.minimumFractionDigits = decimals
        f.maximumFractionDigits = decimals
        f.usesGroupingSeparator = false
        return f
    }

    /// The percent sign as the language writes it.
    public var percentSymbol: String { formatter(0).percentSymbol ?? "%" }

    public func number(_ v: Double, decimals: Int) -> String { formatter(decimals).string(from: NSNumber(value: v)) ?? String(v) }
    public func percent(_ v: Double) -> String { "\(Int(min(max(v, 0), 100_000).rounded()))\(percentSymbol)" }

    /// The number alone of a size in GB: one decimal under 10, none from 10 (the rings' and cards' style).
    /// `binary`: memory is counted in 1024s and still called GB, as the system's own monitor does; disk is decimal, as the Finder.
    public func gbNumber(_ bytes: Int64, oneDecimal: Bool = false, binary: Bool = false) -> String {
        let gb = Double(bytes) / (binary ? gibibyte : 1e9)
        return number(gb, decimals: oneDecimal || gb < 10 ? 1 : 0)
    }

    /// "42 GB", "1.2 GB", "380 MB".
    public func size(_ bytes: Int64, binary: Bool = false) -> String {
        if Double(bytes) >= (binary ? gibibyte : 1e9) {
            return L10n.machineStatusSizeGb(size: gbNumber(bytes, binary: binary), locale: locale)
        }
        let mb = (max(1.0, Double(bytes) / (binary ? 1_048_576.0 : 1e6))).rounded()
        return L10n.machineStatusSizeMb(size: number(mb, decimals: 0), locale: locale)
    }

    public func load(_ v: Double) -> String { number(v, decimals: 1) }

    public func duration(seconds: Int64) -> String {
        let minutes = seconds / 60, hours = minutes / 60, days = hours / 24
        if days > 0 { return L10n.machineStatusDurationDaysHours(days: Int(days), hours: Int(hours % 24), locale: locale) }
        if hours > 0 { return L10n.timeDurationHoursMinutes(hours: Int(hours), minutes: Int(minutes % 60), locale: locale) }
        return L10n.machineStatusDurationMinutes(count: Int(max(1, minutes)), locale: locale)
    }

    /// 「{N 秒/分钟}前的数据」: the age of what is kept.
    public func stale(ageMs: Int64) -> String {
        let s = max(0, ageMs / 1000)
        if s < 60 { return L10n.machineStatusStaleSeconds(count: Int(max(1, s)), locale: locale) }
        return L10n.machineStatusStaleMinutes(count: Int(s / 60), locale: locale)
    }

    /// 「{N} 秒前」 while looking back: N rounded to 2 seconds.
    public func lookback(ageMs: Int64) -> String {
        L10n.machineStatusSecondsAgo(count: Int((Double(max(0, ageMs)) / 2000).rounded()) * 2, locale: locale)
    }

    public func cores(_ n: Int) -> String { L10n.machineStatusCores(count: n, locale: locale) }
}

// MARK: The three rings (A2)

public enum RingKind: Sendable, Equatable { case cpu, memory, disk }

/// One ring: its progress (nil draws only the track), the big figure, the name, how it reads.
public struct RingSpec: Equatable, Sendable {
    public var kind: RingKind
    public var fraction: Double?
    public var value: String
    public var name: String
    public var tone: MachineTone
    public var spoken: String
}

public let noValue = "—"

/// The rings of a computer. A status of nil, or a computer that is not `online`, draws tracks and a dash.
/// The disk ring draws the share used and says the amount free.
public func machineRings(status: PBMachineStatus?, online: Bool, text: StatusText) -> [RingSpec] {
    let have = online ? status : nil
    func ring(_ kind: RingKind, _ fraction: Double?, _ value: String?, _ name: String, _ tone: MachineTone) -> RingSpec {
        let v = value ?? noValue
        return RingSpec(kind: kind, fraction: fraction.map { min(max($0, 0), 1) }, value: v, name: name, tone: tone,
                        spoken: L10n.machineStatusA11YRing(name: name, value: v, locale: text.locale))
    }
    let cpuName = L10n.machineStatusCpu(locale: text.locale)
    let memName = L10n.machineStatusMemory(locale: text.locale)
    let diskName = L10n.machineStatusDiskAvailable(locale: text.locale)
    guard let have else {
        return [ring(.cpu, nil, nil, cpuName, .normal), ring(.memory, nil, nil, memName, .normal), ring(.disk, nil, nil, diskName, .normal)]
    }
    let memFraction = have.memoryTotalBytes > 0 ? Double(have.memoryUsedBytes) / Double(have.memoryTotalBytes) : 0
    let d = have.disk
    let diskFraction = d.totalBytes > 0 ? 1 - Double(d.availableBytes) / Double(d.totalBytes) : 0
    return [
        ring(.cpu, have.cpuPercent / 100, text.percent(have.cpuPercent), cpuName, .normal),
        ring(.memory, memFraction, text.percent(memFraction * 100), memName, machineTone(have.memoryPressure)),
        ring(.disk, diskFraction, text.size(Int64(d.availableBytes)), diskName, machineTone(d.pressure)),
    ]
}

// MARK: The two small cards and the main figure (A3)

/// The memory card: its figure, the pressure scale (nil when the computer does not say) and its two lines.
public struct MemoryCard: Equatable, Sendable {
    public var used: String
    public var usedNumber: Double
    public var pressureWord: String?
    public var position: Double?
    public var tone: MachineTone
    public var total: String
    public var swap: String?
    public var detail: String
}

public func memoryCard(_ s: PBMachineStatus, text: StatusText) -> MemoryCard {
    let word: String? = switch s.memoryPressure {
    case .normal: L10n.machineStatusPressureNormal(locale: text.locale)
    case .warning: L10n.machineStatusPressureHigh(locale: text.locale)
    case .critical: L10n.machineStatusPressureCritical(locale: text.locale)
    default: nil
    }
    let total = text.size(Int64(s.memoryTotalBytes), binary: true)
    let swap = s.hasSwapUsedBytes && s.swapUsedBytes > 0 ? text.size(Int64(s.swapUsedBytes), binary: true) : nil
    return MemoryCard(
        used: text.gbNumber(Int64(s.memoryUsedBytes), oneDecimal: true, binary: true), usedNumber: Double(s.memoryUsedBytes) / gibibyte,
        pressureWord: word, position: pressurePosition(s), tone: machineTone(s.memoryPressure), total: total, swap: swap,
        detail: swap.map { L10n.machineStatusTotalSwap(size: total, swap: $0, locale: text.locale) } ?? L10n.machineStatusTotal(size: total, locale: text.locale))
}

/// Where the dot sits on the scale: the computer's own number only; with a level and no position the scale is not drawn (ui-rulings 18).
public func pressurePosition(_ s: PBMachineStatus) -> Double? {
    guard s.memoryPressure != .unspecified, s.hasMemoryPressurePosition else { return nil }
    return min(max(s.memoryPressurePosition, 0), 1)
}

/// The disk card: the free amount, the bar's segments (shares of the volume) and its lines.
public struct DiskCard: Equatable, Sendable {
    public var free: String
    public var tone: MachineTone
    public var word: String
    public var agentShare: Double
    public var otherShare: Double
    /// The first line under the state word, and the second (remote-agent's share); each its own line (ui-rulings 2).
    public var detail: String
    public var detail2: String?
    public var agent: String?
}

public func diskCard(_ s: PBMachineStatus, text: StatusText) -> DiskCard {
    let d = s.disk
    let t = machineTone(d.pressure)
    let word = switch t {
    case .normal: L10n.machineStatusDiskOk(locale: text.locale)
    case .warn: L10n.machineStatusDiskLow(locale: text.locale)
    case .critical: L10n.machineStatusDiskCritical(locale: text.locale)
    }
    let total = Double(max(1, d.totalBytes))
    let used = d.totalBytes > d.availableBytes ? d.totalBytes - d.availableBytes : 0
    let agentBytes = s.hasAgentBytes ? min(s.agentBytes, used) : 0
    let agent = s.hasAgentBytes ? text.size(Int64(s.agentBytes)) : nil
    let detail = t == .critical
        ? L10n.machineStatusDiskCriticalHint(locale: text.locale)
        : L10n.machineStatusDiskTotal(size: text.size(Int64(d.totalBytes)), locale: text.locale)
    let detail2 = t != .critical ? agent.map { L10n.machineStatusDiskAgentShare(size: $0, locale: text.locale) } : nil
    return DiskCard(
        free: text.gbNumber(Int64(d.availableBytes)), tone: t, word: word, agentShare: Double(agentBytes) / total,
        otherShare: Double(used - agentBytes) / total, detail: detail, detail2: detail2, agent: agent)
}

/// The disk lines appear only when the disk is 注意 or 紧张 (A3 ③).
public func showDiskActions(_ s: PBMachineStatus) -> Bool { machineTone(s.disk.pressure) != .normal }

/// The CPU figures: the big number, the load and the cores (each absent when the computer does not say).
public struct CpuFigures: Equatable, Sendable {
    public var percent: Int
    public var load: String?
    public var cores: String?
}

public func cpuFigures(_ s: PBMachineStatus, text: StatusText) -> CpuFigures {
    CpuFigures(
        percent: Int(min(max(s.cpuPercent, 0), 100).rounded()), load: s.hasLoadOne ? text.load(s.loadOne) : nil,
        cores: s.cores > 0 ? text.cores(Int(s.cores)) : nil)
}

/// The line at the page's foot: how long it has been up and, on a laptop, the battery.
public func footerParts(_ s: PBMachineStatus, text: StatusText) -> [String] {
    var out = [String]()
    if s.uptimeSeconds > 0 { out.append(L10n.machineStatusUptime(duration: text.duration(seconds: Int64(s.uptimeSeconds)), locale: text.locale)) }
    if s.hasBattery {
        let pct = Int(min(max(s.battery.percent, 0), 100).rounded())
        out.append(s.battery.charging
            ? L10n.machineStatusBatteryCharging(percent: pct, locale: text.locale)
            : L10n.machineStatusBattery(percent: pct, locale: text.locale))
    }
    return out
}

// MARK: The chart (A3 ①, A4)

/// The window drawn: 116 s (ui-rulings 10). The newest sample is 118 s from the oldest the daemon keeps, and the right edge trails the
/// newest by up to one sampling interval so that the curve slides evenly; a window of 116 s then always starts on data, never on a blank.
public let chartWindowMs: Int64 = 116_000
public let sampleIntervalMs: Int64 = 2_000
/// Two neighbouring samples more than this apart are not joined (ui-rulings 16): the page was away; no fake points.
public let chartGapMs: Int64 = 10_000

public struct ChartPoint: Equatable, Sendable {
    public var timeMs: Int64
    public var value: Double
    public init(timeMs: Int64, value: Double) { self.timeMs = timeMs; self.value = value }
}

/// The samples of `metric` since `sinceMs` (the recovery point after a stale stretch), oldest first.
public func chartPoints(_ s: PBMachineStatus, metric: StatusMetric, sinceMs: Int64 = 0) -> [ChartPoint] {
    s.samples.filter { $0.timeMs >= sinceMs }.sorted { $0.timeMs < $1.timeMs }
        .map { ChartPoint(timeMs: $0.timeMs, value: metric == .cpu ? $0.cpuPercent : Double($0.memoryUsedBytes)) }
}

/// The top of the vertical axis: 100% for the CPU, the total memory for memory.
public func chartCeiling(_ s: PBMachineStatus, metric: StatusMetric) -> Double {
    metric == .cpu ? 100 : max(1, Double(s.memoryTotalBytes))
}

/// A point of a drawn run: `x` across the window, `y` up the axis, both 0...1.
public struct ChartXY: Equatable, Sendable {
    public var x: Double
    public var y: Double
}

/// The runs of the curve in the window ending at `endMs`: points are placed by their own time, a hole
/// of more than `gapMs` ends a run, and the run is cut exactly at the window's edges.
public func chartRuns(_ points: [ChartPoint], endMs: Int64, ceiling: Double, windowMs: Int64 = chartWindowMs, gapMs: Int64 = chartGapMs) -> [[ChartXY]] {
    let start = endMs - windowMs
    func xy(_ t: Double, _ v: Double) -> ChartXY { ChartXY(x: (t - Double(start)) / Double(windowMs), y: min(max(v / ceiling, 0), 1)) }
    var runs = [[ChartXY]]()
    var open = false
    var prev: ChartPoint?
    for p in points {
        if let prev, p.timeMs - prev.timeMs > gapMs { open = false }
        let before = prev
        prev = p
        if p.timeMs < start { continue }
        if p.timeMs > endMs {
            // The edge falls between the last point inside and this one.
            if open, let before, before.timeMs <= endMs {
                let f = Double(endMs - before.timeMs) / Double(p.timeMs - before.timeMs)
                runs[runs.count - 1].append(xy(Double(endMs), before.value + (p.value - before.value) * f))
            }
            open = false
            break
        }
        if !open {
            open = true
            runs.append([])
            // The first point after the left edge continues the line from just outside it.
            if let before, before.timeMs < start, p.timeMs - before.timeMs <= gapMs {
                let f = Double(start - before.timeMs) / Double(p.timeMs - before.timeMs)
                runs[runs.count - 1].append(xy(Double(start), before.value + (p.value - before.value) * f))
            }
        }
        runs[runs.count - 1].append(xy(Double(p.timeMs), p.value))
    }
    return runs
}

/// The value the curve has at `t`, or nil where there is a hole or no data.
public func chartValue(at t: Int64, in points: [ChartPoint], gapMs: Int64 = chartGapMs) -> Double? {
    for i in points.indices {
        let b = points[i]
        if b.timeMs == t { return b.value }
        if b.timeMs > t {
            guard i > 0 else { return nil }
            let a = points[i - 1]
            if b.timeMs - a.timeMs > gapMs { return nil }
            return a.value + (b.value - a.value) * Double(t - a.timeMs) / Double(b.timeMs - a.timeMs)
        }
    }
    return nil
}

/// On-time samples trail by at most 2 s. After a poll gap, catch up in 500 ms
/// with cubic ease-out. Retargeting keeps the deadline; old snapshots never rewind.
public struct ChartTimeline {
    private var end: Double?
    private var latest: Int64 = 0
    private var lastNow: Double = 0
    private struct Catchup {
        let from: Double, target: Double, start: Double, deadline: Double
    }
    private var catchup: Catchup?

    public init() {}

    public mutating func advance(nowMs: Double, firstMs: Int64, latestMs: Int64, moving: Bool = true) -> Double {
        guard latestMs > 0 else { return 0 }
        guard let previous = end, moving else {
            let value = moving ? max(Double(firstMs), Double(latestMs - sampleIntervalMs)) : Double(latestMs)
            end = value
            latest = latestMs
            lastNow = nowMs
            catchup = nil
            return value
        }
        let now = max(lastNow, nowMs)
        let value: Double
        if let c = catchup {
            let p = min(1, (now - c.start) / (c.deadline - c.start))
            let remaining = 1 - p
            value = c.from + (c.target - c.from) * (1 - remaining * remaining * remaining)
            if p == 1 { catchup = nil }
        } else {
            value = min(Double(latest), previous + now - lastNow)
        }
        lastNow = now
        if latestMs > latest {
            latest = latestMs
            if catchup != nil || Double(latestMs) - value > Double(sampleIntervalMs) {
                catchup = Catchup(from: value, target: Double(latestMs), start: now, deadline: catchup?.deadline ?? now + 500)
            }
        }
        end = value
        return value
    }
}

/// The index of the sample nearest to `t`, or nil without data.
public func nearestSample(_ points: [ChartPoint], at t: Double) -> Int? {
    var best: Int?
    var d = Double.greatestFiniteMagnitude
    for i in points.indices {
        let x = abs(Double(points[i].timeMs) - t)
        if x < d { d = x; best = i }
    }
    return best
}

/// The text a screen reader says for the curve (A7).
public func chartSummary(_ points: [ChartPoint], metric: StatusMetric, text: StatusText) -> String {
    guard let last = points.last else { return "" }
    func fmt(_ v: Double) -> String { metric == .cpu ? text.percent(v) : text.size(Int64(v), binary: true) }
    return L10n.machineStatusA11YChart(max: fmt(points.map(\.value).max() ?? 0), now: fmt(last.value), locale: text.locale)
}

// MARK: The ranking (A3 ④)

/// What a ranking row shows; `bar` is its share of the head (0...1).
public struct UsageRow: Equatable, Sendable, Identifiable {
    public var id: String
    public var title: String
    public var isSession: Bool
    public var agent: PBAgent?
    public var primary: String
    public var secondary: String
    public var bar: Double
    public var top: Bool
    /// The session a row opens: its own for a session, the owner's for a process; empty when there is none known to this client.
    public var sessionId: String
    /// A session of the computer that this client does not list: its title falls back to the executable and the row cannot be opened (ui-rulings 15).
    public var known: Bool
}

/// The previous order of each list, kept per metric so rows do not trade places for nothing.
public final class UsageOrder {
    private var kept = [Bool: (memory: Bool, ids: [String])]()
    public init() {}

    /// The rows in their stable order; a changed metric starts again from the metric's own order.
    public func order(sessions: Bool, memory: Bool, rows: [PBResourceUsage]) -> [PBResourceUsage] {
        let previous = kept[sessions].flatMap { $0.memory == memory ? $0.ids : nil } ?? []
        let out = stableUsage(previous: previous, rows: rows, memory: memory)
        kept[sessions] = (memory, out.map(\.id))
        return out
    }
}

/// The (up to) 8 rows of the list. `titleOf` and `agentOf` say what a session is called and which CLI it runs; a session
/// this client does not list falls back to its executable.
public func usageRows(
    _ s: PBMachineStatus, showSessions: Bool, metric: StatusMetric, order: UsageOrder, text: StatusText,
    titleOf: (String) -> String?, agentOf: (String) -> PBAgent?
) -> [UsageRow] {
    let memory = metric == .memory
    let source = showSessions ? s.sessions : s.processes
    let ordered = Array(order.order(sessions: showSessions, memory: memory, rows: source).prefix(8))
    func main(_ r: PBResourceUsage) -> Double { memory ? Double(r.memoryBytes) : r.cpuPercent }
    let head = ordered.map(main).max() ?? 0
    return ordered.enumerated().map { i, r in
        let sid = showSessions ? r.id : r.sessionID
        let sessionTitle = sid.isEmpty ? nil : titleOf(sid)
        let size = text.size(Int64(r.memoryBytes), binary: true)
        let secondary = (memory ? text.percent(r.cpuPercent) : size) + (!showSessions && sessionTitle != nil ? " · \(sessionTitle!)" : "")
        return UsageRow(
            id: r.id, title: showSessions ? sessionTitle ?? (r.executable.isEmpty ? r.id : r.executable) : r.executable,
            isSession: showSessions, agent: showSessions && sessionTitle != nil ? agentOf(r.id) : nil,
            primary: memory ? size : text.percent(r.cpuPercent), secondary: secondary,
            bar: head <= 0 ? 0 : min(max(main(r) / head, 0), 1), top: i == 0, sessionId: sessionTitle == nil ? "" : sid,
            known: sessionTitle != nil)
    }
}

// MARK: The home reminder (B1)

/// One reminder card for a computer whose disk is low.
public struct DiskReminder: Equatable, Sendable, Identifiable {
    public var machineId: String
    public var critical: Bool
    public var availableBytes: Int64
    public var title: String
    public var detail: String
    public var id: String { machineId }
}

/// The reminders of every online computer under 10 GB, the tightest first.
public func diskReminders(_ state: RemoteState, text: StatusText = StatusText()) -> [DiskReminder] {
    state.machines.values.compactMap { m -> DiskReminder? in
        guard let meta = m.meta, let d = machineDiskReminder(online: m.online, meta: meta) else { return nil }
        let critical = d.availableBytes < 2_000_000_000
        let size = L10n.machineStatusSizeGb(size: text.gbNumber(Int64(d.availableBytes)), locale: text.locale)
        let name = machineLabel(state, id: m.id)
        return DiskReminder(
            machineId: m.id, critical: critical, availableBytes: Int64(d.availableBytes),
            title: critical ? L10n.machineStatusHomeCritical(locale: text.locale) : L10n.machineStatusHomeWarning(locale: text.locale),
            detail: critical
                ? L10n.machineStatusHomeCriticalDetail(computer: name, size: size, locale: text.locale)
                : L10n.machineStatusHomeWarningDetail(computer: name, size: size, locale: text.locale))
    }
    .sorted { a, b in
        if a.critical != b.critical { return a.critical }
        if a.availableBytes != b.availableBytes { return a.availableBytes < b.availableBytes }
        return a.machineId < b.machineId
    }
}

// MARK: What the page shows (A6)

public enum StatusView: Sendable, Equatable { case loading, unavailable, live, stale }

/// The page's face for the controller's state: nothing to show yet, nothing ever read, live, or kept.
public func statusView(state: MachinePollState, last: PBMachineStatus?) -> StatusView {
    if last == nil { return state == .unavailable ? .unavailable : .loading }
    if state == .stale || state == .unavailable { return .stale }
    return .live
}

/// The prompt of 让 Agent 帮我清理, added after what is already written in the draft, and not again when the draft already holds it.
public func cleanupDraft(existing: String, prompt: String) -> String {
    if existing.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return prompt }
    if existing.contains(prompt) { return existing }
    var end = existing.endIndex
    while end > existing.startIndex, existing[existing.index(before: end)].isWhitespace { end = existing.index(before: end) }
    return String(existing[..<end]) + "\n\n" + prompt
}

// MARK: The swap of the main chart and a small card (A3)

public let swapMs = 350.0
public let swapOutMs = 100.0
public let swapInMs = 120.0

/// What the swap shows `ms` into its 350: the card keeps its colour and corners throughout; the old figures fade out over the
/// first 100, nothing shows until the last 120, in which the new ones fade in. Never both at once.
public func swapOldAlpha(_ ms: Double) -> Double { min(max(1 - ms / swapOutMs, 0), 1) }
public func swapNewAlpha(_ ms: Double) -> Double { min(max((ms - (swapMs - swapInMs)) / swapInMs, 0), 1) }
