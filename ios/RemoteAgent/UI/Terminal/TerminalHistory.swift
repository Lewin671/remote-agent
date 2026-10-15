// Reading the terminal's history: a fixed capture, taken by the daemon and
// paged from the newest line back (architecture §11 「回看历史」). The reader
// shows the pages; what is here is the paging — one request at a time, a
// reply only taken if it answers the request still waiting, a timeout that
// leaves what was already read alone.
import CoreGraphics
import Foundation
import Observation
import RAProto

@MainActor @Observable
final class TerminalHistory {
    struct Page: Identifiable, Equatable {
        let id: String
        /// The lines as `capture-pane -e` wrote them, SGR and all.
        let lines: [String]
    }

    enum Phase: Equatable {
        /// Not reading: the live screen and its own gestures.
        case idle
        /// Asked for, the first page not here yet: the live screen stays, with a line saying so.
        case opening
        /// That first page failed or timed out: still the live screen, with 读取失败 and 重试.
        case openingFailed
        /// The reader is on screen.
        case open
    }

    /// The line over the live screen while the first page is on its way, or that it failed.
    enum Opening: Equatable {
        case reading
        case failed
    }

    enum Trouble: Equatable {
        case none
        case failed
        case expired
        case disconnected
    }

    /// The one line above the reader's content.
    enum Status: Equatable {
        case none
        case earlier
        case failed
        case expired
        case start
        case disconnected
    }

    private(set) var phase = Phase.idle
    private(set) var pages: [Page] = []
    private(set) var cursor = ""
    private(set) var pending = ""
    private(set) var trouble = Trouble.none
    /// Output has arrived since reading began; the reader never moves for it.
    private(set) var hasNewOutput = false
    /// How far the screen was pulled when reading was asked for: where the reader opens.
    private(set) var entryPull: CGFloat = 0
    @ObservationIgnored private var timeout: Task<Void, Never>?
    /// How long a page is waited for before the request is given up on. A test moves it.
    @ObservationIgnored private let wait: Duration

    init(wait: Duration = .seconds(10)) {
        self.wait = wait
    }

    var reading: Bool { phase != .idle }
    /// The reader itself, not the live screen under the opening line.
    var open: Bool { phase == .open }

    /// What is over the live screen while the reader is not there yet: nothing, a line
    /// saying the record is being read, or one saying it could not be. The toolbar and
    /// the fit button are the live ones until the reader itself is on screen.
    var opening: Opening? {
        switch phase {
        case .opening: .reading
        case .openingFailed: .failed
        case .idle, .open: nil
        }
    }

    var status: Status {
        if !pending.isEmpty { return .earlier }
        switch trouble {
        case .failed: return .failed
        case .expired: return .expired
        case .disconnected: return .disconnected
        case .none: break
        }
        // No cursor to ask with: the capture's oldest line, or nothing kept at all.
        return cursor.isEmpty && !pages.isEmpty ? .start : .none
    }

    /// Leaves reading and forgets what was read.
    func end() { reset() }

    /// Reads the record from now, opening `pull` points into it.
    func begin(pull: CGFloat, send: @escaping (String, String) -> Void) {
        guard phase == .idle else { return }
        reset()
        phase = .opening
        entryPull = max(0, pull)
        request(send)
    }

    /// The pull went on while the first page was on its way.
    func pulled(_ distance: CGFloat) {
        guard phase == .opening else { return }
        entryPull = max(entryPull, distance)
    }

    /// Asks for the page before the oldest one held.
    func earlier(send: @escaping (String, String) -> Void) {
        guard phase == .open, pending.isEmpty, trouble == .none, !cursor.isEmpty else { return }
        request(send)
    }

    /// Asks again after a failure; nothing retries on its own. It is the 重试 over the
    /// live screen for a first page that failed, and the one in the reader for a later one.
    func retry(send: @escaping (String, String) -> Void) {
        guard pending.isEmpty else { return }
        switch phase {
        case .openingFailed:
            phase = .opening
            request(send)
        case .open where trouble == .failed:
            trouble = .none
            request(send)
        case .idle, .opening, .open:
            break
        }
    }

    /// Output arrived; the reader is told, and nothing moves.
    func markOutput() {
        guard phase != .idle else { return }
        hasNewOutput = true
    }

    /// The stream ended. What was read stays; no more is asked for. A first page that was
    /// still on its way, or that had failed, is nothing to read: the live screen is left plain.
    func disconnected() {
        timeout?.cancel()
        pending = ""
        switch phase {
        case .open: trouble = .disconnected
        case .opening, .openingFailed, .idle: reset()
        }
    }

    func receive(_ page: PBTerminalHistoryPage) {
        guard page.requestID == pending else { return }
        timeout?.cancel()
        pending = ""
        if phase == .opening {
            // A first page that failed does not open the reader: the live screen stays,
            // under 读取失败 and its 重试 (and another pull up is the same retry).
            guard page.error.isEmpty else {
                phase = .openingFailed
                return
            }
            // A capture with nothing kept: no reader, no reaction.
            guard !page.lines.isEmpty || !page.nextCursor.isEmpty else {
                reset()
                return
            }
            phase = .open
        } else if phase == .open {
            guard page.error.isEmpty else {
                trouble = page.error.lowercased().contains("expired") ? .expired : .failed
                return
            }
        } else {
            return
        }
        trouble = .none
        cursor = page.nextCursor
        if !page.lines.isEmpty { pages.insert(Page(id: page.requestID, lines: page.lines), at: 0) }
    }

    private func request(_ send: (String, String) -> Void) {
        let id = UUID().uuidString
        let wait = self.wait
        pending = id
        timeout = Task { [weak self] in
            guard (try? await Task.sleep(for: wait)) != nil, let self, self.pending == id else { return }
            self.pending = ""
            // A first page that never came leaves the live screen with 读取失败, as one that failed does.
            if self.phase == .opening { self.phase = .openingFailed } else { self.trouble = .failed }
        }
        send(id, cursor)
    }

    #if DEBUG
    /// Fills the reader from sample lines, with no connection behind it.
    func seed(_ lines: [String]) {
        reset()
        phase = .open
        pages = [Page(id: "sample", lines: lines)]
        entryPull = 60
    }

    /// The live screen under the opening line, for the gallery.
    func seedOpening(failed: Bool) {
        reset()
        phase = failed ? .openingFailed : .opening
    }
    #endif

    private func reset() {
        timeout?.cancel()
        phase = .idle
        pages = []
        cursor = ""
        pending = ""
        trouble = .none
        hasNewOutput = false
        entryPull = 0
    }
}
