// A session's events turned into what its screens draw, once, off the main
// actor: what is pending and running (`Derived`), and the chat's rows with
// their Markdown already parsed.
//
// The rules are the package's (`EventProjection`, `ChatProjection`,
// `buildChat`); this only decides where and how often they run:
//
//   - reducing events, building rows and parsing Markdown happen on an
//     actor of their own, so a long history never holds up a frame;
//   - a row is identified by the event it stands at (`ChatItem.key`), and a
//     reply's parsed blocks are kept by that key for as long as its text is
//     the same, so a new event costs the rows it adds and nothing before;
//   - the screens observe three separate values (the rows, the pending
//     state, the loading state), each replaced only when it changed, so a
//     new message does not redraw the navigation bar and a new approval
//     does not redraw the chat.
import Foundation
import Observation
import RACore
import RAMarkdown
import RAProto
import SwiftUI

/// One row of the chat: what it shows and, for a reply, its Markdown as parsed.
struct ChatRow: Identifiable, Equatable, Sendable {
    let item: ChatItem
    let blocks: [MarkdownBlock]

    var id: String { item.key }

    /// The blocks are a function of the item, so the item decides.
    static func == (a: ChatRow, b: ChatRow) -> Bool { a.item == b.item }
}

/// What the rows depend on besides the events: how the session's computer
/// and CLI are named, who is who, and what this client sent.
struct ChatParams: Equatable, Sendable {
    var agent = PBAgent.codex
    var machine = ""
    var cwd = ""
    var home = ""
    /// This client's device ID.
    var me = ""
    var devices: [String: DeviceView] = [:]
    /// Messages sent from here by request ID, see `SessionStore.texts`.
    var texts: [String: String] = [:]
    /// The first message of a session started from here.
    var prompt: String?
}

/// What one pass over the events gives.
struct ChatSnapshot: Sendable {
    var derived = Derived()
    var rows: [ChatRow] = []
    var startPrompt: StartPrompt?
    /// A message was sent in what was read of the history (`sentMessage`), which a fork needs.
    var spoke = false
}

actor ChatBuilder {
    private struct Key: Equatable {
        var agent: PBAgent
        var machine, cwd, home, me: String
        var devices: [String: DeviceView]
        var sent: Int
        var prompt: String?
        var initialId: String?
    }

    private struct Parsed {
        var text: String
        var blocks: [MarkdownBlock]
    }

    private let parse: @Sendable (String) -> [MarkdownBlock]
    private var reduce = EventProjection()
    private var chat = ChatProjection()
    private var key: Key?
    private var parsed: [String: Parsed] = [:]

    init(parse: @escaping @Sendable (String) -> [MarkdownBlock] = { Markdown.parse($0) }) {
        self.parse = parse
    }

    func build(_ events: PagedList<PBEvent>, _ p: ChatParams) -> ChatSnapshot {
        let d = reduce.update(events, agent: p.agent)
        let local = Set(p.texts.keys)
        // The daemon queues a new session's first message under an ID of its own.
        let initialId = p.prompt == nil ? nil : d.inputs.keys.first { !local.contains($0) }
        let next = Key(
            agent: p.agent, machine: p.machine, cwd: p.cwd, home: p.home, me: p.me, devices: p.devices, sent: p.texts.count,
            prompt: p.prompt, initialId: initialId)
        // Rows name clients and carry the texts sent from here: when those
        // change, the rows kept from before may say something else.
        if next != key {
            chat = ChatProjection()
            key = next
        }
        var named = RemoteState()
        named.devices = p.devices
        let items = chat.update(
            events, d: d, machine: p.machine, cwd: p.cwd, home: p.home, agent: p.agent,
            textOf: { id in p.texts[id] ?? (id == initialId ? p.prompt : nil) },
            who: { originLabel($0, state: named, me: p.me, machine: p.machine) })
        var rows: [ChatRow] = []
        rows.reserveCapacity(items.count)
        var live = 0
        for item in items {
            guard case .agent(let reply) = item else {
                rows.append(ChatRow(item: item, blocks: []))
                continue
            }
            live += 1
            if let held = parsed[reply.key], held.text == reply.text {
                rows.append(ChatRow(item: item, blocks: held.blocks))
            } else {
                let blocks = parse(reply.text)
                parsed[reply.key] = Parsed(text: reply.text, blocks: blocks)
                rows.append(ChatRow(item: item, blocks: blocks))
            }
        }
        // Rows that are gone (another history under the same builder) take their blocks with them.
        if parsed.count > live * 2 + 16 {
            let keys = Set(items.map(\.key))
            parsed = parsed.filter { keys.contains($0.key) }
        }
        return ChatSnapshot(
            derived: d, rows: rows, startPrompt: pendingStartPrompt(p.prompt, events: events, d: d, localIds: local),
            // The title is the screen's to look at; this is the other half of the rule.
            spoke: sentMessage(SessionView(id: "", machineId: ""), events: events))
    }
}

