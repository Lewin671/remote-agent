import RAProto
import UIKit
import Testing
@testable import RemoteAgent

private typealias Status = TerminalHistory.Status
private typealias Trouble = TerminalHistory.Trouble
private typealias Phase = TerminalHistory.Phase

@MainActor @Suite struct TerminalHistoryTests {
    private func page(_ request: String, lines: [String] = [], cursor: String = "", error: String = "") -> PBTerminalHistoryPage {
        .with {
            $0.requestID = request
            $0.lines = lines
            $0.nextCursor = cursor
            $0.error = error
        }
    }

    /// A capture with nothing kept: no reader, and no line left behind.
    @Test func anEmptyCaptureDoesNotEnterReading() {
        let h = TerminalHistory()
        h.begin(pull: 20) { _, _ in }
        // The live screen, under a line saying the record is being read; not the reader.
        #expect(h.phase == Phase.opening && h.opening == .reading && !h.open && h.reading)
        h.receive(page(h.pending))
        #expect(h.phase == Phase.idle && h.pages.isEmpty && !h.reading && h.pending.isEmpty)
    }

    @Test func theFirstPageOpensTheReaderAtTheCursorItWasSent() {
        let h = TerminalHistory()
        var sent: [(String, String)] = []
        h.begin(pull: 30) { sent.append(($0, $1)) }
        #expect(sent.count == 1 && sent[0].1.isEmpty && sent[0].0 == h.pending)
        h.receive(page(h.pending, lines: ["new 中文"], cursor: "capture:200"))
        #expect(h.phase == Phase.open && h.open && h.opening == nil)
        #expect(h.pages.map(\.lines) == [["new 中文"]] && h.cursor == "capture:200" && h.status == Status.none)
        h.end()
        #expect(h.phase == Phase.idle && h.pages.isEmpty && h.cursor.isEmpty)
    }

    /// The gesture that opened the reader carries on while the first page is on its way.
    @Test func theEntryDistanceGrowsOnlyWhileTheFirstPageIsAway() {
        let h = TerminalHistory()
        h.begin(pull: 20) { _, _ in }
        h.pulled(50)
        #expect(h.entryPull == 50)
        h.receive(page(h.pending, lines: ["a"], cursor: "c"))
        h.pulled(90)
        #expect(h.entryPull == 50)
        h.end()
    }

    @Test func pagingKeepsOneRequestInFlightAndTakesOnlyItsReply() {
        let h = TerminalHistory()
        var sent: [(String, String)] = []
        func send(_ id: String, _ cursor: String) { sent.append((id, cursor)) }
        h.begin(pull: 0, send: send)
        h.earlier(send: send)
        #expect(sent.count == 1)
        h.receive(page(h.pending, lines: ["new"], cursor: "capture:200"))
        h.earlier(send: send)
        #expect(sent.last?.1 == "capture:200")
        let waiting = h.pending
        // A reply to a request nobody is waiting for changes nothing.
        h.receive(page("stale", lines: ["late"]))
        #expect(h.pages.map(\.lines) == [["new"]])
        h.receive(page(waiting, lines: ["old"], cursor: ""))
        #expect(h.pages.map(\.lines) == [["old"], ["new"]])
        #expect(h.status == Status.start)
        h.end()
    }

    /// A failure keeps what was read, says so, and asks for nothing on its own.
    @Test func failureKeepsTheContentAndDoesNotRetryUntilAsked() {
        let h = TerminalHistory()
        var sent: [(String, String)] = []
        func send(_ id: String, _ cursor: String) { sent.append((id, cursor)) }
        h.begin(pull: 0, send: send)
        h.receive(page(h.pending, lines: ["new"], cursor: "capture:200"))
        h.earlier(send: send)
        h.receive(page(h.pending, error: "cannot read terminal history; retry"))
        #expect(h.trouble == Trouble.failed && h.status == Status.failed && h.cursor == "capture:200")
        #expect(h.pages.map(\.lines) == [["new"]])
        let count = sent.count
        h.earlier(send: send)
        #expect(sent.count == count)
        h.retry(send: send)
        #expect(sent.count == count + 1 && sent.last?.1 == "capture:200" && h.trouble == Trouble.none)
        h.receive(page(h.pending, lines: ["old"], cursor: ""))
        #expect(h.pages.map(\.lines) == [["old"], ["new"]])
        h.end()
    }

