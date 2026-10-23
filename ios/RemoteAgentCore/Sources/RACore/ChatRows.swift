// The chat's rows, built from a session's events: what Android's ui/Chat.kt
// works out before it draws anything. The same rules as the web's
// web/src/model.ts, so the three clients show a session alike.
import Foundation
import RAProto

/// A new session's first message as its page shows it before the history has it.
/// `warning`: it may not have gone (orange); `failed`: it did not (red).
public struct StartPrompt: Equatable, Sendable {
    public var text: String
    public var caption: String
    public var warning: Bool
    public var failed: Bool

    public init(text: String, caption: String, warning: Bool = false, failed: Bool = false) {
        self.text = text
        self.caption = caption
        self.warning = warning
        self.failed = failed
    }
}

/// What stands under a message that did not go: 「没发出去：<原因>」.
public func notSentText(_ why: String) -> String { "没发出去：\(why)" }

/// What stands under a message that may not have gone: 「可能没发出去：<原因>」.
public func maybeNotSentText(_ why: String) -> String { "可能没发出去：\(why)" }

/// Why a message written to the terminal may not have gone: the CLI did not say it took it.
public let unconfirmedText = "已送到终端但没确认"

/// A new session's acknowledged prompt until history records it; never resend it here.
public func pendingStartPrompt(_ prompt: String?, events: some Sequence<PBEvent>, d: Derived, localIds: Set<String>, agent: PBAgent) -> StartPrompt? {
    guard let prompt, !prompt.isEmpty, !events.contains(where: { if case .userMessage = $0.kind { return true } else { return false } })
    else { return nil }
    let first = d.inputs.keys.first { !localIds.contains($0) }
    let status = first.flatMap { d.inputs[$0] }
    let why = failureText(reason: first.flatMap { d.failures[$0] } ?? "", agent: agent)
    switch status {
    case .canceled: return nil
    case .failed: return StartPrompt(text: prompt, caption: notSentText(why), failed: true)
    case .needsTerminal: return StartPrompt(text: prompt, caption: maybeNotSentText(why), warning: true)
    case .unconfirmed: return StartPrompt(text: prompt, caption: maybeNotSentText(unconfirmedText), warning: true)
    // Still waiting: the pending bubble shows it, with what it waits for and a way to cancel.
    case .queued, .heldByCli: return nil
    default: return StartPrompt(text: prompt, caption: "已送达电脑")
    }
}

/// The mark before a record line; the screens choose the picture.
public enum NoteIcon: Sendable { case check, x }

/// What the chat shows, built from the events.
public enum ChatItem: Equatable, Sendable, Identifiable {
    case user(User)
    case generated(Generated)
    case agent(Agent)
    case asyncQuestion(AsyncQuestion)
    case thought(Thought)
    case tool(Tool)
    case running(Running)
    case tools(Tools)
    case note(Note)
    case plan(Plan)
    case unsent(Unsent)
    case turnEnd(TurnEnd)
    case fork(Fork)

    /// `from`: who sent it when it was not this client: another client's name, or the computer's terminal.
    public struct User: Equatable, Sendable {
        public var key: String
        public var text: String
        public var local: Bool
        public var time: Int64
        public var from: String?
        public var images: [PBImageRef]
        public var seq: UInt64

        public init(key: String, text: String, local: Bool, time: Int64, from: String? = nil, images: [PBImageRef] = [], seq: UInt64 = 0) {
            self.key = key
            self.text = text
            self.local = local
            self.time = time
            self.from = from
            self.images = images
            self.seq = seq
        }
    }

    /// Images the agent made, as a card of their own.
    public struct Generated: Equatable, Sendable {
        public var key: String
        public var seq: UInt64
        public var prompt: String
        public var images: [PBImageRef]

        public init(key: String, seq: UInt64, prompt: String, images: [PBImageRef]) {
            self.key = key
            self.seq = seq
            self.prompt = prompt
            self.images = images
        }
    }

    public struct Agent: Equatable, Sendable {
        public var key: String
        public var text: String

        public init(key: String, text: String) {
            self.key = key
            self.text = text
        }
    }

    /// Ordinary AI text, answered through the composer rather than a question card.
    public struct AsyncQuestion: Equatable, Sendable {
        public var key: String
        public var questions: [PBQuestion]

        public init(key: String, questions: [PBQuestion]) {
            self.key = key
            self.questions = questions
        }
    }

