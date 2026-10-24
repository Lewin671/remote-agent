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
    // These tests observe frames at sealing, not network delivery. A real
    // socket to a refused port stops TermConn after its first send and makes
    // every later frame depend on that asynchronous connection failure.
    private struct AcceptingSocket: TerminalSocket {
        func send(_ data: Data, done: @escaping @Sendable (Error?) -> Void) { done(nil) }
        func cancel() {}
        func close() {}
    }

    /// The streams asked for, each with the listener it was given, and what was sent on those that opened.
    private final class Streams: @unchecked Sendable {
        private let lock = NSLock()
        private var heard: [any TermListener] = []
        private var answered: [Int: TermConn] = [:]
        private var typed: [PBTerminalFrame] = []
        /// Whether a stream's opening command answers at once, or when `answer` says.
        private let answers: Bool
        init(answers: Bool = true) { self.answers = answers }
        var count: Int { lock.withLock { heard.count } }
        var sent: [PBTerminalFrame] { lock.withLock { typed } }
        func listener(_ i: Int) -> any TermListener { lock.withLock { heard[i] } }
        func add(_ l: any TermListener) -> Int {
            let i = lock.withLock {
                heard.append(l)
                return heard.count - 1
            }
            if answers { answer(i) }
            return i
        }

        func conn(_ i: Int) -> TermConn? { lock.withLock { answered[i] } }

        /// The opening command of stream `i` answers; frames are accepted in memory and kept as sealed.
        func answer(_ i: Int) {
            let conn = TermConn(
                socket: AcceptingSocket(),
                seal: { [self] frame, _, _ in
                    lock.withLock { typed.append(frame) }
                    return Data()
                }, finish: { _ in }, closeLocal: {})
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

    private func terminal(_ streams: Streams, online: Flag = Flag(), fail: String? = nil)
        -> RemoteTerminal
    {
        let terminal = RemoteTerminal()
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
                online: { online.on }, hold: { _ in UUID() }, release: { _ in }))
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

    private func until(_ what: @MainActor () -> Bool) async {
        for _ in 0..<300 where !what() { try? await Task.sleep(for: .milliseconds(10)) }
    }

    /// A pull down reads the record; the live screen keeps being written to behind the
    /// reader and never moves it, and a key leaves reading before it is sent
    /// (architecture §11 「回看历史」).
    @Test func aPullReadsTheRecordApartFromTheLiveScreenAndAKeyLeavesIt() async {
        let streams = Streams()
        let t = terminal(streams)
        t.connect()
        await until { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("live"))
        await until { t.state == .live }

        t.readUp(40)
        #expect(t.reading && t.history.phase == .opening && t.history.entryPull == 40)
        await until { streams.sent.last?.historyRequest.requestID.isEmpty == false }
        let request = streams.sent.last!.historyRequest
        #expect(!request.requestID.isEmpty && request.cursor.isEmpty)

        streams.listener(0).onFrame(
            .with {
                $0.historyPage = .with {
                    $0.requestID = request.requestID
                    $0.lines = ["older"]
                    $0.nextCursor = "capture:200"
                }
            })
        streams.listener(0).onFrame(output(" output"))
        await until { t.history.pending.isEmpty }
        #expect(t.history.open && t.history.pages.first?.lines == ["older"])
        // Output arrived while reading: the pill says so, and the live screen behind has it.
        #expect(t.history.hasNewOutput)
        #expect(t.host.screenText == "live output")
        #expect(t.state == .live)

        // A key leaves reading first, and then goes as it always did.
        t.send(Data("input".utf8))
        #expect(!t.reading)
        await until { streams.sent.last?.input == Data("input".utf8) }
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
        await until { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("live"))
        await until { t.state == .live }
        let before = streams.sent.count
        t.readUp(RemoteTerminal.readPull - 1)
        #expect(!t.reading && streams.sent.count == before)
        t.readUp(RemoteTerminal.readPull)
        await until { streams.sent.count == before + 1 }
        #expect(t.history.phase == .opening && streams.sent.count == before + 1)
        t.close()
    }

    /// A first page that failed does not open the reader: the live screen and its toolbar
    /// stay, a key clears it before going, and the next pull up is the retry.
    @Test func aFailedFirstPageStaysLiveAndAKeyOrAnotherPullClearsIt() async {
        let streams = Streams()
        let t = terminal(streams)
        t.connect()
        await until { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("live"))
        await until { t.state == .live }

        t.readUp(40)
        await until { streams.sent.last?.historyRequest.requestID.isEmpty == false }
        let first = streams.sent.last!.historyRequest
        streams.listener(0).onFrame(
            .with {
                $0.historyPage = .with {
                    $0.requestID = first.requestID
                    $0.error = "cannot capture terminal history; retry"
                }
            })
        await until { t.history.phase == .openingFailed }
        #expect(
            !t.history.open && t.history.opening == TerminalHistory.Opening.failed
                && t.state == .live)
        // The live screen behind the line is untouched.
        #expect(t.host.screenText == "live")

        // A key-bar key clears it first, and then goes as it always did.
        t.send(Data("x".utf8))
        #expect(!t.reading && t.history.opening == nil)
        await until { streams.sent.last?.input == Data("x".utf8) }
        #expect(streams.sent.last?.input == Data("x".utf8))

        // The 键盘 key does the same before it acts.
        t.readUp(40)
        await until { streams.sent.last?.historyRequest.requestID.isEmpty == false }
        let asked = streams.sent.last!.historyRequest
        streams.listener(0).onFrame(
            .with {
                $0.historyPage = .with {
                    $0.requestID = asked.requestID
                    $0.error = "cannot capture terminal history; retry"
                }
            })
        await until { t.history.phase == .openingFailed }
        t.leaveReading()
        #expect(!t.reading)

        // Another pull up asks for the capture again, with no cursor.
        let before = streams.sent.count
        t.readUp(40)
        await until { streams.sent.count == before + 1 }
        #expect(t.history.phase == .opening && streams.sent.count == before + 1)
        let second = streams.sent.last!.historyRequest
        #expect(second.cursor.isEmpty && second.requestID != first.requestID)
        streams.listener(0).onFrame(
            .with {
                $0.historyPage = .with {
                    $0.requestID = second.requestID
                    $0.lines = ["older"]
                    $0.nextCursor = "capture:200"
                }
            })
        await until { t.history.open }
        #expect(t.history.pages.first?.lines == ["older"])
        t.close()
    }

    @Test func aSnapshotThenOutputIsTheScreen() async {
        let streams = Streams()
        let t = terminal(streams)
        #expect(t.state == .connecting)
        t.connect()
        await until { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("one"))
        streams.listener(0).onFrame(output("\r\ntwo"))
        await until { t.host.screenText == "one\ntwo" }
        #expect(t.state == .live)
        #expect(t.size == TerminalSize(80, 24))
        #expect(t.host.screenText == "one\ntwo")
        streams.listener(0).onClose("终端连接断开")
        await until { t.state != .live }
        #expect(t.state == .closed("终端连接断开"))
        // The last picture stays under the notice.
        #expect(t.host.screenText == "one\ntwo")
        t.close()
    }

    /// architecture §11 「积压」: more than the screen keeps up with ends the stream, and another starts from a snapshot.
    @Test func aBacklogReopensTheStreamInsteadOfSkippingBytes() async {
        let streams = Streams()
        let t = terminal(streams)
        t.connect()
        await until { streams.count == 1 }
        let old = streams.listener(0)
        old.onFrame(snapshot("before"))
        await until { t.state == .live }
        // All at once, before the main thread gets to look.
        let chunk = PBTerminalFrame.with { $0.output = Data(repeating: 0x78, count: 64 * 1024) }
        for _ in 0..<40 { old.onFrame(chunk) }
        await until { streams.count == 2 }
        #expect(streams.count == 2)
        #expect(t.state == .connecting)
        // What the old stream still delivers changes nothing.
        old.onFrame(snapshot("stale"))
        old.onClose("late")
        try? await Task.sleep(for: .milliseconds(120))
        #expect(t.state == .connecting)
        #expect(!t.host.screenText.contains("stale"))
        streams.listener(1).onFrame(snapshot("fresh"))
        await until { t.state == .live }
        #expect(t.host.screenText == "fresh")
        #expect(streams.count == 2)
        t.close()
    }

    @Test func closingStopsListeningAndWithoutTheServerNothingOpens() async {
        let streams = Streams()
        let online = Flag()
        let t = terminal(streams, online: online)
        t.connect()
        await until { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("shown"))
        await until { t.state == .live }
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
        await until { t.state != .connecting }
        #expect(t.state == .closed("连不上终端"))
    }

    /// A picture can come before the opening command answers: it is shown, but the terminal is not live, so
    /// the pane offers no keys that would go nowhere, and no fit is claimed.
    @Test func theTerminalIsLiveOnceItHasAPictureAndCanSend() async {
        let streams = Streams(answers: false)
        let t = terminal(streams)
        t.connect()
        await until { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("x"))
        await until { t.host.screenText == "x" }
        #expect(t.host.screenText == "x")
        #expect(t.state == .connecting)
        t.send(Data("ls".utf8))
        t.toggleFit()
        #expect(!t.fitted && streams.sent.isEmpty)
        // The command answered: live, fitted on its own, and what is typed goes out.
        streams.answer(0)
        await until { t.state == .live }
        #expect(t.state == .live)
        await until { !streams.sent.isEmpty } // TermConn seals/sends on its asynchronous writer.
        #expect(t.fitted && streams.sent.map(\.resize.fit) == [true])
        t.send(Data("ls".utf8))
        await until { streams.sent.last?.input == Data("ls".utf8) }
        #expect(streams.sent.last?.input == Data("ls".utf8))
        t.close()
    }

    /// The other order: the command answers first, and nothing is typed into a screen that shows nothing yet.
    @Test func anOpenStreamWithoutAPictureIsNotLiveYet() async {
        let streams = Streams(answers: false)
        let t = terminal(streams)
        t.connect()
        await until { streams.count == 1 }
        streams.answer(0)
        try? await Task.sleep(for: .milliseconds(100))
        #expect(t.state == .connecting)
        streams.listener(0).onFrame(snapshot("x"))
        await until { t.state == .live }
        #expect(t.state == .live && t.fitted)
        t.close()
    }

    /// The daemon says why it ends a stream and then hangs up: what the connection's end says does not replace it.
    @Test func theDaemonsReasonForClosingStays() async {
        let streams = Streams()
        let t = terminal(streams)
        t.connect()
        await until { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("x"))
        streams.listener(0).onFrame(.with { $0.closed = "cannot read the screen" })
        streams.listener(0).onClose("终端连接断开")
        await until { t.state != .connecting }
        try? await Task.sleep(for: .milliseconds(100))
        #expect(t.state == .closed("cannot read the screen"))
        // A stream that just ends says what the connection says.
        t.connect()
        await until { streams.count == 2 }
        streams.listener(1).onClose("终端连接断开")
        await until { t.state != .connecting }
        #expect(t.state == .closed("终端连接断开"))
    }
}
