// The reader's text: the styled runs of a history line drawn as plain
// attributed text, in the colours and font the live terminal is showing
// (architecture §11 「回看历史」). The runs come from RACore's
// `terminalHistoryRuns`; what is here is colours and layout, which need
// UIKit.
//
// The sixteen basic colours are the ones the live SwiftTerm view was
// installed with — its `Color.terminalAppColors`, which this client never
// replaces — and 16...255 are the xterm cube and grey ramp SwiftTerm derives
// from them, so a coloured line looks the same before and after the switch.
// A default foreground or background is the view's own (Theme.Term.text /
// Theme.Term.bg). Dim is the live view's ~50% brightness, inverse swaps the
// two, and bold and italic come from the monospaced system font.
//
// A line is already the pane's width, so nothing wraps. East Asian wide
// characters are two cells in the emulator but about 1.6 cells wide in SF
// Mono with PingFang behind it, so each is followed by enough kern to make
// it exactly two; that is what keeps columns under a ruler line lined up.
import RACore
import SwiftUI
import UIKit

@MainActor
enum TerminalHistoryText {
    /// SwiftTerm 1.11.2's `Color.terminalAppColors`, the palette its
    /// `Terminal` is built with and `TerminalView` installs.
    private static let basic: [RGBA] = [
        RGBA(0x000000), RGBA(0xc23621), RGBA(0x25bc24), RGBA(0xadad27),
        RGBA(0x492ee1), RGBA(0xd338d3), RGBA(0x33bbc8), RGBA(0xcbcccd),
        RGBA(0x818383), RGBA(0xfc391f), RGBA(0x31e722), RGBA(0xeaec23),
        RGBA(0x5833ff), RGBA(0xf935f8), RGBA(0x14f0f0), RGBA(0xe9ebeb),
    ]

    /// The 256 the emulator can name: the sixteen above, then the xterm cube
    /// and the grey ramp, as SwiftTerm's `generateXtermPalette` builds them.
    private static let palette: [RGBA] = {
        var colors = basic
        let v: [UInt8] = [0x00, 0x5f, 0x87, 0xaf, 0xd7, 0xff]
        for i in 0..<216 {
            colors.append(RGBA(UInt32(v[(i / 36) % 6]) << 16 | UInt32(v[(i / 6) % 6]) << 8 | UInt32(v[i % 6])))
        }
        for i in 0..<24 {
            let c = UInt8(8 + i * 10)
            colors.append(RGBA(UInt32(c) << 16 | UInt32(c) << 8 | UInt32(c)))
        }
        return colors
    }()

    static let background = UIColor(Theme.Term.bg)
    static let foreground = UIColor(Theme.Term.text)
    /// The grey of the reader's one status line, under the terminal's own font.
    static let status = UIColor(Theme.Term.tool)
    static let link = UIColor(Theme.Term.link)
    /// The tap target the 重试 link carries, so the delegate can tell it apart.
    static let retryURL = URL(string: "ra-retry://retry")!

    private static func color(_ c: TerminalColor, dim: Bool) -> UIColor {
        let rgba: RGBA
        switch c {
        case .palette(let n):
            rgba = palette[min(max(n, 0), palette.count - 1)]
        case .rgb(let r, let g, let b):
            rgba = RGBA(UInt32(r) << 16 | UInt32(g) << 8 | UInt32(b))
        }
        let factor = dim ? 0.5 : 1
        return UIColor(red: rgba.r * factor, green: rgba.g * factor, blue: rgba.b * factor, alpha: 1)
    }

    /// The monospaced system font the live terminal uses, with a run's weight and slant.
    static func font(points: CGFloat, bold: Bool, italic: Bool) -> UIFont {
        let base = UIFont.monospacedSystemFont(ofSize: points, weight: bold ? .bold : .regular)
        guard italic, let descriptor = base.fontDescriptor.withSymbolicTraits([base.fontDescriptor.symbolicTraits, .traitItalic])
        else { return base }
        return UIFont(descriptor: descriptor, size: points)
    }

    /// The foreground and background of a run, the defaults filled in and inverse swapped.
    private static func colors(_ style: TerminalStyle) -> (fg: UIColor, bg: UIColor?) {
        var fg = style.fg.map { color($0, dim: style.dim) } ?? foreground
        var bg = style.bg.map { color($0, dim: false) }
        if style.inverse {
            let swapped = (fg: bg ?? background, bg: fg)
            fg = swapped.fg
            bg = swapped.bg
        }
        return (fg, bg)
    }

    /// The lines of a page, one paragraph each, at the terminal's line height.
    static func page(_ lines: [String], points: CGFloat, cell: CGSize) -> NSAttributedString {
        let out = NSMutableAttributedString()
        for (i, line) in lines.enumerated() {
            if i > 0 { out.append(NSAttributedString(string: "\n", attributes: plain(points: points, cell: cell))) }
            out.append(self.line(line, points: points, cell: cell))
        }
        return out
    }