    public struct Thought: Equatable, Sendable {
        public var key: String
        public var text: String

        public init(key: String, text: String) {
            self.key = key
            self.text = text
        }
    }

    /// One tool call; `decision` says how its approval went, if it needed one.
    /// `background`: the call returned but its command went on in the
    /// background, so how it ends is not known.
    public struct Tool: Equatable, Sendable {
        public var key: String
        public var call: PBToolCall
        public var decision: String
        public var decisionOk: Bool
        public var background: Bool
        public var seq: UInt64

        public init(key: String, call: PBToolCall, decision: String = "", decisionOk: Bool = true, background: Bool = false, seq: UInt64 = 0) {
            self.key = key
            self.call = call
            self.decision = decision
            self.decisionOk = decisionOk
            self.background = background
            self.seq = seq
        }
    }

    /// A tool that started and has no result yet, where it started. With
    /// `unfinished` its turn ended first: it may still run in the background.
    public struct Running: Equatable, Sendable {
        public var key: String
        public var tool: RunningTool
        public var unfinished: Bool

        public init(key: String, tool: RunningTool, unfinished: Bool) {
            self.key = key
            self.tool = tool
            self.unfinished = unfinished
        }
    }

    /// Two or more completed tool calls in a row, folded into one line.
    public struct Tools: Equatable, Sendable {
        public var key: String
        public var calls: [PBToolCall]
        public var callSeqs: [UInt64]

        public init(key: String, calls: [PBToolCall], callSeqs: [UInt64] = []) {
            self.key = key
            self.calls = calls
            self.callSeqs = callSeqs
        }
    }

    /// A line of its own: a centred note, or with `icon` a record like a tool row, `meta` at its right.
    /// `tone`: 0 plain, 1 allowed, 2 refused.
    public struct Note: Equatable, Sendable {
        public var key: String
        public var text: String
        public var icon: NoteIcon?
        public var tone: Int
        public var meta: String

        public init(key: String, text: String, icon: NoteIcon? = nil, tone: Int = 0, meta: String = "") {
            self.key = key
            self.text = text
            self.icon = icon
            self.tone = tone
            self.meta = meta
        }
    }

    public struct Plan: Equatable, Sendable {
        public var key: String
        public var text: String

        public init(key: String, text: String) {
            self.key = key
            self.text = text
        }
    }

    /// A message the session ended before writing; `text` is known when sent from here.
    public struct Unsent: Equatable, Sendable {
        public var key: String
        public var requestId: String
        public var text: String?

        public init(key: String, requestId: String, text: String?) {
            self.key = key
            self.requestId = requestId
            self.text = text
        }
    }

    public struct TurnEnd: Equatable, Sendable {
        public var key: String
        public var text: String

        public init(key: String, text: String) {
            self.key = key
            self.text = text
        }
    }

    /// Where a forked session leaves its source: `time` 分叉自 the source, by the title it had then.
    public struct Fork: Equatable, Sendable {
        public var key: String
        public var time: String
        public var sourceId: String
        public var sourceTitle: String

        public init(key: String, time: String, sourceId: String, sourceTitle: String) {
            self.key = key
            self.time = time
            self.sourceId = sourceId
            self.sourceTitle = sourceTitle
        }
    }

    /// The row's identity in the list: "e<seq>" of the event it stands at, "u<request>" for an unsent message.
    public var key: String {
        switch self {
        case .user(let x): return x.key
        case .generated(let x): return x.key
        case .agent(let x): return x.key
        case .asyncQuestion(let x): return x.key
        case .thought(let x): return x.key
        case .tool(let x): return x.key
        case .running(let x): return x.key
        case .tools(let x): return x.key
        case .note(let x): return x.key
        case .plan(let x): return x.key
        case .unsent(let x): return x.key
        case .turnEnd(let x): return x.key
        case .fork(let x): return x.key
        }
    }

    public var id: String { key }
}

/// How the fork line names the source: its title now when the session is
/// held here, else the one the event recorded. The line opens the source
/// either way: one on a page of the listing not fetched yet is fetched by
/// its ID when opened (architecture §8 「按 ID 取」).
public func forkSource(_ item: ChatItem.Fork, sessions: [String: SessionView], zone: TimeZone = .current) -> String {
    if let s = sessions[item.sourceId], s.info != nil { return titleOf(s, zone: zone) }
    return plain(item.sourceTitle).ifEmpty("另一个会话")
}

