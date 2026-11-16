// What a session's events say is pending and running: approvals, questions,
// the plan, the message queue, and the tools of the turn (architecture §13,
// 工具执行反馈). Event reduction runs in the shared Go core (`Corebind`),
// exposed to Swift as `PBProjectionState` without leaking gomobile types.
import Corebind
import Foundation
import RAProto

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
    switch b {
    case .requested: return L10n.toolsBackgroundAgent()
    case .timedOut: return L10n.toolsBackgroundTimeout()
    case .byUser: return L10n.toolsBackgroundTerminal()
    case .stillRunning: return L10n.toolsBackgroundTurn()
    default: return L10n.toolsBackgroundReturned()
    }
}

/// A background call's title: 后台命令 for a shell call, else the tool's name with （后台）.
public func backgroundTitle(kind: PBToolKind, itemType: String, agent: PBAgent) -> String {
    kind == .command || kind == .unspecified
        ? L10n.toolsIosBackgroundCommand()
        : L10n.toolsIosBackgroundTitle(title: toolLabel(kind: kind, itemType: itemType, agent: agent))
}

/// Pending approvals and questions, input states and the running turn.
public func derive(_ events: some Sequence<PBEvent>, agent: PBAgent = defaultAgent) -> PBProjectionState {
    let list = Array(events)
    if list.isEmpty { return PBProjectionState() }
    let batch = PBEventBatch.with { $0.events = list }
    var err: NSError?
    guard let batchData = try? batch.serializedData(),
          let outData = CorebindReduce(Int32(effectiveAgent(agent).rawValue), batchData, &err),
          err == nil,
          let state = try? PBProjectionState(serializedBytes: outData)
    else {
        return PBProjectionState()
    }
    return state
}

/// Convenience lookups over the reduced `PBProjectionState` for UI and rendering callers.
extension PBProjectionState {
    public func pendingApproval(_ id: String) -> PBApprovalRequested? {
        approvals.first { $0.approvalID == id }?.request
    }

    public func pendingQuestion(_ id: String) -> PBQuestionAsked? {
        questions.first { $0.questionID == id }?.request
    }

    public func input(_ id: String) -> PBInputProjection? {
        inputs.first { $0.requestID == id }
    }

    public func inputStatus(_ id: String) -> PBInputStatus? {
        input(id)?.status
    }

    public func failureReason(_ id: String) -> String? {
        guard let i = input(id), i.hasFailureReason else { return nil }
        return i.failureReason
    }
}

/// Formats one terminal change from `PBProjectionState.terminalChanges`.
public func terminalChangeText(_ change: PBTerminalChange) -> String {
    switch change.kind {
    case .mode(let m):
        let label = modeOf(m.mode)?.text ?? permissionModeName(m.rawMode)
        return L10n.sessionTerminalModeChange(value: label)
    case .model(let m):
        return L10n.sessionTerminalModelChange(value: m.model) + (m.effort.isEmpty ? "" : " · \(m.effort)")
    case nil:
        return ""
    }
}

/// Derives a session's state as its events grow via the shared Go core:
/// when `events` only adds to what was given before under the same CLI,
/// only the new tail crosses the FFI boundary. Anything else starts over.
public struct EventProjection: Sendable {
    private final class Storage: @unchecked Sendable {
        private let lock = NSLock()
        private let core: CorebindProjection
        private var previousPaged: PagedList<PBEvent>?
        private var previousArray = [PBEvent]()
        private var agent: PBAgent?
        private var result = PBProjectionState()

        init() {
            guard let core = CorebindNewProjection(Int32(effectiveAgent(defaultAgent).rawValue)) else {
                fatalError()
            }
            self.core = core
        }

        func copy() -> Storage {
            lock.lock()
            let prev = previousPaged.map(Array.init) ?? previousArray
            let ag = agent
            lock.unlock()
            let clone = Storage()
            if let ag {
                _ = clone.update(prev, agent: ag)
            }
            return clone
        }

