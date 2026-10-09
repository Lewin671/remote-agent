// What the session screens keep in memory for the account in use: drafts,
// messages sent from here, and the commands whose outcome may be unknown.
// The counterpart of Android's `Outbox`, `Drafts`, `Cancels`, `Resolves`,
// `Answers` and `QuestionPicks` (ui/Actions.kt, ui/Queue.kt, ui/Dock.kt).
//
// It lives in `AccountMemory`, so all of it is plaintext in memory only and
// goes with the account. Nothing here is the truth about a session: the
// daemon's events are (architecture §9 「多客户端的界面状态」). This only holds
// what is in flight from this client and what the user was typing.
//
// The rules, each of which the tests hold it to:
//
//   - a message is sent under one request ID for as long as it exists here,
//     so a retry is never submitted twice (the daemon dedupes by it);
//   - a failure that may have reached the computer is "unknown", never
//     "failed" (architecture §10 「指令」);
//   - an approval or an answer whose outcome is unknown is sent again as it
//     was, the same sealed command (`Attempt`), and the opposite decision is
//     never offered in its place;
//   - a draft is the user's: it survives leaving the page, a failed send and
//     a card another client answered first.
import Foundation
import Observation
import RACore
import RAProto

/// The part of the sync engine the session screens send through. `Remote` is
/// the only real one; tests answer in its place.
protocol SessionCommands: Sendable {
    @discardableResult func command(_ sid: String, _ cmd: PBCommand) async throws -> PBCommandResult
    func prepare(machineId: String, sessionId: String, _ cmd: PBCommand) throws -> PreparedCommand
    @discardableResult func forward(_ p: PreparedCommand, retryOfUnknown: Bool) async throws -> PBCommandResult
}

extension Remote: SessionCommands {}

@MainActor
@Observable
final class SessionStore {
    /// A cancel asked from here that the daemon has not answered, or why it failed.
    enum CancelState: Equatable, Sendable {
        case canceling
        case failed(String)
    }

    /// A decision on an approval sent from here, kept while its outcome is unknown.
    final class Resolve {
        let attempt = Attempt()
        var allow = false
        var message = ""
        var always = false
    }

    /// An answer to one question of several, kept while its outcome is unknown.
    final class Answer {
        let attempt = Attempt()
        /// The options of the answer being sent, 0-based.
        var options: [Int] = []
    }

    // MARK: Drafts

    private var drafts: [String: String] = [:]
    private var reasons: [String: String] = [:]
    private var prompts: [String: String] = [:]

    /// What is being typed in a session's message box.
    func draft(_ sid: String) -> String { drafts[sid] ?? "" }

    func setDraft(_ sid: String, _ text: String) {
        if drafts[sid] ?? "" != text { drafts[sid] = text.isEmpty ? nil : text }
    }

    /// The reason being typed for refusing an approval. It stays when another
    /// client answers the approval first, and is never carried to the next one.
    func reason(_ approvalId: String) -> String { reasons[approvalId] ?? "" }

    func setReason(_ approvalId: String, _ text: String) {
        if reasons[approvalId] ?? "" != text { reasons[approvalId] = text.isEmpty ? nil : text }
    }

    /// The first message of a session started from this client, which the
    /// daemon sends once the CLI is ready. Whoever starts a session calls
    /// this with the new session's ID, so that its page shows the message
    /// before the history has it.
    func rememberPrompt(_ sid: String, _ text: String) {
        if !text.isEmpty { prompts[sid] = text }
    }

    func prompt(_ sid: String) -> String? { prompts[sid] }

    // MARK: Messages sent from here

    private var outbox: [String: [Outgoing]] = [:]
    @ObservationIgnored private var delivering = Set<String>()

    /// The messages sent from this client in a session, oldest first, until the chat shows them.
    func outgoing(_ sid: String) -> [Outgoing] { outbox[sid] ?? [] }

    func find(_ sid: String, requestId: String) -> Outgoing? { outbox[sid]?.first { $0.requestId == requestId } }

