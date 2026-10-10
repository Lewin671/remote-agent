// What the session screens say and decide, apart from how they draw it: the
// wording under a message that is on its way or waiting, whether the chat
// follows new content, what the navigation bar's second line says. On
// Android these sit inside the composables of ui/Session.kt; here they are
// functions of plain values so that the tests can hold them to the same
// words and the same decisions.
import Foundation
import RACore
import RAProto

// MARK: Following the end of the chat

/// Whether the chat stays at its end as content arrives, and what arrived
/// while the user was reading back. The rule is the package's `Follow`:
/// only a drag away from the bottom stops following, and reaching the bottom
/// starts it again; a scroll the app makes never turns it off.
struct ChatScroll: Equatable, Sendable {
    private var follow = Follow()
    private(set) var atBottom = true
    /// How many rows there were when the user last saw the end.
    private(set) var seen = 0

    var following: Bool { follow.on }

    /// The list moved, or was measured again.
    mutating func observe(userDragging: Bool, atBottom: Bool, rows: Int) {
        self.atBottom = atBottom
        follow.observe(userDragging: userDragging, atBottom: atBottom)
        if atBottom { seen = rows }
    }

    /// The content or the room for it changed. True when the list should go
    /// to its end: a growing reply, a new row, the keyboard or the dock
    /// coming up all keep the end in sight for someone who is at it, and
    /// move nothing for someone who is reading back.
    mutating func contentChanged(rows: Int) -> Bool {
        if follow.on { seen = rows }
        return follow.on
    }

    /// The user asked for the end.
    mutating func jumpToEnd(rows: Int) {
        follow = Follow()
        seen = rows
    }

    /// Rows that came in while reading back; the list keeps following otherwise.
    func fresh(rows: Int) -> Int { atBottom ? 0 : max(0, rows - seen) }
}

// MARK: Messages waiting in the daemon's queue

/// What stands under a message the model has not received yet.
func pendingLabel(
    _ p: PendingInput, cancel: SessionStore.CancelState?, resuming: Bool, resumeFailed: Bool, starting: String?
) -> String {
    if cancel == .canceling { return "正在取消…" }
    if case .failed = cancel { return "取消没成功，仍在排队" }
    if resuming { return "正在发送…" }
    if resumeFailed { return "发送没成功，仍已暂停" }
    if p.paused { return "已暂停 · 中断前排着的" }
    if p.held { return "插入这一轮 · 等当前工具结束后并入" }
    if p.mode == .queue { return "排队 · 这一轮结束后发" }
    // No turn to insert into yet: the CLI is still coming up.
    if let starting { return "已送达电脑 · 等 \(starting) 就绪后发出" }
    return "插入这一轮 · 等输入框就绪"
}

/// What follows the label: who sent it when it was not this client, else its place in the queue.
func pendingSuffix(_ p: PendingInput, origin: String?, index: Int) -> String {
    if let origin, origin != "本客户端" { return " · \(origin)" }
    return index > 0 && p.mode == .queue ? " · 第 \(index) 条" : ""
}

/// The queued messages to show: more than three fold to the first two until opened.
func shownPending(_ all: [PendingInput], expanded: Bool) -> [PendingInput] {
    all.count > 3 && !expanded ? Array(all.prefix(2)) : all
}

// MARK: Messages sent from here

enum OutgoingAction: Equatable, Sendable {
    case retry, remove, putBack, terminal

    var label: String {
        switch self {
        case .retry: "重试"
        case .remove: "移除此提示"
        case .putBack: "放回输入框"
        case .terminal: "去终端"
        }
    }
}

enum NoteTone: Equatable, Sendable { case plain, warn, error }

/// How a message sent from here stands, in words, and what can be done about it.
struct OutgoingNote: Equatable, Sendable {
    var caption: String
    var tone = NoteTone.plain
    var sending = false
    /// Drawn as a message that did not go: outlined in red.
    var failed = false
    var actions: [OutgoingAction] = []
}

