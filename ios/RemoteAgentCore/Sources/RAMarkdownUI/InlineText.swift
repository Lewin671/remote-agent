// A block's inline Markdown as one styled Text.
import RAMarkdown
import SwiftUI

/// How one run of text is set: sizes are in points at the default text size.
struct TextSpec: Equatable {
    var size: CGFloat
    var weight: Font.Weight = .regular
    /// A multiple of `size`.
    var lineHeight: CGFloat
    var color: Color
    var codeSize: CGFloat
    var alignment: TextAlignment = .leading
    /// Digits of one width, for a column of quantities.
    var tabular = false
}

/// Marks inline code, so that `ChipRenderer` finds where to draw its chip. `id` tells two code spans apart.
struct CodeChip: TextAttribute {
    let id: Int
}

/// Draws inline code on a rounded chip (§7: `chip`, radius 4, 4 at the
/// sides): a background attribute would fill the whole line height with
/// square corners. The chip is drawn behind the text and takes no room of
/// its own, so its sides reach into the space around the code, and code that
/// wraps gets a chip on each line.
@available(iOS 18.0, macOS 15.0, *)
struct ChipRenderer: TextRenderer {
    let color: Color
    /// Half the chip's height: 0.72 of the code's size, as on the other clients.
    let half: CGFloat

    var displayPadding: EdgeInsets { EdgeInsets(top: 0, leading: 4, bottom: 0, trailing: 4) }

    func draw(layout: Text.Layout, in context: inout GraphicsContext) {
        for line in layout {
            // One code span can be several runs, where a second font draws part of it.
            var open: (id: Int, rect: CGRect)?
            func close() {
                guard let r = open?.rect else { return }
                let chip = CGRect(x: r.minX - 4, y: r.midY - half, width: r.width + 8, height: half * 2)
                context.fill(Path(roundedRect: chip, cornerRadius: 4), with: .color(color))
            }
            for run in line {
                guard let chip = run[CodeChip.self] else {
                    close()
                    open = nil
                    continue
                }
                let rect = run.typographicBounds.rect
                if let o = open, o.id == chip.id {
                    open = (chip.id, o.rect.union(rect))
                } else {
                    close()
                    open = (chip.id, rect)
                }
            }
            close()
        }
        for line in layout { context.draw(line) }
    }
}

/// Builds the styled text of a block from its spans.
private struct RichBuilder {
    enum Piece {
        case plain(AttributedString)
        case code(AttributedString)
    }

    /// What the spans around a run have decided about it.
    struct Style {
        var weight: Font.Weight
        var italic = false
        var struck = false
        var color: Color
    }

    let spec: TextSpec
    let theme: MarkdownTheme
    /// Dynamic Type's factor on every size.
    let scale: CGFloat
    /// Whether code is left for `ChipRenderer` to put a chip behind.
    let chips: Bool
    var pieces: [Piece] = []

    private func font(_ size: CGFloat, _ style: Style, mono: Bool = false) -> Font {
        let f = Font.system(size: size * scale, weight: style.weight, design: mono ? .monospaced : .default)
        return style.italic ? f.italic() : f
    }

    private func run(_ text: String, _ font: Font, _ style: Style, color: Color? = nil) -> AttributedString {
        var a = AttributedString(text)
        a.swiftUI.font = font
        a.swiftUI.foregroundColor = color ?? style.color
        if style.struck { a.swiftUI.strikethroughStyle = .single }
        return a
    }

    private mutating func plain(_ a: AttributedString) {
        if case .plain(let last) = pieces.last {
            pieces[pieces.count - 1] = .plain(last + a)
        } else {
            pieces.append(.plain(a))
        }
    }

    mutating func lineBreak() {
        plain(run("\n", font(spec.size, Style(weight: spec.weight, color: spec.color)), Style(weight: spec.weight, color: spec.color)))
    }

