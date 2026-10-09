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
        return host
    }()

    @ObservationIgnored private var link: Link?
    /// Columns and rows the view has room for at a readable size, for "fit".
    @ObservationIgnored private var fitSize = TerminalSize(0, 0)
    @ObservationIgnored private var fit = FitTracker()
    @ObservationIgnored private var conn: TermConn?
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
                self.fitOnOpen()
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
        case .snapshot(let cols, let rows, let data):
            host.snapshot(cols: cols, rows: rows, data: data)
            size = TerminalSize(cols, rows)
            if fit.onSnapshot(size) { resizedElsewhere = true }
            fitted = fit.fitted
            state = .live
            fitOnOpen()
        case .output(let data):
            host.append(data)
        case .closed(let reason):
            state = .closed(reason)
        }
    }

    func send(_ data: Data) {
        guard state == .live, let conn else { return }
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

    /// The first snapshot and the connection arrive in either order; fit once both are here.
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