public func toolOk(_ t: PBToolCall) -> Bool { t.status != "failed" && (!t.hasExitCode || t.exitCode == 0) }

/// How a tool call is named in the chat: paths shortened, as on the web; the details keep them whole.
public func toolTitle(_ t: PBToolCall, cwd: String, home: String = "") -> String {
    toolTitle(kind: t.kind, title: t.title, itemType: t.itemType, cwd: cwd, home: home)
}

/// The same for a call still running, so that its row reads the same before and after.
public func toolTitle(_ t: RunningTool, cwd: String, home: String = "") -> String {
    toolTitle(kind: t.kind, title: t.title, itemType: t.itemType, cwd: cwd, home: home)
}

/// Codex's item types, which are not names to show.
private func rawItemType(_ s: String) -> Bool { s == "ToolCall" || s == "CommandExecution" }

private func toolTitle(kind: PBToolKind, title: String, itemType: String, cwd: String, home: String) -> String {
    // A file edit's title is one path, spaces and all.
    if kind == .fileChange { return "编辑 " + shortenPath(title, cwd: cwd, home: home) }
    let fallback: String
    if rawItemType(itemType) {
        fallback = kind == .command ? "命令" : "工具"
    } else {
        fallback = itemType.ifEmpty("工具")
    }
    return shortenPaths((rawItemType(title) ? "" : title).ifEmpty(fallback), cwd: cwd, home: home)
}

/// What a call's details show as its input: the tool's input, or else the
/// title as the CLI wrote it when the row shortened its paths (a command's
/// input is often its title). Empty when there is nothing more to show.
public func toolRaw(_ t: PBToolCall, cwd: String, home: String) -> String {
    if !t.input.isEmpty && t.input != t.title { return t.input }
    if rawItemType(t.title) || (t.title.isEmpty && rawItemType(t.itemType)) { return "" }
    let asIs = t.kind == .fileChange ? "编辑 " + t.title : t.title.ifEmpty(t.itemType)
    return asIs != toolTitle(t, cwd: cwd, home: home) ? t.title.ifEmpty(t.itemType) : ""
}

/// "运行了 3 条命令" and the like, for a folded run.
public func toolsSummary(_ calls: [PBToolCall]) -> String {
    let what: String
    if calls.allSatisfy({ $0.kind == .command }) {
        what = "运行了 \(calls.count) 条命令"
    } else if calls.allSatisfy({ $0.kind == .fileChange }) {
        what = "编辑了 \(calls.count) 个文件"
    } else {
        what = "完成了 \(calls.count) 个工具调用"
    }
    let images = calls.reduce(0) { $0 + $1.images.count }
    return what + (images > 0 ? " · \(images) 张截图" : "")
}

/// Canceled, or answered in the CLI's own prompt: the call that follows shows what happened.
private func keepsRecord(_ d: PBApprovalDecision) -> Bool { d != .canceled && d != .terminal }

private func decisionLine(_ d: PBApprovalDecision, message: String) -> String {
    decisionText(d) + (message.isEmpty ? "" : " · " + (d == .allowAlways ? approvalAlwaysText(message) : message))
}

/// A decision as its tool call's row writes it.
public struct CallDecision: Equatable, Sendable {
    public var text: String
    /// Not a refusal.
    public var ok: Bool

    public init(text: String, ok: Bool) {
        self.text = text
        self.ok = ok
    }
}

/// Decisions to show on their tool calls (by the call's seq), and the approvals they came from.
public struct ApprovalDecisions: Equatable, Sendable {
    public var onCall: [UInt64: CallDecision]
    public var merged: Set<String>

    public init(onCall: [UInt64: CallDecision], merged: Set<String>) {
        self.onCall = onCall
        self.merged = merged
    }
}

private struct DigestKey: Hashable {
    var tool: String
    var digest: String
}

