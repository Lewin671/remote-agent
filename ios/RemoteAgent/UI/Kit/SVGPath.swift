// SVG path data to a SwiftUI Path: the whole of the `d` grammar, since the
// icons are Lucide's path strings as lucide-react and Android's ui/Icons.kt
// carry them (compact numbers such as "-.853.505", arc flags, relative
// commands), and the app mark is the Android launcher's vector.
import SwiftUI

enum SVGPath {
    /// The path `d` describes, in its own coordinates. Data that does not parse ends the path where it stops.
    static func parse(_ d: String) -> Path {
        var p = Parser(Array(d.utf8))
        return p.run()
    }

    /// An SVG circle as path data: two half turns.
    static func circle(cx: Int, cy: Int, r: Int) -> String {
        "M\(cx - r) \(cy)a\(r) \(r) 0 1 0 \(2 * r) 0a\(r) \(r) 0 1 0 \(-2 * r) 0z"
    }

    private struct Parser {
        let s: [UInt8]
        var i = 0
        var path = Path()
        var cur = CGPoint.zero
        var start = CGPoint.zero
        /// The last control point, for the smooth commands, and which kind of curve it was of.
        var control: CGPoint?
        var lastCurve: UInt8 = 0

        init(_ s: [UInt8]) { self.s = s }

        mutating func skipSeparators() {
            while i < s.count, s[i] == 0x20 || s[i] == 0x2c || s[i] == 0x0a || s[i] == 0x09 || s[i] == 0x0d { i += 1 }
        }

        mutating func number() -> CGFloat? {
            skipSeparators()
            let from = i
            if i < s.count, s[i] == 0x2d || s[i] == 0x2b { i += 1 }
            var digits = 0
            while i < s.count, s[i] >= 0x30, s[i] <= 0x39 { i += 1; digits += 1 }
            if i < s.count, s[i] == 0x2e {
                i += 1
                while i < s.count, s[i] >= 0x30, s[i] <= 0x39 { i += 1; digits += 1 }
            }
            guard digits > 0 else { i = from; return nil }
            if i < s.count, s[i] == 0x65 || s[i] == 0x45 {
                var j = i + 1
                if j < s.count, s[j] == 0x2d || s[j] == 0x2b { j += 1 }
                if j < s.count, s[j] >= 0x30, s[j] <= 0x39 {
                    while j < s.count, s[j] >= 0x30, s[j] <= 0x39 { j += 1 }
                    i = j
                }
            }
            return Double(String(decoding: s[from..<i], as: UTF8.self)).map { CGFloat($0) }
        }

        /// An arc's flag: one digit, which may be written with nothing after it.
        mutating func flag() -> Bool? {
            skipSeparators()
            guard i < s.count, s[i] == 0x30 || s[i] == 0x31 else { return nil }
            defer { i += 1 }
            return s[i] == 0x31
        }

        mutating func point(relative: Bool) -> CGPoint? {
            guard let x = number(), let y = number() else { return nil }
            return relative ? CGPoint(x: cur.x + x, y: cur.y + y) : CGPoint(x: x, y: y)
        }

        mutating func run() -> Path {
            var command: UInt8 = 0
            while true {
                skipSeparators()
                guard i < s.count else { break }
                let c = s[i]
                if (c >= 0x41 && c <= 0x5a) || (c >= 0x61 && c <= 0x7a) {
                    command = c
                    i += 1
                    if c == 0x5a || c == 0x7a {
                        path.closeSubpath()
                        cur = start
                        control = nil
                        continue
                    }
                } else if command == 0 || command == 0x5a || command == 0x7a {
                    break
                }
                guard step(command) else { break }
                // Coordinates after a moveto without a new letter are linetos.
                if command == 0x4d { command = 0x4c } else if command == 0x6d { command = 0x6c }
            }
            return path
        }

