// The tokens of docs/design-system.md (client-v58) for the system-built
// interface: colours (§2), spacing and radii (§4). Each colour is named as
// the design system names it, so a rule there reads straight into code:
// `DS.ink2`, `DS.orangeInk`, `DS.gbg`.
//
// Where a system colour already means the same thing it is the system's:
// the page (`paper`) is systemBackground, the grey surface (`gbg`) is
// secondarySystemBackground and text (`ink`) is label, so they follow
// Increase Contrast and whatever the system does with them. The rest are
// the mockup's values (its `:root` and `[data-theme="dark"]`), because the
// system's are weaker than the 4.5:1 the design asks of text (secondaryLabel,
// systemBlue) or mean something else (systemGreen as a status).
//
// Type is the system's text styles, which are the design's sizes at the
// default Dynamic Type size and grow with it, and take Bold Text with them:
//
//   34 largeTitle · 22 title2 · 20 title3 · 17 body / headline (600)
//   15 subheadline · 13 footnote · 12 caption · 10 the tab labels (caption2)
//
// Weights by `.weight(_:)` or `.bold()`; code, paths and commands by
// `.monospaced()`. Icons are SF Symbols by the names in design-system §5.
import SwiftUI
import UIKit

enum DS {
    private static func dynamic(_ light: UInt32, _ dark: UInt32, alpha: (Double, Double) = (1, 1)) -> Color {
        Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? rgb(dark, alpha.1) : rgb(light, alpha.0) })
    }

    private static func rgb(_ hex: UInt32, _ alpha: Double) -> UIColor {
        UIColor(
            red: CGFloat(hex >> 16 & 0xff) / 255, green: CGFloat(hex >> 8 & 0xff) / 255, blue: CGFloat(hex & 0xff) / 255,
            alpha: alpha)
    }

    // Surfaces.

    /// The page: white, pure black in the dark.
    static let paper = Color(uiColor: .systemBackground)
    /// The grey surface on the page: cards, grouped lists, the list of what waits, notices.
    static let gbg = Color(uiColor: .secondarySystemBackground)
    /// The rounded lists on a sheet's `gbg`: white, one grey up from `gbg` in the dark.
    static let raised = dynamic(0xFFFFFF, 0x2C2C2E)
    /// Level-3 anchored panels and their half-point rim.
    static let popover = dynamic(0xFFFFFF, 0x2B2B30)
    static let rim = dynamic(0x000000, 0xFFFFFF, alpha: (0.08, 0.10))
    /// Fields, the ＋ circle, inline code.
    static let fill = dynamic(0x000000, 0xFFFFFF, alpha: (0.05, 0.09))
    /// Secondary buttons, a segmented control's groove, the chosen tab's ground, project avatars.
    static let fill2 = dynamic(0x000000, 0xFFFFFF, alpha: (0.09, 0.15))
    /// Your message's bubble.
    static let bubble = dynamic(0xF0F0F3, 0x2C2C2E)

    // Text and graphics.

    /// Text, titles, main lines.
    static let ink = Color(uiColor: .label)
    /// Second lines, metadata, status words, times. The only other colour for text to be read.
    static let ink2 = dynamic(0x6E6E73, 0xA1A1A6)
    /// Arrows, placeholders, the idle ring: graphics, never text to be read.
    static let ink3 = dynamic(0xA1A1A6, 0x6E6E73)
    /// The not-connected dot, thin outlines of waiting bubbles, a quote's bar.
    static let faint = dynamic(0xD2D2D7, 0x48484A)
    /// 1 pt separators.
    static let sep = dynamic(0x000000, 0xFFFFFF, alpha: (0.09, 0.12))

    // The one solid button of a screen.

    /// 墨色: near black, near white in the dark.
    static let btn = dynamic(0x1D1D1F, 0xF5F5F7)
    static let btnFg = dynamic(0xFFFFFF, 0x000000)

    // Colours that each mean one thing.

    /// What can be tapped as text, the chosen one's check, an unread result.
    static let blue = dynamic(0x0066CC, 0x4DA3FF)
    /// Waits for you: the dot, the number in the large title, badges.
    static let orange = dynamic(0xE06C00, 0xFF9F2E)
    /// Waits for you, as text: type words, reasons, the text of an orange notice.
    static let orangeInk = dynamic(0xB55A00, 0xFFB25C)
    /// The ground of a notice that wants you to act.
    static let orangeSoft = dynamic(0xFFF4E6, 0xFF9F2E, alpha: (1, 0.14))
    /// Online.
    static let green = dynamic(0x248A3D, 0x32D15D)
    /// Danger and full access; failures.
    static let red = dynamic(0xD70015, 0xFF5A50)

    // The dark card and the terminal, dark in both themes.

    /// The dark card's ground.
    static let tc = dynamic(0x1D1D1F, 0x242426)
    /// The dark card's outline, only in the dark, where its ground is near the page's.
    static let tcLine = dynamic(0x000000, 0xFFFFFF, alpha: (0, 0.14))
    /// The terminal's ground.
    static let term = dynamic(0x131315, 0x0D0D0F)

    /// Spacing, multiples of 4 (design-system §4).
    enum Space {
        static let xs: CGFloat = 4
        static let s: CGFloat = 8
        static let m: CGFloat = 12
        static let l: CGFloat = 16
        static let xl: CGFloat = 20
        static let xxl: CGFloat = 24
        /// A phone's lists and large titles keep this from the screen's edge.
        static let page: CGFloat = 24
        /// Cards and grouped lists keep this from the screen's edge.
        static let card: CGFloat = 16
    }

    /// Corner radii (design-system §4). A pill is a `Capsule`.
    enum Radius {
        /// Inline code, an image's badge.
        static let inline: CGFloat = 6
        /// Choices on a list, small buttons.
        static let small: CGFloat = 10
        static let code: CGFloat = 14
        static let command: CGFloat = 16
        static let notice: CGFloat = 18
        static let group: CGFloat = 20
        /// The list of what waits.
        static let pending: CGFloat = 24
        static let card: CGFloat = 28
        /// The dark card and dialogs.
        static let darkCard: CGFloat = 30
        /// A bottom sheet's top corners.
        static let sheet: CGFloat = 38
    }

    /// The smallest side of anything that can be tapped.
    static let minHit: CGFloat = 44
    /// The phone's widest column of content on a wide screen.
    static let column: CGFloat = 760
}

