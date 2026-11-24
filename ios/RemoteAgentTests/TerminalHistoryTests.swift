import RACore
import RAProto
import Testing
import UIKit
@testable import RemoteAgent

@MainActor @Suite struct TerminalHistoryTests {
    /// Drives a real clientcore instance for terminal history integration smoke tests.
    @MainActor
    private final class Harness {
        let core = try! CoreClient()
        let terminalID = "term-test"
        var state = TerminalHistoryState()
        var sent: [(id: String, cursor: String)] = []
        var scheduledTimers: [PBCoreScheduleTimerEffect] = []
        private var seq = 0

        deinit { core.close() }

        func dispatch(_ kind: PBCoreTerminalHistoryEventKind, page: PBTerminalHistoryPage? = nil) {
            seq += 1
            let batch = try! core.dispatch(
                .with {
                    $0.randomIds = ["req-\(seq)", "req-\(seq)-b"]
                    $0.terminalHistory = .with {
                        $0.terminalID = terminalID
                        $0.kind = kind
                        if let page { $0.page = page }
                    }
                })
            consume(batch)
        }

        func fireTimer(_ timer: PBCoreScheduleTimerEffect) {
            let batch = try! core.dispatch(
                .with {
                    $0.timerFired = .with {
                        $0.timerID = timer.timerID
                        $0.kind = timer.kind
                        $0.key = timer.key
                    }
                })
            consume(batch)
        }

        private func consume(_ batch: PBCoreEffectBatch) {
            for eff in batch.effects {
                switch eff.effect {
                case .terminalHistorySendRequest(let req)?:
                    sent.append((req.requestID, req.cursor))
                case .terminalHistoryUpdated(let upd)?:
                    state.apply(upd)
                case .scheduleTimer(let timer)?:
                    scheduledTimers.append(timer)
                default:
                    break
                }
            }
        }
    }

    private func page(_ request: String, lines: [String] = [], cursor: String = "", error: String = "") -> PBTerminalHistoryPage {
        .with {
            $0.requestID = request
            $0.lines = lines
            $0.nextCursor = cursor
            $0.error = error
        }
    }

    private func plainLines(_ parsed: PBCoreTerminalHistoryParsedPage) -> [String] {
        parsed.lines.map { $0.runs.map(\.text).joined() }
    }

    /// Parses raw lines through clientcore so UIKit rendering tests exercise the exact proto output.
    private func parsedPage(id: String, lines: [String]) -> PBCoreTerminalHistoryParsedPage {
        let h = Harness()
        h.dispatch(.open)
        h.dispatch(.pageReceived, page: page(h.sent[0].id, lines: lines))
        var parsed = h.state.pages[0]
        parsed.id = id
        return parsed
    }

    private func plainPage(id: String, lines: [String]) -> PBCoreTerminalHistoryParsedPage {
        .with {
            $0.id = id
            $0.lines = lines.map { text in
                .with {
                    $0.runs = [.with { $0.text = text }]
                }
            }
        }
    }

    /// Real Core smoke: open -> page prepend -> failure + manual retry -> expired -> disconnect -> timeout.
    @Test func coreDrivesPagingStatusAndTimeoutDeterministically() {
        let h = Harness()

        // Empty capture leaves idle.
        h.dispatch(.open)
        #expect(h.state.phase == .opening && h.state.opening && !h.state.open && h.state.reading)
        #expect(h.sent.count == 1 && h.sent[0].cursor.isEmpty)
        h.dispatch(.pageReceived, page: page(h.sent[0].id))
        #expect(h.state.phase == .idle && h.state.pages.isEmpty && !h.state.reading)

        // First page opens; stale reply is ignored; earlier page prepends.
        h.dispatch(.open)
        let firstID = h.sent.last!.id
        h.dispatch(.pageReceived, page: page(firstID, lines: ["\u{1b}[32mnew 中文\u{1b}[0m"], cursor: "capture:200"))
        #expect(h.state.phase == .open && h.state.open && h.state.canLoadEarlier && h.state.status == .none)
        #expect(h.state.pages.map(plainLines) == [["new 中文"]])
        #expect(h.state.pages[0].lines[0].runs.first?.fgKind == .palette)
        #expect(h.state.pages[0].lines[0].runs.first?.fgValue == 2)

        h.dispatch(.loadEarlier)
        let secondID = h.sent.last!.id
        #expect(h.sent.last!.cursor == "capture:200")
        h.dispatch(.pageReceived, page: page("stale", lines: ["late"]))
        #expect(h.state.pages.count == 1)

        // Failure keeps loaded content, exposes retry, and does not auto-retry on loadEarlier.
        h.dispatch(.pageReceived, page: page(secondID, error: "cannot read terminal history; retry"))
        #expect(h.state.status == .failed && h.state.canRetry && !h.state.canLoadEarlier)
        let beforeRetry = h.sent.count
        h.dispatch(.loadEarlier)
        #expect(h.sent.count == beforeRetry)
        h.dispatch(.retry)
        #expect(h.sent.count == beforeRetry + 1 && h.sent.last!.cursor == "capture:200")
        h.dispatch(.pageReceived, page: page(h.sent.last!.id, lines: ["old"], cursor: ""))
        #expect(h.state.pages.map(plainLines) == [["old"], ["new 中文"]])
        #expect(h.state.status == .start && !h.state.canLoadEarlier)

        // Disconnect while open keeps pages and shows disconnected status.
        h.dispatch(.disconnected)
        #expect(h.state.phase == .open && h.state.status == .disconnected && h.state.pages.count == 2)
        h.dispatch(.close)
        #expect(h.state.phase == .idle && h.state.pages.isEmpty)

        // Deterministic timeout via Core timer effect (no wall-clock sleep).
        h.scheduledTimers.removeAll()
        h.dispatch(.open)
        let timer = h.scheduledTimers.first { $0.kind == .terminalHistoryTimeout }
        #expect(timer != nil)
        h.fireTimer(timer!)
        #expect(h.state.phase == .openingFailed && h.state.canRetry && !h.state.pending)
        #expect(!renderCoreText(h.state.statusText).isEmpty)

        // Disconnected while openingFailed resets to idle.
        h.dispatch(.disconnected)
        #expect(h.state.phase == .idle && !h.state.reading)
    }

