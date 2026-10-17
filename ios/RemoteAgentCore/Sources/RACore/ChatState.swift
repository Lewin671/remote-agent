// What a session's events say is pending and running: approvals, questions,
// the plan, the message queue, and the tools of the turn (architecture §13,
// 工具执行反馈). From Android's core/Model.kt; the web has the same in
// web/src/model.ts.
import Foundation
import RAProto

// --- events ---

public struct RunningTool: Equatable, Sendable {
    public var title: String
    public var callId: String
    /// Event time of the start, from the daemon; 0 when unknown.
    public var since: Int64
    public var kind: PBToolKind
    public var itemType: String
    /// Seq of the ToolStarted event; its row in the chat keeps this place.
    public var seq: UInt64
    public var input: String
    public var inputDigest: String

    public init(
        title: String, callId: String, since: Int64, kind: PBToolKind = .unspecified, itemType: String = "",
        seq: UInt64 = 0, input: String = "", inputDigest: String = ""
    ) {
        self.title = title
        self.callId = callId
        self.since = since
        self.kind = kind
        self.itemType = itemType
        self.seq = seq
        self.input = input
        self.inputDigest = inputDigest
    }

}

/// The call went on in the background: this event is its start, not its
/// end. The daemon says so (ToolCall.background).
public func wentToBackground(_ c: PBToolCall) -> Bool {
    switch c.background {
    case .unspecified, .UNRECOGNIZED: return false
    default: return true
    }
}

/// Why a call is in the background and what that means, as the web client says it.
public func backgroundNote(_ b: PBToolBackground) -> String {
    let rest = "它什么时候结束这里不知道，请到终端确认。"
    let why: String
    switch b {
    case .requested: why = "按模型的要求在后台运行；"
    case .timedOut: why = "运行超时后被移到后台继续运行；"
    case .byUser: why = "在电脑终端里被移到后台（Ctrl+B）；"
    case .stillRunning: why = "这一轮结束时它仍在运行；"
    default: why = "调用已返回，命令转到了后台；"
    }
    return why + rest
}

/// A background call's title: 后台命令 for a shell call, else the tool's name with （后台）.
public func backgroundTitle(kind: PBToolKind, itemType: String, agent: PBAgent) -> String {
    kind == .command || kind == .unspecified ? "后台命令" : "\(toolLabel(kind: kind, itemType: itemType, agent: agent))（后台）"
}

/// A tool whose end is not known (architecture §13, 工具执行反馈): its call
/// went to the background, or the turn ended before its result came. It is
/// not shown as a success; the terminal is where to check.
public struct BackgroundTool: Equatable, Sendable {
    public var tool: RunningTool
    /// The call returned (it went to the background) rather than never coming back.
    public var returned: Bool

    public init(tool: RunningTool, returned: Bool) {
        self.tool = tool
        self.returned = returned
    }
}

/// A message as the daemon accepted it, and how far it has got.
public struct Accepted: Equatable, Sendable {
    public var text: String
    public var imageIds: [String]
    public var origin: PBOrigin?
    public var stage: PBInputStage

    public init(text: String, imageIds: [String], origin: PBOrigin?, stage: PBInputStage) {
        self.text = text
        self.imageIds = imageIds
        self.origin = origin
        self.stage = stage
    }
}

/// How an approval was decided, and the message that came with the decision.
public struct ApprovalOutcome: Equatable, Sendable {
    public var decision: PBApprovalDecision
    public var message: String

    public init(decision: PBApprovalDecision, message: String) {
        self.decision = decision
        self.message = message
    }
}

/// What a session's events add up to. A value: one handed out never changes
/// when more events are reduced.
public struct Compaction: Equatable, Sendable {
    public var before: Int?
    public var after: Int?
}

