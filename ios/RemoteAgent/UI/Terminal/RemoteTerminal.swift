// A session's terminal: SwiftTerm's emulator fed by the remote byte stream
// (architecture §11; Android's term/RemoteTerminal.kt). The emulator keeps
// the tmux window's size; the keyboard, rotation and the view's own size
// never change it. Only "fit" resizes the tmux window, and each connection
// fits once on its own. When output arrives faster than it is shown the
// stream is dropped and reopened for a fresh snapshot rather than skipping
// bytes.
//
// Every connection has a number. What a connection that was replaced or
// closed still delivers, a frame, its end or its late opening, finds the
// number moved on and changes nothing.
//
// Nothing of the stream is logged: not its bytes, not what is typed.
import CoreGraphics
import Foundation
import Observation
import RACore
import RAProto

/// Read-only history view state driven by clientcore (`PBCoreTerminalHistoryUpdatedEffect`).
/// Pages arrive pre-parsed into styled runs once from Core and are prepended oldest-first.
struct TerminalHistoryState: Equatable {
    var phase: PBCoreTerminalHistoryPhase = .idle
    var status: PBCoreTerminalHistoryStatus = .none
    var statusText: PBCoreText? = nil
    var canRetry: Bool = false
    var canLoadEarlier: Bool = false
    var pending: Bool = false
    var maxCells: Int32 = 0
    var pages: [PBCoreTerminalHistoryParsedPage] = []

    var reading: Bool { phase != .idle }
    var open: Bool { phase == .open }
    var opening: Bool { phase == .opening || phase == .openingFailed }

    mutating func apply(_ upd: PBCoreTerminalHistoryUpdatedEffect) {
        if upd.resetPages { pages = [] }
        if upd.hasAddedPage { pages.insert(upd.addedPage, at: 0) }
        phase = upd.phase
        status = upd.status
        statusText = upd.hasStatusText ? upd.statusText : nil
        canRetry = upd.canRetry
        canLoadEarlier = upd.canLoadEarlier
        pending = upd.pending
        maxCells = upd.maxCells
    }
}

@MainActor
@Observable
final class RemoteTerminal {
    enum State: Equatable {
        case connecting
        /// The screen shows the remote terminal and what is typed goes to it.
        case live
        /// Core requires explicit user confirmation before rebuilding the terminal stream.
        case confirmRebuild(TermConfirmKind)
        case closed(String)

        var isClosed: Bool {
            if case .closed = self { return true }
            return false
        }
    }

    /// What the pane needs of the app: the session's stream, whether the main
    /// connection is up, and where open terminals are kept so that the
    /// background closes them (`AppModel.holdTerminal`).
    struct Link {
        var open: @Sendable (any TermListener) async throws -> TermConn
        var online: @MainActor () -> Bool
        var hold: @MainActor (TermConn) -> UUID
        var release: @MainActor (UUID) -> Void
        var reportStream: @MainActor (String, PBCoreTerminalStreamEventKind, Bool) -> Void = { _, _, _ in }
        var subscribeReopen: @MainActor (String, @escaping @MainActor @Sendable () -> Void) -> () -> Void = { _, _ in {} }
        var sendHistory: @MainActor (String, PBCoreTerminalHistoryEventKind, PBTerminalHistoryPage?) -> Void = { _, _, _ in }
        var subscribeHistory: @MainActor (
            String,
            @escaping @MainActor @Sendable (PBCoreTerminalHistorySendRequestEffect) -> Void,
            @escaping @MainActor @Sendable (PBCoreTerminalHistoryUpdatedEffect) -> Void
        ) -> () -> Void = { _, _, _ in {} }
    }

    static var paused: String { L10n.terminalIosPaused() }
    private nonisolated static let frameInterval = 0.033

    private(set) var history = TerminalHistoryState()
    /// Output has arrived since reading began; kept local so live streaming never crosses into Core.
    private(set) var hasNewOutput = false
    /// How far the screen was pulled when reading was asked for: where the reader opens.
    private(set) var entryPull: CGFloat = 0
    /// Reading the record: the reader is opening or on screen.
    var reading: Bool { history.reading }

    /// How far the live screen has to be pulled before the record is read: a small
    /// drag is not a wish to read (the web asks for 12 CSS px, Android for three rows).
    static let readPull: CGFloat = 12