    /// One history line as attributed text.
    static func line(_ text: String, points: CGFloat, cell: CGSize) -> NSAttributedString {
        let out = NSMutableAttributedString()
        for run in terminalHistoryRuns(text) {
            let font = font(points: points, bold: run.style.bold, italic: run.style.italic)
            let (fg, bg) = colors(run.style)
            var attributes = plain(points: points, cell: cell, font: font)
            attributes[.foregroundColor] = fg
            if let bg { attributes[.backgroundColor] = bg }
            if run.style.underline { attributes[.underlineStyle] = NSUnderlineStyle.single.rawValue }
            append(run.text, attributes: attributes, font: font, cellWidth: cell.width, to: out)
        }
        return out
    }

    /// The reader's one status line: the mockup's own small grey line, first in
    /// the flow, with 重试 beside it after a failure.
    static func statusLine(_ status: TerminalHistory.Status, online: Bool) -> NSAttributedString {
        let paragraph = NSMutableParagraphStyle()
        paragraph.minimumLineHeight = 18
        paragraph.maximumLineHeight = 18
        paragraph.paragraphSpacing = 6
        let attributes: [NSAttributedString.Key: Any] = [
            .font: UIFont.systemFont(ofSize: 12), .foregroundColor: self.status, .paragraphStyle: paragraph,
        ]
        let out = NSMutableAttributedString(string: statusText(status), attributes: attributes)
        guard status == .failed else { return out }
        out.append(NSAttributedString(string: " ", attributes: attributes))
        var linkAttributes = attributes
        linkAttributes[.foregroundColor] = online ? link : self.status
        linkAttributes[.link] = retryURL
        out.append(NSAttributedString(string: "重试", attributes: linkAttributes))
        return out
    }

    private static func statusText(_ status: TerminalHistory.Status) -> String {
        switch status {
        case .none: ""
        case .earlier: "读取更早的记录…"
        case .failed: "读取失败"
        case .expired: "记录已过期，回到实时后重新往上滑"
        case .start: "已经是保留的最早记录"
        case .disconnected: "终端断开了，已读到的内容还能看；重新连接后再往上滑读取更早的"
        }
    }

    /// The whole reader: the status line, then the pages from oldest to newest.
    static func document(
        pages: [TerminalHistory.Page], status: TerminalHistory.Status, online: Bool, points: CGFloat, cell: CGSize
    ) -> NSAttributedString {
        let out = NSMutableAttributedString()
        out.append(statusLine(status, online: online))
        for page in pages {
            out.append(NSAttributedString(string: "\n", attributes: plain(points: points, cell: cell)))
            out.append(self.page(page.lines, points: points, cell: cell))
        }
        return out
    }

    /// The attributes everything shares: the font, the colour, the one line height.
    private static func plain(points: CGFloat, cell: CGSize, font: UIFont? = nil) -> [NSAttributedString.Key: Any] {
        let paragraph = NSMutableParagraphStyle()
        paragraph.minimumLineHeight = cell.height
        paragraph.maximumLineHeight = cell.height
        paragraph.lineBreakMode = .byClipping
        return [
            .font: font ?? self.font(points: points, bold: false, italic: false),
            .foregroundColor: foreground,
            .paragraphStyle: paragraph,
        ]
    }

    /// Appends a run, padding every cluster that is more than one cell out to
    /// the whole cells the emulator would give it. ASCII is always one, so a
    /// run of it is appended in one go.
    private static func append(
        _ text: String, attributes: [NSAttributedString.Key: Any], font: UIFont, cellWidth: CGFloat, to out: NSMutableAttributedString
    ) {
        guard cellWidth > 0, text.unicodeScalars.contains(where: { $0.value > 0x7F }) else {
            out.append(NSAttributedString(string: text, attributes: attributes))
            return
        }
        for character in text {
            let piece = String(character)
            let start = out.length
            out.append(NSAttributedString(string: piece, attributes: attributes))
            let advance = measure(piece, font: font)
            let cells = Int((advance / cellWidth).rounded())
            guard cells != 1, cells >= 0 else { continue }
            let kern = CGFloat(cells) * cellWidth - advance
            if abs(kern) > 0.05 { out.addAttribute(.kern, value: kern, range: NSRange(location: start, length: out.length - start)) }
        }
    }

    /// How wide the monospaced font draws a cluster. Measured once each, as it is the same in every line.
    private static var widths: [String: CGFloat] = [:]

    private static func measure(_ cluster: String, font: UIFont) -> CGFloat {
        let key = "\(font.pointSize)/\(font.fontDescriptor.symbolicTraits.rawValue)/\(cluster)"
        if let w = widths[key] { return w }
        let w = (cluster as NSString).size(withAttributes: [.font: font]).width
        widths[key] = w
        return w
    }
}
