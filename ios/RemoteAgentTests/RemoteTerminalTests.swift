import Foundation
import RAProto
import Testing

@testable import RACore
@testable import RemoteAgent

/// The terminal's connection, with a stream whose opening command answers
/// when the test says, if at all: what its listener hears is what drives the
/// screen (Android has no test of this).
@MainActor
@Suite struct RemoteTerminalTests {
    /// The streams asked for, each with the listener it was given, and what was sent on those that opened.
    private final class Streams: @unchecked Sendable {
        private let lock = NSLock()
        private var heard: [any TermListener] = []
        private var answered: [Int: TermConn] = [:]
        private var typed: [PBTerminalFrame] = []
        private var historyDeliverer: (@MainActor (PBTerminalHistoryPage) -> Void)?
        /// Whether a stream's opening command answers at once, or when `answer` says.
        private let answers: Bool
        init(answers: Bool = true) { self.answers = answers }
        var count: Int { lock.withLock { heard.count } }
        var sent: [PBTerminalFrame] { lock.withLock { typed } }
        func listener(_ i: Int) -> any TermListener { lock.withLock { heard[i] } }
        @MainActor func setHistoryDeliverer(_ fn: @escaping @MainActor (PBTerminalHistoryPage) -> Void) {
            historyDeliverer = fn
        }
        @MainActor func deliverHistory(_ page: PBTerminalHistoryPage) {
            historyDeliverer?(page)
        }
        func add(_ l: any TermListener) -> Int {
            let i = lock.withLock {
                heard.append(l)
                return heard.count - 1
            }
            if answers { answer(i) }
            return i
        }

        func conn(_ i: Int) -> TermConn? { lock.withLock { answered[i] } }

        /// The opening command of stream `i` answers; frames are captured directly in memory.
        func answer(_ i: Int) {
            let conn = TermConn(
                sendFrame: { [self] frame in
                    lock.withLock { typed.append(frame) }
                },
                closeLocal: {}
            )
            lock.withLock { answered[i] = conn }
        }
    }

    private final class Flag: @unchecked Sendable {
        private let lock = NSLock()
        private var value = true
        var on: Bool {
            get { lock.withLock { value } }
            set { lock.withLock { value = newValue } }
        }
    }

    private struct RecordedEvent: Equatable, Sendable {
        let kind: PBCoreTerminalStreamEventKind
        let opened: Bool
    }

    private final class CoreRecorder: @unchecked Sendable {
        private let lock = NSLock()
        private var events: [RecordedEvent] = []
        private var reopenHandler: (@MainActor @Sendable () -> Void)?

        var recorded: [RecordedEvent] {
            lock.withLock { events }
        }

        func append(_ kind: PBCoreTerminalStreamEventKind, _ opened: Bool) {
            lock.withLock { events.append(RecordedEvent(kind: kind, opened: opened)) }
        }

        func setReopen(_ handler: @escaping @MainActor @Sendable () -> Void) {
            lock.withLock { reopenHandler = handler }
        }

        func clearReopen() {
            lock.withLock { reopenHandler = nil }
        }

        @MainActor func triggerReopen() {
            let h = lock.withLock { reopenHandler }
            h?()
        }
    }

    @MainActor
    private final class CoreHistoryDriver {
        private let core = try! CoreClient()
        private var reqHandler: (@MainActor @Sendable (PBCoreTerminalHistorySendRequestEffect) -> Void)?
        private var updHandler: (@MainActor @Sendable (PBCoreTerminalHistoryUpdatedEffect) -> Void)?
        private var seq = 0

        func subscribe(
            onRequest: @escaping @MainActor @Sendable (PBCoreTerminalHistorySendRequestEffect) -> Void,
            onUpdated: @escaping @MainActor @Sendable (PBCoreTerminalHistoryUpdatedEffect) -> Void
        ) -> () -> Void {
            reqHandler = onRequest
            updHandler = onUpdated
            return { [weak self] in
                self?.reqHandler = nil
                self?.updHandler = nil
            }
        }

        func send(terminalID: String, kind: PBCoreTerminalHistoryEventKind, rawPage: Data?) {
            seq += 1
            let batch = try! core.dispatch(
                .with {
                    $0.randomIds = ["req-\(seq)", "req-\(seq)-b"]
                    $0.terminalHistory = .with {
                        $0.terminalID = terminalID
                        $0.kind = kind
                        if let rawPage, !rawPage.isEmpty { $0.rawPage = rawPage }
                    }
                })
            for eff in batch.effects {
                switch eff.effect {
                case .terminalHistorySendRequest(let req)?:
                    reqHandler?(req)
                case .terminalHistoryUpdated(let upd)?:
                    updHandler?(upd)
                default:
                    break
                }
            }
        }
    }

