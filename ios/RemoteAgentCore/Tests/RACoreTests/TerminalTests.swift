import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's term/KeysTest.
@Suite struct TerminalKeysTests {
    /// A snapshot of that size arrives; whether it changed without this screen asking.
    private func snapshot(_ f: inout FitTracker, _ size: TerminalSize) -> Bool { f.onSnapshot(size) }

    @Test func arrowsFollowTheCursorKeyMode() throws {
        let left = try #require(terminalKeyRows[0].first { $0.label == "左" })
        let right = try #require(terminalKeyRows[0].first { $0.label == "右" })
        #expect(terminalKeySequence(left, appCursor: false) == "\u{1b}[D")
        #expect(terminalKeySequence(left, appCursor: true) == "\u{1b}OD")
        #expect(terminalKeySequence(right, appCursor: false) == "\u{1b}[C")
    }

    @Test func theFirstRowHasEscArrowsAndEnter() {
        #expect(terminalKeyRows[0].map(\.label) == ["Esc", "左", "上", "下", "右", "回车"])
        #expect(terminalKeyRows[1].map(\.label) == ["Tab", "Ctrl+C"])
    }

    @Test func fittedOnlyWhileTheWindowKeepsTheRequestedSize() {
        var f = FitTracker()
        #expect(!snapshot(&f, TerminalSize(120, 40)))
        f.request(TerminalSize(42, 30))
        #expect(f.fitted)
        // The old size still in flight does not undo the request.
        #expect(!snapshot(&f, TerminalSize(120, 40)))
        #expect(f.fitted)
        #expect(!snapshot(&f, TerminalSize(42, 30)))
        #expect(f.fitted)
        // Someone else resized it: no longer fitted, and it says so.
        #expect(snapshot(&f, TerminalSize(80, 30)))
        #expect(!f.fitted)
    }

    @Test func fittingToTheSizeItAlreadyHasNeedsNoNewSnapshot() {
        var f = FitTracker()
        _ = f.onSnapshot(TerminalSize(42, 30))
        f.request(TerminalSize(42, 30))
        #expect(snapshot(&f, TerminalSize(80, 30)))
        #expect(!f.fitted)
    }

    @Test func releasingIsNotAChangeFromElsewhere() {
        var f = FitTracker()
        _ = f.onSnapshot(TerminalSize(120, 40))
        f.request(TerminalSize(42, 30))
        _ = f.onSnapshot(TerminalSize(42, 30))
        f.release()
        #expect(!f.fitted)
        #expect(!snapshot(&f, TerminalSize(120, 40)))
        #expect(snapshot(&f, TerminalSize(100, 40)))
    }

    // --- not in Android's tests ---

    @Test func whatTheKeysSend() {
        let sent = terminalKeyRows.joined().map { terminalKeySequence($0, appCursor: false) }
        #expect(sent == ["\u{1b}", "\u{1b}[D", "\u{1b}[A", "\u{1b}[B", "\u{1b}[C", "\r", "\t", "\u{03}"])
    }
}

/// Android's term/RedactTest.
@Suite struct TerminalRedactTests {
    @Test func logsOnlyTheSize() {
        let m = "OSC Manipulate selection, invalid string 'sk-secret-token"
        let out = terminalLogRedact(m)
        #expect(!out.contains("secret"))
        #expect(out == "terminal: emulator message of \(m.utf16.count) chars")
    }

    // --- not in Android's tests ---

    @Test func countsAsAndroidDoes() {
        #expect(terminalLogRedact(nil) == "terminal: emulator message of 0 chars")
        #expect(terminalLogRedact("中😀") == "terminal: emulator message of 3 chars")
    }
}

/// Android's term/TerminalFramesTest.
@Suite struct TerminalFramesTests {
    private func output(_ text: String) -> PBTerminalFrame {
        .with { $0.output = Data(text.utf8) }
    }

    @Test func preservesSnapshotOutputAndCloseOrderAcrossBoundedDrains() {
        let q = TerminalFrames()
        let snapshot = PBTerminalFrame.with { $0.snapshot = .with { $0.cols = 80 } }
        let frames = [snapshot, output("abc"), output("def"), PBTerminalFrame.with { $0.closed = "done" }]
        #expect(q.offer(frames[0]) == .schedule)
        for f in frames.dropFirst() { #expect(q.offer(f) == .queued) }
        let (first, more) = q.drain(maxBatchBytes: 3)
        #expect(first == Array(frames.prefix(2)))
        #expect(more)
        let (last, again) = q.drain(maxBatchBytes: 3)
        #expect(last == Array(frames.dropFirst(2)))
        #expect(!again)
        #expect(q.offer(output("new")) == .schedule)
    }

    @Test func overflowIsReportedOnceAndOldQueuesCannotAffectNewConnections() {
        let old = TerminalFrames(maxBytes: 3, maxFrames: 2)
        #expect(old.offer(output("abc")) == .schedule)
        #expect(old.offer(output("d")) == .overflow)
        #expect(old.offer(output("late")) == .closed)
        #expect(old.drain().batch.isEmpty)
        let next = TerminalFrames()
        #expect(next.offer(output("new")) == .schedule)
        old.close()
        #expect(old.offer(output("late")) == .closed)
        let got = next.drain().batch
        #expect(got.count == 1)
        #expect(got.first?.output == Data("new".utf8))
        let small = TerminalFrames(maxFrames: 1)
        _ = small.offer(output(""))
        #expect(small.offer(output("")) == .overflow)
    }

    // --- not in Android's tests ---

    @Test func aBatchFeedsAdjacentOutputAtOnceAndKeepsTheRestInPlace() {
        let snapshot = PBTerminalFrame.with { $0.snapshot = .with { $0.cols = 80; $0.rows = 24; $0.data = Data("S".utf8) } }
        let steps = terminalSteps([
            output("a"), output("b"), snapshot, output("c"), .with { $0.closed = "" }, output("d"), .with { $0.closed = "over" },
        ])
        #expect(
            steps == [
                .output(Data("ab".utf8)), .snapshot(cols: 80, rows: 24, data: Data("S".utf8)), .output(Data("c".utf8)),
                .closed("终端已关闭"), .output(Data("d".utf8)), .closed("over"),
            ])
        #expect(terminalSteps([]).isEmpty)
    }