    /// The live screen was pulled down: read the record from now. A first page that
    /// failed is retried by the same pull; anything else while one is on its way only
    /// moves the reader's opening.
    func readUp(_ distance: CGFloat) {
        guard distance >= Self.readPull, state == .live, conn != nil else { return }
        if history.phase == .opening {
            entryPull = max(entryPull, distance)
            return
        }
        if history.phase == .openingFailed {
            entryPull = max(0, distance)
            hasNewOutput = false
            link?.sendHistory(terminalId, .retry, nil)
            return
        }
        guard history.phase == .idle else { return }
        // The reader is read-only, so the keyboard has no business staying up.
        host.hideKeyboard()
        entryPull = max(0, distance)
        hasNewOutput = false
        link?.sendHistory(terminalId, .open, nil)
    }

    /// The reader reached the top: read the page before the oldest held.
    func loadEarlier() {
        guard state == .live, conn != nil else { return }
        link?.sendHistory(terminalId, .loadEarlier, nil)
    }

    /// The reader's 重试.
    func retryHistory() {
        guard state == .live, conn != nil else { return }
        link?.sendHistory(terminalId, .retry, nil)
    }

    /// Leaves reading, if reading; the live screen is what is left.
    func leaveReading() {
        hasNewOutput = false
        entryPull = 0
        link?.sendHistory(terminalId, .close, nil)
    }

    private func historyRequest(_ id: String, _ cursor: String) {
        guard let conn else { return }
        var frame = PBTerminalFrame()
        frame.historyRequest.requestID = id
        frame.historyRequest.cursor = cursor
        conn.send(frame)
    }

    private(set) var state = State.connecting
    /// The tmux window's size, from the latest snapshot.
    private(set) var size = TerminalSize(0, 0)
    private(set) var fitted = false
    /// The window changed size without this screen asking: another screen fitted it or the computer's terminal was resized.
    private(set) var resizedElsewhere = false
    /// The phone's keyboard is up.
    private(set) var keyboard = false

    /// The screen, made when first shown.
    @ObservationIgnored private(set) lazy var host: TerminalHostView = {
        let host = TerminalHostView()
        host.onInput = { [weak self] in self?.send($0) }
        host.onCapacity = { [weak self] in self?.roomChanged($0) }
        host.onKeyboard = { [weak self] in self?.keyboard = $0 }
        host.onReadUp = { [weak self] in self?.readUp($0) }
        return host
    }()

    let terminalId = UUID().uuidString
    @ObservationIgnored private var link: Link?
    /// Columns and rows the view has room for at a readable size, for "fit".
    @ObservationIgnored private var fitSize = TerminalSize(0, 0)
    @ObservationIgnored private var fit = FitTracker()
    @ObservationIgnored private var conn: TermConn?
    /// This connection's first snapshot is on the screen.
    @ObservationIgnored private var pictured = false
    @ObservationIgnored private var held: UUID?
    @ObservationIgnored private var job: Task<Void, Never>?
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var active = false
    @ObservationIgnored private var liveNotified = false
    @ObservationIgnored private var reopenCancel: (() -> Void)?
    @ObservationIgnored private var historyCancel: (() -> Void)?
    /// This connection has yet to fit on its own; never again once 还原 was chosen here.
    @ObservationIgnored private var autoFit = false
    @ObservationIgnored private var released = false
    @ObservationIgnored private var frames = TerminalFrames()

    init() {}

    func use(_ link: Link) {
        reopenCancel?()
        historyCancel?()
        self.link = link
        reopenCancel = link.subscribeReopen(terminalId) { [weak self] in
            guard let self, self.active else { return }
            self.openAttempt()
        }
        historyCancel = link.subscribeHistory(
            terminalId,
            { [weak self] req in
                self?.historyRequest(req.requestID, req.cursor)
            },
            { [weak self] upd in
                guard let self else { return }
                self.history.apply(upd)
                if self.history.phase == .idle {
                    self.entryPull = 0
                    self.hasNewOutput = false
                }
            })
    }

    func connectIfNeeded() {
        guard conn == nil && job == nil && (!active || !state.isClosed) else { return }
        active = true
        openAttempt()
    }

    func connect() {
        active = true
        link?.reportStream(terminalId, .manualReconnect, false)
        openAttempt()
    }

