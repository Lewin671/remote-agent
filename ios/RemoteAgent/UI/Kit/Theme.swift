// The design tokens of docs/design-system.md: colours (§2), radii and
// spacing (§4). The values are the ones at the top of the mockup
// (docs/design/client-v38.html, :root and [data-theme="dark"]) and in
// Android's ui/Theme.kt; what each colour is for is in the design system.
//
// There is no asset catalogue behind them: a colour is one dynamic UIColor
// that resolves by the trait collection, so sheets, covers and UIKit views
// follow the theme without anything handing a palette down.
import SwiftUI
import UIKit

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

/// The colours of one theme, by the names the design system gives them.
struct Palette: Sendable, Equatable {
    var bg, fg, fg2, muted, faint, line, card, soft, chip, seg, track: RGBA
    var blue, blueBg, blueSoft: RGBA
    var orange, orangeInk, orangeSoft: RGBA
    var green, greenInk: RGBA
    var red, redBg, redSoft: RGBA
    var gray: RGBA

    static let light = Palette(
        bg: RGBA(0xEEEEEC), fg: RGBA(0x16181C), fg2: RGBA(0x3A3F46), muted: RGBA(0x626970), faint: RGBA(0x9AA0A7),
        line: RGBA(0xE6E8EB), card: RGBA(0xFFFFFF), soft: RGBA(0xF5F5F7), chip: RGBA(0xEFF0F2), seg: RGBA(0xECEEF0),
        track: RGBA(0xD9DCE0),
        blue: RGBA(0x2563D9), blueBg: RGBA(0x2F6FEB), blueSoft: RGBA(0xEDF3FF),
        orange: RGBA(0xE07A12), orangeInk: RGBA(0x8A4A06), orangeSoft: RGBA(0xFFF3E3),
        green: RGBA(0x22A05A), greenInk: RGBA(0x177A43),
        red: RGBA(0xC62F2F), redBg: RGBA(0xC62F2F), redSoft: RGBA(0xFDECEC),
        gray: RGBA(0xB3B9C0))

    static let dark = Palette(
        bg: RGBA(0x0B0C0E), fg: RGBA(0xE8EAED), fg2: RGBA(0xC9CED4), muted: RGBA(0xA0A7AF), faint: RGBA(0x6B727A),
        line: RGBA(0x2A2D32), card: RGBA(0x17181B), soft: RGBA(0x1F2125), chip: RGBA(0x272A2F), seg: RGBA(0x212327),
        track: RGBA(0x3A3E44),
        blue: RGBA(0x7AA7FF), blueBg: RGBA(0x2F6FEB), blueSoft: RGBA(0x5A91FF, alpha: 0.16),
        orange: RGBA(0xF0A04B), orangeInk: RGBA(0xF3BD82), orangeSoft: RGBA(0xF0A04B, alpha: 0.14),
        green: RGBA(0x3FBF73), greenInk: RGBA(0x5FD08B),
        red: RGBA(0xFF7B7B), redBg: RGBA(0xC62F2F), redSoft: RGBA(0xFF6B6B, alpha: 0.14),
        gray: RGBA(0x555B62))
}

/// What a status dot says (design-system §2): running, waiting for you, idle, or ended / offline / unknown.
enum StatusTone: Sendable, CaseIterable { case run, wait, idle, off }