    mutating func append(_ spans: [MarkdownSpan], _ style: Style) {
        for s in spans {
            switch s {
            case .text(let v): plain(run(v, font(spec.size, style), style))
            case .code(let v):
                var a = run(v, font(spec.codeSize, style, mono: true), style)
                if chips {
                    pieces.append(.code(a))
                } else {
                    a.swiftUI.backgroundColor = theme.chip
                    plain(a)
                }
            case .bold(let c):
                var inner = style
                inner.weight = .semibold
                append(c, inner)
            case .italic(let c):
                var inner = style
                inner.italic = true
                append(c, inner)
            case .strike(let c):
                var inner = style
                inner.struck = true
                inner.color = theme.muted
                append(c, inner)
            case .link(let v, let href):
                var a = run(v, font(spec.size, style), style, color: theme.link)
                // The parser let only http(s) through; one Foundation cannot read stays text.
                if let url = URL(string: href), isWebLink(url) { a.link = url }
                plain(a)
            // A target that cannot be opened, mostly a local path: shown as a path, never a link.
            case .ref(let v, _): plain(run(v, font(theme.inlineCodeSize, style, mono: true), style, color: theme.secondary))
            }
        }
    }

    var hasChips: Bool {
        pieces.contains { if case .code = $0 { true } else { false } }
    }

    @available(iOS 18.0, macOS 15.0, *)
    var chipped: Text {
        var out = Text(verbatim: "")
        for (i, p) in pieces.enumerated() {
            switch p {
            case .plain(let a): out = out + Text(a)
            case .code(let a): out = out + Text(a).customAttribute(CodeChip(id: i))
            }
        }
        return out
    }

    var joined: Text {
        var all = AttributedString()
        for p in pieces {
            switch p {
            case .plain(let a), .code(let a): all += a
            }
        }
        return Text(all)
    }
}

/// Whether a URL may leave the app: the same rule as `Markdown.safeHref`, asked again where the link is opened.
func isWebLink(_ url: URL) -> Bool {
    let scheme = url.scheme?.lowercased()
    return scheme == "https" || scheme == "http"
}

extension View {
    /// Sets lines `height` apart. Before the system could do that, the space
    /// between lines is widened instead, with half of it above the first and
    /// below the last line, taking a line of the system font as 1.2 of its size.
    @ViewBuilder func lineBox(height: CGFloat, fontSize: CGFloat) -> some View {
        if #available(iOS 26.0, macOS 26.0, *) {
            lineHeight(.exact(points: height))
        } else {
            let extra = max(0, height - fontSize * 1.2)
            lineSpacing(extra).padding(.vertical, extra / 2)
        }
    }
}

/// Inline Markdown as text. `lines` keeps the single line breaks of a paragraph or of a cell's `<br>`; a heading has none.
struct InlineText: View, Equatable {
    let source: String
    var lines = false
    let spec: TextSpec
    let look: Look

    private var theme: MarkdownTheme { look.theme }
    private var scale: CGFloat { look.scale }

    private func build(chips: Bool) -> RichBuilder {
        var b = RichBuilder(spec: spec, theme: theme, scale: scale, chips: chips)
        let style = RichBuilder.Style(weight: spec.weight, color: spec.color)
        if lines {
            for (i, line) in source.split(separator: "\n", omittingEmptySubsequences: false).enumerated() {
                if i > 0 { b.lineBreak() }
                b.append(Markdown.inlineSpans(String(line)), style)
            }
        } else {
            b.append(Markdown.inlineSpans(source), style)
        }
        return b
    }

    private func set(_ text: Text) -> some View {
        text
            .multilineTextAlignment(spec.alignment)
            .monospacedDigit(spec.tabular)
            .lineBox(height: spec.size * spec.lineHeight * scale, fontSize: spec.size * scale)
            .tint(theme.link)
            // Text takes the lines it needs, whatever height it is offered: a long word or a run of CJK wraps, nothing is cut.
            .fixedSize(horizontal: false, vertical: true)
    }

    var body: some View {
        if #available(iOS 18.0, macOS 15.0, *) {
            let b = build(chips: true)
            if b.hasChips {
                set(b.chipped).textRenderer(ChipRenderer(color: theme.chip, half: spec.codeSize * scale * 0.72))
            } else {
                set(b.joined)
            }
        } else {
            set(build(chips: false).joined)
        }
    }
}

extension View {
    @ViewBuilder fileprivate func monospacedDigit(_ on: Bool) -> some View {
        if on { monospacedDigit() } else { self }
    }
}
