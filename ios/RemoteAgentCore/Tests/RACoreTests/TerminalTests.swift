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

    @Test func historyPagesCountTowardsBacklogAndKeepOrder() {
        let frame = PBTerminalFrame.with { $0.historyPage = .with { $0.requestID = "r"; $0.lines = ["中文中文"] } }
        let q = TerminalFrames(maxBytes: 3)
        #expect(q.offer(frame) == .overflow)
        let steps = terminalSteps([output("before"), frame, output("after")])
        #expect(steps == [.output(Data("before".utf8)), .history(frame.historyPage), .output(Data("after".utf8))])
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
        #expect(terminalFontSize(preferred: 42, smallest: 5, width: 600, height: 4000, cols: 80, rows: 24, measure: cell) == 12)
        // The height limits it too.
        #expect(terminalFontSize(preferred: 42, smallest: 5, width: 6000, height: 240, cols: 80, rows: 10, measure: cell) == 20)
        // A grid that fits keeps the size asked for; one that never fits stops at the smallest.
        #expect(terminalFontSize(preferred: 42, smallest: 5, width: 6000, height: 4000, cols: 80, rows: 24, measure: cell) == 42)
        #expect(terminalFontSize(preferred: 42, smallest: 5, width: 100, height: 4000, cols: 300, rows: 24, measure: cell) == 5)
        // No snapshot yet.
        #expect(terminalFontSize(preferred: 42, smallest: 5, width: 100, height: 100, cols: 0, rows: 0, measure: cell) == 42)
    }

    /// The floor is 6 (architecture §11 「尺寸」): below that a window is
    /// unreadable, so it is kept and panned instead.
    @Test func theFontNeverGoesBelowSix() {
        #expect(terminalFontFloor == 6)
        // A window far wider than the area still stops at the floor, whatever it is asked for.
        let floor = terminalFontSize(
            preferred: 22, smallest: terminalFontFloor, width: 1, height: 1, cols: 1000, rows: 1000, measure: cell)
        #expect(floor == terminalFontFloor && floor == 6)
    }
}

/// The direction of a drag on the live screen (architecture §11 「尺寸」): the
/// dominant axis decides, a downward pull reads, an upward one does nothing.
@Suite struct TerminalDragTests {
    @Test func theDominantAxisDecides() {
        // Sideways, either way: the screen is panned, and the remote is untouched.
        #expect(terminalDrag(dx: 40, dy: 3) == .pan)
        #expect(terminalDrag(dx: -40, dy: 3) == .pan)
        // A tie goes to the sideways pan.
        #expect(terminalDrag(dx: 30, dy: 30) == .pan)
        // Downwards: the record is read.
        #expect(terminalDrag(dx: 3, dy: 40) == .read)
        // Upwards: nothing, as a live screen always has. `.ignore`, and not `nil`.
        #expect(terminalDrag(dx: 3, dy: -40) == .ignore)
        #expect(terminalDrag(dx: -3, dy: -40) == .ignore)
        #expect(terminalDrag(dx: 3, dy: -40) != nil)
    }

    @Test func tooShortADragSaysNothing() {
        #expect(terminalDrag(dx: 2, dy: 2) == nil)
        #expect(terminalDrag(dx: 7, dy: 7) == nil)
        // Once either axis reaches the threshold it is decided.
        #expect(terminalDrag(dx: 8, dy: 0) == .pan)
        #expect(terminalDrag(dx: 0, dy: 8) == .read)
        #expect(terminalDrag(dx: 0, dy: -8) == .ignore)
    }
}

@Suite struct TerminalStringFilterTests {
    private func run(_ frames: [[UInt8]]) -> [UInt8] {
        var f = TerminalStringFilter()
        return frames.flatMap { [UInt8](f.take(Data($0))) }
    }

    private func run(_ text: String) -> String {
        String(decoding: run([[UInt8](text.utf8)]), as: UTF8.self)
    }

    private let esc: UInt8 = 0x1B
    private let sub: UInt8 = 0x1A

    @Test func controlStringsAreLeftOutAndTheEmulatorIsTakenBackToText() {
        // Sixel is a DCS, Kitty graphics an APC; each ends with ST. In the string's place the emulator gets SUB, after the ESC it already had.
        #expect(run("a\u{1B}Pq#0~~\u{1B}\\b") == "a\u{1B}\u{1A}b")
        #expect(run("a\u{1B}_Gf=24;AAAA\u{1B}\\b") == "a\u{1B}\u{1A}b")
        // An APC also ends at BEL, as the emulator reads it; a DCS does not.
        #expect(run("a\u{1B}_G;AA\u{07}b") == "a\u{1B}\u{1A}b")
        #expect(run("a\u{1B}Pq\u{07}~\u{1B}\\b") == "a\u{1B}\u{1A}b")
        // SUB cancels either; CAN only an APC, as the emulator reads them.
        #expect(run("a\u{1B}Pq~\u{1A}b\u{1B}_G\u{18}c") == "a\u{1B}\u{1A}b\u{1B}\u{1A}c")
        #expect(run("a\u{1B}Pq~\u{18}~\u{1B}\\b") == "a\u{1B}\u{1A}b")
        // CAN does not take the emulator out of a sequence: what follows it can still begin a string.
        #expect(run("a\u{1B}\u{18}Pq~\u{1B}\\b") == "a\u{1B}\u{18}\u{1A}b")
        // Other sequences, and text of more than one byte, pass through.
        let plain = "\u{1B}[31m红吐\u{1B}]0;吐槽 title\u{07}\u{1B}(B é\u{1B}[0m\u{1B}]8;;x\u{1B}\\吐"
        #expect(run(plain) == plain)
    }

