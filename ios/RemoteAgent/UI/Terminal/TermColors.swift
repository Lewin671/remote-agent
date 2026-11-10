// The terminal's own dark colours, in either theme: the emulator draws its
// screen on `bg`, and what frames it (the bars, the keys, the reader)
// matches it so the screen does not sit in a box of another black.
import SwiftUI
import UIKit

enum TermColors {
    static let bg = rgb(0x0D1117)
    static let bar = rgb(0x161B22)
    static let key = rgb(0x2D333B)
    static let fg = rgb(0xE6EDF3)
    static let text = rgb(0xC9D1D9)
    static let dim = rgb(0x9DA7B3)
    static let tool = rgb(0x8B949E)
    static let link = rgb(0x79C0FF)
    static let line = rgb(0x30363D)
    static let rule = rgb(0x21262D)

    private static func rgb(_ hex: UInt32) -> Color {
        Color(red: Double(hex >> 16 & 0xff) / 255, green: Double(hex >> 8 & 0xff) / 255, blue: Double(hex & 0xff) / 255)
    }
}

/// One colour as 8-bit sRGB with an alpha.
struct RGBA: Sendable, Equatable {
    var r: Double
    var g: Double
    var b: Double
    var a: Double

    /// `hex` is 0xRRGGBB.
    init(_ hex: UInt32, alpha: Double = 1) {
        r = Double(hex >> 16 & 0xff) / 255
        g = Double(hex >> 8 & 0xff) / 255
        b = Double(hex & 0xff) / 255
        a = alpha
    }

    var uiColor: UIColor { UIColor(red: r, green: g, blue: b, alpha: a) }

    /// Relative luminance (WCAG 2), ignoring alpha.
    var luminance: Double {
        func linear(_ c: Double) -> Double { c <= 0.03928 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4) }
        return 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b)
    }

    /// The WCAG contrast ratio against `other`, 1 to 21.
    func contrast(with other: RGBA) -> Double {
        let (hi, lo) = (max(luminance, other.luminance), min(luminance, other.luminance))
        return (hi + 0.05) / (lo + 0.05)
    }
}