public struct Derived: Equatable, Sendable {
    /// Pending approvals, in the order they were asked.
    public var approvals = OrderedMap<String, PBApprovalRequested>()
    public var resolved = [String: ApprovalOutcome]()
    /// Who decided an approval, when the daemon says (v2).
    public var resolvedBy = [String: PBOrigin]()
    public var questions = OrderedMap<String, PBQuestionAsked>()
    public var answered = [String: [String]]()
    /// Latest state per request ID of messages sent with Send.
    public var inputs = OrderedMap<String, PBInputStatus>()
    /// Full latest input state: mode and pause are needed for pending bubbles.
    public var inputDetails = OrderedMap<String, PBInputState>()
    /// What the daemon accepted, per request ID: text, images and sender, shared by every client.
    public var accepted = [String: Accepted]()
    /// Why a message ended FAILED or NEEDS_TERMINAL, per request ID.
    public var failures = [String: String]()
    /// Messages that never went out because the session ended: request ID to
    /// the seq of the event that says so. The daemon records them as CANCELED
    /// with reason "exited"; ones queued before that existed, left queued
    /// when the session ended, count too.
    public var unsent = OrderedMap<String, UInt64>()
    public var running = false
    public var compactions = [UInt64: Compaction]()
    public var usage: PBTokenUsage?
    public var plan: PBPlanProposed?
    /// Tools started and not yet completed in this turn, oldest first.
    public var tools = [RunningTool]()
    /// Tools of the last turn whose end is not known, until another turn starts.
    public var background = [BackgroundTool]()
    /// Start seq to the seq of the call that completed it.
    public var completedAt = [UInt64: UInt64]()
    /// Seqs of starts the turn ended without a result for.
    public var unfinished = Set<UInt64>()
    /// Of those, the ones whose turn completed normally: no result came, nothing more is known.
    public var noResult = Set<UInt64>()
    /// User messages typed in the terminal rather than sent from a client.
    public var typedLocally = Set<UInt64>()
    /// Mode or model changes made in the terminal, by the seq of the turn context that shows them.
    public var terminalChanges = [UInt64: String]()

    public init() {}
}

/// Pending approvals and questions, input states and the running turn.
public func derive(_ events: some Sequence<PBEvent>, agent: PBAgent = .codex) -> Derived {
    var r = EventReducer(agent: agent)
    r.append(events)
    return r.snapshot()
}

struct EventReducer: Sendable {
    private var d = Derived()
    // The mode as the chat names it, a CLI's own name for one outside the four; empty until a context says.
    private var mode = ""
    private var model = ""
    // The model and effort last switched to from here, until a context shows a model.
    private var expected: (model: String, effort: String)?
    // Messages sent from clients are recorded as submitted before the CLI
    // records them; the rest were typed in the terminal.
    private var submitted = 0
    // A user message with no submitted state before it, which may still come
    // right after: Claude Code records the message first.
    private var unclaimed: UInt64?
    private var queuedAt = [String: UInt64]()
    // A plan was proposed since entering plan mode: leaving it is the plan's
    // answer, however given, not a mode switch made in the terminal.
    private var compacting: UInt64?
    private var planned = false
    private let claude: Bool
    // Starts a turn ended without a result, by call ID, for a result that comes later.
    private var unfinishedById = [String: RunningTool]()

    init(agent: PBAgent) {
        claude = effectiveAgent(agent) == .claude
    }

    mutating func append(_ events: some Sequence<PBEvent>) {
        for e in events { reduce(e) }
    }

    private mutating func endTools() {
        d.running = false
        d.approvals.removeAll()
        d.questions.removeAll()
        for t in d.tools {
            d.unfinished.insert(t.seq)
            d.noResult.insert(t.seq)
            if !t.callId.isEmpty { unfinishedById[t.callId] = t }
        }
        d.tools.removeAll()
        d.background.removeAll()
    }

