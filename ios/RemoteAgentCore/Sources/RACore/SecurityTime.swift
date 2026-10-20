import Foundation

/// Chinese calendar labels and a zero-padded 24-hour clock, independent of system locale.
public func securityTime(_ ms: UInt64, now: Date = Date(), timeOnly: Bool = false, timeZone: TimeZone = .current) -> String {
    guard ms > 0 else { return "尚未读取" }
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = timeZone
    let date = Date(timeIntervalSince1970: Double(ms) / 1000)
    let parts = calendar.dateComponents([.month, .day, .hour, .minute], from: date)
    let clock = String(format: "%02d:%02d", locale: Locale(identifier: "en_US_POSIX"), parts.hour!, parts.minute!)
    if timeOnly { return clock }
    let label: String
    if calendar.isDate(date, inSameDayAs: now) { label = "今天" }
    else if calendar.isDate(date, inSameDayAs: calendar.date(byAdding: .day, value: 1, to: now)!) { label = "明天" }
    else { label = "\(parts.month!)月\(parts.day!)日" }
    return "\(label) \(clock)"
}