        func update(_ events: some RandomAccessCollection<PBEvent>, agent: PBAgent) -> PBProjectionState {
            lock.lock()
            defer { lock.unlock() }
            let effective = effectiveAgent(agent)
            if let paged = events as? PagedList<PBEvent> {
                if self.agent == effective, let prev = previousPaged, paged.extends(prev) {
                    if paged.count == prev.count { return result }
                    let tail = Array(paged[prev.count...])
                    if applyAppend(tail) {
                        previousPaged = paged
                        previousArray.removeAll(keepingCapacity: false)
                        self.agent = effective
                    }
                    return result
                }
                let all = Array(paged)
                core.reset(Int32(effective.rawValue))
                if applyAppend(all) {
                    previousPaged = paged
                    previousArray.removeAll(keepingCapacity: false)
                    self.agent = effective
                }
                return result
            }

            if previousArray.isEmpty, let prev = previousPaged {
                previousArray = Array(prev)
            }
            let append = self.agent == effective && events.count >= previousArray.count
                && zip(events, previousArray).allSatisfy { $0 == $1 }
            if append && events.count == previousArray.count {
                previousPaged = nil
                return result
            }
            if append {
                let tail = Array(events.dropFirst(previousArray.count))
                if applyAppend(tail) {
                    previousPaged = nil
                    previousArray.append(contentsOf: tail)
                    self.agent = effective
                }
            } else {
                let all = Array(events)
                core.reset(Int32(effective.rawValue))
                if applyAppend(all) {
                    previousPaged = nil
                    previousArray = all
                    self.agent = effective
                }
            }
            return result
        }

        func currentActions(live: PBLiveSnapshot?) -> PBProjectionState {
            lock.lock()
            defer { lock.unlock() }
            let liveBytes = (try? live?.serializedData()) ?? Data()
            return decodeProjectionResult { try core.currentActions(liveBytes) } ?? result
        }

        private func applyAppend(_ events: [PBEvent]) -> Bool {
            let batchData: Data
            if events.isEmpty {
                batchData = Data()
            } else {
                let batch = PBEventBatch.with { $0.events = events }
                guard let encoded = try? batch.serializedData() else { return false }
                batchData = encoded
            }
            guard let state = decodeProjectionResult({ try core.append(batchData) }) else {
                return false
            }
            result = state
            return true
        }

        private func decodeProjectionResult(_ call: () throws -> Data) -> PBProjectionState? {
            do {
                let out = try call()
                return try? PBProjectionState(serializedBytes: out)
            } catch {
                // gomobile converts a 0-byte []byte return to nil NSData* with nil error,
                // which Swift's ObjC bridge throws as Foundation._GenericObjCError.nilError.
                // Real Go errors use domain "go".
                if (error as NSError).domain != "go" {
                    return PBProjectionState()
                }
                return nil
            }
        }
    }

    private var storage = Storage()

    public init() {}

    public mutating func update(_ events: some RandomAccessCollection<PBEvent>, agent: PBAgent = defaultAgent) -> PBProjectionState {
        if !isKnownUniquelyReferenced(&storage) {
            storage = storage.copy()
        }
        return storage.update(events, agent: agent)
    }

    public mutating func update(
        _ events: some RandomAccessCollection<PBEvent>, agent: PBAgent = defaultAgent, live: PBLiveSnapshot?
    ) -> PBProjectionState {
        if !isKnownUniquelyReferenced(&storage) {
            storage = storage.copy()
        }
        _ = storage.update(events, agent: agent)
        return storage.currentActions(live: live)
    }

    public mutating func currentActions(live: PBLiveSnapshot?) -> PBProjectionState {
        if !isKnownUniquelyReferenced(&storage) {
            storage = storage.copy()
        }
        return storage.currentActions(live: live)
    }
}

/// How long a tool has run, in ms, or nil when its start time is unknown.
/// While disconnected (`frozenAt` > 0) the reading stops where the
/// connection dropped, so it does not suggest the tool is still running.
public func toolElapsed(_ t: PBRunningTool, now: Int64, frozenAt: Int64 = 0) -> Int64? {
    if t.sinceMs <= 0 { return nil }
    let end = frozenAt > 0 ? min(frozenAt, now) : now
    return max(0, end - t.sinceMs)
}

/// 2:12, 1:02:03: a running timer.
public func clock(_ ms: Int64) -> String {
    let s = ms / 1000
    return s >= 3600 ? "\(s / 3600):\(pad2(Int(s % 3600 / 60))):\(pad2(Int(s % 60)))" : "\(s / 60):\(pad2(Int(s % 60)))"
}

/// A tool's short name: Claude Code's own tool name (Bash, Read), else what kind of tool it is.
public func toolLabel(kind: PBToolKind, itemType: String, agent: PBAgent) -> String {
    if agentInfo(agent).toolLabelFromItemType && !itemType.isEmpty { return itemType }
    switch kind {
    case .command: return L10n.toolsCommand()
    case .fileChange: return L10n.toolsIosEdit()
    case .mcp: return "MCP"
    case .webSearch: return L10n.toolsIosSearch()
    default: return L10n.toolsTool()
    }
}

public func eventTime(_ e: PBEvent) -> Int64 { e.hasTime ? timestampMs(e.time) : 0 }

