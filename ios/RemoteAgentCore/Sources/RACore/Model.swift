// View models derived from the decrypted state, as in web/src/model.ts and
// Android's core/Model.kt: statuses, titles, times, modes and devices. The
// projects are in Projects.swift, the orders in Order.swift, and what a
// session's events say is pending in ChatState.swift.
import Foundation
import RAProto

// --- CLIs ---

/// The CLI a session runs; sessions from before Claude Code support are Codex.
public func effectiveAgent(_ a: PBAgent) -> PBAgent {
    switch a {
    case .unspecified, .UNRECOGNIZED: return .codex
    default: return a
    }
}

public func agentName(_ a: PBAgent) -> String {
    effectiveAgent(a) == .claude ? "Claude Code" : "Codex"
}

extension SessionView {
    public var agent: PBAgent { effectiveAgent(info?.agent ?? .unspecified) }
}

/// The CLIs a machine can start; Codex while it lists none.
public func machineAgents(_ m: MachineView?) -> [PBAgent] {
    let listed = (m?.meta?.agents ?? []).map { effectiveAgent($0.agent) }.distinct()
    return listed.isEmpty ? [.codex] : listed
}

// --- sessions ---

public enum Tone: Sendable { case run, wait, idle, off }

/// One state per session, the highest in the priority list of the design
/// (client-v1, 状态优先级). The raw value is the place in that list, which
/// 需要处理 sorts by.
public enum Status: Int, CaseIterable, Sendable {
    case loading, exited, offline
    /// Only shown (`shownStatus`): the server is out of reach, so what the session is doing is not known.
    case unlinked
    case approval, question, plan, popup, running, starting, idle

    public var text: String {
        switch self {
        case .loading: return "读取中"
        case .exited: return "已结束"
        case .offline: return "电脑离线"
        case .unlinked: return "未连接"
        case .approval: return "等待审批"
        case .question: return "等你回答"
        case .plan: return "计划待批准"
        case .popup: return "终端里有弹窗"
        case .running: return "运行中"
        case .starting: return "启动中"
        case .idle: return "空闲"
        }
    }

    public var tone: Tone {
        switch self {
        case .loading, .exited, .offline, .unlinked: return .off
        case .approval, .question, .plan, .popup: return .wait
        case .running, .starting: return .run
        case .idle: return .idle
        }
    }
}

public func exited(_ s: SessionView) -> Bool { s.info?.status == .exited }

/// The state to show for a session. Out of reach of the server, the last
/// one heard is not shown as if it still held (client-v30 「断线」); that a
/// session ended stays true.
public func shownStatus(_ state: RemoteState, _ s: SessionView) -> Status {
    let st = statusOf(s, machine: state.machines[s.machineId])
    return state.unlinked && st != .exited && st != .loading ? .unlinked : st
}

public func statusOf(_ s: SessionView, machine: MachineView?) -> Status {
    guard let i = s.info else { return .loading }
    if i.status == .exited { return .exited }
    if let machine, !machine.online { return .offline }
    if i.pendingApprovals > 0 { return .approval }
    if i.pendingQuestions > 0 { return .question }
    if i.planOpen { return .plan }
    if i.status == .blocked { return .popup }
    if i.status == .running { return .running }
    if i.status == .starting { return .starting }
    return .idle
}

/// A hint for a popup in the terminal. Claude Code's trust dialog has its
/// cursor on "No, exit", so Enter alone ends the session
/// (claude-code-notes.md §1); Codex's needs none. Same text as the web.
public func popupHint(agent: PBAgent, detail: String) -> String {
    effectiveAgent(agent) == .claude && detail == "trust folder"
        ? "默认选中的是 No, exit。按 ↓ 选中 Yes, I trust this folder 再按 ⏎" : ""
}

public func needsAttention(_ s: SessionView, machine: MachineView?) -> Bool { statusOf(s, machine: machine).tone == .wait }