    /// The texts of the messages sent from here, by request ID: the daemon's queue events do not always carry them.
    func texts(_ sid: String) -> [String: String] {
        Dictionary((outbox[sid] ?? []).map { ($0.requestId, $0.text) }, uniquingKeysWith: { a, _ in a })
    }

    /// Sends a message. The returned task ends when the computer has answered or the try is given up.
    @discardableResult
    func send(_ commands: any SessionCommands, sid: String, text: String, mode: PBSendMode = .steer) -> Task<Void, Never> {
        let o = Outgoing(requestId: newRequestId(), text: text, mode: mode)
        outbox[sid, default: []].append(o)
        return deliver(commands, sid: sid, o)
    }

    /// Takes in a message made elsewhere, as one with images is: kept on disk first, or left by an earlier run
    /// (`ImageCenter`). It is not sent by this; `deliver` does that.
    func add(_ sid: String, _ o: Outgoing) {
        outbox[sid, default: []].append(o)
    }

    /// Sends, or sends again, a message under its own request ID.
    @discardableResult
    func deliver(_ commands: any SessionCommands, sid: String, _ o: Outgoing) -> Task<Void, Never> {
        // One try at a time: a second tap while the first is on its way adds nothing.
        guard delivering.insert(o.requestId).inserted else { return Task {} }
        o.state = .sending
        o.error = ""
        var cmd = PBCommand()
        cmd.send = .with {
            $0.sessionID = sid
            $0.text = o.text
            $0.requestID = o.requestId
            $0.imageIds = o.imageIds
            $0.mode = o.mode
        }
        // Not tied to the page: a message keeps going when its page is left.
        return Task { [cmd] in
            do {
                try await commands.command(sid, cmd)
                o.state = .sent
            } catch {
                o.error = errorText(error)
                o.state = uncertain(error) ? .unknown : .failed
            }
            delivering.remove(o.requestId)
        }
    }

    /// Drops a message from here for good.
    func remove(_ sid: String, _ o: Outgoing) {
        outbox[sid]?.removeAll { $0.requestId == o.requestId }
    }

    /// A message that was not submitted goes back into the box, for the user
    /// to send again as a new message. What is already typed stays in front of it.
    func putBack(_ sid: String, _ o: Outgoing) {
        appendToDraft(sid, o.text)
        remove(sid, o)
    }

    func appendToDraft(_ sid: String, _ text: String) {
        let current = draft(sid)
        let kept = SessionText.trimEnd(current)
        setDraft(sid, SessionText.isBlank(current) ? text : kept + "\n" + text)
    }

    /// Lets go of the messages the CLI has recorded: the chat shows them from the session's events from then on.
    func settle(_ sid: String, inputs: OrderedMap<String, PBInputStatus>) {
        guard let list = outbox[sid], list.contains(where: { inputs[$0.requestId] == .submitted }) else { return }
        outbox[sid] = list.filter { inputs[$0.requestId] != .submitted }
    }

    // MARK: Cancels

    private(set) var cancels: [String: CancelState] = [:]

    /// Asks the daemon to take a queued message back. Its InputState says how
    /// that ended; a failure stays here so that the bubble can offer another try.
    @discardableResult
    func cancel(_ commands: any SessionCommands, sid: String, requestId: String) -> Task<Void, Never> {
        if cancels[requestId] == .canceling { return Task {} }
        cancels[requestId] = .canceling
        var cmd = PBCommand()
        cmd.cancelInput = .with {
            $0.sessionID = sid
            $0.requestID = requestId
        }
        return Task { [cmd] in
            do {
                try await commands.command(sid, cmd)
                cancels[requestId] = nil
            } catch {
                // A failed_precondition can race with delivery; while the
                // input stays pending the bubble keeps a retry.
                cancels[requestId] = .failed(errorText(error))
            }
        }
    }

    /// Forgets the cancels of messages that have left the queue.
    func settleCancels(inputs: OrderedMap<String, PBInputStatus>) {
        for id in cancels.keys {
            if let status = inputs[id], status != .queued, status != .heldByCli { cancels[id] = nil }
        }
    }

    // MARK: Approvals