    private mutating func reduce(_ e: PBEvent) {
        switch e.kind {
        case .approvalRequested(let a): d.approvals[a.approvalID] = a
        case .approvalResolved(let r):
            d.approvals.removeValue(forKey: r.approvalID)
            d.resolved[r.approvalID] = ApprovalOutcome(decision: r.decision, message: r.message)
            if let o = e.originOrNil { d.resolvedBy[r.approvalID] = o }
        case .questionAsked(let q): if !q.async { d.questions[q.questionID] = q }
        case .questionAnswered(let a):
            d.questions.removeValue(forKey: a.questionID)
            d.answered[a.questionID] = a.answers.flatMap(\.answers)
        case .turnStarted:
            d.running = true
            d.background.removeAll()
        case .turnEnded(let t):
            d.running = false
            d.questions.removeAll()
            // Still running when an interrupted turn ended: whether it
            // ever ends is not known (an interrupt does not stop
            // background commands). A turn that completed without a
            // tool's result only says no result came.
            let aborted = t.outcome == .aborted
            for tool in d.tools {
                if aborted { d.background.append(BackgroundTool(tool: tool, returned: false)) } else { d.noResult.insert(tool.seq) }
                d.unfinished.insert(tool.seq)
                if !tool.callId.isEmpty { unfinishedById[tool.callId] = tool }
            }
            d.tools.removeAll()
        case .inputState(let st):
            let late = d.inputs[st.requestID] == .unconfirmed
            d.inputs[st.requestID] = st.status
            let before = d.inputDetails[st.requestID]
            var detail = st
            if let before {
                if st.mode == .unspecified { detail.mode = before.mode }
                if st.text.isEmpty { detail.text = before.text }
            }
            d.inputDetails[st.requestID] = detail
            if let prev = d.accepted[st.requestID] {
                if st.stage != .unspecified {
                    var next = prev
                    next.stage = st.stage
                    d.accepted[st.requestID] = next
                }
            } else if st.status == .queued && (!st.text.isEmpty || !st.imageIds.isEmpty) {
                d.accepted[st.requestID] = Accepted(text: st.text, imageIds: st.imageIds, origin: e.originOrNil, stage: st.stage)
            }
            if st.status == .failed || st.status == .needsTerminal { d.failures[st.requestID] = st.reason }
            if st.status == .canceled && st.reason == "exited" { d.unsent[st.requestID] = e.seq }
            if st.status == .queued && queuedAt[st.requestID] == nil { queuedAt[st.requestID] = e.seq }
            if st.status == .submitted {
                // Codex marks a message submitted before recording it;
                // Claude Code records the message first and the state
                // follows, claiming only a message after its enqueue. A
                // message confirmed after it was reported unconfirmed is
                // the one recorded right after this state.
                if !late, claude, let u = unclaimed, e.seq >= u, e.seq - u <= 3,
                    queuedAt[st.requestID].map({ u > $0 }) ?? true {
                    d.typedLocally.remove(u)
                    unclaimed = nil
                } else {
                    submitted += 1
                }
            }
        case .userMessage(let m):
            if e.hasOrigin && e.origin.kind != .unspecified {
                // The daemon says who sent it: no guessing.
                if e.origin.kind == .local { d.typedLocally.insert(e.seq) }
                if submitted > 0 && !m.requestID.isEmpty { submitted -= 1 }
            } else if submitted > 0 {
                submitted -= 1
            } else if m.text != "Implement the plan." {
                // Codex sends that one itself when a plan is approved.
                d.typedLocally.insert(e.seq)
                unclaimed = e.seq
            }
        case .toolStarted(let t):
            d.tools.append(
                RunningTool(
                    title: t.title, callId: t.callID, since: eventTime(e), kind: t.kind, itemType: t.itemType, seq: e.seq,
                    input: t.input, inputDigest: t.inputDigest))
        case .toolCall(let c):
            // Starts and calls pair by call ID (for Codex, later
            // entries of one call carry "<id>#2" and have no start).
            let id = c.callID
            var matched = false
            if !id.isEmpty, let j = d.tools.firstIndex(where: { $0.callId == id }) {
                matched = true
                let t = d.tools.remove(at: j)
                d.completedAt[t.seq] = e.seq
                if wentToBackground(c) { d.background.append(BackgroundTool(tool: t, returned: true)) }
            } else if !id.isEmpty, let t = unfinishedById[id], d.completedAt[t.seq] == nil {
                matched = true
                // A call reported after its turn ended (a Codex exec still
                // running): it belongs to that start, not to a new row.
                d.completedAt[t.seq] = e.seq
                d.unfinished.remove(t.seq)
                d.noResult.remove(t.seq)
                d.background.removeAll { $0.tool.seq == t.seq }
                if wentToBackground(c) { d.background.append(BackgroundTool(tool: t, returned: true)) }
            }
            if !matched && wentToBackground(c) {
                // Without a separate ToolStarted the result is the first
                // known start; it must not consume another tool's start.
                let t = RunningTool(title: c.title, callId: id, since: eventTime(e), kind: c.kind,
                    itemType: c.itemType, seq: e.seq, input: c.input, inputDigest: c.inputDigest)
                d.background.append(BackgroundTool(tool: t, returned: true))
            }
        case .tokenUsage(let u):
            d.usage = u
            if let seq = compacting { d.compactions[seq]?.after = usagePercent(u) }
            compacting = nil
        case .planProposed(let p):
            d.plan = p
            planned = true
        case .settingsChanged(let c):
            if c.compacted {
                d.compactions[e.seq] = Compaction(before: usagePercent(d.usage))
                compacting = e.seq
            }
            // The next context should show this model; the client names it
            // by slug or alias, the context by the CLI's own ID.
            if !c.model.isEmpty { expected = (c.model, c.effort) }
            if let m = modeOf(c.mode) { mode = m.text }
        case .turnContext(let c):
            // Claude Code's mode by its own name, so that one outside the four is a change like any other.
            let m = c.permissionMode.isEmpty
                ? contextMode(
                    reviewer: c.approvalsReviewer, policy: c.approvalPolicy, sandbox: c.sandbox, collaboration: c.collaborationMode)?.text ?? ""
                : permissionModeName(c.permissionMode)
            let planning = c.collaborationMode == "plan" || c.permissionMode == "plan"
            let mm = "\(c.model) \(c.effort)"
            var changes = [String]()
            if !mode.isEmpty && !m.isEmpty && m != mode && !(mode == Mode.plan.text && planned) { changes.append("模式切到了 \(m)") }
            if !m.isEmpty && m != mode { planned = false }
            // Plan mode runs at its own effort; only a model change counts
            // there. A context without a model is Claude Code's mode, read
            // off the screen; it says nothing about the model. After a
            // switch from here the next context is compared with what was
            // chosen; a different model means the terminal changed it since.
            let changed: Bool
            if let expected {
                changed = !(modelIs(c.model, expected.model) && (expected.effort.isEmpty || c.effort.isEmpty || c.effort == expected.effort))
            } else {
                changed = mm != model
            }
            if (!model.isEmpty || expected != nil) && !c.model.isEmpty && changed && !planning {
                changes.append("模型切到了 \(c.model)" + (c.effort.isEmpty ? "" : " · \(c.effort)"))
            }
            if !c.model.isEmpty && !planning { expected = nil }
            if !changes.isEmpty { d.terminalChanges[e.seq] = changes.joined(separator: "，") }
            if !m.isEmpty { mode = m }
            if !planning && !c.model.isEmpty { model = mm }
        case .sessionState(let s):
            if s.status == .exited { endTools() }
        case .sessionForked:
            // What came before is a copy of the source's history: nothing
            // of it is pending or running in this session.
            endTools()
            submitted = 0
            unclaimed = nil
            unfinishedById.removeAll()
        default: break
        }
    }

