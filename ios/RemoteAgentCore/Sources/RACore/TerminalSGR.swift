// One line of terminal history as styled runs. The daemon captures history
// with `capture-pane -e`, so a line carries SGR sequences from the program
// that wrote it; the reader shows them instead of replaying them into a
// terminal (architecture §11 「回看历史」). The contract is
// testdata/terminal/history-sgr.json, read by the web, Android and iOS
// readers alike.
//
// Only SGR (`ESC [ … m`) means anything here; every other escape or control
// sequence is dropped without printing it. Each line is parsed on its own,
// starting from the default style. Colours are null for the terminal's
// default, a 256-colour palette index (0-15 being the basic and bright
// ones), or `#rrggbb`. Adjacent runs with the same style are merged and
// empty runs are omitted, so a plain line is one run and an empty one is no
// runs at all.
import Foundation

/// The foreground or background of a run.
public enum TerminalColor: Sendable, Equatable {
    /// A 256-colour palette index, 0-15 the basic and bright colours.
    case palette(Int)
    /// Truecolor, the components as they were written.
    case rgb(UInt8, UInt8, UInt8)

    /// `#rrggbb`, as the shared cases spell a truecolor value.
    public var hex: String {
        guard case .rgb(let r, let g, let b) = self else { return "" }
        return String(format: "#%02x%02x%02x", r, g, b)
    }
}

/// What a run looks like: colours, and the attributes SGR sets.
public struct TerminalStyle: Sendable, Equatable {
    public var fg: TerminalColor?
    public var bg: TerminalColor?
    public var bold = false
    public var dim = false
    public var italic = false
    public var underline = false
    public var inverse = false

    public init() {}

    public init(
        fg: TerminalColor? = nil, bg: TerminalColor? = nil, bold: Bool = false, dim: Bool = false, italic: Bool = false,
        underline: Bool = false, inverse: Bool = false
    ) {
        self.fg = fg
        self.bg = bg
        self.bold = bold
        self.dim = dim
        self.italic = italic
        self.underline = underline
        self.inverse = inverse
    }
}

/// A stretch of a line that shares one style.
public struct TerminalRun: Sendable, Equatable {
    public var text: String
    public var style: TerminalStyle

    public init(text: String, style: TerminalStyle = TerminalStyle()) {
        self.text = text
        self.style = style
    }
}

/// The styled runs of one history line.
public func terminalHistoryRuns(_ line: String) -> [TerminalRun] {
    var runs: [TerminalRun] = []
    var text = [UInt8]()
    var style = TerminalStyle()

    func flush() {
        guard !text.isEmpty else { return }
        let piece = String(decoding: text, as: UTF8.self)
        text.removeAll(keepingCapacity: true)
        if var last = runs.last, last.style == style {
            last.text += piece
            runs[runs.count - 1] = last
        } else {
            runs.append(TerminalRun(text: piece, style: style))
        }
    }

    let bytes = Array(line.utf8)
    var i = 0
    while i < bytes.count {
        let b = bytes[i]
        if b == 0x1B {
            flush()
            i += 1
            guard i < bytes.count else { break }
            switch bytes[i] {
            case 0x5B:  // [ — a control sequence: parameters up to a final byte.
                i += 1
                var params = [UInt8]()
                while i < bytes.count {
                    let c = bytes[i]
                    if c == 0x1B { break }  // a new escape begins here, and this one is left out
                    if (0x40...0x7E).contains(c) {
                        if c == 0x6D { applySGR(params, &style) }  // m
                        i += 1
                        break
                    }
                    if (0x20...0x3F).contains(c) { params.append(c) }
                    i += 1
                }
            case 0x5D, 0x50, 0x58, 0x5E, 0x5F:  // ] OSC, P DCS, X SOS, ^ PM, _ APC — a string
                i = skipString(bytes, i + 1)
            default:
                i += 1  // any other escape is two bytes and means nothing here
            }
        } else if b == 0x09 {
            text.append(b)
            i += 1
        } else if b < 0x20 || b == 0x7F {
            i += 1  // other controls are dropped
        } else {
            text.append(b)
            i += 1
        }
    }
    flush()
    return runs
}

/// Past a string sequence: to BEL, or to ST (`ESC \`). An ESC that is not
/// followed by `\` ends it and begins a sequence of its own.
private func skipString(_ bytes: [UInt8], _ start: Int) -> Int {
    var i = start
    while i < bytes.count {
        if bytes[i] == 0x07 { return i + 1 }
        if bytes[i] == 0x1B { return i + 1 < bytes.count && bytes[i + 1] == 0x5C ? i + 2 : i }
        i += 1
    }
    return i
}

/// Applies one SGR parameter list to the style. Parameters are decimal,
/// separated by `;`; an empty list, and an empty parameter, is a nought.
private func applySGR(_ raw: [UInt8], _ style: inout TerminalStyle) {
    var codes = String(decoding: raw, as: UTF8.self).split(separator: ";", omittingEmptySubsequences: false).map { Int($0) ?? 0 }
    if codes.isEmpty { codes = [0] }
    var i = 0
    while i < codes.count {
        switch codes[i] {
        case 0: style = TerminalStyle()
        case 1: style.bold = true
        case 2: style.dim = true
        case 3: style.italic = true
        case 4: style.underline = true
        case 7: style.inverse = true
        case 22:
            style.bold = false
            style.dim = false
        case 23: style.italic = false
        case 24: style.underline = false
        case 27: style.inverse = false
        case 30...37: style.fg = .palette(codes[i] - 30)
        case 39: style.fg = nil
        case 40...47: style.bg = .palette(codes[i] - 40)
        case 49: style.bg = nil
        case 90...97: style.fg = .palette(codes[i] - 90 + 8)
        case 100...107: style.bg = .palette(codes[i] - 100 + 8)
        case 38, 48:
            let foreground = codes[i] == 38
            if i + 1 < codes.count {
                switch codes[i + 1] {
                case 5 where i + 2 < codes.count:
                    let n = codes[i + 2]
                    if (0...255).contains(n) { set(foreground, .palette(n), &style) }
                    i += 2
                case 2 where i + 4 < codes.count:
                    let colour = TerminalColor.rgb(byte(codes[i + 2]), byte(codes[i + 3]), byte(codes[i + 4]))
                    set(foreground, colour, &style)
                    i += 4
                default: break
                }
            }
        default: break  // blink, overline and the rest mean nothing in a reader
        }
        i += 1
    }
}

private func set(_ foreground: Bool, _ colour: TerminalColor, _ style: inout TerminalStyle) {
    if foreground { style.fg = colour } else { style.bg = colour }
}

private func byte(_ n: Int) -> UInt8 { UInt8(min(max(n, 0), 255)) }