@MainActor
@Observable
final class SessionModel {
    private(set) var rows: [ChatRow] = []
    private(set) var derived = Derived()
    private(set) var startPrompt: StartPrompt?
    /// A message was sent in the history read so far.
    private(set) var spoke = false
    /// The history was read to its end at least once.
    private(set) var loaded = false
    /// An event does not open: the history stops before it.
    private(set) var error: String?
    /// The last fetch failed; the next connection fetches again.
    private(set) var fetchError: String?
    /// The first pass over the events has been drawn from: until then the page is still reading.
    private(set) var ready = false

    @ObservationIgnored private let builder: ChatBuilder
    @ObservationIgnored private var events = PagedList<PBEvent>()
    @ObservationIgnored private var params = ChatParams()
    @ObservationIgnored private var asked = 0
    @ObservationIgnored private var applied = 0
    #if DEBUG
    /// How long the last pass took off the main actor, and how long applying it took on it, in ms.
    @ObservationIgnored private(set) var lastBuildMs = 0.0
    @ObservationIgnored private(set) var lastApplyMs = 0.0
    @ObservationIgnored private(set) var passes = 0
    #endif

    init(builder: ChatBuilder = ChatBuilder()) {
        self.builder = builder
    }

    /// Follows the session until the task is cancelled, which lets its
    /// history go (architecture §10 「后台和内存」). The feed keeps only the
    /// latest snapshot for a reader that is behind, and a frame passes
    /// between two of them: a burst of events is one pass.
    func follow(_ remote: Remote, _ sid: String) async {
        for await snapshot in remote.events(sid).updates() {
            await show(snapshot)
            if (try? await Task.sleep(nanoseconds: 16_000_000)) == nil { return }
        }
    }

    /// Takes a snapshot of the history. Apart from `follow` for tests and sample data.
    func show(_ snapshot: SessionEvents) async {
        events = snapshot.events
        await rebuild()
        if loaded != snapshot.loaded { loaded = snapshot.loaded }
        if error != snapshot.error { error = snapshot.error }
        if fetchError != snapshot.fetchError { fetchError = snapshot.fetchError }
    }

    /// What the rows depend on besides the events; a change builds them again.
    func use(_ p: ChatParams) {
        guard p != params else { return }
        params = p
        Task { await rebuild() }
    }