    func snapshot() -> Derived { d }
}

/// Derives a session's state as its events grow: the reducer's state is
/// kept while what is given only adds to what was given before, so an
/// update costs what the new events cost. Anything else (another CLI, a
/// shorter or changed history) starts over. What it returns is a value and
/// stays as it was returned.
public struct EventProjection: Sendable {
    private var previous = [PBEvent]()
    private var agent: PBAgent?
    private var reducer = EventReducer(agent: .codex)
    private var result = Derived()

    public init() {}

    public mutating func update(_ events: some RandomAccessCollection<PBEvent>, agent: PBAgent = .codex) -> Derived {
        // An event is a handle on shared storage, so comparing one with the
        // copy held from last time is a pointer comparison.
        let append = self.agent == agent && events.count >= previous.count && zip(events, previous).allSatisfy { $0 == $1 }
        if append && events.count == previous.count { return result }
        if append {
            let tail = events.dropFirst(previous.count)
            reducer.append(tail)
            previous.append(contentsOf: tail)
        } else {
            reducer = EventReducer(agent: agent)
            reducer.append(events)
            previous = Array(events)
        }
        result = reducer.snapshot()
        self.agent = agent
        return result
    }
}

/// Match Claude Code by daemon digest, or Codex by the command it reported for the call.
public func awaitingApproval(_ t: RunningTool, _ d: Derived) -> Bool {
    d.approvals.values.contains { a in
        if !t.inputDigest.isEmpty { return !a.inputDigest.isEmpty && a.inputDigest == t.inputDigest }
        return a.inputDigest.isEmpty && !a.command.isEmpty && (a.command == t.input || a.command == t.title)
    }
}