    /// Core-parsed SGR runs render into expected UIKit foreground/background/trait attributes.
    @Test func coreParsedSgrRunsRenderIntoAttributedText() {
        let parsed = parsedPage(id: "sgr", lines: ["\u{1b}[1;3;4;38;2;18;52;86mhi\u{1b}[0m"])
        let cell = TerminalFont.cell(points: 14)
        let attributed = TerminalHistoryText.line(parsed.lines[0], points: 14, cell: cell)
        #expect(attributed.string == "hi")
        let attrs = attributed.attributes(at: 0, effectiveRange: nil)
        let font = attrs[.font] as? UIFont
        #expect(font?.fontDescriptor.symbolicTraits.contains(.traitBold) == true)
        #expect(font?.fontDescriptor.symbolicTraits.contains(.traitItalic) == true)
        #expect((attrs[.underlineStyle] as? Int) == NSUnderlineStyle.single.rawValue)
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        (attrs[.foregroundColor] as? UIColor)?.getRed(&r, green: &g, blue: &b, alpha: &a)
        #expect(abs(r - 18.0 / 255.0) < 0.01 && abs(g - 52.0 / 255.0) < 0.01 && abs(b - 86.0 / 255.0) < 0.01)
    }

    /// A wide character takes two cells, so a line of Chinese lines up under a ruler
    /// line of ASCII, as the emulator has it (docs/design/client-v40.html · 终端).
    @Test func wideCharactersTakeTwoCellsUnderARulerLine() {
        let points: CGFloat = 14
        let cell = TerminalFont.cell(points: points)
        let samples = ["|12345678|", "|中文中文|", "中文", "\u{1b}[32m中文\u{1b}[0m", "|✅❌⭐⚡|", "|☀\u{fe0f}👨\u{200d}👩\u{200d}👧e\u{0301}abc|"]
        let parsed = parsedPage(id: "wide-ruler", lines: samples)
        func width(_ index: Int) -> CGFloat {
            TerminalHistoryText.line(parsed.lines[index], points: points, cell: cell).size().width
        }
        // Both are ten cells wide: eight in the middle and two ends.
        #expect(abs(width(0) - 10 * cell.width) < 1)
        #expect(abs(width(1) - width(0)) < 0.5)
        // A fullwidth letter is two cells too, and the colours do not change the widths.
        #expect(abs(width(2) - 4 * cell.width) < 0.5)
        #expect(abs(width(3) - width(2)) < 0.5)
        // Wide emojis, VS16/ZWJ clusters, and combining marks all follow Core's unified widths.
        #expect(abs(width(4) - width(0)) < 1)
        #expect(abs(width(5) - width(0)) < 1)
    }

    /// Prepending a page keeps the line in sight where it was.
    @Test func prependingHistoryKeepsTheVisibleTextInPlace() {
        let view = TerminalHistoryTextView()
        view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        let cell = CGSize(width: 8.4, height: 17)
        let recent = plainPage(id: "recent", lines: (201...400).map { "history-\($0) 中文" })
        show(view, [recent], status: .none, cell: cell, entry: 0)
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
        let older = plainPage(id: "older", lines: (1...200).map { "history-\($0) 中文" })
        show(view, [older, recent], status: .loadingEarlier, cell: cell, entry: 0)
        #expect(visibleText() == before)
        #expect(view.contentOffset.y > 70)
    }