    private func rebuild() async {
        asked += 1
        let turn = asked
        #if DEBUG
        let started = ContinuousClock.now
        #endif
        let snapshot = await builder.build(events, params)
        // A later pass that finished first already shows more.
        guard turn > applied else { return }
        applied = turn
        #if DEBUG
        let built = ContinuousClock.now
        #endif
        if snapshot.rows != rows { rows = snapshot.rows }
        if snapshot.derived != derived { derived = snapshot.derived }
        if snapshot.startPrompt != startPrompt { startPrompt = snapshot.startPrompt }
        if snapshot.spoke != spoke { spoke = snapshot.spoke }
        if !ready { ready = true }
        #if DEBUG
        lastBuildMs = Double((built - started).components.attoseconds) / 1e15 + Double((built - started).components.seconds) * 1000
        let done = ContinuousClock.now - built
        lastApplyMs = Double(done.components.attoseconds) / 1e15 + Double(done.components.seconds) * 1000
        passes += 1
        // `-sessionPerf`: what each pass cost, in numbers only.
        if SessionDebug.perf {
            print(String(format: "PERF pass %d events %d rows %d build %.2f ms apply %.3f ms", passes, events.count, rows.count, lastBuildMs, lastApplyMs))
        }
        #endif
    }
}

/// Asks for a session that is not held here by its ID: the listing comes a
/// page at a time, so one opened from a notification, a link or a fork's
/// source may not have been fetched (architecture §8 「按 ID 取」).
@MainActor
@Observable
final class SessionFinder {
    private(set) var lookup = SessionLookup.reading

    /// Looks the session up until it is held or known to be missing, or the task is cancelled.
    /// With sample data there is no server to ask: it "has none".
    func run(_ remote: Remote, _ sid: String, sample: Bool = false) async {
        lookup = .reading
        if sample {
            lookup = .missing
            return
        }
        // `lookUpSession` reports from wherever it runs; the stream brings its word back here.
        let (stream, word) = AsyncStream<SessionLookup>.makeStream()
        let work = Task.detached {
            try? await lookUpSession(fetch: { try await remote.fetchSession(sid) }, report: { word.yield($0) })
            word.finish()
        }
        await withTaskCancellationHandler {
            for await l in stream { lookup = l }
        } onCancel: {
            work.cancel()
        }
    }
}

/// How a page about a session stands while the session is not held here:
/// nil once it is. It waits for the connection, and `attempt` changing asks
/// again at once. Android's `sessionLookup`.
private struct SessionLookupTask: ViewModifier {
    let sid: String
    let attempt: Int
    let finder: SessionFinder
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context

    private struct Key: Equatable {
        var sid: String
        var held: Bool
        var online: Bool
        var attempt: Int
    }

    func body(content: Content) -> some View {
        let key = Key(sid: sid, held: model.state.sessions[sid] != nil, online: model.state.conn == .online, attempt: attempt)
        content.task(id: key) {
            if key.held || !key.online { return }
            await finder.run(context.remote, sid, sample: model.isDemo)
        }
    }
}

extension View {
    /// Looks `sid` up while it is not held, into `finder`.
    func sessionLookup(_ sid: String, attempt: Int = 0, into finder: SessionFinder) -> some View {
        modifier(SessionLookupTask(sid: sid, attempt: attempt, finder: finder))
    }
}

/// Follows a session's events into `chat` for a page about one thing in it
/// (an approval, a question, the plan), and looks the session up while it
/// is not held here.
private struct SessionFollow: ViewModifier {
    let sid: String
    let chat: SessionModel
    let finder: SessionFinder
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context

    private struct Key: Equatable {
        var sid: String
        var held: Bool
    }

    func body(content: Content) -> some View {
        let session = model.state.sessions[sid]
        content
            // Asked for again once the session is held: a history is only read for a session that is known.
            .task(id: Key(sid: sid, held: session != nil)) { await chat.follow(context.remote, sid) }
            .onChange(of: session?.agent, initial: true) { _, agent in
                // Which CLI it is decides how its events are read.
                chat.use(ChatParams(agent: agent ?? .codex, me: context.me))
            }
            .sessionLookup(sid, into: finder)
            .sessionSamples(sid)
    }
}

extension View {
    func followSession(_ sid: String, chat: SessionModel, finder: SessionFinder) -> some View {
        modifier(SessionFollow(sid: sid, chat: chat, finder: finder))
    }
}