/// The session's title; without one, "<folder> · <start time>".
public func titleOf(_ s: SessionView, zone: TimeZone = .current) -> String {
    let t = plain(s.info?.title ?? "").jvmTrim()
    if !t.isEmpty { return t }
    guard let i = s.info else { return "新会话" }
    let created: Int64 = i.hasCreatedAt ? i.createdAt.seconds * 1000 : 0
    return created > 0 ? "\(projectName(i.cwd)) · \(hhmm(created, zone: zone))" : projectName(i.cwd)
}

/// The most a session's title holds, counted in code points as the daemon counts them.
public let titleMax = 60

/// What the rename field starts with: the session's title, or nothing while
/// it only has the "<folder> · <start time>" placeholder. Same as the web.
public func renameInitial(_ s: SessionView) -> String { titleInput((s.info?.title ?? "").jvmTrim()) }

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

/// The title to send for what was typed, trimmed as the daemon trims it;
/// nil when it is the title the field started with and there is nothing to
/// save. Empty asks for the automatic title back.
public func renameTitle(typed: String, initial: String) -> String? {
    let t = typed.jvmTrim()
    return t.jvmEquals(initial) ? nil : t
}

/// The session was forked from another one (Session.forked_from).
public func forked(_ s: SessionView) -> Bool { !(s.info?.forkedFrom ?? "").isEmpty }

/// Whether a message was ever sent in the session: its title comes from the
/// first one, and the history loaded so far may show one.
public func sentMessage(_ s: SessionView, events: some Sequence<PBEvent>) -> Bool {
    !(s.info?.title ?? "").isEmpty || events.contains { if case .userMessage = $0.kind { return true } else { return false } }
}

/// Why the session cannot be forked now, as the ⋯ menu writes it under
/// 分叉会话; nil when it can. A fork takes an idle or ended session in which
/// a message was sent. `running`: a turn is going on by the events, which
/// can be ahead of the session's status. The menu only offers a fork while
/// the computer is reachable and the session is read, so offline and loading
/// do not come here; they read as not up yet.
public func forkBlocked(status: Status, running: Bool, sent: Bool) -> String? {
    switch status {
    case .running, .approval, .question: return "这一轮结束后可用"
    case .plan, .popup: return "处理完等待事项后可用"
    case .starting, .loading, .offline: return "启动完成后可用"
    case .unlinked: return "连上服务器后可用"
    case .idle: return running ? "这一轮结束后可用" : !sent ? "发过消息后可用" : nil
    case .exited: return !sent ? "发过消息后可用" : nil
    }
}