/// `input`: what the daemon's events say of the message, nil while they say nothing yet.
func outgoingNote(
    state: Outgoing.State, error: String, input: PBInputStatus?, failure: String?, agent: PBAgent
) -> OutgoingNote {
    let failed = input == .failed || input == .needsTerminal || (input == nil && state != .sending && state != .sent)
    switch input {
    case .failed:
        // Nothing was submitted and the terminal is as it was.
        return OutgoingNote(
            caption: "没能发送：" + failureText(reason: failure ?? "", agent: agent) + "。电脑的输入框已恢复原样", tone: .error,
            failed: failed, actions: [.putBack])
    case .needsTerminal:
        // Not submitted, and part of it may still be in the input box.
        return OutgoingNote(
            caption: "没能发送：" + failureText(reason: failure ?? "", agent: agent) + "。电脑的输入框里可能还留着一部分，不会自动重发",
            tone: .error, failed: failed, actions: [.terminal, .putBack])
    case .unconfirmed:
        return OutgoingNote(
            caption: "已送到终端，但没能确认 \(agentName(agent)) 收到，不会自动重发", tone: .warn, failed: failed, actions: [.remove])
    default:
        break
    }
    switch state {
    case .sending: return OutgoingNote(caption: "发送中…", sending: true, failed: failed)
    case .sent: return OutgoingNote(caption: "已送达电脑", failed: failed)
    // The same request again: the daemon runs a request ID at most once.
    case .unknown: return OutgoingNote(caption: "没能确认送达：" + error, tone: .error, failed: failed, actions: [.retry, .remove])
    case .failed: return OutgoingNote(caption: "没能送达：" + error, tone: .error, failed: failed, actions: [.retry, .remove])
    }
}

/// Whether a message sent from here is still shown under the chat: until
/// the daemon's events have it, and for as long as they say it did not go.
/// The events are the truth: once they have the message queued, submitted
/// or canceled, a try from here that failed or was left unknown did arrive.
func outgoingShown(input: PBInputStatus?) -> Bool {
    input == nil || input == .unconfirmed || input == .failed || input == .needsTerminal
}

// MARK: The message box

/// The placeholder of the message box.
func composerPlaceholder(_ d: Derived) -> String {
    if !d.questions.isEmpty { return "排队发送，回答后继续" }
    return d.tools.isEmpty ? "消息" : "消息…"
}

/// Whether what is in the box can be sent now.
func composerCanSend(text: String, online: Bool, keeping: Bool, hasImages: Bool, imagesReady: Bool, refused: Bool) -> Bool {
    !keeping && online && !refused && (!text.jvmIsBlank || hasImages) && (!hasImages || imagesReady)
}

/// Text starting with a slash is sent to the model as it is: the CLIs' slash commands are not run from here.
func slashWarning(_ text: String) -> Bool {
    text.drop { $0.isWhitespace }.hasPrefix("/")
}

let slashWarningText = "以 / 开头的内容会作为普通消息发给模型，而不是执行斜杠命令。"

// MARK: The navigation bar

/// The second line of a session's navigation bar: its state, then its
/// approval mode, which alone is red when it is full access. A session
/// that ended only says so, and a CLI that is still starting has not
/// reported a mode yet.
struct SessionSubtitle: Equatable, Sendable {
    var status: String
    /// "" when no mode is written.
    var mode: String
    var danger: Bool

    var text: String { mode.isEmpty ? status : "\(status) · \(mode)" }
}

func sessionSubtitle(status: Status, mode: ModeLabel?) -> SessionSubtitle {
    if status == .exited || (status == .starting && mode == nil) {
        return SessionSubtitle(status: status.text, mode: "", danger: false)
    }
    return SessionSubtitle(status: status.text, mode: mode?.text ?? "审批：CLI 配置（未读到）", danger: mode?.mode == .full)
}

// MARK: The view

