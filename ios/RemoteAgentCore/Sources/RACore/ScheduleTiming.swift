import Foundation

/// Picker values are local calendar components. Changing the zone never changes the wall clock.
public struct ScheduleTiming: Equatable, Sendable {
    public enum Kind: Int, CaseIterable, Sendable { case once, daily, weekly, interval, afterTurn }
    public enum End: Int, CaseIterable, Sendable { case never, count, date }
    public var kind: Kind = .daily
    private var dateParts: DateComponents
    private var untilParts: DateComponents
    public var timezone: String
    public var date: Date {
        get { calendar.date(from: dateParts) ?? .distantFuture }
        set { dateParts = Self.parts(newValue, zone: calendar.timeZone) }
    }
    public var until: Date {
        get { calendar.date(from: untilParts) ?? .distantFuture }
        set { untilParts = Self.parts(newValue, zone: calendar.timeZone) }
    }
    public var weekdays: Set<Int> = [1]
    public var workdays = false
    public var interval = 1
    public var unit = 3600
    public var end: End = .never
    public var count = 10
    private var original: ScheduleTrigger?
    private var initialEnd: End = .never
    private var initialCount = 10
    private var initialUntil = DateComponents()
    private var initialZone = ""
    private var calendar: Calendar {
        var c = Calendar(identifier: .gregorian); c.timeZone = TimeZone(identifier: timezone) ?? .current; return c
    }
    private static func parts(_ date: Date, zone: TimeZone) -> DateComponents {
        var c = Calendar(identifier: .gregorian); c.timeZone = zone
        return c.dateComponents([.year, .month, .day, .hour, .minute, .second], from: date)
    }
    public init(trigger: ScheduleTrigger? = nil, now: Date = Date(), zone: TimeZone = .current) {
        let editingZone = trigger?.timezone.flatMap(TimeZone.init(identifier:)) ?? zone
        timezone = trigger?.timezone ?? zone.identifier
        dateParts = Self.parts(trigger?.at.flatMap(scheduleDate) ?? now.addingTimeInterval(300), zone: editingZone)
        untilParts = Self.parts(trigger?.until.flatMap(scheduleDate) ?? now.addingTimeInterval(86400), zone: editingZone)
        original = trigger
        if let t = trigger {
            switch t.type {
            case "at": kind = .once
            case "interval":
                kind = .interval
                let n = Int(t.intervalSeconds ?? 3600)
                unit = n % 86400 == 0 ? 86400 : n % 3600 == 0 ? 3600 : n % 60 == 0 ? 60 : 1
                interval = max(1, n / unit)
            case "after_turn": kind = .afterTurn
            default:
                let days = Set(t.weekdays ?? [])
                workdays = days == Set(1...5)
                kind = days.isEmpty || days == Set(1...7) || workdays ? .daily : .weekly
                weekdays = days.isEmpty ? [1] : days
                let hm = (t.time ?? "08:00").split(separator: ":").compactMap { Int($0) }
                dateParts.hour = hm.first ?? 8; dateParts.minute = hm.last ?? 0; dateParts.second = 0
            }
            if t.until != nil { end = .date }
            if let n = t.maxRuns, n > 0 { count = Int(n); end = .count }
        }
        initialEnd = end; initialCount = count; initialUntil = untilParts; initialZone = timezone
    }
    private func absolute(_ parts: DateComponents) throws -> Date {
        guard let d = calendar.date(from: parts), Self.parts(d, zone: calendar.timeZone) == parts else {
            throw UserError("这个时区在该时刻发生夏令时跳转，请换一个时间")
        }
        return d
    }
    public func trigger(now: Date = Date()) throws -> ScheduleTrigger {
        guard TimeZone(identifier: timezone) != nil else { throw UserError("请选择有效的 IANA 时区") }
        var t = ScheduleTrigger(type: "calendar")
        switch kind {
        case .once:
            let at = try absolute(dateParts)
            guard at > now else { throw UserError("请选择未来的时间") }
            t.type = "at"; t.at = scheduleISO(at)
        case .daily, .weekly:
            t.timezone = timezone
            t.time = String(format: "%02d:%02d", dateParts.hour ?? 8, dateParts.minute ?? 0)
            if kind == .weekly {
                guard !weekdays.isEmpty else { throw UserError("请至少选择一天") }
                t.weekdays = weekdays.sorted()
            } else if workdays { t.weekdays = Array(1...5) }
        case .interval:
            guard interval > 0, interval <= 315_360_000, unit > 0 else { throw UserError("请输入有效的间隔") }
            let seconds = Int64(interval) * Int64(unit)
            if let original, original.type == "interval", original.intervalSeconds == seconds { t = original }
            else { t.type = "interval"; t.intervalSeconds = seconds; t.delaySeconds = seconds }
        case .afterTurn:
            guard let original, original.type == "after_turn" else { throw UserError("请选择一次、每天、每周或每隔") }
            t = original
        }
        if kind != .once {
            if let original, end == initialEnd {
                t.maxRuns = original.maxRuns; t.until = original.until
                if end == .count, count != initialCount {
                    guard count > 0 else { throw UserError("运行次数必须大于 0") }; t.maxRuns = Int64(count)
                }
                if original.until != nil && (untilParts != initialUntil) {
                    let u = try absolute(untilParts)
                    guard u > now else { throw UserError("结束时间必须在未来") }; t.until = scheduleISO(u)
                }
            } else {
                t.maxRuns = nil; t.until = nil
                if end == .count {
                    guard count > 0 else { throw UserError("运行次数必须大于 0") }; t.maxRuns = Int64(count)
                }
                if end == .date {
                    let u = try absolute(untilParts)
                    guard u > now else { throw UserError("结束时间必须在未来") }; t.until = scheduleISO(u)
                }
            }
        }
        return t
    }
}