/// The session a fork made, or nil when the answer names none.
public func forkedSession(_ r: PBCommandResult?) -> String? {
    guard let r, case .forkSession(let f) = r.kind, f.hasSession, !f.session.id.isEmpty else { return nil }
    return f.session.id
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

/// The one-line preview for a list, following the session's status.
public func previewOf(_ s: SessionView, status: Status) -> String {
    guard let i = s.info else { return "" }
    switch status {
    case .plan: return "计划待批准"
    case .popup: return i.statusDetail.ifEmpty("终端里有弹窗")
    default: return plain(i.preview).ifEmpty(i.title.isEmpty ? "还没发消息" : "")
    }
}

/// Short relative time, like the mockup: 刚刚, 3 分钟前, 9:12, 昨天, 9月27日.
public func whenText(_ ms: Int64, now: Int64 = nowMs(), zone: TimeZone = .current) -> String {
    if ms <= 0 { return "" }
    let d = now - ms
    if d < 60_000 { return "刚刚" }
    if d < 3_600_000 { return "\(d / 60_000) 分钟前" }
    let then = LocalDay(ms: ms, zone: zone)
    let today = LocalDay(ms: now, zone: zone)
    if then.sameDay(today) { return then.hhmm }
    if then.epochDay == today.epochDay - 1 { return "昨天" }
    return "\(then.month)月\(then.day)日"
}

public func hhmm(_ ms: Int64, zone: TimeZone = .current) -> String { LocalDay(ms: ms, zone: zone).hhmm }

/// 9月27日 9:12, or the time alone for a moment of today.
public func dateTime(_ ms: Int64, now: Int64 = nowMs(), zone: TimeZone = .current) -> String {
    let t = LocalDay(ms: ms, zone: zone)
    let day = t.sameDay(LocalDay(ms: now, zone: zone)) ? "" : "\(t.month)月\(t.day)日 "
    return day + t.hhmm
}

// --- approval modes ---

public enum Mode: CaseIterable, Sendable {
    case ask, auto, plan, full

    public var text: String {
        switch self {
        case .ask: return "询问"
        case .auto: return "自动"
        case .plan: return "计划"
        case .full: return "完全放行"
        }
    }

    public var proto: PBApprovalMode {
        switch self {
        case .ask: return .ask
        case .auto: return .auto
        case .plan: return .plan
        case .full: return .fullAccess
        }
    }
}

public func modeOf(_ m: PBApprovalMode) -> Mode? { Mode.allCases.first { $0.proto == m } }

/// Claude Code's permission mode as a client mode, or nil for its own ones such as acceptEdits.
public func permissionMode(_ pm: String) -> Mode? {
    switch pm {
    case "default": return .ask
    case "auto": return .auto
    case "plan": return .plan
    case "bypassPermissions": return .full
    default: return nil
    }
}

/// Claude Code's permission mode as the chat names it: one of the four, or its own name for a mode outside them.
public func permissionModeName(_ pm: String) -> String {
    permissionMode(pm)?.text ?? (pm == "acceptEdits" ? "accept edits" : pm)
}

/// A mode as shown: one of the four, or a CLI's own mode by its name.
public struct ModeLabel: Equatable, Sendable {
    public var text: String
    public var mode: Mode?

    public init(text: String, mode: Mode?) {
        self.text = text
        self.mode = mode
    }
}

/// The approval mode named from turn-context fields: Claude Code's permission mode, or Codex's approval fields.
public func contextMode(reviewer: String, policy: String, sandbox: String, collaboration: String, permission: String = "") -> Mode? {
    if !permission.isEmpty { return permissionMode(permission) }
    if reviewer.isEmpty && policy.isEmpty && collaboration.isEmpty { return nil }
    if collaboration == "plan" { return .plan }
    if sandbox == "danger-full-access" && policy == "never" { return .full }
    if reviewer == "auto_review" { return .auto }
    return .ask
}

public func modeOf(_ s: SessionView) -> Mode? { modeLabel(s)?.mode }

/// The session's mode. Session.mode is either CLI's mode as a client mode;
/// unspecified with a permission mode is one of Claude Code's own, shown by
/// its name. With neither (a Codex context outside the four modes), it is
/// named from the approval fields.
public func modeLabel(_ s: SessionView) -> ModeLabel? {
    guard let i = s.info else { return nil }
    if let m = modeOf(i.mode) { return ModeLabel(text: m.text, mode: m) }
    if !i.permissionMode.isEmpty {
        let m = permissionMode(i.permissionMode)
        return ModeLabel(text: m?.text ?? i.permissionMode, mode: m)
    }
    return contextMode(reviewer: i.approvalsReviewer, policy: i.approvalPolicy, sandbox: i.sandbox, collaboration: i.collaborationMode)
        .map { ModeLabel(text: $0.text, mode: $0) }
}

/// Whether a session's model comes without the auto mode: the CLI's own list
/// says so (Claude Code's Haiku 4.5 and older). A model the list does not
/// know, or a list not read, gives no answer.
public func autoUnavailable(_ models: [PBModelInfo]?, model: String) -> Bool {
    guard let models, !model.isEmpty else { return false }
    return findModel(models, id: model) { $0.slug }?.autoUnavailable == true
}

private let modelWordBreak = Rx("[-_.\(Jre.s)]")

/// The entry of a model list that a session's model is. Codex reports the slug
/// it was started with; Claude Code offers aliases (sonnet, opus) but records
/// the full ID (claude-sonnet-5-5), so an alias matches a full ID that has it
/// as one of its words.
public func findModel<M>(_ models: [M], id: String, slug: (M) -> String) -> M? {
    if id.isEmpty { return nil }
    // A "[1m]" suffix names the 1M window on the command line; it is not part of the ID.
    let id = id.removingSuffix("[1m]")
    if let exact = models.first(where: { slug($0) == id }) { return exact }
    let words = modelWordBreak.split(id.lowercased())
    return models.first { words.contains(slug($0).lowercased()) }
}

/// Whether the model a CLI reports (`id`) is the one chosen by slug or alias.
public func modelIs(_ id: String, _ slug: String) -> Bool {
    !id.isEmpty && !slug.isEmpty && (id.jvmEquals(slug) || modelWordBreak.split(id.lowercased()).contains(slug.lowercased()))
}

/// The approval mode a new session gets when none is chosen: the daemon's
/// default for that CLI (architecture §13), or nil when the daemon has none
/// and the CLI's own configuration decides.
public func defaultMode(_ settings: PBDaemonSettings, agent: PBAgent) -> Mode? {
    modeOf(effectiveAgent(agent) == .claude ? settings.claude.mode : settings.defaultMode)
}

/// Context window use in percent, or nil when unknown.
public func contextPercent(_ s: SessionView) -> Int? {
    guard let i = s.info, i.contextWindow > 0 else { return nil }
    return min(100, Int(Int32(truncatingIfNeeded: (i.contextTokens &* 100) / i.contextWindow)))
}

/// Minutes left until a deadline, for "剩 N 分钟".
public func minutesLeft(deadlineMs: Int64, now: Int64 = nowMs()) -> Int64 { max(0, (deadlineMs - now + 59_999) / 60_000) }

// --- devices and where actions came from ---

private let spaces = Rx("\(Jre.space)+")

/// What kind of client, from what it says it runs on: Chrome, Safari, Android; a browser that says nothing is 浏览器.
public func platformLabel(_ meta: PBDeviceMeta?) -> String {
    let first = spaces.split((meta?.description_p ?? "").jvmTrim()).first ?? ""
    if let unit = first.utf16.first, !isJvmDigit(unit) { return first }
    switch meta?.platform {
    case "android": return "Android"
    case "ios": return "iOS"
    case "web": return "浏览器"
    default: return "客户端"
    }
}

/// A client's name: the one given in its sealed metadata, else its kind and the end of its ID (Chrome · a8f2).
public func clientName(_ d: DeviceView?, id: String) -> String {
    (d?.meta?.name ?? "").jvmTrim().ifEmpty(defaultClientName(d, id: id))
}

/// The name a client has until the user gives it one, and again once they take theirs back.
public func defaultClientName(_ d: DeviceView?, id: String) -> String { "\(platformLabel(d?.meta)) · \(shortId(id))" }

/// What a client is, under its name in the list: an app or a browser, and what it says it runs on.
public func clientDetail(_ d: DeviceView?) -> String {
    let kind: String
    switch d?.meta?.platform {
    case "android", "ios": kind = "App"
    case "web": kind = "浏览器"
    default: kind = ""
    }
    return [kind, (d?.meta?.description_p ?? "").jvmTrim()].filter { !$0.isEmpty }.joined(separator: " · ").ifEmpty("种类未知")
}

/// A day as lists write it: 9月27日, with the year when it is not this one.
public func dayText(_ ms: Int64, now: Int64 = nowMs(), zone: TimeZone = .current) -> String {
    let day = LocalDay(ms: ms, zone: zone)
    let year = day.year == LocalDay(ms: now, zone: zone).year ? "" : "\(day.year)年"
    return "\(year)\(day.month)月\(day.day)日"
}

/// When a client was last used: 刚刚, 3 分钟前, 今天 9:12, 昨天, 9月27日; "" when the server has no record of it.
public func lastUsed(_ d: DeviceView?, now: Int64 = nowMs(), zone: TimeZone = .current) -> String {
    let t = d?.lastSeenMs ?? 0
    if t <= 0 { return "" }
    let ago = now - t
    if ago < 60_000 { return "刚刚" }
    if ago < 3_600_000 { return "\(ago / 60_000) 分钟前" }
    let then = LocalDay(ms: t, zone: zone)
    let today = LocalDay(ms: now, zone: zone)
    if then.sameDay(today) { return "今天 \(then.hhmm)" }
    if then.epochDay == today.epochDay - 1 { return "昨天" }
    return dayText(t, now: now, zone: zone)
}

/// What the list says beside a client: when it was last used, else the day it was added.
public func clientUsed(_ d: DeviceView?, now: Int64 = nowMs(), zone: TimeZone = .current) -> String {
    let used = lastUsed(d, now: now, zone: zone)
    if !used.isEmpty { return used + (used.utf16.last.map(isJvmDigit) == true ? " " : "") + "使用" }
    if let d, d.createdMs > 0 { return "\(dayText(d.createdMs, now: now, zone: zone))添加" }
    return "已配对"
}

/// Clients as the list orders them: this one, then the most recently used
/// first, then those with no record, the newest first. Clients equal in all
/// of that go by ID: they come out of a dictionary, whose order means nothing.
public func sortClients(_ clients: some Sequence<DeviceView>, me: String) -> [DeviceView] {
    clients.stableSorted { a, b in
        if (a.id != me) != (b.id != me) { return a.id == me }
        if a.lastSeenMs != b.lastSeenMs { return a.lastSeenMs > b.lastSeenMs }
        if a.createdMs != b.createdMs { return a.createdMs > b.createdMs }
        return jvmLess(a.id, b.id)
    }
}

/// The last four hex digits of a device ID, as shown next to a name.
public func shortId(_ id: String) -> String { id.jvmTakeLast(4) }

/// Who an event says caused it (Event.origin), for display, or nil when
/// the event does not say or this client did it: what was done here is
/// not labelled. Only a signed client is named, and only by the account's
/// device list; one the list no longer has was removed, and anything
/// unsure is "另一客户端", never a guess. Typing on the computer is
/// "<machine> 终端", as is a message the client tells was typed there
/// (`typedHere`).
public func originLabel(_ o: PBOrigin?, state: RemoteState, me: String, machine: String) -> String? {
    guard let o else { return nil }
    switch o.kind {
    case .client:
        if o.deviceID == me { return nil }
        // Before the device list is read nothing is known of who it was.
        if o.deviceID.isEmpty || state.devices.isEmpty { return "另一客户端" }
        if let d = state.devices[o.deviceID] { return clientName(d, id: o.deviceID) }
        return "已移除的设备"
    case .local: return terminalLabel(machine)
    default: return nil
    }
}

/// Who typed a message in the computer's terminal: 「MacBook 终端」.
public func terminalLabel(_ machine: String) -> String {
    machine.isEmpty ? "终端" : "\(machine) 终端"
}

extension PBEvent {
    /// Who caused the event, when it says.
    public var originOrNil: PBOrigin? { hasOrigin ? origin : nil }
}

/// What the machine says it offers; nil until its metadata is read.
public func capabilities(_ m: MachineView?) -> PBMachineCapabilities? {
    guard let meta = m?.meta, meta.hasCapabilities else { return nil }
    return meta.capabilities
}

/// Why no command goes to `m`: it speaks another protocol version than this
/// app (`protocolVersion`), said by which side to update; nil when they
/// match or the machine's metadata is not read yet.
public func protocolMismatch(_ m: MachineView?) -> String? {
    guard let theirs = m?.meta?.protocolVersion else { return nil }
    if theirs < protocolVersion { return "\(machineName(m)) 上的 remote-agent 版本较旧，请在电脑上更新后再试" }
    if theirs > protocolVersion { return "这个 App 版本较旧，请更新后再试" }
    return nil
}

/// A model's context window as the pickers write it: 1M 上下文, 200k 上下文; nil when unknown.
public func contextText(tokens: Int64) -> String? {
    if tokens <= 0 { return nil }
    if tokens >= 1_000_000 {
        return "\("\(Double(tokens / 100_000) / 10.0)".removingSuffix(".0"))M 上下文"
    }
    return "\((tokens + 500) / 1000)k 上下文"
}

/// The standard context window, which a Claude Code session can be held to.
public let standardWindow: Int64 = 200_000