    /// A line wider than the area is panned to, not wrapped or clipped, and the
    /// reader opens at the live screen's own sideways offset (architecture §11 「尺寸」).
    @Test func wideLinesArePannedToAndTheReaderOpensAtTheLiveOffset() {
        let view = TerminalHistoryTextView()
        view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        let cell = CGSize(width: 8.4, height: 17)
        let wide = plainPage(id: "wide", lines: (1...40).map { _ in String(repeating: "x", count: 200) })
        view.liveOffsetX = 120
        show(view, [wide], status: .none, cell: cell, entry: 0)
        // 200 columns are wider than the area: the reader scrolls sideways to them.
        #expect(view.contentSize.width > view.bounds.width)
        #expect(abs(view.contentOffset.x - 120) < 1)
        // A line that fits is not scrolled to, and the offset starts at the left.
        let narrow = TerminalHistoryTextView()
        narrow.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        narrow.liveOffsetX = 120
        show(narrow, [plainPage(id: "narrow", lines: ["short", "中文"])], status: .none, cell: cell, entry: 0)
        #expect(narrow.contentSize.width <= narrow.bounds.width)
        #expect(narrow.contentOffset.x == 0)
    }

    /// The reader pans over at least the live grid's width, so opening at the live
    /// screen's offset is not clamped away when the captured lines are shorter
    /// (architecture §11 「尺寸」).
    @Test func theReaderPansOverAtLeastTheLiveGridWidth() {
        let cell = CGSize(width: 8.4, height: 17)
        let short = plainPage(id: "p", lines: ["short", "lines"])
        // The live grid is 600 pt wide and panned to 120; the captured lines are short.
        let view = TerminalHistoryTextView()
        view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        view.liveOffsetX = 120
        show(view, [short], status: .none, cell: cell, entry: 0, gridWidth: 600)
        #expect(view.contentSize.width >= 600)
        #expect(abs(view.contentOffset.x - 120) < 1)
        // Without the live grid, the same view opens no further than the text needs.
        let narrow = TerminalHistoryTextView()
        narrow.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        narrow.liveOffsetX = 120
        show(narrow, [short], status: .none, cell: cell, entry: 0, gridWidth: 0)
        #expect(narrow.contentSize.width <= narrow.bounds.width)
        #expect(narrow.contentOffset.x == 0)
    }

    /// Every loaded line is laid out, however many there are: history can hold
    /// 100,000 tmux rows, well past a million points, and a measurement capped
    /// there would leave the newest rows unreachable (architecture §11 「回看历史」).
    @Test func everyLoadedLineIsLaidOutPastAMillionPoints() {
        let view = TerminalHistoryTextView()
        view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        // 100 pt lines: 12,000 of them is 1,200,000 pt, past the old 1,000,000 pt cap.
        let cell = CGSize(width: 8.4, height: 100)
        let lines = (1...12_000).map { "history-\($0)" }
        show(view, [plainPage(id: "p", lines: lines)], status: .none, cell: cell, entry: 0)
        #expect(view.contentSize.height >= CGFloat(lines.count) * cell.height)
    }

    /// The reader is shown before it has a size: it places nothing then, and once it
    /// is laid out places the entry pull and the live screen's sideways offset.
    @Test func theReaderWaitsForUsableBoundsBeforePlacing() {
        let view = TerminalHistoryTextView()
        view.liveOffsetX = 100
        let cell = CGSize(width: 8.4, height: 17)
        let wide = plainPage(id: "p", lines: (1...400).map { _ in String(repeating: "x", count: 200) })
        view.content = TerminalHistoryTextView.Content(
            pages: [wide], status: .none, statusText: nil, canRetry: false, canLoadEarlier: false, online: true, entry: 60)
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
        let page = plainPage(id: "p", lines: lines)
        func offset(_ entry: CGFloat) -> CGFloat {
            let view = TerminalHistoryTextView()
            view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
            show(view, [page], status: .none, cell: cell, entry: entry)
            return max(0, view.contentSize.height - view.bounds.height) - view.contentOffset.y
        }
        // A pull smaller than three lines still opens three lines up.
        #expect(abs(offset(0) - 51) < 1)
        #expect(abs(offset(200) - 200) < 1)
    }

    /// What the pane's representable does, without SwiftUI.
    private func show(
        _ view: TerminalHistoryTextView,
        _ pages: [PBCoreTerminalHistoryParsedPage],
        status: PBCoreTerminalHistoryStatus,
        statusText: PBCoreText? = nil,
        canRetry: Bool = false,
        canLoadEarlier: Bool = false,
        online: Bool = true,
        cell: CGSize,
        entry: CGFloat,
        gridWidth: CGFloat = 0
    ) {
        view.content = TerminalHistoryTextView.Content(
            pages: pages,
            status: status,
            statusText: statusText,
            canRetry: canRetry,
            canLoadEarlier: canLoadEarlier,
            online: online,
            entry: entry)
        view.metrics = { (14, cell) }
        view.liveGridWidth = gridWidth
        view.show()
    }
}
