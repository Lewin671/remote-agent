// The type scale of docs/design-system.md §3: 30 / 22 / 17 / 15 / 13 / 12,
// and 11 for tab labels and badges. On Apple devices the design is drawn in
// the system fonts at their nominal weights (SF Pro, PingFang, SF Mono), so
// nothing is bundled and no weight is lowered as Android's platformWeight
// does.
//
// Each size follows Dynamic Type: it is the size at the default setting and
// grows with the text style it is tied to, up to the accessibility sizes.
import SwiftUI

/// One size of the scale, with what it takes to draw it.
struct TypeStyle: Sendable, Equatable {
    /// Points at the default Dynamic Type size.
    var size: CGFloat
    var weight: Font.Weight = .regular
    /// The mockup's line height as a multiple of the size (1.5 unless said).
    var lineHeight: CGFloat = 1.5
    /// Letter spacing in points at `size`.
    var tracking: CGFloat = 0
    /// The system text style whose scaling it follows.
    var relativeTo: Font.TextStyle
    var mono = false

    func with(weight: Font.Weight) -> TypeStyle {
        var s = self
        s.weight = weight
        return s
    }

    func with(lineHeight: CGFloat) -> TypeStyle {
        var s = self
        s.lineHeight = lineHeight
        return s
    }
}

/// The type scale.
enum T {
    /// A first-level page's large title: 设置, 加入账户.
    static let title30 = TypeStyle(size: 30, weight: .bold, lineHeight: 1.2, tracking: -0.5, relativeTo: .largeTitle)
    /// The home page's scope title, one large number, a pairing code.
    static let h22 = TypeStyle(size: 22, weight: .bold, lineHeight: 1.3, tracking: -0.3, relativeTo: .title2)
    /// Sheet and dialog titles, a full-page question, a reply's first and second level headings.
    static let h17 = TypeStyle(size: 17, weight: .semibold, lineHeight: 1.35, relativeTo: .headline)
    /// Text, a list's main line, fields.
    static let body = TypeStyle(size: 15, relativeTo: .subheadline)
    /// Buttons, a row's title that has a description under it.
    static let bodyB = TypeStyle(size: 15, weight: .semibold, relativeTo: .subheadline)
    /// A reply's text, at the looser line height.
    static let chat = TypeStyle(size: 15, lineHeight: 1.6, relativeTo: .subheadline)
    /// A list's second line, notices, link-like actions.
    static let sm = TypeStyle(size: 13, relativeTo: .footnote)
    /// Section labels, text actions.
    static let smB = TypeStyle(size: 13, weight: .semibold, relativeTo: .footnote)
    /// Metadata, times, captions.
    static let meta = TypeStyle(size: 12, relativeTo: .caption)
    static let metaB = TypeStyle(size: 12, weight: .semibold, relativeTo: .caption)
    static let metaM = TypeStyle(size: 12, weight: .medium, relativeTo: .caption)
    /// Tab bar labels and image badges only.
    static let tab = TypeStyle(size: 11, relativeTo: .caption2)
    /// Code, paths and commands.
    static let mono = TypeStyle(size: 12, relativeTo: .caption, mono: true)
    /// Inline code and paths inside a reply.
    static let mono13 = TypeStyle(size: 13, relativeTo: .footnote, mono: true)
    /// A pairing code, checked digit by digit: the one larger mono size.
    static let code22 = TypeStyle(size: 22, weight: .semibold, lineHeight: 1.3, tracking: 4, relativeTo: .title2, mono: true)
}

/// The system font draws a line at about this multiple of its size; the rest of a style's line height is added between lines.
private let naturalLineHeight: CGFloat = 1.2

private struct Typed: ViewModifier {
    let style: TypeStyle
    let color: Color?
    @ScaledMetric private var size: CGFloat

    init(style: TypeStyle, color: Color?) {
        self.style = style
        self.color = color
        _size = ScaledMetric(wrappedValue: style.size, relativeTo: style.relativeTo)
    }

    func body(content: Content) -> some View {
        content
            .font(.system(size: size, weight: style.weight, design: style.mono ? .monospaced : .default))
            .tracking(style.tracking * size / style.size)
            .lineSpacing(max(0, size * (style.lineHeight - naturalLineHeight)))
            .ink(color)
    }
}

extension View {
    /// The foreground colour, or the surrounding one when there is none to set.
    @ViewBuilder func ink(_ color: Color?) -> some View {
        if let color { foregroundStyle(color) } else { self }
    }

    /// Draws the text inside in a size of the scale, optionally in a colour of the theme.
    func type(_ style: TypeStyle, _ color: Color? = nil) -> some View {
        modifier(Typed(style: style, color: color))
    }
}

/// A length that grows with Dynamic Type as text in `style` does: an inline icon, a dot's offset.
struct ScaledLength: DynamicProperty {
    @ScaledMetric private var value: CGFloat

    init(_ value: CGFloat, relativeTo style: Font.TextStyle) {
        _value = ScaledMetric(wrappedValue: value, relativeTo: style)
    }

    var wrappedValue: CGFloat { value }
}
