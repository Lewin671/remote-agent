// Remaining shell helpers: wall-clock timestamp, running timer, title input clamping,
// one-line plan preview stripping, relative disconnect time, and model list folding.
import Foundation

/// The wall clock in milliseconds since the epoch.
public func nowMs() -> Int64 { Int64((Date().timeIntervalSince1970 * 1000).rounded(.down)) }

private func pad2(_ n: Int) -> String { n < 10 && n >= 0 ? "0\(n)" : "\(n)" }

// --- sessions ---

/// 2:12, 1:02:03: a running timer.
public func clock(_ ms: Int64) -> String {
    let s = ms / 1000
    return s >= 3600 ? "\(s / 3600):\(pad2(Int(s % 3600 / 60))):\(pad2(Int(s % 60)))" : "\(s / 60):\(pad2(Int(s % 60)))"
}

/// The most a session's title holds, counted in code points as the daemon counts them.
public let titleMax = 60

/// What the rename field takes of `text`: one line, and nothing past `titleMax`.
public func titleInput(_ text: String) -> String {
    // By code point: Swift's Character would take \r\n for one, and an emoji sequence for one.
    var line = String.UnicodeScalarView()
    var inBreak = false
    for u in text.unicodeScalars {
        if u == "\r" || u == "\n" {
            if !inBreak { line.append(" ") }
            inBreak = true
        } else {
            line.append(u)
            inBreak = false
        }
    }
    return line.count <= titleMax ? String(line) : String(String.UnicodeScalarView(line.prefix(titleMax)))
}

private let plainPatterns: [(NSRegularExpression, String)] = [
    (#"\[Image #[0-9]+\][ \t\n\x0B\f\r]*"#, ""),
    (#"```[a-zA-Z0-9]*"#, ""),
    (#"`([^`]*)`"#, "$1"),
    (#"\*\*([^*]+)\*\*"#, "$1"),
    (#"\[([^\]]+)\]\([^)]*\)"#, "$1"),
    (#"(?:\A|(?<=[\n\u0085\u2028\u2029])|(?<=\r)(?!\n))[ \t\n\x0B\f\r]{0,3}(#{1,6}[ \t\n\x0B\f\r]+|[-*+][ \t\n\x0B\f\r]+|>[ \t\n\x0B\f\r]?)"#, ""),
    (#"[ \t\n\x0B\f\r]+"#, " "),
].map { (try! NSRegularExpression(pattern: $0.0), $0.1) }

/// Text for one-line previews: Markdown marks and the CLIs' image placeholders removed.
public func plain(_ text: String) -> String {
    var t = text
    for (re, template) in plainPatterns {
        t = re.stringByReplacingMatches(in: t, range: NSRange(t.startIndex..., in: t), withTemplate: template)
    }
    return t.trimmingCharacters(in: .whitespacesAndNewlines)
}

/// Short relative time, like the mockup: 刚刚, 3 分钟前, 9:12, 昨天, 9月27日.
public func whenText(_ ms: Int64, now: Int64 = nowMs(), zone: TimeZone = .current) -> String {
    if ms <= 0 { return "" }
    let d = now - ms
    if d < 60_000 { return L10n.timeJustNow() }
    if d < 3_600_000 { return L10n.timeMinutesAgo(count: Int(d / 60_000)) }
    var cal = Calendar(identifier: .gregorian)
    cal.timeZone = zone
    let thenDate = Date(timeIntervalSince1970: Double(ms) / 1000)
    let nowDate = Date(timeIntervalSince1970: Double(now) / 1000)
    let then = cal.dateComponents([.year, .month, .day, .hour, .minute], from: thenDate)
    if cal.isDate(thenDate, inSameDayAs: nowDate) {
        return "\(then.hour ?? 0):\(pad2(then.minute ?? 0))"
    }
    if let yesterday = cal.date(byAdding: .day, value: -1, to: nowDate),
       cal.isDate(thenDate, inSameDayAs: yesterday) {
        return L10n.timeYesterday()
    }
    return L10n.timeMonthDay(day: then.day ?? 1, month: then.month ?? 1)
}

/// The models a picker lists before 全部模型: a short list whole, so none is out
/// of reach; a long one its first three and the picked one, so 推理强度 below
/// the list stays in sight.
public func shownModels<M: Equatable>(_ models: [M], picked: M?, all: Bool) -> [M] {
    if all || models.count <= 5 { return models }
    let top = Array(models.prefix(3))
    guard let picked, !top.contains(picked) else { return top }
    return top + [picked]
}
