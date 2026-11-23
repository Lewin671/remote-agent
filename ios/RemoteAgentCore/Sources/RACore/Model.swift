// Remaining shell helpers derived from decrypted state: running clock,
// title input clamping, one-line preview stripping, relative time, model list
// folding, client display names, and machine capabilities/protocol check.
import Foundation
import RAProto

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

// --- devices and where actions came from ---

/// A client's name: the one given in its sealed metadata, else its default
/// (`defaultClientName`). `devices` is the account's list, which the default
/// is told apart in.
public func clientName(_ d: DeviceView?, id: String, among devices: [String: DeviceView]) -> String {
    givenName(d).ifEmpty(defaultClientName(d, id: id, among: devices))
}

private func givenName(_ d: DeviceView?) -> String { (d?.meta?.name ?? "").jvmTrim() }

/// The name a client has until the user gives it one, and again once they
/// take theirs back: what it is and runs on, from what it says of itself
/// (Chrome · macOS, Android · Pixel 9, iPhone). The end of its ID follows
/// only when another client of the account has the same default name.
public func defaultClientName(_ d: DeviceView?, id: String, among devices: [String: DeviceView]) -> String {
    let identity = clientDefaultIdentity(d?.meta)
    let base = identity.text
    let twin = devices.values.contains { o in o.id != id && o.kind == .client && clientDefaultIdentity(o.meta) == identity }
    return twin ? "\(base) · \(shortId(id))" : base
}

/// What a client is called by what it says it runs on: the browser and its
/// system for a web client (「Chrome on macOS」), Android and the phone's
/// model (「Android 16 · Pixel 9」), the kind of Apple device
/// (「iOS 27.0 · iPhone」); its kind alone when it says nothing more.
private enum ClientDefaultIdentity: Equatable {
    case raw(String), browser, client
    var text: String {
        switch self { case .raw(let value): value; case .browser: L10n.securityBrowser(); case .client: L10n.securityClient() }
    }
}

private func clientDefaultIdentity(_ meta: PBDeviceMeta?) -> ClientDefaultIdentity {
    let description = (meta?.description_p ?? "").jvmTrim()
    // What follows the system's version.
    let model = description.components(separatedBy: " · ").dropFirst().joined(separator: " · ").jvmTrim()
    switch meta?.platform {
    case "ios":
        return .raw(model.ifEmpty("iOS"))
    case "android":
        return .raw(model.isEmpty ? "Android" : "Android · \(model)")
    case "web":
        let words = description.components(separatedBy: " on ").map { $0.jvmTrim() }
        let browser = words[0]
        return browser.isEmpty ? .browser : .raw(([browser] + words.dropFirst().prefix(1).filter { !$0.isEmpty }).joined(separator: " · "))
    default:
        return .client
    }
}

/// The last four hex digits of a device ID, as shown next to a name.
private func shortId(_ id: String) -> String { id.jvmTakeLast(4) }

/// What the machine says it offers; nil until its metadata is read.
public func capabilities(_ m: MachineView?) -> PBMachineCapabilities? {
    m?.pathState.capabilities
}

/// Why no command goes to `m`: it speaks another protocol version than this
/// app (`peerProtocolVersion`), said by which side to update; nil when they
/// match or the machine's metadata is not read yet.
public func protocolMismatch(_ m: MachineView?) -> String? {
    guard let theirs = m?.pathState.protocolVersion else { return nil }
    if theirs < peerProtocolVersion { return L10n.errorsRemoteAgentOnIsOutOfDateUpdateIt(value: String(describing: machineName(m))) }
    if theirs > peerProtocolVersion { return L10n.errorsIosThisAppVersionIsOutOfDateUpdateAnd() }
    return nil
}