    private func plainLines(_ page: PBCoreTerminalHistoryParsedPage?) -> [String]? {
        page?.lines.map { $0.runs.map(\.text).joined() }
    }

    private func terminal(
        _ streams: Streams,
        online: Flag = Flag(),
        fail: String? = nil,
        recorder: CoreRecorder = CoreRecorder(),
        autoReopenOnClosed: Bool = false
    )
        -> RemoteTerminal
    {
        let terminal = RemoteTerminal()
        let historyDriver = CoreHistoryDriver()
        streams.setHistoryDeliverer { page in
            historyDriver.send(terminalID: terminal.terminalId, kind: .pageReceived, page: page)
        }
        terminal.host.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        terminal.host.layoutIfNeeded()
        terminal.use(
            RemoteTerminal.Link(
                open: { listener in
                    let i = streams.add(listener)
                    if let fail { throw UserError(fail) }
                    while true {
                        if let conn = streams.conn(i) { return conn }
                        try await Task.sleep(for: .milliseconds(5))
                    }
                },
                online: { online.on },
                hold: { _ in UUID() },
                release: { _ in },
                reportStream: { _, kind, opened in
                    recorder.append(kind, opened)
                    if autoReopenOnClosed && kind == .closed && opened {
                        DispatchQueue.main.async {
                            MainActor.assumeIsolated { recorder.triggerReopen() }
                        }
                    }
                },
                subscribeReopen: { _, handler in
                    recorder.setReopen(handler)
                    return { recorder.clearReopen() }
                },
                sendHistory: { id, kind, rawPage in
                    historyDriver.send(terminalID: id, kind: kind, rawPage: rawPage)
                },
                subscribeHistory: { _, onRequest, onUpdated in
                    historyDriver.subscribe(onRequest: onRequest, onUpdated: onUpdated)
                }))
        return terminal
    }

    private func snapshot(_ text: String, cols: Int32 = 80, rows: Int32 = 24) -> PBTerminalFrame {
        .with {
            $0.snapshot = .with {
                $0.cols = cols
                $0.rows = rows
                $0.data = Data(text.utf8)
            }
        }
    }

    private func output(_ text: String) -> PBTerminalFrame { .with { $0.output = Data(text.utf8) } }