    /// An expired capture has its own line, and no 重试: the way back is to the live screen.
    @Test func anExpiredCaptureSaysWhereToGo() {
        let h = TerminalHistory()
        var sent: [(String, String)] = []
        func send(_ id: String, _ cursor: String) { sent.append((id, cursor)) }
        h.begin(pull: 0, send: send)
        h.receive(page(h.pending, lines: ["new"], cursor: "capture:200"))
        h.earlier(send: send)
        h.receive(page(h.pending, error: "history expired; reopen history"))
        #expect(h.trouble == Trouble.expired && h.status == Status.expired && h.pages.count == 1)
        let count = sent.count
        h.retry(send: send)
        #expect(sent.count == count)
        h.end()
    }

    @Test func aClosedStreamKeepsWhatWasReadAndSaysSo() {
        let h = TerminalHistory()
        h.begin(pull: 0) { _, _ in }
        h.receive(page(h.pending, lines: ["new"], cursor: "c"))
        h.disconnected()
        #expect(h.phase == Phase.open && h.trouble == Trouble.disconnected && h.status == Status.disconnected)
        #expect(h.pages.map(\.lines) == [["new"]] && h.pending.isEmpty)
        h.end()
    }

    /// Closed before the first page was here, or after one failed: there is nothing to
    /// read, so the live screen is left plain.
    @Test func aClosedStreamWhileOpeningLeavesNoReader() {
        let h = TerminalHistory()
        h.begin(pull: 0) { _, _ in }
        h.disconnected()
        #expect(h.phase == Phase.idle && h.pages.isEmpty && h.opening == nil)
        h.begin(pull: 0) { _, _ in }
        h.receive(page(h.pending, error: "cannot capture terminal history; retry"))
        h.disconnected()
        #expect(h.phase == Phase.idle && h.opening == nil)
    }

    /// A first page that fails does not open the reader: the live screen stays, under
    /// 读取失败, and the 重试 asks for the capture again.
    @Test func aFailedFirstPageKeepsTheLiveScreenAndCanBeRetried() {
        let h = TerminalHistory()
        var sent: [(String, String)] = []
        func send(_ id: String, _ cursor: String) { sent.append((id, cursor)) }
        h.begin(pull: 0, send: send)
        h.receive(page(h.pending, error: "cannot capture terminal history; retry or return to the live terminal"))
        #expect(h.phase == Phase.openingFailed && h.opening == .failed && !h.open)
        #expect(h.pages.isEmpty && h.status == Status.none)
        h.retry(send: send)
        #expect(h.phase == Phase.opening && h.opening == .reading && sent.count == 2 && sent.last?.1 == "")
        h.receive(page(h.pending, lines: ["a"], cursor: "c"))
        #expect(h.open && h.opening == nil && h.pages.map(\.lines) == [["a"]] && h.trouble == Trouble.none)
        h.end()
    }

    /// A first page that never comes leaves the same live screen, with the same 重试.
    @Test func aFirstPageThatTimesOutKeepsTheLiveScreen() async {
        let h = TerminalHistory(wait: .milliseconds(1))
        var sent = 0
        h.begin(pull: 0) { _, _ in sent += 1 }
        // A machine busy with other tests may take its time: give it a while to give up.
        for _ in 0..<300 where h.phase != Phase.openingFailed { try? await Task.sleep(for: .milliseconds(10)) }
        #expect(h.phase == Phase.openingFailed && h.opening == .failed && !h.open)
        #expect(sent == 1 && h.pending.isEmpty)
        h.end()
        #expect(h.phase == Phase.idle)
    }

    @Test func outputIsNoticedOnlyWhileReading() {
        let h = TerminalHistory()
        h.markOutput()
        #expect(!h.hasNewOutput)
        h.begin(pull: 0) { _, _ in }
        h.markOutput()
        #expect(h.hasNewOutput)
        h.end()
        #expect(!h.hasNewOutput)
    }

