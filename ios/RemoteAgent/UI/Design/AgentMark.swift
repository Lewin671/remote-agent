// The mark of a CLI as a view (design-system §5 「CLI 的标」): filled in the
// colour of the text beside it and as tall as that text, never in a brand
// colour. Where the CLI's name is written next to it the mark is decoration
// (`named`); where it stands for the name, VoiceOver says the name.
// The shapes are in AgentGlyphs.swift.
import RACore
import RAProto
import SwiftUI
import UIKit

/// A CLI's mark, `scale` times the height of the letters of `style`, with the gap
/// to the words after it (0.4 em) and its foot a little under their baseline, so
/// that in an `HStack(alignment: .firstTextBaseline)` it sits as a letter does.
struct AgentMark: View {
    private let agent: PBAgent
    private let named: Bool
    private let scale: CGFloat
    private let color: Color?
    @ScaledMetric private var em: CGFloat

    /// - Parameters:
    ///   - style: the text style of the words beside it; the mark grows with it.
    ///   - scale: its side in ems of that text: 1 beside a name, 1.15 where it leads a line alone.
    ///   - named: the name is written beside it, so the mark says nothing to VoiceOver.
    init(_ agent: PBAgent, style: Font.TextStyle, scale: CGFloat = 1, color: Color? = nil, named: Bool = false) {
        self.agent = agent
        self.named = named
        self.scale = scale
        self.color = color
        _em = ScaledMetric(wrappedValue: Self.size(of: style), relativeTo: style)
    }

    /// The size of a text style at the default Dynamic Type setting.
    static func size(of style: Font.TextStyle) -> CGFloat {
        switch style {
        case .footnote: 13
        case .subheadline: 15
        case .headline, .body: 17
        case .callout: 16
        case .caption: 12
        default: 17
        }
    }

    var body: some View {
        if let glyph = AgentGlyph(agent) {
            let side = em * scale
            AgentMarkShape(glyph: glyph)
                .fill()
                .ink(color)
                .frame(width: side, height: side)
                .alignmentGuide(.firstTextBaseline) { $0.height - 0.16 * side }
                .padding(.trailing, 0.4 * side)
                .accessibilityElement()
                .accessibilityLabel(named ? "" : agentName(agent))
                .accessibilityAddTraits(.isImage)
                .accessibilityHidden(named)
        }
    }
}

/// A mark fitted into its rectangle, centred, whatever the box its path is drawn on.
struct AgentMarkShape: Shape {
    let glyph: AgentGlyph

    func path(in rect: CGRect) -> Path {
        let box = glyph.box
        let s = min(rect.width / box.width, rect.height / box.height)
        return glyph.path.applying(CGAffineTransform(
            a: s, b: 0, c: 0, d: s,
            tx: rect.midX - box.midX * s, ty: rect.midY - box.midY * s))
    }
}

extension AgentGlyph {
    /// The mark's outline on its own box, read once.
    var path: Path { Self.paths.value(for: self) }
    private static let paths = GlyphPaths()

    /// The mark as a template image `side` points square, for where only an image fits, such as an item of a system menu.
    func image(side: CGFloat) -> UIImage {
        let drawn = AgentMarkShape(glyph: self).path(in: CGRect(x: 0, y: 0, width: side, height: side)).cgPath
        let image = UIGraphicsImageRenderer(size: CGSize(width: side, height: side)).image { context in
            UIColor.black.setFill()
            context.cgContext.addPath(drawn)
            context.cgContext.fillPath()
        }
        return image.withRenderingMode(.alwaysTemplate)
    }
}

/// Each mark's path is parsed on first use and kept.
private final class GlyphPaths: @unchecked Sendable {
    private let lock = NSLock()
    private var parsed = [String: Path]()

    func value(for glyph: AgentGlyph) -> Path {
        lock.lock()
        defer { lock.unlock() }
        if let path = parsed[glyph.data] { return path }
        let path = SVGPathData.path(glyph.data) ?? Path()
        parsed[glyph.data] = path
        return path
    }
}