    @Test func relayRecoveryKeepsHealthyTerminalAndDoesNotRetypeAfterRealLegLoss() async {
        let streams=Streams(), online=Flag()
        let terminal=terminal(streams, online: online, autoReopenOnClosed: true)
        defer {terminal.close()}
        terminal.connect()
        await eventually {streams.count==1}
        streams.listener(0).onFrame(snapshot("retained"))
        await eventually {terminal.state == .live}
        terminal.send(Data("once".utf8))
        await eventually {streams.sent.contains{$0.input==Data("once".utf8)}}
        online.on=false;terminal.connectIfNeeded()
        #expect(terminal.state == .live && streams.count==1)
        streams.listener(0).onFrame(output(" screen"))
        await eventually {terminal.host.screenText=="retained screen"}
        online.on=true;terminal.connectIfNeeded()
        #expect(streams.count==1)
        streams.listener(0).onClose("closed")
        await eventually {streams.count==2}
        guard streams.count==2 else { #expect(Bool(false), "terminal leg was not recovered"); return }
        #expect(terminal.host.screenText=="retained screen")
        streams.listener(1).onFrame(snapshot("latest"))
        await eventually {terminal.state == .live}
        #expect(streams.sent.filter{$0.input==Data("once".utf8)}.count==1)
    }

    /// A pull down reads the record; the live screen keeps being written to behind the
    /// reader and never moves it, and a key leaves reading before it is sent
    /// (architecture §11 「回看历史」).
    @Test func aPullReadsTheRecordApartFromTheLiveScreenAndAKeyLeavesIt() async {
        let streams = Streams()
        let t = terminal(streams)
        t.connect()
        await eventually { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("live"))
        await eventually { t.state == .live }

        t.readUp(40)
        #expect(t.reading && t.history.phase == .opening && t.entryPull == 40)
        t.readUp(65)
        #expect(t.entryPull == 65)
        await eventually { streams.sent.last?.historyRequest.requestID.isEmpty == false }
        let request = streams.sent.last!.historyRequest
        #expect(!request.requestID.isEmpty && request.cursor.isEmpty)

        streams.deliverHistory(
            .with {
                $0.requestID = request.requestID
                $0.lines = ["older"]
                $0.nextCursor = "capture:200"
            })
        streams.listener(0).onFrame(output(" output"))
        await eventually { !t.history.pending && t.hasNewOutput }
        #expect(t.history.open && plainLines(t.history.pages.first) == ["older"])
        t.readUp(90)
        #expect(t.entryPull == 65)
        // Output arrived while reading: the pill says so, and the live screen behind has it.
        #expect(t.hasNewOutput)
        #expect(t.host.screenText == "live output")
        #expect(t.state == .live)

        // A key leaves reading first, and then goes as it always did.
        t.send(Data("input".utf8))
        #expect(!t.reading)
        await eventually { streams.sent.last?.input == Data("input".utf8) }
        #expect(streams.sent.last?.input == Data("input".utf8))
        t.close()
    }

    /// Reading the record needs a live stream: before one there is nothing to capture.
    @Test func aPullWithoutALiveStreamDoesNothing() {
        let streams = Streams()
        let t = terminal(streams)
        t.readUp(40)
        #expect(!t.reading && streams.sent.isEmpty)
    }

    /// A pull has to be worth something: an accidental small drag asks for nothing
    /// (client-v40 · 终端 · 「往上滑不会变成方向键发给 CLI」).
    @Test func aSmallPullIsNotAWishToRead() async {
        let streams = Streams()
        let t = terminal(streams)
        t.connect()
        await eventually { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("live"))
        await eventually { t.state == .live }
        // Going live queues the initial fit on TermConn's asynchronous writer.
        // Observe it before counting: otherwise the wait below can mistake that
        // resize for the history request and race its actual delivery.
        await eventually { streams.sent.contains { $0.resize.fit } }
        let before = streams.sent.count
        t.readUp(RemoteTerminal.readPull - 1)
        #expect(!t.reading && streams.sent.count == before)
        t.readUp(RemoteTerminal.readPull)
        await eventually { streams.sent.count == before + 1 }
        #expect(t.history.phase == .opening && streams.sent.count == before + 1)
        t.close()
    }

    /// A first page that failed does not open the reader: the live screen and its toolbar
    /// stay, a key clears it before going, and the next pull up is the retry.
    @Test func aFailedFirstPageStaysLiveAndAKeyOrAnotherPullClearsIt() async {
        let streams = Streams()
        let t = terminal(streams)
        t.connect()
        await eventually { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("live"))
        await eventually { t.state == .live }

        t.readUp(40)
        await eventually { streams.sent.last?.historyRequest.requestID.isEmpty == false }
        let first = streams.sent.last!.historyRequest
        streams.deliverHistory(
            .with {
                $0.requestID = first.requestID
                $0.error = "cannot capture terminal history; retry"
            })
        await eventually { t.history.phase == .openingFailed }
        #expect(
            !t.history.open && t.history.opening && t.history.canRetry
                && t.state == .live)
        // The live screen behind the line is untouched.
        #expect(t.host.screenText == "live")

        // A key-bar key clears it first, and then goes as it always did.
        t.send(Data("x".utf8))
        #expect(!t.reading && !t.history.opening)
        await eventually { streams.sent.last?.input == Data("x".utf8) }
        #expect(streams.sent.last?.input == Data("x".utf8))

        // The 键盘 key does the same before it acts.
        t.readUp(40)
        await eventually { streams.sent.last?.historyRequest.requestID.isEmpty == false }
        let asked = streams.sent.last!.historyRequest
        streams.deliverHistory(
            .with {
                $0.requestID = asked.requestID
                $0.error = "cannot capture terminal history; retry"
            })
        await eventually { t.history.phase == .openingFailed }
        t.leaveReading()
        #expect(!t.reading)

        // Another pull up asks for the capture again, with no cursor.
        let before = streams.sent.count
        t.readUp(40)
        await eventually { streams.sent.count == before + 1 }
        #expect(t.history.phase == .opening && streams.sent.count == before + 1)
        let second = streams.sent.last!.historyRequest
        #expect(second.cursor.isEmpty && second.requestID != first.requestID)
        streams.deliverHistory(
            .with {
                $0.requestID = second.requestID
                $0.lines = ["older"]
                $0.nextCursor = "capture:200"
            })
        await eventually { t.history.open }
        #expect(plainLines(t.history.pages.first) == ["older"])
        t.close()
    }

