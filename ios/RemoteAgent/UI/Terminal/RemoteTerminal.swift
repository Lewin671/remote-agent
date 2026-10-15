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
import Foundation
import Observation
import RACore
import RAProto

@MainActor
@Observable
final class RemoteTerminal {
    enum State: Equatable {
        case connecting
        /// The screen shows the remote terminal and what is typed goes to it.
        case live
        case closed(String)
    }

    /// What the pane needs of the app: the session's stream, whether the main
    /// connection is up, and where open terminals are kept so that the
    /// background closes them (`AppModel.holdTerminal`).
    struct Link {
        var open: @Sendable (any TermListener) async throws -> TermConn
        var online: @MainActor () -> Bool
        var hold: @MainActor (TermConn) -> UUID
        var release: @MainActor (UUID) -> Void
    }

    static let paused = "终端已暂停，连接恢复后会重新获取画面"
    private nonisolated static let frameInterval = 0.033

    let history = TerminalHistory()
    /// Reading the record: the reader is opening or on screen.
    var reading: Bool { history.reading }

    /// How far the live screen has to be pulled before the record is read: a small
    /// drag is not a wish to read (the web asks for 12 CSS px, Android for three rows).
    static let readPull: CGFloat = 12

    /// The live screen was pulled down: read the record from now. A first page that
    /// failed is retried by the same pull; anything else while one is on its way only
    /// moves the reader's opening.
    func readUp(_ distance: CGFloat) {
        guard distance >= Self.readPull, state == .live, let conn else { return }
        // A pull that carries on while the first page is on its way opens the reader further up.
        history.pulled(distance)
        if history.phase == .openingFailed {
            history.retry { [self] id, cursor in historyRequest(id, cursor, conn) }
            return
        }
        guard history.phase == .idle else { return }
        // The reader is read-only, so the keyboard has no business staying up.
        host.hideKeyboard()
        history.begin(pull: distance) { [self] id, cursor in historyRequest(id, cursor, conn) }
    }

    /// The reader reached the top: read the page before the oldest held.
    func loadEarlier() {
        guard state == .live, let conn else { return }
        history.earlier { [self] id, cursor in historyRequest(id, cursor, conn) }
    }

    /// The reader's 重试.
    func retryHistory() {
        guard state == .live, let conn else { return }
        history.retry { [self] id, cursor in historyRequest(id, cursor, conn) }
    }

    /// Leaves reading, if reading; the live screen is what is left.
    func leaveReading() { history.end() }

    private func historyRequest(_ id: String, _ cursor: String, _ conn: TermConn) {
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
    /// This connection has yet to fit on its own; never again once 还原 was chosen here.
    @ObservationIgnored private var autoFit = false
    @ObservationIgnored private var released = false
    @ObservationIgnored private var frames = TerminalFrames()

    func use(_ link: Link) {
        self.link = link
    }

    func connect() {
        history.end()
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
        fit.reset()
        fitted = false
        autoFit = !released
        let listener = Listener(queue: queue) { [weak self] offer in
            switch offer {
            case .schedule: onMain(after: Self.frameInterval) { self?.flush(gen, queue) }
            case .overflow: onMain { if let self, gen == self.generation { self.connect() } }
            case .queued, .closed: break
            }
        }
        job = Task { [weak self] in
            do {
                let opened = try await link.open(listener)
                guard let self, gen == self.generation else {
                    opened.close()
                    return
                }
                self.conn = opened
                self.held = link.hold(opened)
                self.goLive()
            } catch {
                guard let self, gen == self.generation else { return }
                self.state = .closed(errorText(error))
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
        case .history(let page): history.receive(page)
        case .snapshot(let cols, let rows, let data):
            host.snapshot(cols: cols, rows: rows, data: data)
            size = TerminalSize(cols, rows)
            if fit.onSnapshot(size) { resizedElsewhere = true }
            fitted = fit.fitted
            pictured = true
            goLive()
        case .output(let data):
            // The live screen keeps being written to while reading; the reader never moves for it.
            history.markOutput()
            host.append(data)
        case .closed(let reason):
            history.disconnected()
            // The daemon says why before it hangs up; the end of the connection that follows says less.
            if case .closed = state { return }
            state = .closed(reason)
        }
    }

    /// The first snapshot and the connection arrive in either order: the
    /// snapshot can be here before the opening command has answered. Live,
    /// and fitted, once both are here; until then keys would go nowhere.
    private func goLive() {
        guard pictured, conn != nil else { return }
        if state == .connecting { state = .live }
        fitOnOpen()
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

    func close() {
        history.end()
        generation += 1
        frames.close()
        state = .closed(Self.paused)
        job?.cancel()
        drop()
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

    /// The reader over sample history, with no connection behind it.
    func showHistory(_ lines: [String]) {
        history.seed(lines)
    }

    /// The live screen under the opening line, with no connection behind it.
    func showOpening(failed: Bool) {
        history.seedOpening(failed: failed)
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

    init(queue: TerminalFrames, wake: @escaping @Sendable (TerminalFrames.Offer) -> Void) {
        self.queue = queue
        self.wake = wake
    }

    func onFrame(_ frame: PBTerminalFrame) {
        wake(queue.offer(frame))
    }

    func onClose(_ reason: String) {
        var frame = PBTerminalFrame()
        frame.closed = reason
        wake(queue.offer(frame))
    }
}