    @Test func aStringMayBeginInOneFrameAndEndInAnother() {
        #expect(run([[0x61, esc], [0x50, 0x71, 0x23], [0x7E, esc], [0x5C, 0x62]]) == [0x61, esc, sub, 0x62])
        #expect(run([[esc, 0x5B, 0x31], [0x6D, 0x61]]) == [esc, 0x5B, 0x31, 0x6D, 0x61])
    }

    @Test func anEscapeSequenceInsideAStringEndsItAndIsRead() {
        #expect(run("a\u{1B}Pq~~\u{1B}[1mb") == "a\u{1B}\u{1A}\u{1B}[1mb")
        // Also another string.
        #expect(run("a\u{1B}Pq~~\u{1B}_G;AA\u{1B}\\b") == "a\u{1B}\u{1A}\u{1B}\u{1A}b")
    }

    @Test func whatIsLeftOutCannotJoinWhatFollowsIntoAString() {
        // ESC ESC P…ST Pq…: the first ESC must not meet the P that follows the string.
        let out = run([[esc, esc, 0x50, 0x24, 0x71, 0x6D, esc, 0x5C, 0x50, 0x71, 0x7E, esc, 0x5C]])
        #expect(out == [esc, esc, sub, 0x50, 0x71, 0x7E, esc, 0x5C])
        // The same with a control the emulator runs without leaving its escape state.
        #expect(run([[esc, 0x0A, 0x50, 0x71, 0x7E, esc, 0x5C, 0x62]]) == [esc, 0x0A, sub, 0x62])
    }

    @Test func theRawControlsBeginAStringOnlyInsideASequence() {
        // In text 0x90 and 0x9F are part of a character, or printed as they are.
        #expect(run([[0xE5, 0x90, 0x90, 0x9F, 0x61]]) == [0xE5, 0x90, 0x90, 0x9F, 0x61])
        // Inside any sequence they begin a string.
        #expect(run([[esc, 0x90, 0x71, 0x7E, esc, 0x5C, 0x62]]) == [esc, sub, 0x62])
        #expect(run([[esc, 0x9F, 0x47, 0x3B, 0x41, 0x07, 0x62]]) == [esc, sub, 0x62])
        #expect(run([[esc, 0x5B, 0x31, 0x90, 0x71, 0x7E, 0x9C, 0x62]]) == [esc, 0x5B, 0x31, sub, 0x62])
        #expect(run([[esc, 0x58, 0x61, 0x9F, 0x47, 0x07, 0x62]]) == [esc, 0x58, 0x61, sub, 0x62])
        // An OSC takes them as its own once it has begun in this frame, and not as the first byte of one.
        #expect(run([[esc, 0x5D, 0x30, 0x3B, 0xE5, 0x90, 0x90, 0x07, 0x62]]) == [esc, 0x5D, 0x30, 0x3B, 0xE5, 0x90, 0x90, 0x07, 0x62])
        #expect(run([[esc, 0x5D, 0x90, 0x71, 0x7E, 0x9C, 0x62]]) == [esc, 0x5D, sub, 0x62])
        #expect(run([[esc, 0x5D, 0x30, 0x3B, 0xE5], [0x90, 0x71, 0x7E, 0x9C, 0x62]]) == [esc, 0x5D, 0x30, 0x3B, 0xE5, sub, 0x62])
    }

    @Test func aStringEndsWhereItWouldForTheEmulatorAndWhatFollowsIsShown() {
        // A raw control inside the string ends it; the ones that act do so in their two-byte form.
        #expect(run([[esc, 0x90, 0x85, 0x78]]) == [esc, sub, esc, 0x45, 0x78])
        #expect(run([[esc, 0x9F, 0x85, 0x78]]) == [esc, sub, esc, 0x45, 0x78])
        #expect(run([[esc, 0x50, 0x71, 0x7E, 0x9B, 0x31, 0x6D, 0x78]]) == [esc, sub, esc, 0x5B, 0x31, 0x6D, 0x78])
        #expect(run([[esc, 0x50, 0x71, 0x7E, 0x8D, 0x78]]) == [esc, sub, 0x78])
        // Before its final byte a DCS takes no character of more than one byte: it is over, and the text goes on.
        #expect(run([[esc, 0x50, 0x31, 0xE5, 0x78]]) == [esc, sub, 0x78])
        // After it, and in one that is ignored, such characters are the string's.
        #expect(run([[esc, 0x50, 0x71, 0xE5, 0x90, 0x78, esc, 0x5C, 0x79]]) == [esc, sub, 0x79])
        #expect(run([[esc, 0x50, 0x3A, 0xE5, 0x78, esc, 0x5C, 0x79]]) == [esc, sub, 0x79])
        // An APC that has begun takes the raw controls as its own within the frame, and not in the next.
        #expect(run([[esc, 0x5F, 0x47, 0x85, 0x78, 0x07, 0x79]]) == [esc, sub, 0x79])
        #expect(run([[esc, 0x5F, 0x47], [0x85, 0x78]]) == [esc, sub, esc, 0x45, 0x78])
    }
}
