import RAProto
import UIKit
import Testing
@testable import RemoteAgent

@MainActor @Suite struct TerminalHistoryTests {
    @Test func emptyCaptureHasNoBlankPage() {
        let h = TerminalHistory()
        h.request(fresh: true) { _, _ in }
        var page = PBTerminalHistoryPage(); page.requestID = h.pending
        h.receive(page)
        #expect(h.pages.isEmpty && h.cursor.isEmpty && h.error.isEmpty)
        h.close()
    }

    @Test func prependingHistoryKeepsTheVisibleTextInPlace() {
        let view = TerminalHistoryTextView()
        view.frame = CGRect(x: 0, y: 0, width: 400, height: 500)
        let recent = TerminalHistory.Page(id: "recent", text: (201...400).map { "history-\($0) 中文" }.joined(separator: "\n"))
        view.show(pages: [recent], font: 14)
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
        let older = TerminalHistory.Page(id: "older", text: (1...200).map { "history-\($0) 中文" }.joined(separator: "\n"))
        view.show(pages: [older, recent], font: 14)
        #expect(visibleText() == before)
        #expect(view.contentOffset.y > 70)
    }

    @Test func paginationRejectsLateRepliesAndRetainsCursorOnFailure() {
        let history = TerminalHistory()
        var sent: [(String, String)] = []
        func send(_ id: String, _ cursor: String) { sent.append((id, cursor)) }
        history.request(fresh: true, send: send)
        let first = history.pending
        history.request(send: send)
        #expect(sent.count == 1)
        var page = PBTerminalHistoryPage()
        page.requestID = first; page.lines = ["new 中文"]; page.nextCursor = "capture:200"
        history.receive(page)
        history.request(send: send)
        #expect(sent.last?.1 == "capture:200")
        page.requestID = history.pending; page.error = "expired"; history.receive(page)
        #expect(!history.error.isEmpty && history.cursor == "capture:200")
        history.request(send: send)
        page.requestID = history.pending; page.error = ""; page.lines = ["old"]; page.nextCursor = ""
        history.receive(page)
        #expect(history.pages.map(\.text) == ["old", "new 中文"])
        history.request(send: send); #expect(sent.count == 3)
        history.close(); history.request(fresh: true, send: send)
        history.receive(page)
        #expect(history.pages.isEmpty && !history.pending.isEmpty)
        history.disconnected(); #expect(history.pending.isEmpty)
        history.close()
    }
}
