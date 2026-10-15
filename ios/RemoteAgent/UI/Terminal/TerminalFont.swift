// The terminal's font and its cell (Android's term/TerminalFont.kt).
//
// The system's monospaced font, SF Mono: nothing is bundled. What it lacks
// (Chinese, the ⏵ ⎿ ✻ of Claude Code, Braille spinners) Core Text takes from
// the system's other fonts, PingFang for Chinese, so no fallback chain is
// named here as Android has to.
//
// Sizes are counted in device pixels, as Android counts them, so that the
// search for the size at which a remote grid fits goes pixel by pixel. The
// cell is measured as SwiftTerm measures it (`computeFontDimensions`): the
// advance of a W, and the font's ascent, descent and leading rounded up to
// whole points. A size other than the emulator's font is that font's cell
// scaled, because that is how `TerminalHostView` shows it.
import CoreText
import RACore
import UIKit

@MainActor
enum TerminalFont {
    /// The size in points a terminal opens at, and what 字号 can be set to.
    /// The smallest size is the floor the fitting search stops at, so a size
    /// 字号 offers is always a size the screen can be shown at.
    static let standard = 14
    static let sizes = terminalFontFloor...22
    /// The size last chosen on this device.
    static let key = "terminal-display.font"

    static var preferred: Int {
        get {
            let stored = UserDefaults.standard.integer(forKey: key)
            return stored == 0 ? standard : min(max(stored, sizes.lowerBound), sizes.upperBound)
        }
        set { UserDefaults.standard.set(newValue, forKey: key) }
    }

    static func font(points: CGFloat) -> UIFont {
        .monospacedSystemFont(ofSize: points, weight: .regular)
    }

    private static var measured: [CGFloat: CGSize] = [:]

    /// The cell of the font of a size, in points.
    static func cell(points: CGFloat) -> CGSize {
        if let c = measured[points] { return c }
        let font = font(points: points)
        let height = ceil(CTFontGetAscent(font) + CTFontGetDescent(font) + CTFontGetLeading(font))
        let width = max(1, ("W" as NSString).size(withAttributes: [.font: font]).width)
        let c = CGSize(width: width, height: max(1, height))
        measured[points] = c
        return c
    }

    /// The cell, in pixels, of the font of `base` points shown at `pixels`: its own cell at its own size, scaled at any other.
    static func geometry(pixels: Int, scale: CGFloat, base: CGFloat) -> TerminalGeometry {
        let cell = cell(points: base)
        let zoom = CGFloat(pixels) / scale / base
        return TerminalGeometry(cellWidth: Double(cell.width * zoom * scale), lineHeight: Double(cell.height * zoom * scale))
    }
}