    private func openAttempt() {
        leaveReading()
        guard let link else { return }
        guard link.online() else {
            close()
            return
        }
        job?.cancel()
        drop()
        generation += 1
        let gen = generation
        frames.close()
        let queue = TerminalFrames()
        frames = queue
        state = .connecting
        pictured = false
        liveNotified = false
        fit.reset()
        fitted = false
        autoFit = !released
        let listener = Listener(
            queue: queue,
            wake: { [weak self] offer in
                switch offer {
                case .schedule: onMain(after: Self.frameInterval) { self?.flush(gen, queue) }
                case .overflow: onMain {
                    if let self, gen == self.generation {
                        let wasOpened = self.conn != nil || self.liveNotified
                        self.link?.sendHistory(self.terminalId, .disconnected, nil)
                        self.drop()
                        self.state = .closed(L10n.errorsTerminalConnectionDropped())
                        self.link?.reportStream(self.terminalId, .closed, wasOpened)
                    }
                }
                case .queued, .closed: break
                }
            },
            onConfirm: { [weak self] kind in
                onMain {
                    if let self, gen == self.generation, !self.state.isClosed {
                        self.state = .confirmRebuild(kind)
                    }
                }
            },
            onRebuild: { [weak self] in
                onMain {
                    if let self, gen == self.generation, self.active {
                        self.openAttempt()
                    }
                }
            }
        )
        job = Task { [weak self] in
            do {
                let opened = try await link.open(listener)
                guard let self, gen == self.generation else {
                    opened.close()
                    return
                }
                self.job = nil
                self.conn = opened
                self.held = link.hold(opened)
                self.goLive()
            } catch {
                guard let self, gen == self.generation else { return }
                self.job = nil
                self.state = .closed(errorText(error))
                link.reportStream(self.terminalId, .closed, false)
            }
        }
    }

    private func flush(_ gen: Int, _ queue: TerminalFrames) {
        guard gen == generation else { return }
        let (batch, more) = queue.drain()
        for step in terminalSteps(batch) { apply(step) }
        if more { onMain(after: Self.frameInterval) { [weak self] in self?.flush(gen, queue) } }
    }

    private func apply(_ step: TerminalStep) {
        switch step {
        case .snapshot(let cols, let rows, let data):
            host.snapshot(cols: cols, rows: rows, data: data)
            size = TerminalSize(cols, rows)
            if fit.onSnapshot(size) { resizedElsewhere = true }
            fitted = fit.fitted
            pictured = true
            goLive()
        case .output(let data):
            // The live screen keeps being written to while reading; the reader never moves for it.
            if history.reading { hasNewOutput = true }
            host.append(data)
        case .closed(let reason):
            link?.sendHistory(terminalId, .disconnected, nil)
            // The daemon says why before it hangs up; the end of the connection that follows says less.
            if case .closed = state { return }
            let opened = conn != nil || liveNotified
            state = .closed(reason)
            if opened {
                let gen = generation
                onMain { [weak self] in
                    guard let self, gen == self.generation, self.link?.online() == true else { return }
                    self.link?.reportStream(self.terminalId, .closed, true)
                }
            }
        }
    }

    /// The first snapshot and the connection arrive in either order: the
    /// snapshot can be here before the opening command has answered. Live,
    /// and fitted, once both are here; until then keys would go nowhere.
    private func goLive() {
        guard pictured, conn != nil else { return }
        if state == .connecting {
            state = .live
        }
        fitOnOpen()
        if !liveNotified {
            liveNotified = true
            link?.reportStream(terminalId, .live, true)
        }
    }

    func send(_ data: Data) {
        guard state == .live, let conn else { return }
        // A key leaves reading first, and then goes as it always did.
        leaveReading()
        var frame = PBTerminalFrame()
        frame.input = data
        conn.send(frame)
    }

    func send(_ key: TerminalKey) {
        send(Data(terminalKeySequence(key, appCursor: host.appCursor).utf8))
    }

    /// Resizes the tmux window to this screen, or lets it follow the attached terminals again.
    func toggleFit() {
        guard let conn else { return }
        if fitted {
            var frame = PBTerminalFrame()
            frame.resize.fit = false
            conn.send(frame)
            fit.release()
            released = true
        } else {
            released = false
            fitToScreen()
        }
        resizedElsewhere = false
        fitted = fit.fitted
    }

    func dismissResized() {
        resizedElsewhere = false
    }

    /// The size chosen with 字号. Changing it refits a window that is fitted, and no other.
    func setFont(_ points: Int) {
        host.preferred = points
        if fitted { fitToScreen() }
    }

    private func roomChanged(_ room: TerminalSize) {
        fitSize = room
        // The area was not laid out yet when the first snapshot came.
        if autoFit { Task { self.fitOnOpen() } }
    }

    private func fitOnOpen() {
        if autoFit && fitToScreen() { autoFit = false }
    }

