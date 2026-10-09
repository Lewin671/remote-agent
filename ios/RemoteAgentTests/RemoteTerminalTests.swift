import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// The terminal's connection, with a stream whose opening command never
/// answers: what its listener hears is what drives the screen (Android has
/// no test of this).
@MainActor
@Suite struct RemoteTerminalTests {
    private struct Unanswered: Error {}

    /// The streams asked for, each with the listener it was given.
    private final class Streams: @unchecked Sendable {
        private let lock = NSLock()
        private var heard: [any TermListener] = []
        var count: Int { lock.withLock { heard.count } }
        func listener(_ i: Int) -> any TermListener { lock.withLock { heard[i] } }
        func add(_ l: any TermListener) { lock.withLock { heard.append(l) } }
    }

    private final class Flag: @unchecked Sendable {
        private let lock = NSLock()
        private var value = true
        var on: Bool {
            get { lock.withLock { value } }
            set { lock.withLock { value = newValue } }
        }
    }

    private func terminal(_ streams: Streams, online: Flag = Flag(), fail: String? = nil) -> RemoteTerminal {
        let terminal = RemoteTerminal()
        terminal.host.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        terminal.host.layoutIfNeeded()
        terminal.use(
            RemoteTerminal.Link(
                open: { listener in
                    streams.add(listener)
                    if let fail { throw UserError(fail) }
                    try await Task.sleep(for: .seconds(3600))
                    throw Unanswered()
                },
                online: { online.on }, hold: { _ in UUID() }, release: { _ in }))
        return terminal
    }

    private func snapshot(_ text: String, cols: Int32 = 80, rows: Int32 = 24) -> PBTerminalFrame {
        .with { $0.snapshot = .with { $0.cols = cols; $0.rows = rows; $0.data = Data(text.utf8) } }
    }

    private func output(_ text: String) -> PBTerminalFrame { .with { $0.output = Data(text.utf8) } }

    private func until(_ what: @MainActor () -> Bool) async {
        for _ in 0..<300 where !what() { try? await Task.sleep(for: .milliseconds(10)) }
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

    /// A picture can come before the opening command answers: until then nothing is sent and no fit is claimed.
    @Test func nothingIsSentBeforeTheStreamIsOpen() async {
        let streams = Streams()
        let t = terminal(streams)
        t.connect()
        await until { streams.count == 1 }
        streams.listener(0).onFrame(snapshot("x"))
        await until { t.state == .live }
        t.send(Data("ls".utf8))
        t.toggleFit()
        #expect(!t.fitted)
        t.close()
    }
}