        mutating func step(_ command: UInt8) -> Bool {
            let relative = command >= 0x61
            let upper = relative ? command - 0x20 : command
            var newControl: CGPoint?
            switch upper {
            case 0x4d:  // M
                guard let p = point(relative: relative) else { return false }
                path.move(to: p)
                cur = p
                start = p
            case 0x4c:  // L
                guard let p = point(relative: relative) else { return false }
                path.addLine(to: p)
                cur = p
            case 0x48:  // H
                guard let x = number() else { return false }
                cur = CGPoint(x: relative ? cur.x + x : x, y: cur.y)
                path.addLine(to: cur)
            case 0x56:  // V
                guard let y = number() else { return false }
                cur = CGPoint(x: cur.x, y: relative ? cur.y + y : y)
                path.addLine(to: cur)
            case 0x43:  // C
                guard let c1 = point(relative: relative), let c2 = point(relative: relative),
                      let p = point(relative: relative) else { return false }
                path.addCurve(to: p, control1: c1, control2: c2)
                newControl = c2
                cur = p
            case 0x53:  // S
                guard let c2 = point(relative: relative), let p = point(relative: relative) else { return false }
                let c1 = (lastCurve == 0x43 || lastCurve == 0x53) ? reflected() : cur
                path.addCurve(to: p, control1: c1, control2: c2)
                newControl = c2
                cur = p
            case 0x51:  // Q
                guard let c = point(relative: relative), let p = point(relative: relative) else { return false }
                path.addQuadCurve(to: p, control: c)
                newControl = c
                cur = p
            case 0x54:  // T
                guard let p = point(relative: relative) else { return false }
                let c = (lastCurve == 0x51 || lastCurve == 0x54) ? reflected() : cur
                path.addQuadCurve(to: p, control: c)
                newControl = c
                cur = p
            case 0x41:  // A
                guard let rx = number(), let ry = number(), let rotation = number(), let large = flag(),
                      let sweep = flag(), let p = point(relative: relative) else { return false }
                arc(to: p, rx: rx, ry: ry, rotation: rotation * .pi / 180, large: large, sweep: sweep)
                cur = p
            default:
                return false
            }
            control = newControl
            lastCurve = upper
            return true
        }

        func reflected() -> CGPoint {
            guard let c = control else { return cur }
            return CGPoint(x: 2 * cur.x - c.x, y: 2 * cur.y - c.y)
        }

        /// An elliptical arc from the current point, as cubic curves of at most a quarter turn (SVG 1.1, F.6.5).
        mutating func arc(to end: CGPoint, rx: CGFloat, ry: CGFloat, rotation phi: CGFloat, large: Bool, sweep: Bool) {
            if cur == end { return }
            var rx = abs(rx), ry = abs(ry)
            if rx == 0 || ry == 0 {
                path.addLine(to: end)
                return
            }
            let (cosP, sinP) = (cos(phi), sin(phi))
            let dx = (cur.x - end.x) / 2, dy = (cur.y - end.y) / 2
            let x1 = cosP * dx + sinP * dy, y1 = -sinP * dx + cosP * dy
            // Radii too small to reach the end point are scaled up to just reach it.
            let lambda = x1 * x1 / (rx * rx) + y1 * y1 / (ry * ry)
            if lambda > 1 {
                rx *= lambda.squareRoot()
                ry *= lambda.squareRoot()
            }
            let num = rx * rx * ry * ry - rx * rx * y1 * y1 - ry * ry * x1 * x1
            let den = rx * rx * y1 * y1 + ry * ry * x1 * x1
            let k = (large == sweep ? -1 : 1) * max(0, num / den).squareRoot()
            let cx1 = k * rx * y1 / ry, cy1 = -k * ry * x1 / rx
            let cx = cosP * cx1 - sinP * cy1 + (cur.x + end.x) / 2
            let cy = sinP * cx1 + cosP * cy1 + (cur.y + end.y) / 2
            let theta = atan2((y1 - cy1) / ry, (x1 - cx1) / rx)
            var delta = atan2((-y1 - cy1) / ry, (-x1 - cx1) / rx) - theta
            if sweep, delta < 0 { delta += 2 * .pi } else if !sweep, delta > 0 { delta -= 2 * .pi }
            let n = max(1, Int((abs(delta) / (.pi / 2)).rounded(.up)))
            let step = delta / CGFloat(n)
            let t = 4 / 3 * tan(step / 4)
            func at(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
                CGPoint(x: cx + rx * x * cosP - ry * y * sinP, y: cy + rx * x * sinP + ry * y * cosP)
            }
            var a = theta
            for _ in 0..<n {
                let b = a + step
                path.addCurve(
                    to: at(cos(b), sin(b)),
                    control1: at(cos(a) - t * sin(a), sin(a) + t * cos(a)),
                    control2: at(cos(b) + t * sin(b), sin(b) - t * cos(b)))
                a = b
            }
        }
    }
}
