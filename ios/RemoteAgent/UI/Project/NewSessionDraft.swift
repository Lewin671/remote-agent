// What a project's new-session box keeps between visits, for this run and
// only in memory (Android's `Drafts.NewSession` and `Starts` in
// ui/Actions.kt and ui/Project.kt): the first message as far as it is
// written, the configuration it will start with, the CLI picked next to
// the box, and the start itself while its outcome is unknown.
import Observation
import RACore
import RAProto

/// A first message with the configuration it will start with, kept and
/// restored together so that a draft reopened later still goes where it
/// was going; and the start of it.
@MainActor
@Observable
final class NewSessionDraft {
    var text = ""
    var config: DraftConfig
    /// The CLI picked next to the message box for this draft, or nil to use the one the project remembers.
    var agent: PBAgent?

    /// The start whose outcome is unknown is sent again as it was, so the daemon answers from its log
    /// instead of starting a second session (architecture §10).
    @ObservationIgnored private let attempt = Attempt()
    /// The first message of the start that was last sent.
    private(set) var prompt = ""
    private(set) var running = false
    private(set) var error = ""
    private(set) var frozen = false
    private(set) var expired = false

    init(_ initial: NewConfig?, settled: Bool) {
        config = DraftConfig(initial, settled: settled)
    }

    var drafting: Bool { !promptText(text).isEmpty }

    var progress: NewSessionProgress {
        NewSessionProgress(running: running, error: error, frozen: frozen, expired: expired, prompt: prompt)
    }

    /// Starts a session with what is written. `onStarted` gets the new
    /// session's ID and the configuration it started with; a refusal, or no
    /// answer, leaves the text as it is.
    func start(_ remote: Remote, _ cfg: NewConfig, onStarted: @escaping @MainActor (_ sid: String, _ used: NewConfig, _ prompt: String) -> Void) {
        guard !running else { return }
        let typed = promptText(text)
        // Other content is a new start; the unknown one is given up.
        if attempt.frozen && typed != prompt { attempt.abandon() }
        // An unknown start is resent as prepared, with the mode it had.
        let retry = attempt.frozen
        running = true
        error = ""
        Task {
            do {
                let prepared = try attempt.next {
                    prompt = typed
                    return try remote.prepare(
                        machineId: cfg.machineId, sessionId: "",
                        createSessionCommand(cfg, prompt: typed, mode: cfg.mode?.proto ?? .unspecified))
                }
                let result = try await remote.forward(prepared, retryOfUnknown: retry)
                attempt.settle()
                finish("")
                // Only a session that started takes the draft.
                if let sid = startedSession(result) {
                    if promptText(text) == prompt { text = "" }
                    onStarted(sid, cfg, prompt)
                }
            } catch {
                finish(errorText(attempt.settle(error) ?? error))
            }
        }
    }

    /// Gives up on a start whose outcome is unknown: the next ↑ is a new one.
    func abandon() {
        attempt.abandon()
        finish("")
    }

    private func finish(_ error: String) {
        self.error = error
        frozen = attempt.frozen
        expired = attempt.expired
        running = false
    }

    #if DEBUG
    /// A state to look at, for screenshots of sample data.
    func show(running: Bool = false, error: String = "", frozen: Bool = false, expired: Bool = false) {
        self.running = running
        self.error = error
        self.frozen = frozen
        self.expired = expired
        prompt = promptText(text)
    }
    #endif
}

/// The new-session drafts of the account's projects, by project path.
@MainActor
final class NewSessionDrafts {
    private var byProject: [String: NewSessionDraft] = [:]

    /// The draft of a project; `initial` only seeds one that does not exist yet (`settled`: see `ProjectStart`).
    func of(_ key: String, initial: NewConfig?, settled: Bool) -> NewSessionDraft {
        if let d = byProject[key] { return d }
        let d = NewSessionDraft(initial, settled: settled)
        byProject[key] = d
        return d
    }
}

/// The first message of each session started from this device in this run,
/// for the session's page: until the CLI is ready the message is shown
/// there as a pending bubble (Android's `Outbox.rememberPrompt`).
@MainActor
final class StartedPrompts {
    private var bySession: [String: String] = [:]

    func remember(_ sid: String, _ prompt: String) { bySession[sid] = prompt }

    func prompt(_ sid: String) -> String? { bySession[sid] }
}

extension AccountContext {
    var newSessionDrafts: NewSessionDrafts { memory.value { NewSessionDrafts() } }
    var startedPrompts: StartedPrompts { memory.value { StartedPrompts() } }
}