/// Which decided approvals are shown on their tool call instead of a line
/// of their own, exactly as approvalsOnCalls in web/src/model.ts. Only the
/// daemon's digests count (ApprovalRequested.input_digest against
/// ToolCall.input_digest, the same computation on the daemon), never one
/// worked out here: the tool and digest must match exactly one
/// request (including pending requests in the count), which has a decision, and exactly one call, and the call must come after the request.
/// Codex gives no digest, so its approvals keep their
/// own line; so do parallel or repeated calls that cannot be told apart.
public func approvalDecisions(_ events: some Sequence<PBEvent>, d: Derived) -> ApprovalDecisions {
    var reqs = [DigestKey: [(id: String, seq: UInt64)]]()
    var calls = [DigestKey: [UInt64]]()
    for e in events {
        switch e.kind {
        case .approvalRequested(let a):
            if a.inputDigest.isEmpty { continue }
            reqs[DigestKey(tool: a.toolName, digest: a.inputDigest), default: []].append((a.approvalID, e.seq))
        case .toolCall(let t):
            if !t.inputDigest.isEmpty { calls[DigestKey(tool: t.itemType, digest: t.inputDigest), default: []].append(e.seq) }
        default: break
        }
    }
    var onCall = [UInt64: CallDecision]()
    var merged = Set<String>()
    for (key, rs) in reqs {
        let cs = calls[key] ?? []
        guard rs.count == 1, cs.count == 1, cs[0] >= rs[0].seq, let r = d.resolved[rs[0].id] else { continue }
        onCall[cs[0]] = CallDecision(text: decisionLine(r.decision, message: r.message), ok: r.decision != .deny && r.decision != .timeout)
        merged.insert(rs[0].id)
    }
    return ApprovalDecisions(onCall: onCall, merged: merged)
}