    /// A wide character takes two cells, so a line of Chinese lines up under a ruler
    /// line of ASCII, as the emulator has it (docs/design/client-v40.html · 终端).
    @Test func wideCharactersTakeTwoCellsUnderARulerLine() {
        let points: CGFloat = 14
        let cell = TerminalFont.cell(points: points)
        func width(_ line: String) -> CGFloat { TerminalHistoryText.line(line, points: points, cell: cell).size().width }
        // Both are ten cells wide: eight in the middle and two ends.
        #expect(abs(width("|12345678|") - 10 * cell.width) < 1)
        #expect(abs(width("|中文中文|") - width("|12345678|")) < 0.5)
        // A fullwidth letter is two cells too, and the colours do not change the widths.
        #expect(abs(width("中文") - 4 * cell.width) < 0.5)
        #expect(abs(width("\u{1b}[32m中文\u{1b}[0m") - width("中文")) < 0.5)
    }

    /// Prepending a page keeps the line in sight where it was.
    @Test func prependingHistoryKeepsTheVisibleTextInPlace() {
        let view = TerminalHistoryTextView()
        view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        let cell = CGSize(width: 8.4, height: 17)
        let recent = TerminalHistory.Page(id: "recent", lines: (201...400).map { "history-\($0) 中文" })
        show(view, [recent], status: Status.none, cell: cell, entry: 0)
        #expect(view.contentOffset.y > 0 && !view.isEditable)
        view.contentOffset = CGPoint(x: 0, y: 70)
        func visibleText() -> String {
            let point = CGPoint(x: 5, y: view.contentOffset.y + 12)
            let glyph = view.layoutManager.glyphIndex(for: point, in: view.textContainer)
            let character = view.layoutManager.characterIndexForGlyph(at: glyph)
            let text = view.text as NSString
            return text.substring(with: NSRange(location: character, length: min(25, text.length - character)))
        }
        let before = visibleText()
        let older = TerminalHistory.Page(id: "older", lines: (1...200).map { "history-\($0) 中文" })
        show(view, [older, recent], status: Status.earlier, cell: cell, entry: 0)
        #expect(visibleText() == before)
        #expect(view.contentOffset.y > 70)
    }

    /// A line wider than the area is panned to, not wrapped or clipped, and the
    /// reader opens at the live screen's own sideways offset (architecture §11 「尺寸」).
    @Test func wideLinesArePannedToAndTheReaderOpensAtTheLiveOffset() {
        let view = TerminalHistoryTextView()
        view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        let cell = CGSize(width: 8.4, height: 17)
        let page = TerminalHistory.Page(id: "wide", lines: (1...40).map { _ in String(repeating: "x", count: 200) })
        view.liveOffsetX = 120
        show(view, [page], status: Status.none, cell: cell, entry: 0)
        // 200 columns are wider than the area: the reader scrolls sideways to them.
        #expect(view.contentSize.width > view.bounds.width)
        #expect(abs(view.contentOffset.x - 120) < 1)
        // A line that fits is not scrolled to, and the offset starts at the left.
        let narrow = TerminalHistoryTextView()
        narrow.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        narrow.liveOffsetX = 120
        show(narrow, [TerminalHistory.Page(id: "narrow", lines: ["short", "中文"] )], status: Status.none, cell: cell, entry: 0)
        #expect(narrow.contentSize.width <= narrow.bounds.width)
        #expect(narrow.contentOffset.x == 0)
    }

