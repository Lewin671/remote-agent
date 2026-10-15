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

    /// The live screen keeps no scrollback of its own, so there is nothing to
    /// scroll and no second scroll system beside the reader
    /// (architecture §11 「回看历史」).
    @Test func theLiveScreenHasNoScrollbackOfItsOwn() {
        let view = host()
        let history = (1...100).map { "history-\($0)" }.joined(separator: "\r\n")
        view.snapshot(cols: 80, rows: 10, data: Data(history.utf8))
        let terminal = view.terminal
        #expect(!terminal.isScrollEnabled)
        // Only the window's lines are held, so the view cannot move on its own.
        #expect(terminal.contentSize.height <= terminal.bounds.height + 1)
        // What is above the window went nowhere: only the live rows are on the screen.
        #expect(text(view) == (91...100).map { "history-\($0)" }.joined(separator: "\n"))
        // Output carries on at the bottom of the screen the remote sized.
        view.append(Data("\r\nnew output".utf8))
        #expect(terminal.contentOffset.y <= 1)
        #expect(text(view).hasSuffix("new output"))
    }

    /// A pull down is a wish to read: it is reported, and nothing goes to the remote.
    @Test func aPullDownIsReportedAndSendsNothing() {
        let view = host()
        view.snapshot(cols: 80, rows: 24, data: Data("live".utf8))
        var pulls: [CGFloat] = []
        var sent: [Data] = []
        view.onReadUp = { pulls.append($0) }
        view.onInput = { sent.append($0) }
        // The gesture that would enter reading, as the host hands it on.
        view.reportPull(30)
        view.reportPull(48)
        #expect(pulls == [30, 48])
        #expect(sent.isEmpty)
        // It is the pan recognizer that carries a wheel or a trackpad, and not the scroll view.
        let pans = (view.terminal.gestureRecognizers ?? []).compactMap { $0 as? UIPanGestureRecognizer }
        #expect(pans.contains { $0.allowedScrollTypesMask == .all })
    }

    /// 字号 offers 6 to 22 (architecture §11 「尺寸」), and opens at 14.
    @Test func theFontSizesGoFromSixToTwentyTwo() {
        #expect(TerminalFont.sizes == 6...22)
        #expect(TerminalFont.standard == 14)
        #expect(TerminalFont.sizes.contains(TerminalFont.standard))
        #expect(TerminalFont.sizes.lowerBound == terminalFontFloor)
        #expect(TerminalFont.cell(points: CGFloat(TerminalFont.sizes.lowerBound)).width > 0)
    }

    /// A window wider than the area at the floor is kept at 6, not shrunk below,
    /// and the rest of each line is reached by panning sideways (architecture §11 「尺寸」).
    @Test func aWindowTooWideForTheAreaIsKeptAtTheFloorAndPanned() {
        let view = host(width: 400, height: 500)
        view.snapshot(cols: 165, rows: 48, data: Data("wide sample".utf8))
        // 165 columns cannot fit 400 pt at the floor, so the floor is what is shown.
        #expect(view.shownPoints == CGFloat(terminalFontFloor))
        #expect(view.terminal.font.pointSize == 14)
        // The picture is wider than the area, and there it can be panned to the right edge.
        #expect(view.maxHorizontalOffset > 0)
        #expect(view.horizontalOffset == 0)
        var sent: [Data] = []
        view.onInput = { sent.append($0) }
        view.pan(by: 10_000)
        // The scroll view lands on the pixel grid, so within a point of the edge.
        #expect(abs(view.horizontalOffset - view.maxHorizontalOffset) < 1)
        view.pan(by: -10_000)
        #expect(view.horizontalOffset == 0)
        // Panning is the phone looking around; nothing goes to the remote.
        #expect(sent.isEmpty)
    }

    /// A window that fits has nothing to pan, at any size.
    @Test func aWindowThatFitsHasNothingToPan() {
        let view = host(width: 400, height: 500)
        view.snapshot(cols: 40, rows: 20, data: Data("fits".utf8))
        #expect(view.shownPoints == 14)
        #expect(view.maxHorizontalOffset == 0)
        view.pan(by: 500)
        #expect(view.horizontalOffset == 0)
        // A smaller 字号 gives more columns, and still nothing to pan.
        view.preferred = TerminalFont.sizes.lowerBound
        #expect(view.shownPoints == CGFloat(terminalFontFloor))
        #expect(view.maxHorizontalOffset == 0)
    }

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
    /// iTerm2's inline images: SwiftTerm would decode the file and put the picture in the buffer, moving the cursor past it.
    @Test func outputCannotPutAnImageOnTheScreen() {
        let host = host()
        host.snapshot(cols: 80, rows: 24, data: Data("before".utf8))
        let png = UIGraphicsImageRenderer(size: CGSize(width: 64, height: 64)).image { ctx in
            UIColor.red.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 64, height: 64))
        }.pngData()!
        host.append(Data("\u{1B}]1337;File=inline=1;width=20;height=10:\(png.base64EncodedString())\u{07}after".utf8))
        let emulator = host.terminal.getTerminal()
        #expect(text(host) == "beforeafter")
        #expect(emulator.getCursorLocation().y == 0)
        // The same with the other terminator, and split over two frames.
        let again = Data("\u{1B}]1337;File=inline=1:\(png.base64EncodedString())\u{1B}\\!".utf8)
        host.append(again.prefix(40))
        host.append(again.dropFirst(40))
        #expect(text(host) == "beforeafter!")
        #expect(emulator.getCursorLocation().y == 0)
    }

    @Test func outputCannotDrawSixelOrKittyGraphics() {
        let host = host()
        host.snapshot(cols: 80, rows: 24, data: Data("before".utf8))
        // A red Sixel square, and a Kitty image of one pixel.
        host.append(Data("\u{1B}Pq#0;2;100;0;0#0~~~~~~-~~~~~~\u{1B}\\after".utf8))
        let emulator = host.terminal.getTerminal()
        #expect(text(host) == "beforeafter")
        #expect(emulator.getCursorLocation().y == 0)
        let kitty = Data("\u{1B}_Gf=24,s=1,v=1,a=T;/wAA\u{1B}\\!".utf8)
        host.append(kitty.prefix(9))
        host.append(kitty.dropFirst(9))
        #expect(text(host) == "beforeafter!")
        #expect(emulator.getCursorLocation().y == 0)
        // A snapshot starts over, also in the middle of a string.
        host.append(Data("\u{1B}Pq#0~~".utf8))
        host.snapshot(cols: 80, rows: 24, data: Data("fresh".utf8))
        #expect(text(host) == "fresh")
    }

    /// How many images the emulator holds in its lines. SwiftTerm keeps them out of its public interface.
    private func images(_ host: TerminalHostView) -> Int {
        let emulator = host.terminal.getTerminal()
        var n = 0
        for row in 0..<emulator.rows {
            guard let line = emulator.getLine(row: row) else { continue }
            guard let held = Mirror(reflecting: line).children.first(where: { $0.label == "images" })?.value else { continue }
            let some = Mirror(reflecting: held)
            guard some.displayStyle == .optional, let list = some.children.first?.value else { continue }
            n += Mirror(reflecting: list).children.count
        }
        return n
    }

    /// Every way of putting the pieces of a graphics sequence together, in one frame or several, draws no image.
    @Test func noArrangementOfOutputDrawsAnImage() {
        let sixel = [UInt8]("#0;2;100;0;0#0~~~~~~-~~~~~~".utf8)
        let kitty = [UInt8]("f=24,s=1,v=1,a=T;/wAA".utf8)
        // The emulator does draw them when asked directly: the count below means something.
        let bare = host()
        bare.snapshot(cols: 80, rows: 24, data: Data())
        bare.terminal.feed(byteArray: ([0x1B, 0x50, 0x71] + sixel + [0x1B, 0x5C])[...])
        #expect(images(bare) > 0)

        // The pieces that matter, often; the rest of what moves the parser, now and then.
        let core: [[UInt8]] = [
            [0x1B], [0x50], [0x5F], [0x71], [0x47], sixel, kitty, [0x1B, 0x5C], [0x90], [0x9F], [0x18], [0x1A], [0x5D], [0xE5],
        ]
        let rest: [[UInt8]] = [
            [0x9C], [0x07], [0x9B], [0x9D], [0x98], [0x9E], [0x5B], [0x58], [0x5E], [0x20], [0x24], [0x30], [0x3B], [0x3A], [0x3F],
            [0x6D], [0x61], [0x5C], [0xC2], [0xE5, 0x90, 0x90], [0x0A], [0x7F], [0x19], [0x85],
        ]
        var seed: UInt64 = 0x9E37_79B9_7F4A_7C15
        func next(_ n: Int) -> Int {
            seed = seed &* 6_364_136_223_846_793_005 &+ 1_442_695_040_888_963_407
            return Int((seed >> 33) % UInt64(n))
        }
        let host = host()
        func drawn(_ stream: [UInt8], cut: Int) -> Bool {
            host.snapshot(cols: 80, rows: 24, data: Data())
            host.append(Data(stream[..<cut]))
            if cut < stream.count { host.append(Data(stream[cut...])) }
            guard images(host) > 0 else { return false }
            Issue.record("an image was drawn by \(stream.map { String($0, radix: 16) }.joined(separator: " ")) cut at \(cut)")
            return true
        }
        // Every order of up to five of the pieces a graphics sequence is made of, and of what ends or cancels one.
        let few: [[UInt8]] = [
            [0x1B], [0x50], [0x18], [0x1A], [0x1B, 0x5C], [0x50, 0x71] + sixel, [0x5F, 0x47] + kitty, [0x90, 0x71] + sixel,
        ]
        var order = [Int]()
        func every(_ left: Int) -> Bool {
            if !order.isEmpty {
                let stream = order.flatMap { few[$0] }
                if drawn(stream, cut: stream.count) { return true }
            }
            guard left > 0 else { return false }
            for i in few.indices {
                order.append(i)
                defer { order.removeLast() }
                if every(left - 1) { return true }
            }
            return false
        }
        if every(5) { return }
        // And at random, with the rest of what moves the parser, in one frame or cut at a point of its own.
        for round in 0..<4000 {
            var stream: [UInt8] = []
            for _ in 0..<(2 + next(8)) { stream += next(5) == 0 ? rest[next(rest.count)] : core[next(core.count)] }
            if drawn(stream, cut: round % 2 == 0 ? stream.count : next(stream.count + 1)) { return }
        }
    }

    /// Leaving the strings out changes nothing else: with strings that draw nothing, the screen is the one the
    /// emulator draws from the output as it came, for every short order of what begins, fills and ends them.
    @Test func leavingStringsOutShowsWhatTheOutputShows() {
        let pieces: [[UInt8]] = [
            [0x1B], [0x50], [0x5F], [0x90], [0x9F], [0x18], [0x1A], [0x19], [0x1B, 0x5C], [0x9C], [0x07], [0x85], [0x9B], [0x9D],
            [0x24, 0x71, 0x6D], [0x5A, 0x78], [0x3A], [0x31, 0x3B], [0xE5, 0x90, 0x90], [0x61, 0x62], [0x5D, 0x30, 0x3B], [0x0A],
        ]
        let filtered = host()
        let bare = host()
        func same(_ frames: [[UInt8]]) -> Bool {
            filtered.snapshot(cols: 40, rows: 8, data: Data())
            bare.snapshot(cols: 40, rows: 8, data: Data())
            for frame in frames {
                filtered.append(Data(frame))
                bare.terminal.feed(byteArray: frame[...])
            }
            // What is still open is closed, so that both are back in plain text, and a mark shows where they are.
            let end: [UInt8] = [0x1A, 0x7C]
            filtered.append(Data(end))
            bare.terminal.feed(byteArray: end[...])
            let (a, b) = (filtered.terminal.getTerminal(), bare.terminal.getTerminal())
            if text(filtered) == text(bare), a.getCursorLocation().x == b.getCursorLocation().x, a.getCursorLocation().y == b.getCursorLocation().y {
                return true
            }
            let said = frames.map { $0.map { String($0, radix: 16) }.joined(separator: " ") }.joined(separator: " | ")
            Issue.record("\(said): shown as \(text(filtered).debugDescription), not \(text(bare).debugDescription)")
            return false
        }
        var order = [Int]()
        func every(_ left: Int) -> Bool {
            if !order.isEmpty, !same([order.flatMap { pieces[$0] }]) { return false }
            guard left > 0 else { return true }
            for i in pieces.indices {
                order.append(i)
                defer { order.removeLast() }
                if !every(left - 1) { return false }
            }
            return true
        }
        guard every(3) else { return }
        // Longer ones at random, in one frame or two.
        var seed: UInt64 = 0x2545_F491_4F6C_DD1D
        func next(_ n: Int) -> Int {
            seed = seed &* 6_364_136_223_846_793_005 &+ 1_442_695_040_888_963_407
            return Int((seed >> 33) % UInt64(n))
        }
        for round in 0..<6000 {
            var stream: [UInt8] = []
            for _ in 0..<(4 + next(8)) { stream += pieces[next(pieces.count)] }
            let cut = round % 2 == 0 ? stream.count : next(stream.count + 1)
            let frames = cut < stream.count && cut > 0 ? [Array(stream[..<cut]), Array(stream[cut...])] : [stream]
            if !same(frames) { return }
        }
    }

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
        // 120 columns do not fit 400 pt at the floor: the font stops there and the
        // rest of each line is panned to (architecture §11 「尺寸」).
        #expect(host.shownPoints == CGFloat(terminalFontFloor))
        #expect(host.terminal.frame.maxX > 400)
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
        #expect(host.shownPoints == 14 && host.terminal.font.pointSize == 14)
        #expect(text(host) == "small")
    }

    /// SwiftTerm resets the terminal when its font is set. A rotation or 字号 must not cost the program what
    /// it set, since the remote does not say it again: the view is scaled, and its font left alone.
    @Test func showingAtAnotherSizeKeepsWhatTheProgramSet() {
        let host = host(width: 400, height: 500)
        host.snapshot(cols: 90, rows: 40, data: Data("wide".utf8))
        let emulator = host.terminal.getTerminal()
        // Application cursor keys, a scroll region, and bold for what comes next.
        host.append(Data("\u{1B}[?1h\u{1B}[5;20r\u{1B}[1m".utf8))
        #expect(host.appCursor && emulator.buffer.scrollTop == 4 && emulator.buffer.scrollBottom == 19)
        var shown = [host.shownPoints]
        // Turned on its side, then a smaller size chosen without the window being fitted.
        host.frame.size = CGSize(width: 800, height: 300)
        host.layoutIfNeeded()
        shown.append(host.shownPoints)
        host.preferred = 10
        host.frame.size = CGSize(width: 1200, height: 900)
        host.layoutIfNeeded()
        shown.append(host.shownPoints)
        #expect(Set(shown).count == 3 && shown[2] == 10)
        #expect(host.terminal.frame.maxX <= 1200 && host.terminal.frame.maxY <= 900)
        #expect((emulator.cols, emulator.rows) == (90, 40))

        #expect(host.appCursor)
        #expect(emulator.buffer.scrollTop == 4 && emulator.buffer.scrollBottom == 19)
        host.append(Data("\u{1B}[10;1Hbold".utf8))
        #expect(emulator.getCharData(col: 0, row: 9)?.attribute.style.contains(.bold) == true)
        #expect(text(host) == "wide\nbold")
        // The size chosen becomes the font with the next snapshot, which resets the terminal itself.
        #expect(host.terminal.font.pointSize == 14)
        host.snapshot(cols: 90, rows: 40, data: Data("again".utf8))
        #expect(host.terminal.font.pointSize == 10 && host.shownPoints == 10)
        #expect((emulator.cols, emulator.rows) == (90, 40))
        #expect(!host.appCursor && text(host) == "again")
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
