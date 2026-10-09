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
// whole points.
import CoreText
import RACore
import UIKit

@MainActor
enum TerminalFont {
    /// The size in points a terminal opens at, and what 字号 can be set to.
    static let standard = 14
    static let sizes = 10...22
    /// The size last chosen on this device.
    static let key = "terminal-display.font"

    static var preferred: Int {
        get {
            let stored = UserDefaults.standard.integer(forKey: key)
            return stored == 0 ? standard : min(max(stored, sizes.lowerBound), sizes.upperBound)
        }
        set { UserDefaults.standard.set(newValue, forKey: key) }
    }

    static func font(pixels: Int, scale: CGFloat) -> UIFont {
        .monospacedSystemFont(ofSize: CGFloat(pixels) / scale, weight: .regular)
    }

    private static var measured: [String: TerminalGeometry] = [:]

    /// A size's cell, in pixels.
    static func geometry(pixels: Int, scale: CGFloat) -> TerminalGeometry {
        let id = "\(pixels)@\(scale)"
        if let g = measured[id] { return g }
        let font = font(pixels: pixels, scale: scale)
        let height = ceil(CTFontGetAscent(font) + CTFontGetDescent(font) + CTFontGetLeading(font))
        let width = max(1, ("W" as NSString).size(withAttributes: [.font: font]).width)
        let g = TerminalGeometry(cellWidth: Double(width * scale), lineHeight: max(1, Int((height * scale).rounded())))
        measured[id] = g
        return g
    }
}