    @Test func aDrainedQueueCountsOnlyWhatStillWaits() {
        let q = TerminalFrames(maxBytes: 4, maxFrames: 2)
        for _ in 0..<50 {
            #expect(q.offer(output("abcd")) == .schedule)
            #expect(q.drain().batch.count == 1)
        }
        #expect(q.offer(output("ab")) == .schedule)
        #expect(q.offer(output("abc")) == .overflow)
    }

    @Test func aQueueThatNeverRunsEmptyLetsGoOfWhatWasTaken() {
        final class Freed: @unchecked Sendable {
            private let lock = NSLock()
            private var n = 0
            var count: Int { lock.withLock { n } }
            func add() { lock.withLock { n += 1 } }
        }
        let freed = Freed()
        // Too long to be kept inline: the bytes are let go of when the last frame holding them is.
        func tracked() -> PBTerminalFrame {
            let size = 64
            let bytes = UnsafeMutableRawPointer.allocate(byteCount: size, alignment: 1)
            bytes.initializeMemory(as: UInt8.self, repeating: 0x61, count: size)
            return .with {
                $0.output = Data(bytesNoCopy: bytes, count: size, deallocator: .custom { p, _ in
                    p.deallocate()
                    freed.add()
                })
            }
        }
        let q = TerminalFrames()
        _ = q.offer(tracked())
        // Output keeps coming: there is always a frame behind the ones taken.
        for round in 1...100 {
            _ = q.offer(tracked())
            let (batch, more) = q.drain(maxBatchBytes: 64)
            #expect(batch.count == 1)
            #expect(more)
            _ = consume batch
            #expect(freed.count == round)
        }
    }
}

/// Android's term/TerminalGeometryTest.
@Suite struct TerminalGeometryTests {
    @Test func capacityAccountsForRoundedRowsAndInitialBaseline() {
        let g = TerminalGeometry(cellWidth: 18.5, lineHeight: 45, topOffset: 8)
        let size = g.capacity(width: 1059, height: 1673)
        #expect(size == TerminalSize(57, 37))
        #expect(g.contains(width: 1059, height: 1673, cols: size.cols, rows: size.rows))
        #expect(!g.contains(width: 1059, height: 1673, cols: size.cols + 1, rows: size.rows))
        #expect(!g.contains(width: 1059, height: 1673, cols: size.cols, rows: size.rows + 1))
    }

    // --- not in Android's tests ---

    /// iOS shows a font's cell scaled, so a line is not always whole pixels: rows are counted with what it is.
    @Test func aLineThatIsNotWholePixelsIsCountedAsItIs() {
        let g = TerminalGeometry(cellWidth: 10, lineHeight: 20.4)
        #expect(g.capacity(width: 800, height: 1020) == TerminalSize(80, 50))
        #expect(g.capacity(width: 800, height: 1019) == TerminalSize(80, 49))
        #expect(g.contains(width: 800, height: 1020, cols: 80, rows: 50))
        #expect(!g.contains(width: 800, height: 1019, cols: 80, rows: 50))
    }

    @Test func keyboardSizedViewportDoesNotProduceNegativeCapacity() {
        #expect(TerminalGeometry(cellWidth: 19, lineHeight: 40, topOffset: 8).capacity(width: -1, height: 3) == TerminalSize(0, 0))
    }

    // --- not in Android's tests ---

    /// A font whose cell is 0.6 by 1.2 of its size, as a monospaced one roughly is.
    private func cell(_ size: Int) -> TerminalGeometry {
        TerminalGeometry(cellWidth: Double(size) * 0.6, lineHeight: (Double(size) * 1.2).rounded(.up))
    }

    @Test func theFontShrinksUntilTheRemoteGridFits() {
        // 80 columns in 600: 0.6 × 12 × 80 = 576 fits, 13 does not.
        #expect(terminalFontSize(preferred: 42, width: 600, height: 4000, cols: 80, rows: 24, measure: cell) == 12)
        // The height limits it too.
        #expect(terminalFontSize(preferred: 42, width: 6000, height: 240, cols: 80, rows: 10, measure: cell) == 20)
        // A grid that fits keeps the size asked for; one that never fits stops at the smallest.
        #expect(terminalFontSize(preferred: 42, width: 6000, height: 4000, cols: 80, rows: 24, measure: cell) == 42)
        #expect(terminalFontSize(preferred: 42, width: 100, height: 4000, cols: 300, rows: 24, measure: cell) == 5)
        // No snapshot yet.
        #expect(terminalFontSize(preferred: 42, width: 100, height: 100, cols: 0, rows: 0, measure: cell) == 42)
    }
}