extension View {
    /// The shadow of what floats over the page: the dark card, the ink button, a dialog (`float`).
    func floatShadow() -> some View {
        modifier(FloatShadow())
    }
}

private struct FloatShadow: ViewModifier {
    @Environment(\.colorScheme) private var scheme

    func body(content: Content) -> some View {
        content.shadow(color: .black.opacity(scheme == .dark ? 0.6 : 0.18), radius: 20, y: 16)
    }
}

import RAMarkdownUI

extension DS {
    /// A reply's Markdown in the design's tokens and design-system §7's
    /// sizes: text 17, headings 20 / 17 / 15, inline code 15, code blocks
    /// and tables 13. One theme for both: the colours resolve by the scheme.
    static let markdown: MarkdownTheme = {
        var t = MarkdownTheme(
            text: DS.ink, secondary: DS.ink2, muted: DS.ink2, line: DS.sep, soft: DS.gbg, chip: DS.fill, track: DS.faint,
            link: DS.blue, added: DS.green, removed: DS.red)
        t.bodySize = 17
        t.bodyLineHeight = 1.55
        t.headingSizes = [20, 17, 15]
        t.inlineCodeSize = 15
        t.largeCodeSize = 17
        t.codeSize = 13
        t.tableHeadSize = 13
        t.tableCellSize = 13
        return t
    }()
}
