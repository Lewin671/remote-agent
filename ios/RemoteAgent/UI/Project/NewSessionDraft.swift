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
    /// What a start sends. With another message, other images, another
    /// machine or other settings it is another start.
    struct Start: Equatable {
        var prompt: String
        var config: NewConfig
        var imageIds: [String]
        var fileIds: [String] = []
    }

    var text = ""
    var config: DraftConfig
    /// The CLI picked next to the message box for this draft, or nil to use the one the project remembers.
    var agent: PBAgent?

    /// The start whose outcome is unknown is sent again as it was, so the daemon answers from its log
    /// instead of starting a second session (architecture §10).
    @ObservationIgnored private let attempt = Attempt()
    /// The first message of the start that was last sent.
    private(set) var prompt = ""
    /// The whole of the start that was last prepared: what a retry of an unknown one sends again.
    @ObservationIgnored private var sent: Start?
    /// The images the prepared request names, kept for a retry of the same request.
    @ObservationIgnored private var taken: [PendingImage] = []
    @ObservationIgnored private var preparedImages: [Processed?] = []
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

    /// Starts a session with what is written and the images of `images`.
    /// There is no start with nothing to send, and none while an image is
    /// not on the machine yet: never an empty session, never one with only
    /// some of the images. `onStarted` gets the new session's ID and the
    /// configuration it started with, `onImagesUsed` the images that went
    /// with it; a refusal, or no answer, leaves the text and the images as
    /// they are. While an earlier start's outcome is unknown, the same
    /// start is that request again and anything else gives it up.
    func start(
        _ remote: any SessionCommands, _ cfg: NewConfig, images: ImageDraft? = nil,
        onImagesUsed: @escaping @MainActor ([PendingImage]) -> Void = { _ in },
        onStarted: @escaping @MainActor (_ sid: String, _ used: NewConfig, _ prompt: String) -> Void
    ) {
        guard !running else { return }
        let typed = promptText(text)
        let attached = images?.images ?? []
        guard !typed.isEmpty || !attached.isEmpty, attached.allSatisfy({ $0.readyFor(cfg.machineId) }) else { return }
        let wanted = Start(prompt: typed, config: cfg, imageIds: attached.filter { !$0.isFile }.compactMap(\.imageId), fileIds: attached.filter(\.isFile).compactMap(\.imageId))
        // Another message, other images, another machine or other settings is a new start; the unknown one is given up.
        // An image uploaded again after its deadline is the same picked image, so the unknown start keeps its image IDs.
        let same = typed == sent?.prompt && cfg == sent?.config &&
            attached.map(\.id) == taken.map(\.id) && attached.map(\.processed) == preparedImages
        if attempt.frozen && !same {
            attempt.abandon()
            taken = []
            preparedImages = []
        }
        // An unknown start is resent as prepared, with the mode it had.
        let retry = attempt.frozen
        running = true
        error = ""
        Task {
            do {
                let prepared = try attempt.next {
                    sent = wanted
                    prompt = typed
                    // The images this request carries; any picked after it stay in the draft.
                    taken = attached
                    preparedImages = attached.map(\.processed)
                    return try remote.prepare(
                        machineId: cfg.machineId, sessionId: "",
                        createSessionCommand(cfg, prompt: typed, mode: cfg.mode?.proto ?? .unspecified, imageIds: wanted.imageIds, fileIds: wanted.fileIds))
                }
                let result = try await remote.forward(prepared, retryOfUnknown: retry)
                attempt.settle()
                finish("")
                // Only a session that started takes the draft.
                if let sid = startedSession(result) {
                    if promptText(text) == prompt { text = "" }
                    let used = taken
                    taken = []
                    preparedImages = []
                    if !used.isEmpty { onImagesUsed(used) }
                    // What the session started with is what the request said, not what the page says by now.
                    onStarted(sid, sent?.config ?? cfg, prompt)
                }
            } catch {
                finish(errorText(attempt.settle(error) ?? error))
            }
        }
    }

    /// Gives up on a start whose outcome is unknown: the next ↑ is a new one.
    func abandon() {
        attempt.abandon()
        taken = []
        preparedImages = []
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
    private var byProject: [JvmKey: NewSessionDraft] = [:]

    /// The draft of a project; `initial` only seeds one that does not exist yet (`settled`: see `ProjectStart`).
    func of(_ key: String, initial: NewConfig?, settled: Bool) -> NewSessionDraft {
        let key = JvmKey(key)
        if let d = byProject[key] { return d }
        let d = NewSessionDraft(initial, settled: settled)
        byProject[key] = d
        return d
    }
}

extension AccountContext {
    var newSessionDrafts: NewSessionDrafts { memory.value { NewSessionDrafts() } }
}