    /// The reader pans over at least the live grid's width, so opening at the live
    /// screen's offset is not clamped away when the captured lines are shorter
    /// (architecture §11 「尺寸」).
    @Test func theReaderPansOverAtLeastTheLiveGridWidth() {
        let cell = CGSize(width: 8.4, height: 17)
        let page = TerminalHistory.Page(id: "p", lines: ["short", "lines"])
        // The live grid is 600 pt wide and panned to 120; the captured lines are short.
        let view = TerminalHistoryTextView()
        view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        view.liveOffsetX = 120
        show(view, [page], status: Status.none, cell: cell, entry: 0, gridWidth: 600)
        #expect(view.contentSize.width >= 600)
        #expect(abs(view.contentOffset.x - 120) < 1)
        // Without the live grid, the same view opens no further than the text needs.
        let narrow = TerminalHistoryTextView()
        narrow.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        narrow.liveOffsetX = 120
        show(narrow, [page], status: Status.none, cell: cell, entry: 0, gridWidth: 0)
        #expect(narrow.contentSize.width <= narrow.bounds.width)
        #expect(narrow.contentOffset.x == 0)
    }

    /// The reader is shown before it has a size: it places nothing then, and once it
    /// is laid out places the entry pull and the live screen's sideways offset.
    @Test func theReaderWaitsForUsableBoundsBeforePlacing() {
        let view = TerminalHistoryTextView()
        view.liveOffsetX = 100
        let cell = CGSize(width: 8.4, height: 17)
        let page = TerminalHistory.Page(id: "p", lines: (1...400).map { _ in String(repeating: "x", count: 200) })
        view.content = TerminalHistoryTextView.Content(pages: [page], status: Status.none, online: true, entry: 60)
        view.metrics = { (14, cell) }
        view.show()
        // No area yet: nothing placed, so the offset is not clamped against nothing.
        #expect(view.contentOffset == .zero)
        view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        view.layoutIfNeeded()
        view.show()
        // Laid out now: the entry pull is applied and the live offset is kept.
        #expect(view.contentOffset.y > 0)
        #expect(view.contentSize.width > view.bounds.width)
        #expect(abs(view.contentOffset.x - 100) < 1)
    }

    /// Only a vertical gesture leaves past the bottom (architecture §11 「尺寸」):
    /// a sideways pan only looks along a wide line.
    @Test func onlyAVerticalGestureLeavesPastTheBottom() {
        #expect(TerminalHistoryTextView.leavesReading(offsetY: 700, bottom: 640, lineHeight: 17, sideways: false))
        // The same overscroll from a sideways pan does not leave.
        #expect(!TerminalHistoryTextView.leavesReading(offsetY: 700, bottom: 640, lineHeight: 17, sideways: true))
        // At the bottom, or only a little past it, does not leave either.
        #expect(!TerminalHistoryTextView.leavesReading(offsetY: 640, bottom: 640, lineHeight: 17, sideways: false))
        #expect(!TerminalHistoryTextView.leavesReading(offsetY: 660, bottom: 640, lineHeight: 17, sideways: false))
        #expect(TerminalHistoryTextView.leavesReading(offsetY: 665, bottom: 640, lineHeight: 17, sideways: false))
    }

    /// The reader opens above the newest line, by the entry pull at least three lines,
    /// so the gesture that opened it does not leave again at once.
    @Test func theReaderOpensTheEntryPullAboveTheNewestLine() {
        let cell = CGSize(width: 8.4, height: 17)
        let lines = (1...400).map { "history-\($0)" }
        let page = TerminalHistory.Page(id: "p", lines: lines)
        func offset(_ entry: CGFloat) -> CGFloat {
            let view = TerminalHistoryTextView()
            view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
            show(view, [page], status: Status.none, cell: cell, entry: entry)
            return max(0, view.contentSize.height - view.bounds.height) - view.contentOffset.y
        }
        // A pull smaller than three lines still opens three lines up.
        #expect(abs(offset(0) - 51) < 1)
        #expect(abs(offset(200) - 200) < 1)
    }

    /// What the pane's representable does, without SwiftUI.
    private func show(
        _ view: TerminalHistoryTextView, _ pages: [TerminalHistory.Page], status: Status, online: Bool = true, cell: CGSize,
        entry: CGFloat, gridWidth: CGFloat = 0
    ) {
        view.content = TerminalHistoryTextView.Content(pages: pages, status: status, online: online, entry: entry)
        view.metrics = { (14, cell) }
        view.liveGridWidth = gridWidth
        view.show()
    }
}
