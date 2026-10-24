import Foundation

/// The native picker edits a copy; Cancel never alters the task definition.
public struct ScheduleTiming: Equatable, Sendable {
    public enum Kind: Int, CaseIterable, Sendable { case once, daily, weekly, interval, afterTurn }
    public enum End: Int, CaseIterable, Sendable { case never, count, date }
    public var kind: Kind = .daily
    public var date: Date
    public var weekdays: Set<Int> = [1]
    public var workdays = false
    public var interval = 1
    /// Seconds per selected unit: minute / hour / day.
    public var unit = 3600
    public var end: End = .never
    public var count = 10
    public var until: Date
    public var timezone: String
    private var afterTrigger: ScheduleTrigger?
    public init(trigger: ScheduleTrigger? = nil, now: Date = Date(), zone: TimeZone = .current) {
        timezone = zone.identifier; date = now.addingTimeInterval(300); until = now.addingTimeInterval(86400)
        guard let t = trigger else { return }
        timezone = t.timezone ?? zone.identifier
        if let at = t.at.flatMap(scheduleDate) { date = at }
        switch t.type {
        case "at": kind = .once
        case "interval":
            kind = .interval
            let n = Int(t.intervalSeconds ?? 3600)
            unit = n % 3600 == 0 ? 3600 : n % 60 == 0 ? 60 : 1; interval = max(1, n / unit)
        case "after_turn": kind = .afterTurn; afterTrigger = t
        default:
            let days = Set(t.weekdays ?? [])
            workdays = days == Set(1...5)
            kind = days.isEmpty || days == Set(1...7) || workdays ? .daily : .weekly
            weekdays = days.isEmpty ? [1] : days
            var cal = Calendar(identifier: .gregorian); cal.timeZone = TimeZone(identifier: timezone) ?? zone
            let hm = (t.time ?? "08:00").split(separator: ":").compactMap { Int($0) }
            date = cal.date(bySettingHour: hm.first ?? 8, minute: hm.last ?? 0, second: 0, of: now) ?? now
        }
        if let u = t.until.flatMap(scheduleDate) { until = u; end = .date }
        if let max = t.maxRuns, max > 0 { count = Int(max); end = .count }
    }
    public func trigger(now: Date = Date()) throws -> ScheduleTrigger {
        guard let zone = TimeZone(identifier: timezone) else { throw UserError("请选择有效的 IANA 时区") }
        var t = ScheduleTrigger(type: "calendar")
        switch kind {
        case .once:
            guard date > now else { throw UserError("请选择未来的时间") }
            t.type = "at"; t.at = scheduleISO(date)
        case .daily, .weekly:
            t.timezone = timezone
            t.time = scheduleTime(date, zone: zone)
            if kind == .weekly {
                guard !weekdays.isEmpty else { throw UserError("请至少选择一天") }
                t.weekdays = weekdays.sorted()
            } else if workdays { t.weekdays = Array(1...5) }
        case .interval:
            guard interval > 0, interval <= 315_360_000, unit > 0 else { throw UserError("请输入有效的间隔") }
            t.type = "interval"; t.intervalSeconds = Int64(interval) * Int64(unit)
        case .afterTurn:
            guard let original = afterTrigger else { throw UserError("请选择一次、每天、每周或每隔") }
            t = original
        }
        t.maxRuns = nil; t.until = nil
        if kind != .once {
            if end == .count {
                guard count > 0 else { throw UserError("运行次数必须大于 0") }; t.maxRuns = Int64(count)
            }
            if end == .date {
                guard until > now else { throw UserError("结束时间必须在未来") }; t.until = scheduleISO(until)
            }
        }
        return t
    }
}