public func decisionText(_ d: PBApprovalDecision) -> String {
    switch d {
    case .allow, .allowAlways: return L10n.approvalAllowed()
    case .deny: return L10n.approvalDenied()
    case .timeout: return L10n.approvalTimedOut()
    case .canceled: return L10n.queueCanceled()
    case .terminal: return L10n.approvalInTerminal()
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
    case "image_not_recognized": return L10n.composerImagesUnrecognized(agent: agentName(agent))
    case "input_changed": return L10n.composerInputUnconfirmed()
    case "timeout": return L10n.composerReadyTimeout()
    case "restarted": return L10n.composerComputerRestarted()
    case "write_failed": return L10n.composerWriteFailed()
    case "exited": return L10n.sessionEnded()
    default: return L10n.composerNotWritten()
    }
}

/// Messages the daemon accepted and has not written yet, in the order it accepted them.
public func queued(_ d: PBProjectionState) -> [String] {
    d.inputs.filter { $0.status == .queued || $0.status == .heldByCli }.map(\.requestID)
}

public struct PendingInput: Equatable, Sendable, Identifiable {
    public var files: [PBSentFile]
    public var id: String
    public var text: String
    public var mode: PBSendMode
    public var paused: Bool
    public var held: Bool
    public var origin: PBOrigin?
    public var time: Int64

    public init(id: String, text: String, mode: PBSendMode, paused: Bool, held: Bool, origin: PBOrigin?, files: [PBSentFile] = [], time: Int64 = 0) {
        self.id = id
        self.text = text
        self.mode = mode
        self.paused = paused
        self.held = held
        self.time = time
        self.origin = origin
        self.files = files
    }
}

/// Only inputs the model has not received; event order is acceptance order.
public func pendingInputs(_ d: PBProjectionState) -> [PendingInput] {
    var times = [String: Int64]()
    for input in d.inputs where input.hasAccepted && input.accepted.timeMs > 0 {
        times[input.requestID] = input.accepted.timeMs
    }
    return d.pendingInputs.map { p in
        PendingInput(
            id: p.requestID,
            text: p.text,
            mode: p.mode != .unspecified ? p.mode : .steer,
            paused: p.paused,
            held: p.held,
            origin: p.hasOrigin ? p.origin : nil,
            files: p.files,
            time: times[p.requestID] ?? 0
        )
    }
}

private let alwaysCommands = Rx("(?:Yes, and )?don't ask again for commands that start with [`]?(\(Jre.dot)+?)[`]?")
private let alwaysAccess = Rx("(?:Yes, and )?always allow access to (\(Jre.dot)+?) from this project")

/// CLI's native second approval option, translated only when its form is known.
public func approvalAlwaysText(_ message: String) -> String {
    if let m = alwaysCommands.matchEntire(message) { return L10n.approvalIosAlwaysPrefix(command: m.text(1).trimming(ascii: UInt8(ascii: "`"))) }
    if let m = alwaysAccess.matchEntire(message) { return L10n.approvalAlwaysDirectory(path: m.text(1)) }
    if message.jvmContainsIgnoringCase("accept edits") { return L10n.approvalAutoEdits() }
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
    case "draft": return QueueWhy(short: L10n.queueIosTerminalDraftShort(), long: L10n.queueTerminalDraft())
    case "popup": return QueueWhy(short: L10n.queueWhenReady(), long: L10n.queueIosPopupWait())
    case "unrecognized": return QueueWhy(short: L10n.queuePaused(), long: L10n.queueUnrecognized())
    default: break
    }
    switch status {
    case .question: return QueueWhy(short: L10n.queueIosAfterAnswer(), long: L10n.queueIosQuestionWait())
    case .approval: return QueueWhy(short: L10n.queueIosAfterApproval(), long: L10n.queueIosApprovalWait())
    case .plan: return QueueWhy(short: L10n.queueIosAfterPlan(), long: L10n.queueIosPlanWait())
    case .popup: return QueueWhy(short: L10n.queueWhenReady(), long: L10n.queueIosPopupWait())
    case .running: return QueueWhy(short: L10n.queueAfterCurrentTurn(), long: L10n.queueIosTurnWait())
    case .starting: return QueueWhy(short: L10n.queueWhenReady(), long: L10n.queueIosAgentWait(agent: agentName(s.agent)))
    default: return QueueWhy(short: L10n.queueIosSendingSoon(), long: L10n.queueIosInputWait())
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

public func inputTimingLabel(agent: PBAgent, mode: PBSendMode, held: Bool, starting: String?) -> String {
    if !agentInfo(agent).steersDuringTurn { return starting.map { L10n.queueAgentReady(agent: $0) } ?? L10n.queueAfterTurn() }
    if held { return L10n.queueInsert() }
    if mode == .queue { return L10n.queueAfterTurn() }
    return starting.map { L10n.queueAgentReady(agent: $0) } ?? L10n.queueInsert()
}