    /// Explicit user font changes may refit an already fitted window.
    @discardableResult
    private func fitToScreen() -> Bool {
        guard state == .live, let conn else { return false }
        if fitSize.cols < 20 || fitSize.rows < 5 { return false }
        var frame = PBTerminalFrame()
        frame.resize.cols = Int32(fitSize.cols)
        frame.resize.rows = Int32(fitSize.rows)
        frame.resize.fit = true
        conn.send(frame)
        fit.request(fitSize)
        resizedElsewhere = false
        fitted = fit.fitted
        return true
    }

    func confirmRebuild() {
        conn?.confirmRebuild()
    }

    func closeFromConfirm() {
        close()
    }

    func close() {
        active = false
        liveNotified = false
        hasNewOutput = false
        entryPull = 0
        history = TerminalHistoryState()
        link?.sendHistory(terminalId, .disposed, nil)
        generation += 1
        frames.close()
        state = .closed(Self.paused)
        job?.cancel()
        job = nil
        drop()
        link?.reportStream(terminalId, .disposed, false)
    }

    private func drop() {
        if let held { link?.release(held) }
        held = nil
        conn?.close()
        conn = nil
    }

    #if DEBUG
    /// A screen without a stream, for sample data and the gallery.
    func show(cols: Int, rows: Int, data: Data, fitted: Bool = true) {
        host.snapshot(cols: cols, rows: rows, data: data)
        size = TerminalSize(cols, rows)
        self.fitted = fitted
        state = .live
    }

    func show(_ state: State) {
        self.state = state
    }

    /// The reader over sample history, parsed by clientcore with no connection behind it.
    func showHistory(_ lines: [String]) {
        hasNewOutput = false
        entryPull = 60
        history = TerminalHistoryState()
        guard let core = try? CoreClient() else { return }
        defer { core.close() }
        let sampleID = "sample"
        let openBatch = try? core.dispatch(
            .with {
                $0.randomIds = ["sample"]
                $0.terminalHistory = .with {
                    $0.terminalID = sampleID
                    $0.kind = .open
                }
            })
        let reqID = openBatch?.effects.compactMap { eff -> String? in
            if case .terminalHistorySendRequest(let r)? = eff.effect { return r.requestID }
            return nil
        }.first ?? "sample"
        let pageBatch = try? core.dispatch(
            .with {
                $0.terminalHistory = .with {
                    $0.terminalID = sampleID
                    $0.kind = .pageReceived
                    $0.page = .with {
                        $0.requestID = reqID
                        $0.lines = lines
                    }
                }
            })
        for eff in pageBatch?.effects ?? [] {
            if case .terminalHistoryUpdated(let upd)? = eff.effect {
                history.apply(upd)
            }
        }
    }

    /// The live screen under the opening line, with no connection behind it.
    func showOpening(failed: Bool) {
        hasNewOutput = false
        entryPull = 0
        history = TerminalHistoryState(
            phase: failed ? .openingFailed : .opening,
            status: failed ? .openingFailed : .opening,
            statusText: PBCoreText.with { $0.key = failed ? "terminal.read_failed" : "terminal.reading" },
            canRetry: failed,
            canLoadEarlier: false,
            pending: !failed
        )
    }
    #endif
}

/// Runs on the main thread, later; in the order asked.
private func onMain(after seconds: TimeInterval = 0, _ body: @escaping @MainActor @Sendable () -> Void) {
    DispatchQueue.main.asyncAfter(deadline: .now() + seconds) { MainActor.assumeIsolated(body) }
}

/// The stream's reader puts what arrives in the queue and says when the main thread should look.
private final class Listener: TermListener {
    private let queue: TerminalFrames
    private let wake: @Sendable (TerminalFrames.Offer) -> Void
    private let onConfirm: @Sendable (TermConfirmKind) -> Void
    private let onRebuild: @Sendable () -> Void

    init(
        queue: TerminalFrames,
        wake: @escaping @Sendable (TerminalFrames.Offer) -> Void,
        onConfirm: @escaping @Sendable (TermConfirmKind) -> Void = { _ in },
        onRebuild: @escaping @Sendable () -> Void = {}
    ) {
        self.queue = queue
        self.wake = wake
        self.onConfirm = onConfirm
        self.onRebuild = onRebuild
    }

    func onFrame(_ frame: PBTerminalFrame) {
        wake(queue.offer(frame))
    }

    func onClose(_ reason: String) {
        var frame = PBTerminalFrame()
        frame.closed = reason
        wake(queue.offer(frame))
    }

    func onConfirmRebuild(_ kind: TermConfirmKind) {
        onConfirm(kind)
    }

    func onRebuildConfirmed() {
        onRebuild()
    }
}
