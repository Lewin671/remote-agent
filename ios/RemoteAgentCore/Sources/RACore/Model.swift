// Remaining shell helpers: running clock, title input clamping,
// one-line preview stripping, relative time, and model list folding.
import Foundation

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

private let plainPlaceholder = Rx("\\[Image #\(Jre.digit)+\\]\(Jre.space)*")
private let plainFence = Rx("```[a-zA-Z0-9]*")
private let plainCode = Rx("`([^`]*)`")
private let plainBold = Rx("\\*\\*([^*]+)\\*\\*")
private let plainLink = Rx("\\[([^\\]]+)\\]\\([^)]*\\)")
private let plainLineMark = Rx("\(Jre.lineStart)\(Jre.space){0,3}(#{1,6}\(Jre.space)+|[-*+]\(Jre.space)+|>\(Jre.space)?)")
private let plainSpaces = Rx("\(Jre.space)+")

/// Text for one-line previews: Markdown marks and the CLIs' image placeholders removed.
public func plain(_ text: String) -> String {
    var t = plainPlaceholder.replace(text, with: "")
    t = plainFence.replace(t, with: "")
    t = plainCode.replaceWithGroup1(t)
    t = plainBold.replaceWithGroup1(t)
    t = plainLink.replaceWithGroup1(t)
    t = plainLineMark.replace(t, with: "")
    t = plainSpaces.replace(t, with: " ")
    return t.jvmTrim()
}

/// Short relative time, like the mockup: 刚刚, 3 分钟前, 9:12, 昨天, 9月27日.
public func whenText(_ ms: Int64, now: Int64 = nowMs(), zone: TimeZone = .current) -> String {
    if ms <= 0 { return "" }
    let d = now - ms
    if d < 60_000 { return L10n.timeJustNow() }
    if d < 3_600_000 { return L10n.timeMinutesAgo(count: Int(d / 60_000)) }
    let then = LocalDay(ms: ms, zone: zone)
    let today = LocalDay(ms: now, zone: zone)
    if then.sameDay(today) { return then.hhmm }
    if then.epochDay == today.epochDay - 1 { return L10n.timeYesterday() }
    return L10n.timeMonthDay(day: then.day, month: then.month)
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
