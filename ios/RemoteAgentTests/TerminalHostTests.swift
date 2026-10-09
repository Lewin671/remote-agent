import Foundation
import RACore
import SwiftTerm
import Testing
import UIKit

@testable import RemoteAgent

/// The terminal's screen: what it draws from the stream, and what the stream may not do to the phone.
@MainActor
@Suite struct TerminalHostTests {
    private func host(width: CGFloat = 400, height: CGFloat = 500) -> TerminalHostView {
        let host = TerminalHostView(preferred: 14)
        host.frame = CGRect(x: 0, y: 0, width: width, height: height)
        host.layoutIfNeeded()
        return host
    }

    private func text(_ host: TerminalHostView) -> String { host.screenText }

    /// Android's term/TerminalBatchParsingTest: bytes fed one at a time and all at once draw the same screen.
    @Test func batchingPreservesSplitUtf8AndControlSequences() {
        let bytes = Data("hello 中😀\r\nworld\u{1b}[1;1HOK\u{1b}[2;5Hdone".utf8)
        let split = host()
        let merged = host()
        split.snapshot(cols: 80, rows: 24, data: Data())
        merged.snapshot(cols: 80, rows: 24, data: Data())
        for byte in bytes { split.append(Data([byte])) }
        merged.append(bytes)
        let (a, b) = (split.terminal.getTerminal(), merged.terminal.getTerminal())
        #expect(a.getCursorLocation().x == b.getCursorLocation().x)
        #expect(a.getCursorLocation().y == b.getCursorLocation().y)
        #expect(text(split) == text(merged))
        #expect(text(merged).contains("中😀"))
    }

    /// android-client 「终端」: OSC 52 from the output does not write the clipboard.
    @Test func outputCannotWriteTheClipboard() {
        let host = host()
        host.snapshot(cols: 80, rows: 24, data: Data())
        let before = UIPasteboard.general.changeCount
        let payload = Data("stolen".utf8).base64EncodedString()
        host.append(Data("\u{1b}]52;c;\(payload)\u{07}after".utf8))
        host.append(Data("\u{1b}]52;c;\(payload)\u{1b}\\".utf8))
        #expect(UIPasteboard.general.changeCount == before)
        #expect(text(host).contains("after"))
    }

    /// Queries, focus and mouse modes, a title, a bell, a notification: nothing comes back as input.
    @Test func theEmulatorAnswersNothing() {
        let host = host()
        var sent: [Data] = []
        host.onInput = { sent.append($0) }
        host.snapshot(cols: 80, rows: 24, data: Data())
        let asks = [
            "\u{1b}[6n", "\u{1b}[5n", "\u{1b}[c", "\u{1b}[>c", "\u{1b}[?1004h", "\u{1b}[?1000h\u{1b}[?1006h", "\u{1b}[18t", "\u{1b}[21t",
            "\u{1b}]0;title\u{07}", "\u{1b}]10;?\u{07}", "\u{1b}]11;?\u{07}", "\u{07}", "\u{1b}]777;notify;t;b\u{07}",
            "\u{1b}]8;;https://example.com\u{07}link\u{1b}]8;;\u{07}", "\u{1b}P$qm\u{1b}\\", "\u{1b}[?u", "\u{05}",
        ]
        for ask in asks { host.append(Data(ask.utf8)) }
        _ = host.terminal.becomeFirstResponder()
        _ = host.terminal.resignFirstResponder()
        #expect(sent.isEmpty)
        // What the user types is still sent.
        host.terminal.insertText("ls")
        #expect(sent == [Data("ls".utf8)])
    }

    @Test func theGridIsTheRemoteWindowsWhateverTheArea() {
        let host = host(width: 400, height: 500)
        host.snapshot(cols: 120, rows: 40, data: Data("wide".utf8))
        let emulator = host.terminal.getTerminal()
        #expect((emulator.cols, emulator.rows) == (120, 40))
        // The whole window fits the area, at a smaller font than the one chosen.
        #expect(host.terminal.frame.maxX <= 400 && host.terminal.frame.maxY <= 500)
        #expect(host.terminal.font.pointSize < 14)
        // A shorter area, as under the keyboard, and a turned one: the grid stays.
        for size in [CGSize(width: 400, height: 200), CGSize(width: 800, height: 300), CGSize(width: 400, height: 500)] {
            host.frame.size = size
            host.layoutIfNeeded()
            #expect((emulator.cols, emulator.rows) == (120, 40))
            #expect(text(host) == "wide")
        }
        // A window that fits keeps the size chosen, and a new snapshot takes the new size.
        host.snapshot(cols: 40, rows: 20, data: Data("small".utf8))
        #expect((emulator.cols, emulator.rows) == (40, 20))
        #expect(host.terminal.font.pointSize == 14)
        #expect(text(host) == "small")
    }

    @Test func theRoomIsCountedAtTheSizeChosenAndFollowsIt() {
        let host = host(width: 400, height: 500)
        var rooms: [TerminalSize] = []
        host.onCapacity = { rooms.append($0) }
        let at14 = host.capacity
        #expect(at14.cols >= 20 && at14.rows >= 5)
        host.preferred = 20
        #expect(rooms.count == 1)
        #expect(host.capacity.cols < at14.cols && host.capacity.rows < at14.rows)
        // A remote window does not change the room there is.
        host.snapshot(cols: 200, rows: 60, data: Data())
        #expect(rooms.count == 1)
    }

    @Test func theKeyBarHasAnIconForEveryKeyThatNamesOne() {
        for key in terminalKeyRows.joined() {
            guard let name = key.icon else { continue }
            #expect(Lucide.all.contains { $0.name == name }, "\(key.label)")
        }
    }
}