/// Whether the session's terminal can be shown now. An ended session and
/// one whose computer is away have none: the page shows the chat meanwhile
/// and leaves the view picked on this device as it was (design-system §6
/// 「视图切换」).
func terminalAvailable(_ status: Status) -> Bool {
    status != .exited && status != .offline
}

// MARK: The menu

/// 分叉会话 in a session's menu: what the row says, and what a tap does.
struct ForkAction: Equatable, Sendable {
    enum Tap: Equatable, Sendable { case fork, abandon }

    var text = "分叉会话"
    var note = ""
    /// The note is something that went wrong, not when the row can be used.
    var noteError = false
    var enabled = true
    var busy = false
    var tap = Tap.fork
}

/// `blocked`: why the session cannot be forked now (`forkBlocked`). The
/// rest is how a fork asked from here stands (`SessionStore.Fork`). A fork
/// that may have happened is only ever sent again as it was; once that
/// request has expired the row gives it up, and only the tap after that
/// asks for a new fork.
func forkAction(blocked: String?, running: Bool, error: String, frozen: Bool, expired: Bool) -> ForkAction {
    if running { return ForkAction(text: "正在分叉…", busy: true) }
    if let blocked { return ForkAction(note: blocked, enabled: false) }
    if expired {
        return ForkAction(
            text: "放弃上次的分叉", note: "不确定上次有没有分叉出新会话，它已过期，不会再重发。请先看看会话列表", noteError: true, tap: .abandon)
    }
    if frozen { return ForkAction(note: "上次的分叉结果不确定。再点会重发同一个请求，电脑收到过就不会再分叉一次", noteError: true) }
    return ForkAction(note: error.isEmpty ? "" : "没能分叉：\(error)", noteError: true)
}

// MARK: In place of the message box

/// What the card says when a session ended, and whether it can be resumed from here now.
func endedText(detail: String, unlinked: Bool, machineOnline: Bool, machine: String) -> String {
    let how = detail == "stopped" ? "会话是被手动结束的。" : "tmux 会话已经不在了。"
    if unlinked { return how + "连上服务器后才能恢复。" }
    if machineOnline { return how + "恢复会在 \(machine) 上用同样的设置重新打开它，聊天记录都在。" }
    return how + "\(machine) 不在线，连上后才能恢复。"
}

/// What the card says when the session's computer is away. `lastSeen` is when it went, in words, or "".
func offlineText(lastSeen: String) -> String {
    (lastSeen.isEmpty ? "" : lastSeen + "断开，") + "可能是合盖睡眠、断网，或 remote-agent 没在运行。电脑上的会话可能还在跑，重新连上后这里会自动更新，现在只能看历史。"
}

// MARK: Telling VoiceOver what arrived

/// Decides which arrivals VoiceOver is told about. A reply or something
/// that needs the user is said; tool lines, notes and the user's own
/// messages are not, and nothing is said more often than once in a while,
/// so a busy turn does not talk over itself.
struct LiveAnnouncer: Equatable, Sendable {
    private var lastKey: String?
    private var lastAt: Int64 = 0
    private var waiting = Set<String>()
    /// The least time between two announcements, in ms.
    static let gapMs: Int64 = 4_000

    /// What to say now, if anything. `rows` is the chat as it is now, `dock` what waits for the user.
    mutating func announce(last: ChatItem?, dock: [DockItem], agent: String, now: Int64, initial: Bool) -> String? {
        let ids = Set(dock.map(\.id))
        let arrived = ids.subtracting(waiting)
        waiting = ids
        let key = last?.key
        let newReply = key != lastKey
        lastKey = key
        // What was already there when the page opened is read, not announced.
        if initial { return nil }
        if let first = dock.first(where: { arrived.contains($0.id) }) {
            lastAt = now
            return dockSummary(first, agent: agent).label
        }
        guard newReply, case .agent = last, now - lastAt >= Self.gapMs else { return nil }
        lastAt = now
        return "\(agent) 回复了"
    }
}