/// The chat's rows. Runs of two or more completed tool calls fold into one
/// line. A decided approval is a line where it was asked, unless
/// approvalDecisions ties it to its call, which then carries the decision.
/// `textOf` gives the text of a message sent from here by its request ID;
/// `who` names who caused an event (Event.origin), see `originLabel`.
public func buildChat(
    _ events: some RandomAccessCollection<PBEvent>,
    d: Derived,
    machine: String,
    cwd: String = "",
    home: String = "",
    now: Int64 = nowMs(),
    zone: TimeZone = .current,
    textOf: (String) -> String? = { _ in nil },
    who: (PBOrigin?) -> String? = { _ in nil }
) -> [ChatItem] {
    var out = [ChatItem]()
    var run = [PBToolCall]()
    var runSeqs = [UInt64]()
    var runKey = ""
    let decisions = approvalDecisions(events, d: d)
    var turnTokens: Int64 = 0
    var lastTotal: Int64 = 0
    func flush() {
        switch run.count {
        case 0: break
        case 1: out.append(.tool(.init(key: runKey, call: run[0], seq: runSeqs[0])))
        default: out.append(.tools(.init(key: runKey, calls: run, callSeqs: runSeqs)))
        }
        run.removeAll()
        runSeqs.removeAll()
    }
    // Messages that never went out, by the seq that says so.
    var unsentAt = [UInt64: [String]]()
    for (id, seq) in d.unsent.entries { unsentAt[seq, default: []].append(id) }
    // A started tool keeps its place: its call, once it comes, is shown
    // where it started (the same row, updated), not where it completed.
    var callAt = [UInt64: PBEvent]()
    var placed = Set<UInt64>()
    for (startSeq, callSeq) in d.completedAt where callSeq >= 1 && callSeq - 1 < UInt64(events.count) {
        let e = events[events.index(events.startIndex, offsetBy: Int(callSeq - 1))]
        if e.seq == callSeq {
            callAt[startSeq] = e
            placed.insert(callSeq)
        }
    }
    var running = [UInt64: RunningTool]()
    for t in d.tools { running[t.seq] = t }
    var startedTools = [UInt64: RunningTool]()
    for e in events {
        if case .toolStarted(let t) = e.kind {
            startedTools[e.seq] = RunningTool(
                title: t.title, callId: t.callID, since: eventTime(e), kind: t.kind, itemType: t.itemType, seq: e.seq,
                input: t.input, inputDigest: t.inputDigest)
        }
    }
    func addCall(_ k: String, _ callEvent: PBEvent, background: Bool) {
        let t = callEvent.toolCall
        if let decided = decisions.onCall[callEvent.seq] {
            // Kept out of a fold so that its decision stays in sight.
            flush()
            // The approval is said next to it; whether it went to the background still shows.
            out.append(.tool(.init(key: k, call: t, decision: decided.text, decisionOk: decided.ok, background: background, seq: callEvent.seq)))
        } else if background {
            flush()
            out.append(.tool(.init(key: k, call: t, background: true, seq: callEvent.seq)))
        } else if toolOk(t) {
            if run.isEmpty { runKey = k }
            run.append(t)
            runSeqs.append(callEvent.seq)
        } else {
            flush()
            out.append(.tool(.init(key: k, call: t, seq: callEvent.seq)))
        }
    }
    for e in events {
        let k = "e\(e.seq)"
        if isSkeleton(e) {
            flush()
            out.append(.agent(.init(key: k, text: skeletonMessage)))
            continue
        }
        if let ids = unsentAt[e.seq] {
            flush()
            for id in ids { out.append(.unsent(.init(key: "u\(id)", requestId: id, text: textOf(id)))) }
        }
        switch e.kind {
        case .toolStarted:
            if let c = callAt[e.seq] {
                addCall(k, c, background: wentToBackground(c.toolCall))
            } else {
                let r = running[e.seq]
                if r != nil || d.unfinished.contains(e.seq), let tool = r ?? startedTools[e.seq] {
                    flush()
                    out.append(.running(.init(key: k, tool: tool, unfinished: r == nil)))
                }
            }
        case .toolCall(let c):
            if !placed.contains(e.seq) { addCall(k, e, background: wentToBackground(c)) }
        case .userMessage(let m):
            flush()
            out.append(
                .user(
                    .init(
                        key: k, text: imageText(m.text, refs: m.images), local: d.typedLocally.contains(e.seq), time: eventTime(e),
                        from: sender(e, d: d, machine: machine, who: who), images: m.images, seq: e.seq)))
        case .agentMessage(let m):
            if !m.text.jvmIsBlank {
                flush()
                out.append(.agent(.init(key: k, text: m.text)))
            }
        case .questionAsked(let q):
            if q.async {
                flush()
                out.append(.asyncQuestion(.init(key: k, questions: q.questions)))
            }
        case .reasoning(let r):
            if !r.text.jvmIsBlank {
                flush()
                out.append(.thought(.init(key: k, text: r.text)))
            }
        case .approvalRequested(let a):
            // A decided request is a line where it was asked, unless its
            // call carries the decision instead.
            guard let r = d.resolved[a.approvalID], !decisions.merged.contains(a.approvalID), keepsRecord(r.decision) else { break }
            flush()
            let ok = r.decision == .allow || r.decision == .allowAlways
            let what = shortenPaths(a.command.ifEmpty(a.toolName.ifEmpty(a.description_p)), cwd: cwd, home: home)
            let by = who(d.resolvedBy[a.approvalID])
            out.append(
                .note(
                    .init(
                        key: k, text: what, icon: ok ? .check : .x, tone: ok ? 1 : 2,
                        meta: decisionLine(r.decision, message: r.message) + (by.map { " · \($0)" } ?? ""))))
        case .questionAnswered(let q):
            flush()
            let answers = q.answers.flatMap(\.answers)
            let by = who(e.originOrNil)
            out.append(
                .note(
                    .init(
                        key: k,
                        text: (answers.isEmpty ? "已回答提问" : "已回答：" + answers.joined(separator: "、")) + (by.map { " · \($0)" } ?? ""),
                        icon: .check, tone: 1)))
        case .imageGenerated(let g):
            if !g.images.isEmpty {
                flush()
                out.append(.generated(.init(key: k, seq: e.seq, prompt: g.revisedPrompt, images: g.images)))
            }
        case .planProposed(let p):
            flush()
            out.append(.plan(.init(key: k, text: p.text)))
        case .tokenUsage(let u):
            if lastTotal > 0 { turnTokens += max(0, u.totalTokens - lastTotal) }
            lastTotal = u.totalTokens

        case .turnStarted: turnTokens = 0
        case .turnEnded(let t):
            flush()
            var parts = [String]()
            if t.outcome == .aborted { parts.append("已中断" + (who(e.originOrNil).map { "（\($0)）" } ?? "")) }
            if t.durationMs > 0 { parts.append("本轮用时 " + duration(t.durationMs)) }
            if turnTokens > 0 { parts.append(turnTokens >= 1000 ? "\((turnTokens + 500) / 1000)k tokens" : "\(turnTokens) tokens") }
            if !parts.isEmpty { out.append(.turnEnd(.init(key: k, text: parts.joined(separator: " · ")))) }
        case .settingsChanged(let c):
            let by = who(e.originOrNil)
            let text: String?
            if c.compacted {
                if let usage = d.compactions[e.seq], let before = usage.before, let after = usage.after {
                    text = "上下文已压缩 · \(before)% → \(after)%"
                } else { text = "上下文已压缩" }
            } else if !c.model.isEmpty {
                text = (by.map { "\($0) 将模型切到 " } ?? "切换到 ") + c.model + (c.effort.isEmpty ? "" : " · \(c.effort)")
            } else {
                text = modeOf(c.mode).map { (by.map { "\($0) 将模式切到 " } ?? "模式切换到 ") + $0.text }
            }
            if let text {
                flush()
                out.append(.note(.init(key: k, text: text)))
            }
        case .turnContext:
            if let change = d.terminalChanges[e.seq] {
                flush()
                out.append(.note(.init(key: k, text: "\(machine) 终端里\(change)")))
            }
        case .sessionState(let s):
            if s.status == .exited {
                flush()
                out.append(.turnEnd(.init(key: k, text: dateTime(eventTime(e), now: now, zone: zone) + " 结束")))
            }
        case .sessionForked(let f):
            flush()
            out.append(.fork(.init(key: k, time: dateTime(eventTime(e), now: now, zone: zone), sourceId: f.sourceSessionID, sourceTitle: f.sourceTitle)))
        default: break
        }
    }
    flush()
    return out
}