/// How long a tool has run, in ms, or nil when its start time is unknown.
/// While disconnected (`frozenAt` > 0) the reading stops where the
/// connection dropped, so it does not suggest the tool is still running.
public func toolElapsed(_ t: RunningTool, now: Int64, frozenAt: Int64 = 0) -> Int64? {
    if t.since <= 0 { return nil }
    let end = frozenAt > 0 ? min(frozenAt, now) : now
    return max(0, end - t.since)
}

/// 2:12, 1:02:03: a running timer.
public func clock(_ ms: Int64) -> String {
    let s = ms / 1000
    return s >= 3600 ? "\(s / 3600):\(pad2(Int(s % 3600 / 60))):\(pad2(Int(s % 60)))" : "\(s / 60):\(pad2(Int(s % 60)))"
}

/// A tool's short name: Claude Code's own tool name (Bash, Read), else what kind of tool it is.
public func toolLabel(kind: PBToolKind, itemType: String, agent: PBAgent) -> String {
    if effectiveAgent(agent) == .claude && !itemType.isEmpty { return itemType }
    switch kind {
    case .command: return "命令"
    case .fileChange: return "编辑"
    case .mcp: return "MCP"
    case .webSearch: return "搜索"
    default: return "工具"
    }
}

public func eventTime(_ e: PBEvent) -> Int64 { e.hasTime ? timestampMs(e.time) : 0 }

public func decisionText(_ d: PBApprovalDecision) -> String {
    switch d {
    case .allow, .allowAlways: return "已允许"
    case .deny: return "已拒绝"
    case .timeout: return "超时，已拒绝"
    case .canceled: return "已取消"
    case .terminal: return "已在终端处理"
    default: return ""
    }
}

private let recommended = Rx("\\A(\(Jre.dot)*?)\(Jre.space)*\\((Recommended|推荐)\\)\(Jre.space)*\(Jre.end)", ignoreCase: true)

/// Splits a "(Recommended)" marker off an option label.
public func optionLabel(_ label: String) -> (label: String, recommended: Bool) {
    if let m = recommended.find(label) { return (m.text(1), true) }
    return (label, false)
}

// --- the message queue ---

/// Why a message was not submitted (InputState.reason), in words.
public func failureText(reason: String, agent: PBAgent) -> String {
    switch reason {
    case "image_not_recognized": return "\(agentName(agent)) 没认出图片"
    case "input_changed": return "无法确认终端输入框的内容"
    case "timeout": return "等待超时"
    case "restarted": return "电脑上的 remote-agent 重启了"
    case "write_failed": return "写入终端失败"
    case "exited": return "会话已结束"
    case "": return "原因未知"
    default: return reason
    }
}

/// Messages the daemon accepted and has not written yet, in the order it accepted them.
public func queued(_ d: Derived) -> [String] {
    d.inputs.entries.filter { $0.value == .queued || $0.value == .heldByCli }.map(\.key)
}