enum Theme {
    private static func dynamic(_ key: KeyPath<Palette, RGBA>) -> Color {
        let (light, dark) = (Palette.light[keyPath: key], Palette.dark[keyPath: key])
        return Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? dark.uiColor : light.uiColor })
    }

    /// The page behind cards / cards and lists / secondary surfaces (fields, code, notices).
    static let bg = dynamic(\.bg), card = dynamic(\.card), soft = dynamic(\.soft)
    /// Secondary buttons, tags and inline code / progress tracks and the bar of a quote / a segmented control's groove.
    static let chip = dynamic(\.chip), track = dynamic(\.track), seg = dynamic(\.seg)
    /// Text / secondary text and code / metadata and captions / arrows and separators, never text to be read.
    static let fg = dynamic(\.fg), fg2 = dynamic(\.fg2), muted = dynamic(\.muted), faint = dynamic(\.faint)
    /// 1 pt separators and outlines.
    static let line = dynamic(\.line)
    /// What can be tapped, the chosen one, running (text) / the one solid main action / the chosen one's surface.
    static let blue = dynamic(\.blue), blueBg = dynamic(\.blueBg), blueSoft = dynamic(\.blueSoft)
    /// Needs you: the dot / the word / a tinted tag.
    static let orange = dynamic(\.orange), orangeInk = dynamic(\.orangeInk), orangeSoft = dynamic(\.orangeSoft)
    /// Idle, online, done: the dot / the word.
    static let green = dynamic(\.green), greenInk = dynamic(\.greenInk)
    /// Failed, or cannot be taken back (text) / the solid button of a confirmation / a tinted tag.
    static let red = dynamic(\.red), redBg = dynamic(\.redBg), redSoft = dynamic(\.redSoft)
    /// Ended, offline, unknown.
    static let gray = dynamic(\.gray)
    /// Text on a solid blue or red button.
    static let onSolid = Color.white
    /// Behind a dialog or a menu.
    static let scrim = Color.black.opacity(0.35)

    /// A status dot.
    static func dot(_ tone: StatusTone) -> Color {
        switch tone {
        case .run: blueBg
        case .wait: orange
        case .idle: green
        case .off: gray
        }
    }

    /// The status word next to a dot.
    static func ink(_ tone: StatusTone) -> Color {
        switch tone {
        case .run: blue
        case .wait: orangeInk
        case .idle: greenInk
        case .off: muted
        }
    }

    /// Corner radii (design-system §4). A pill is a `Capsule`.
    enum Radius {
        /// Inline code.
        static let code: CGFloat = 4
        /// Code blocks, tool output, image badges.
        static let xs: CGFloat = 6
        /// Buttons, fields, choices, notices, small cards.
        static let sm: CGFloat = 10
        /// Grouped lists, cards, the context box.
        static let md: CGFloat = 14
        /// Sheets and dialogs.
        static let lg: CGFloat = 20
    }

    /// Spacing in multiples of 4 (design-system §4).
    enum Space {
        static let xs: CGFloat = 4
        static let s: CGFloat = 8
        static let m: CGFloat = 12
        static let l: CGFloat = 16
        static let xl: CGFloat = 24
        /// A phone page's side margin.
        static let page: CGFloat = 16
    }

    /// The smallest side of anything that can be tapped.
    static let minHit: CGFloat = 44
    /// Settings-like pages keep a centred column of about this width on a wide screen.
    static let readableWidth: CGFloat = 672

    /// The terminal keeps its own dark colours in either theme.
    enum Term {
        static let bg = Color(uiColor: RGBA(0x0D1117).uiColor)
        static let bar = Color(uiColor: RGBA(0x161B22).uiColor)
        static let key = Color(uiColor: RGBA(0x2D333B).uiColor)
        static let fg = Color(uiColor: RGBA(0xE6EDF3).uiColor)
        static let text = Color(uiColor: RGBA(0xC9D1D9).uiColor)
        static let dim = Color(uiColor: RGBA(0x9DA7B3).uiColor)
        static let tool = Color(uiColor: RGBA(0x8B949E).uiColor)
        static let link = Color(uiColor: RGBA(0x79C0FF).uiColor)
        static let line = Color(uiColor: RGBA(0x30363D).uiColor)
        static let rule = Color(uiColor: RGBA(0x21262D).uiColor)
    }

    /// Project avatars: one fixed gradient per project, the same in both themes.
    static let avatarGradients: [LinearGradient] = [
        (0x5B8DEF, 0x3A63D8), (0xF0A35A, 0xE0772B), (0x6CC28A, 0x34A061), (0xB28BE8, 0x8A5FD6),
    ].map { pair in
        LinearGradient(
            colors: [Color(uiColor: RGBA(UInt32(pair.0)).uiColor), Color(uiColor: RGBA(UInt32(pair.1)).uiColor)],
            startPoint: .topLeading, endPoint: .bottomTrailing)
    }
    /// The grey avatar of 全部项目.
    static let avatarAll = LinearGradient(
        colors: [Color(uiColor: RGBA(0x8E99A6).uiColor), Color(uiColor: RGBA(0x66717E).uiColor)],
        startPoint: .topLeading, endPoint: .bottomTrailing)
}

/// Set on grey (soft) pages, where notices and code use the card surface instead (design-system §2).
private struct OnSoftKey: EnvironmentKey { static let defaultValue = false }

extension EnvironmentValues {
    var onSoft: Bool {
        get { self[OnSoftKey.self] }
        set { self[OnSoftKey.self] = newValue }
    }
}