/// SVG path data (the `d` of a `<path>`) read into a `Path`: the commands M L H V C A Z, absolute and relative,
/// with the shorthand SVG allows (numbers run together, repeated coordinates, flags without a gap). Another command, or
/// data that ends in the middle of one, gives nil.
enum SVGPathData {
    static func path(_ d: String) -> Path? {
        var scan = Scan(Array(d.utf8))
        var p = Path()
        var at = CGPoint.zero
        var start = CGPoint.zero
        var command: UInt8 = 0
        while true {
            scan.skipSeparators()
            guard let next = scan.peek else { break }
            if isLetter(next) {
                command = next
                scan.advance()
                if command == UInt8(ascii: "Z") || command == UInt8(ascii: "z") {
                    p.closeSubpath()
                    at = start
                    continue
                }
            } else if command == 0 || command == UInt8(ascii: "Z") || command == UInt8(ascii: "z") {
                return nil
            }
            let relative = command >= UInt8(ascii: "a")
            let base = relative ? at : .zero
            switch relative ? command : command + 32 {
            case UInt8(ascii: "m"):
                guard let x = scan.number(), let y = scan.number() else { return nil }
                at = CGPoint(x: base.x + x, y: base.y + y)
                start = at
                p.move(to: at)
                // Further pairs after a moveto are lines.
                command = relative ? UInt8(ascii: "l") : UInt8(ascii: "L")
            case UInt8(ascii: "l"):
                guard let x = scan.number(), let y = scan.number() else { return nil }
                at = CGPoint(x: base.x + x, y: base.y + y)
                p.addLine(to: at)
            case UInt8(ascii: "h"):
                guard let x = scan.number() else { return nil }
                at = CGPoint(x: base.x + x, y: at.y)
                p.addLine(to: at)
            case UInt8(ascii: "v"):
                guard let y = scan.number() else { return nil }
                at = CGPoint(x: at.x, y: base.y + y)
                p.addLine(to: at)
            case UInt8(ascii: "c"):
                guard let x1 = scan.number(), let y1 = scan.number(), let x2 = scan.number(), let y2 = scan.number(),
                    let x = scan.number(), let y = scan.number()
                else { return nil }
                at = CGPoint(x: base.x + x, y: base.y + y)
                p.addCurve(
                    to: at, control1: CGPoint(x: base.x + x1, y: base.y + y1), control2: CGPoint(x: base.x + x2, y: base.y + y2))
            case UInt8(ascii: "a"):
                guard let rx = scan.number(), let ry = scan.number(), let turn = scan.number(),
                    let large = scan.flag(), let sweep = scan.flag(), let x = scan.number(), let y = scan.number()
                else { return nil }
                let end = CGPoint(x: base.x + x, y: base.y + y)
                addArc(&p, from: at, to: end, rx: rx, ry: ry, turn: turn, large: large, sweep: sweep)
                at = end
            default:
                return nil
            }
        }
        return p
    }

    private static func isLetter(_ c: UInt8) -> Bool {
        (c >= UInt8(ascii: "A") && c <= UInt8(ascii: "Z")) || (c >= UInt8(ascii: "a") && c <= UInt8(ascii: "z"))
    }