    @Test func aSnapshotThenOutputIsTheScreen() async {
        let streams = Streams()
        let online = Flag()
        let t = terminal(streams, online: online)
        #expect(t.state == .connecting)
        t.connect()
        await eventually { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("one"))
        streams.listener(0).onFrame(output("\r\ntwo"))
        await eventually { t.host.screenText == "one\ntwo" }
        #expect(t.state == .live)
        #expect(t.size == TerminalSize(80, 24))
        #expect(t.host.screenText == "one\ntwo")
        online.on = false
        streams.listener(0).onClose("终端连接断开")
        await eventually { t.state != .live }
        #expect(t.state == .closed(L10n.errorsTerminalConnectionDropped()))
        // The last picture stays under the notice.
        #expect(t.host.screenText == "one\ntwo")
        t.close()
    }

    /// architecture §11 「积压」: more than the screen keeps up with ends the stream, and Core's reopen opens another from a snapshot.
    @Test func aBacklogReopensTheStreamInsteadOfSkippingBytes() async {
        let streams = Streams()
        let t = terminal(streams, autoReopenOnClosed: true)
        t.connect()
        await eventually { streams.count == 1 }
        let old = streams.listener(0)
        old.onFrame(snapshot("before"))
        await eventually { t.state == .live }
        // All at once, before the main thread gets to look.
        let chunk = PBTerminalFrame.with { $0.output = Data(repeating: 0x78, count: 64 * 1024) }
        for _ in 0..<40 { old.onFrame(chunk) }
        await eventually { streams.count == 2 }
        #expect(streams.count == 2)
        #expect(t.state == .connecting)
        // What the old stream still delivers changes nothing.
        old.onFrame(snapshot("stale"))
        old.onClose("late")
        try? await Task.sleep(for: .milliseconds(120))
        #expect(t.state == .connecting)
        #expect(!t.host.screenText.contains("stale"))
        streams.listener(1).onFrame(snapshot("fresh"))
        await eventually { t.state == .live }
        #expect(t.host.screenText == "fresh")
        #expect(streams.count == 2)
        t.close()
    }

    @Test func closingStopsListeningAndWithoutTheServerNothingOpens() async {
        let streams = Streams()
        let online = Flag()
        let t = terminal(streams, online: online)
        t.connect()
        await eventually { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("shown"))
        await eventually { t.state == .live }
        t.close()
        #expect(t.state == .closed(RemoteTerminal.paused))
        streams.listener(0).onFrame(output(" more"))
        streams.listener(0).onClose("late")
        try? await Task.sleep(for: .milliseconds(120))
        #expect(t.state == .closed(RemoteTerminal.paused))
        #expect(t.host.screenText == "shown")
        online.on = false
        t.connect()
        try? await Task.sleep(for: .milliseconds(50))
        #expect(streams.count == 1)
        #expect(t.state == .closed(RemoteTerminal.paused))
    }

    @Test func aStreamThatCannotOpenSaysWhy() async {
        let streams = Streams()
        let t = terminal(streams, fail: "连不上终端")
        t.connect()
        await eventually { t.state != .connecting }
        #expect(t.state == .closed(L10n.errorsCannotConnectToTerminal()))
    }

    /// A picture can come before the opening command answers: it is shown, but the terminal is not live, so
    /// the pane offers no keys that would go nowhere, and no fit is claimed.
    @Test func theTerminalIsLiveOnceItHasAPictureAndCanSend() async {
        let streams = Streams(answers: false)
        let t = terminal(streams)
        t.connect()
        await eventually { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("x"))
        await eventually { t.host.screenText == "x" }
        #expect(t.host.screenText == "x")
        #expect(t.state == .connecting)
        t.send(Data("ls".utf8))
        t.toggleFit()
        #expect(!t.fitted && streams.sent.isEmpty)
        // The command answered: live, fitted on its own, and what is typed goes out.
        streams.answer(0)
        await eventually { t.state == .live }
        #expect(t.state == .live)
        await eventually { !streams.sent.isEmpty } // TermConn seals/sends on its asynchronous writer.
        #expect(t.fitted && streams.sent.map(\.resize.fit) == [true])
        t.send(Data("ls".utf8))
        await eventually { streams.sent.last?.input == Data("ls".utf8) }
        #expect(streams.sent.last?.input == Data("ls".utf8))
        t.close()
    }