    @ObservationIgnored private var resolves: [String: Resolve] = [:]
    /// Bumped when an attempt changes, for the views that read one.
    private var attempts = 0

    func resolve(_ approvalId: String) -> Resolve {
        _ = attempts
        if let r = resolves[approvalId] { return r }
        let r = Resolve()
        resolves[approvalId] = r
        return r
    }

    /// Sends a decision, or the earlier one while its outcome is unknown;
    /// returns the decision that went. Throws what to show.
    @discardableResult
    func resolveApproval(
        _ commands: any SessionCommands, machineId: String, sid: String, approvalId: String, allow: Bool, message: String,
        always: Bool = false
    ) async throws -> Bool {
        let r = resolve(approvalId)
        defer { attempts += 1 }
        do {
            let retry = r.attempt.frozen
            let prepared = try r.attempt.next {
                r.allow = allow
                r.message = message
                r.always = always
                var cmd = PBCommand()
                cmd.resolveApproval = .with {
                    $0.sessionID = sid
                    $0.approvalID = approvalId
                    $0.allow = allow
                    $0.message = message
                    $0.always = always
                }
                return try commands.prepare(machineId: machineId, sessionId: sid, cmd)
            }
            try await commands.forward(prepared, retryOfUnknown: retry)
            r.attempt.settle()
            return r.allow
        } catch {
            throw r.attempt.settle(error) ?? error
        }
    }

    // MARK: Questions

    @ObservationIgnored private var answers: [String: Answer] = [:]
    /// Where the user is in a question, shared by the dock and the full
    /// page: which of several questions, and what is ticked.
    var questionIndex: [String: Int] = [:]
    var singlePick: [String: Int] = [:]
    var multiPick: [String: Set<Int>] = [:]

    func answer(_ questionId: String, _ index: Int) -> Answer {
        _ = attempts
        let key = "\(questionId)#\(index)"
        if let a = answers[key] { return a }
        let a = Answer()
        answers[key] = a
        return a
    }

    /// Sends `options`, or the earlier answer while its outcome is unknown,
    /// and returns the options that were answered. A new request would be
    /// refused once the CLI had moved on to the next question.
    func sendAnswer(
        _ commands: any SessionCommands, _ a: Answer, options: [Int], prepare: ([Int]) throws -> PreparedCommand
    ) async throws -> [Int] {
        defer { attempts += 1 }
        do {
            let retry = a.attempt.frozen
            let prepared = try a.attempt.next {
                a.options = options
                return try prepare(options)
            }
            try await commands.forward(prepared, retryOfUnknown: retry)
            a.attempt.settle()
            return a.options
        } catch {
            throw a.attempt.settle(error) ?? error
        }
    }

    /// Gives up on an answer that expired with its outcome unknown.
    func abandon(_ a: Answer) {
        a.attempt.abandon()
        attempts += 1
    }
}

#if DEBUG
extension SessionStore {
    /// Puts a message in a session's outbox as it stands, without sending it: for sample data.
    func debugAppend(_ sid: String, _ o: Outgoing) {
        outbox[sid, default: []].append(o)
    }
}
#endif

extension AccountContext {
    /// The session screens' memory for this account.
    var sessionStore: SessionStore { memory.value(SessionStore.self) { SessionStore() } }
}

/// Text handled as Android's Kotlin handles it, where the two differ from Swift's defaults.
enum SessionText {
    static func trim(_ s: String) -> String { s.trimmingCharacters(in: .whitespacesAndNewlines) }

    static func trimEnd(_ s: String) -> String {
        var end = s.endIndex
        while end > s.startIndex, s[s.index(before: end)].isWhitespace { end = s.index(before: end) }
        return String(s[..<end])
    }

    static func isBlank(_ s: String) -> Bool { s.allSatisfy(\.isWhitespace) }

    static func lines(_ s: String) -> [String] {
        s.split(omittingEmptySubsequences: false, whereSeparator: \.isNewline).map(String.init)
    }

    static func firstLine(_ s: String) -> String { lines(s).first ?? "" }
}
