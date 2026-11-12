import Foundation

/// Chinese calendar labels and a zero-padded 24-hour clock, independent of system locale.
public func securityTime(_ ms: UInt64, now: Date = Date(), timeOnly: Bool = false, timeZone: TimeZone = .current) -> String {
    guard ms > 0 else { return L10n.securityIosNotReadYet() }
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = timeZone
    let date = Date(timeIntervalSince1970: Double(ms) / 1000)
    let parts = calendar.dateComponents([.month, .day, .hour, .minute], from: date)
    let clock = String(format: "%02d:%02d", locale: Locale(identifier: "en_US_POSIX"), parts.hour!, parts.minute!)
    if timeOnly { return clock }
    let label: String
    if calendar.isDate(date, inSameDayAs: now) { label = L10n.timeToday() }
    else if calendar.isDate(date, inSameDayAs: calendar.date(byAdding: .day, value: 1, to: now)!) { label = L10n.timeTomorrow() }
    else { label = L10n.timeMonthDay(day: Int(parts.day!), month: Int(parts.month!)) }
    return "\(label) \(clock)"
}