    /// The other order: the command answers first, and nothing is typed into a screen that shows nothing yet.
    @Test func anOpenStreamWithoutAPictureIsNotLiveYet() async {
        let streams = Streams(answers: false)
        let t = terminal(streams)
        t.connect()
        await eventually { streams.count == 1 }
        streams.answer(0)
        try? await Task.sleep(for: .milliseconds(100))
        #expect(t.state == .connecting)
        streams.listener(0).onFrame(snapshot("x"))
        await eventually { t.state == .live }
        #expect(t.state == .live && t.fitted)
        t.close()
    }

    /// The daemon says why it ends a stream and then hangs up: what the connection's end says does not replace it.
    @Test func theDaemonsReasonForClosingStays() async {
        let streams = Streams()
        let online = Flag()
        let t = terminal(streams, online: online)
        t.connect()
        await eventually { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("x"))
        online.on = false
        streams.listener(0).onFrame(.with { $0.closed = "cannot read the screen" })
        streams.listener(0).onClose("终端连接断开")
        await eventually { t.state != .connecting }
        try? await Task.sleep(for: .milliseconds(100))
        #expect(t.state == .closed("cannot read the screen"))
        // A stream that just ends says what the connection says.
        online.on = true
        t.connect()
        await eventually { streams.count == 2 }
        online.on = false
        streams.listener(1).onClose("终端连接断开")
        await eventually { t.state != .connecting }
        #expect(t.state == .closed(L10n.errorsTerminalConnectionDropped()))
    }

    @Test func terminalEmitsCoreStreamEventsAndReopensOnlyWhenActiveAndOnline() async {
        let streams = Streams()
        let online = Flag()
        let recorder = CoreRecorder()
        let t = terminal(streams, online: online, recorder: recorder)

        t.connect()
        #expect(recorder.recorded.last == RecordedEvent(kind: .manualReconnect, opened: false))
        await eventually { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("s0"))
        await eventually { t.state == .live }
        await eventually { recorder.recorded.last == RecordedEvent(kind: .live, opened: true) }

        // A close on a previously live stream emits `.closed(opened: true)` and waits for Core's reopen signal.
        streams.listener(0).onClose("c0")
        await eventually { recorder.recorded.last == RecordedEvent(kind: .closed, opened: true) }
        try? await Task.sleep(for: .milliseconds(50))
        #expect(streams.count == 1)

        // Core's reopen callback opens a new stream while active and online.
        recorder.triggerReopen()
        await eventually { streams.count == 2 }
        streams.listener(1).onFrame(snapshot("s1"))
        await eventually { t.state == .live }

        // Explicit close emits `.disposed` and suppresses later Core reopen signals until connect/connectIfNeeded.
        t.close()
        #expect(recorder.recorded.last == RecordedEvent(kind: .disposed, opened: false))
        recorder.triggerReopen()
        try? await Task.sleep(for: .milliseconds(50))
        #expect(streams.count == 2)

        // Coming back online calls `connectIfNeeded`, reactivating and opening a fresh stream.
        t.connectIfNeeded()
        await eventually { streams.count == 3 }
        t.close()
    }

    @Test func openFailureReportsUnopenedCloseAndDoesNotAutoSpin() async {
        let streams = Streams()
        let recorder = CoreRecorder()
        let t = terminal(streams, fail: "连不上终端", recorder: recorder)

        t.connect()
        await eventually { t.state != .connecting }
        #expect(recorder.recorded.contains { $0 == RecordedEvent(kind: .manualReconnect, opened: false) })
        #expect(recorder.recorded.last == RecordedEvent(kind: .closed, opened: false))

        // Calling connectIfNeeded while still active and in `.closed` waits for Core rather than re-opening immediately.
        t.connectIfNeeded()
        try? await Task.sleep(for: .milliseconds(50))
        #expect(streams.count == 1)
    }

    @Test func offlineReopenSignalDeactivatesAndAllowsConnectIfNeededWhenOnline() async {
        let streams = Streams()
        let online = Flag()
        let recorder = CoreRecorder()
        let t = terminal(streams, online: online, recorder: recorder)

        t.connect()
        await eventually { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("s0"))
        await eventually { t.state == .live }

        online.on = false
        streams.listener(0).onClose("c0")
        await eventually { t.state.isClosed }

        // A reopen signal arriving before peer readiness must transition to disposed/inactive so `connectIfNeeded` works later.
        recorder.triggerReopen()
        await eventually { recorder.recorded.last == RecordedEvent(kind: .disposed, opened: false) }

        online.on = true
        t.connectIfNeeded()
        await eventually { streams.count == 2 }
        t.close()
    }
}