    /// An elliptical arc as cubic curves of at most a quarter turn each (SVG 1.1 implementation notes F.6).
    private static func addArc(
        _ p: inout Path, from a: CGPoint, to b: CGPoint, rx rx0: CGFloat, ry ry0: CGFloat, turn: CGFloat, large: Bool, sweep: Bool
    ) {
        var rx = abs(rx0), ry = abs(ry0)
        if a == b { return }
        if rx == 0 || ry == 0 {
            p.addLine(to: b)
            return
        }
        let phi = turn * .pi / 180
        let cosPhi = cos(phi), sinPhi = sin(phi)
        let dx = (a.x - b.x) / 2, dy = (a.y - b.y) / 2
        let x1 = cosPhi * dx + sinPhi * dy
        let y1 = -sinPhi * dx + cosPhi * dy
        let tooSmall = x1 * x1 / (rx * rx) + y1 * y1 / (ry * ry)
        if tooSmall > 1 {
            rx *= tooSmall.squareRoot()
            ry *= tooSmall.squareRoot()
        }
        let numerator = rx * rx * ry * ry - rx * rx * y1 * y1 - ry * ry * x1 * x1
        let denominator = rx * rx * y1 * y1 + ry * ry * x1 * x1
        let coefficient = (large == sweep ? -1 : 1) * max(0, numerator / denominator).squareRoot()
        let cx1 = coefficient * rx * y1 / ry
        let cy1 = -coefficient * ry * x1 / rx
        let cx = cosPhi * cx1 - sinPhi * cy1 + (a.x + b.x) / 2
        let cy = sinPhi * cx1 + cosPhi * cy1 + (a.y + b.y) / 2
        func angle(_ ux: CGFloat, _ uy: CGFloat, _ vx: CGFloat, _ vy: CGFloat) -> CGFloat {
            atan2(ux * vy - uy * vx, ux * vx + uy * vy)
        }
        let ux = (x1 - cx1) / rx, uy = (y1 - cy1) / ry
        let vx = (-x1 - cx1) / rx, vy = (-y1 - cy1) / ry
        let theta = angle(1, 0, ux, uy)
        var delta = angle(ux, uy, vx, vy)
        if !sweep && delta > 0 { delta -= 2 * .pi }
        if sweep && delta < 0 { delta += 2 * .pi }
        let pieces = max(1, Int((abs(delta) / (.pi / 2) - 1e-9).rounded(.up)))
        let step = delta / CGFloat(pieces)
        let k = 4 / 3 * tan(step / 4)
        func point(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
            CGPoint(x: cx + rx * x * cosPhi - ry * y * sinPhi, y: cy + rx * x * sinPhi + ry * y * cosPhi)
        }
        for i in 0..<pieces {
            let t0 = theta + step * CGFloat(i), t1 = t0 + step
            p.addCurve(
                to: i == pieces - 1 ? b : point(cos(t1), sin(t1)),
                control1: point(cos(t0) - k * sin(t0), sin(t0) + k * cos(t0)),
                control2: point(cos(t1) + k * sin(t1), sin(t1) - k * cos(t1)))
        }
    }

    /// A cursor over the bytes of the data.
    private struct Scan {
        private let bytes: [UInt8]
        private var i = 0

        init(_ bytes: [UInt8]) { self.bytes = bytes }

        var peek: UInt8? { i < bytes.count ? bytes[i] : nil }

        mutating func advance() { i += 1 }

        mutating func skipSeparators() {
            while let c = peek, c == UInt8(ascii: " ") || c == UInt8(ascii: ",") || c == 10 || c == 13 || c == 9 { i += 1 }
        }

        /// A number, or nil where there is none: 12, -.5, 3.25e-2; a second point in a row (`.5.5`) starts the next number.
        mutating func number() -> CGFloat? {
            skipSeparators()
            let begin = i
            if let c = peek, c == UInt8(ascii: "-") || c == UInt8(ascii: "+") { i += 1 }
            var digits = 0
            while let c = peek, isDigit(c) { i += 1; digits += 1 }
            if peek == UInt8(ascii: ".") {
                i += 1
                while let c = peek, isDigit(c) { i += 1; digits += 1 }
            }
            guard digits > 0 else { i = begin; return nil }
            if let c = peek, c == UInt8(ascii: "e") || c == UInt8(ascii: "E") {
                let before = i
                i += 1
                if let s = peek, s == UInt8(ascii: "-") || s == UInt8(ascii: "+") { i += 1 }
                if let d = peek, isDigit(d) { while let d = peek, isDigit(d) { i += 1 } } else { i = before }
            }
            return Double(String(decoding: bytes[begin..<i], as: UTF8.self)).map { CGFloat($0) }
        }

        /// An arc's flag: one digit, 0 or 1, which may be followed at once by the next number.
        mutating func flag() -> Bool? {
            skipSeparators()
            guard let c = peek, c == UInt8(ascii: "0") || c == UInt8(ascii: "1") else { return nil }
            i += 1
            return c == UInt8(ascii: "1")
        }

        private func isDigit(_ c: UInt8) -> Bool { c >= UInt8(ascii: "0") && c <= UInt8(ascii: "9") }
    }
}