public struct PendingInput: Equatable, Sendable, Identifiable {
    public var id: String
    public var text: String
    public var mode: PBSendMode
    public var paused: Bool
    public var held: Bool
    public var origin: PBOrigin?

    public init(id: String, text: String, mode: PBSendMode, paused: Bool, held: Bool, origin: PBOrigin?) {
        self.id = id
        self.text = text
        self.mode = mode
        self.paused = paused
        self.held = held
        self.origin = origin
    }
}

/// Only inputs the model has not received; event order is acceptance order.
public func pendingInputs(_ d: Derived) -> [PendingInput] {
    d.inputDetails.values.compactMap { st in
        guard st.status == .queued || st.status == .heldByCli else { return nil }
        let accepted = d.accepted[st.requestID]
        return PendingInput(
            id: st.requestID, text: accepted?.text ?? st.text, mode: st.mode != .unspecified ? st.mode : .steer,
            paused: st.paused, held: st.status == .heldByCli, origin: accepted?.origin)
    }
}

private let alwaysCommands = Rx("(?:Yes, and )?don't ask again for commands that start with [`]?(\(Jre.dot)+?)[`]?")
private let alwaysAccess = Rx("(?:Yes, and )?always allow access to (\(Jre.dot)+?) from this project")

/// CLI's native second approval option, translated only when its form is known.
public func approvalAlwaysText(_ message: String) -> String {
    if let m = alwaysCommands.matchEntire(message) { return "`\(m.text(1).trimming(ascii: UInt8(ascii: "`")))` 开头的命令以后不再问" }
    if let m = alwaysAccess.matchEntire(message) { return "本项目访问 \(m.text(1)) 以后不再问" }
    if message.jvmContainsIgnoringCase("accept edits") { return "本会话自动接受编辑" }
    return message
}

/// Why the first queued message waits: a short form for the line above the
/// message box and a longer one for the queue page. The daemon's own reason
/// (Session.input_wait) comes first; else the session's state says it.
public struct QueueWhy: Equatable, Sendable {
    public var short: String
    public var long: String

    public init(short: String, long: String) {
        self.short = short
        self.long = long
    }
}

public func queueWhy(_ s: SessionView, status: Status) -> QueueWhy {
    switch s.info?.inputWait {
    case "draft": return QueueWhy(short: "终端里有草稿", long: "终端里有没发出的草稿，发完或清掉后再发")
    case "popup": return QueueWhy(short: "就绪后发送", long: "等终端里的弹窗处理完")
    case "unrecognized": return QueueWhy(short: "已暂停", long: "认不出输入框，已暂停")
    default: break
    }
    switch status {
    case .question: return QueueWhy(short: "回答后继续", long: "等提问回答完")
    case .approval: return QueueWhy(short: "审批后继续", long: "等审批处理完")
    case .plan: return QueueWhy(short: "处理计划后继续", long: "等计划处理完")
    case .popup: return QueueWhy(short: "就绪后发送", long: "等终端里的弹窗处理完")
    case .running: return QueueWhy(short: "本轮结束后发送", long: "等待本轮结束")
    case .starting: return QueueWhy(short: "就绪后发送", long: "等 \(agentName(s.agent)) 就绪")
    default: return QueueWhy(short: "即将发送", long: "等待输入框就绪")
    }
}

/// Where a message the user tried to cancel ended up, once it left the queue:
/// the cancel worked, someone else canceled it, or it was written first.
public enum CancelOutcome: Sendable { case canceled, canceledElsewhere, written, unknown }

public func cancelOutcome(status: PBInputStatus?, canceledHere: Bool) -> CancelOutcome {
    switch status {
    case .canceled: return canceledHere ? .canceled : .canceledElsewhere
    case .submitted, .unconfirmed: return .written
    default: return .unknown
    }
}

private func usagePercent(_ u: PBTokenUsage?) -> Int? {
    guard let u, u.contextWindow > 0 else { return nil }
    return min(100, Int((u.lastTotalTokens * 100) / u.contextWindow))
}
