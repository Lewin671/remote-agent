// How a reply's Markdown looks: docs/design-system.md §7. The numbers here
// are that section's; the app passes its own theme when its palette differs.
import SwiftUI

/// Colors, sizes and words for `MarkdownView`. Sizes are in points at the
/// default text size and grow with Dynamic Type; distances do not, as on the
/// other clients.
public struct MarkdownTheme: Equatable, Sendable {
    /// `fg`: a reply's text.
    public var text: Color
    /// `fg2`: quotes, code, refs and commentary.
    public var secondary: Color
    /// `muted`: markers, table heads, small headings, struck and finished text.
    public var muted: Color
    /// `line`: between a table's rows.
    public var line: Color
    /// `soft`: behind a code block.
    public var soft: Color
    /// `chip`: behind inline code.
    public var chip: Color
    /// `track`: a quote's bar.
    public var track: Color
    /// `blue`: links and "展开全文".
    public var link: Color
    /// `green-ink` and `red`: a diff's added and removed lines.
    public var added: Color
    public var removed: Color

    public var bodySize: CGFloat = 15
    public var bodyLineHeight: CGFloat = 1.6
    /// Headings of level 1–2, 3, and 4–6.
    public var headingSizes: [CGFloat] = [17, 15, 13]
    public var headingLineHeight: CGFloat = 1.35
    /// Inline code and refs; in the two large headings code is `largeCodeSize`.
    public var inlineCodeSize: CGFloat = 13
    public var largeCodeSize: CGFloat = 15
    public var codeSize: CGFloat = 12
    public var codeLineHeight: CGFloat = 1.5
    public var tableHeadSize: CGFloat = 12
    public var tableCellSize: CGFloat = 13
    public var tableLineHeight: CGFloat = 1.45

    /// A table cell is never narrower or wider than this; longer text wraps.
    public var tableCellMinWidth: CGFloat = 34
    public var tableCellMaxWidth: CGFloat = 240
    /// A column of a table that is a little too wide is not narrowed below this. Grows with the text size.
    public var tableReadableWidth: CGFloat = 96
    public var tableColumnGap: CGFloat = 24
    public var tableRowPadding: CGFloat = 8

    public var strings = Strings()

    /// What the renderer says itself. The app's language is Chinese, so are the defaults.
    public struct Strings: Equatable, Sendable {
        public var expand = "展开全文"
        public var collapse = "收起"
        /// `%d` is the number of lines in the block.
        public var lineCount = "共 %d 行"
        public var copy = "复制代码"
        public var copied = "已复制"
        public var copyFailed = "复制失败"
        public var taskDone = "已完成"
        public var taskTodo = "未完成"

        public init() {}
    }

    public init(
        text: Color, secondary: Color, muted: Color, line: Color, soft: Color, chip: Color, track: Color,
        link: Color, added: Color, removed: Color
    ) {
        self.text = text
        self.secondary = secondary
        self.muted = muted
        self.line = line
        self.soft = soft
        self.chip = chip
        self.track = track
        self.link = link
        self.added = added
        self.removed = removed
    }

    /// The light palette of the design (client-v38.html `:root`).
    public static let light = MarkdownTheme(
        text: Color(hex: 0x16181C), secondary: Color(hex: 0x3A3F46), muted: Color(hex: 0x626970), line: Color(hex: 0xE6E8EB),
        soft: Color(hex: 0xF5F5F7), chip: Color(hex: 0xEFF0F2), track: Color(hex: 0xD9DCE0),
        link: Color(hex: 0x2563D9), added: Color(hex: 0x177A43), removed: Color(hex: 0xC62F2F))

    /// The dark palette of the design (`[data-theme="dark"]`).
    public static let dark = MarkdownTheme(
        text: Color(hex: 0xE8EAED), secondary: Color(hex: 0xC9CED4), muted: Color(hex: 0xA0A7AF), line: Color(hex: 0x2A2D32),
        soft: Color(hex: 0x1F2125), chip: Color(hex: 0x272A2F), track: Color(hex: 0x3A3E44),
        link: Color(hex: 0x7AA7FF), added: Color(hex: 0x5FD08B), removed: Color(hex: 0xFF7B7B))

    public static func standard(for scheme: ColorScheme) -> MarkdownTheme {
        scheme == .dark ? dark : light
    }
}

extension Color {
    fileprivate init(hex: UInt32) {
        self.init(
            .sRGB, red: Double((hex >> 16) & 0xFF) / 255, green: Double((hex >> 8) & 0xFF) / 255, blue: Double(hex & 0xFF) / 255, opacity: 1)
    }
}

/// Which of the theme's text colors a run of blocks is set in.
public enum MarkdownTone: Hashable, Sendable {
    /// A reply.
    case primary
    /// Commentary written while working, and whatever a quote holds.
    case secondary
    /// What is under a finished task.
    case muted
}

/// Everything the drawing depends on besides the Markdown itself. It is
/// read from the environment once, by `MarkdownView`, and handed down as a
/// value: a block's view then depends on nothing but what it is given, and
/// comparing that is enough to know whether it must be drawn again.
struct Look: Equatable {
    var theme: MarkdownTheme
    /// Dynamic Type's factor on every text size: 1 at the default size.
    var scale: CGFloat
}

extension MarkdownTheme {
    func color(_ tone: MarkdownTone) -> Color {
        switch tone {
        case .primary: text
        case .secondary: secondary
        case .muted: muted
        }
    }
}

extension EnvironmentValues {
    /// The theme `MarkdownView` draws with; nil is the design's own, light or dark with the color scheme.
    @Entry public var markdownTheme: MarkdownTheme?
}

extension View {
    /// Sets the theme of every `MarkdownView` below.
    public func markdownTheme(_ theme: MarkdownTheme?) -> some View {
        environment(\.markdownTheme, theme)
    }
}