/// Who sent a message, said only when it was not this client: the
/// daemon's word, else the computer's terminal when the client tells it
/// was typed there.
func sender(_ e: PBEvent, d: Derived, machine: String, who: (PBOrigin?) -> String?) -> String? {
    who(e.originOrNil) ?? (d.typedLocally.contains(e.seq) ? terminalLabel(machine) : nil)
}

/// 12 秒, 4 分 12 秒, 1 小时 3 分: how long a turn took.
public func duration(_ ms: Int64) -> String {
    let s = ms / 1000
    if s < 60 { return "\(s) 秒" }
    if s < 3600 { return "\(s / 60) 分 \(s % 60) 秒" }
    return "\(s / 3600) 小时 \(s % 3600 / 60) 分"
}

/// Ordinary messages append without rebuilding old rows. State-changing events use the full builder.
public struct ChatProjection: Sendable {
    private struct Context: Equatable {
        var machine: String
        var cwd: String
        var home: String
        var agent: PBAgent
    }

    private var previous = PagedList<PBEvent>()
    private var items = [ChatItem]()
    private var context: Context?

    public init() {}

    public mutating func update(
        _ events: some RandomAccessCollection<PBEvent>,
        d: Derived,
        machine: String,
        cwd: String = "",
        home: String = "",
        agent: PBAgent = .codex,
        now: Int64 = nowMs(),
        zone: TimeZone = .current,
        textOf: (String) -> String? = { _ in nil },
        who: (PBOrigin?) -> String? = { _ in nil }
    ) -> [ChatItem] {
        let next = Context(machine: machine, cwd: cwd, home: home, agent: agent)
        // Published histories share immutable pages. Check their append lineage
        // instead of comparing every old protobuf on every arriving message.
        let append = context == next && !previous.isEmpty && events.count > previous.count
            && ((events as? PagedList<PBEvent>).map { $0.extends(previous) }
                ?? zip(events, previous).allSatisfy { $0 == $1 })
        let tail = append ? Array(events.dropFirst(previous.count)) : []
        let messages = append && tail.allSatisfy {
            switch $0.kind {
            case .userMessage, .agentMessage, .reasoning: return true
            default: return false
            }
        }
        if messages {
            for e in tail {
                let key = "e\(e.seq)"
                switch e.kind {
                case .userMessage(let m):
                    items.append(
                        .user(
                            .init(
                                key: key, text: imageText(m.text, refs: m.images), local: d.typedLocally.contains(e.seq),
                                time: eventTime(e), from: sender(e, d: d, machine: machine, who: who), images: m.images, seq: e.seq)))
                case .agentMessage(let m): if !m.text.jvmIsBlank { items.append(.agent(.init(key: key, text: m.text))) }
                case .reasoning(let r): if !r.text.jvmIsBlank { items.append(.thought(.init(key: key, text: r.text))) }
                default: break
                }
            }
            previous = (events as? PagedList<PBEvent>) ?? previous.appending(tail)
        } else {
            items = buildChat(events, d: d, machine: machine, cwd: cwd, home: home, now: now, zone: zone, textOf: textOf, who: who)
            previous = (events as? PagedList<PBEvent>) ?? PagedList(events)
        }
        context = next
        return items
    }
}
