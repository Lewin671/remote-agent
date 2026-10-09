// An agent's reply, drawn: docs/design-system.md §7. The parser is
// RAMarkdown; Android's ui/Markdown.kt draws the same blocks the same way.
//
// A reply grows token by token. Every block's view is identified by where
// the block stands among its siblings and compares equal while the block
// does, so a new token redraws the block it lands in and none before it.
import RAMarkdown
import SwiftUI

/// An agent's reply as Markdown. Text can be selected, and only http(s)
/// links can be opened.
public struct MarkdownView: View {
    private let blocks: [MarkdownBlock]
    private let tone: MarkdownTone
    private let bleed: CGFloat

    @Environment(\.markdownTheme) private var custom
    @Environment(\.colorScheme) private var scheme
    @Environment(\.openURL) private var openURL
    // The design's sizes are fixed in points, so they are scaled by hand: with the body text, which is what a reply is.
    @ScaledMetric(relativeTo: .body) private var scale: CGFloat = 1

    /// Parses `text` each time the view is made: linear in the text, but a
    /// caller that redraws for other reasons does better to keep
    /// `Markdown.parse`'s result and pass it to `init(blocks:)`.
    public init(_ text: String, tone: MarkdownTone = .primary, bleed: CGFloat = 0) {
        self.init(blocks: Markdown.parse(text), tone: tone, bleed: bleed)
    }

    /// `bleed` is the padding the caller keeps at the reply's sides: a table
    /// too wide for the reply scrolls across it, to the edge of the screen.
    public init(blocks: [MarkdownBlock], tone: MarkdownTone = .primary, bleed: CGFloat = 0) {
        self.blocks = blocks
        self.tone = tone
        self.bleed = bleed
    }

    public var body: some View {
        let outer = openURL
        BlockStack(blocks: blocks, place: .reply, tone: tone, bleed: bleed, look: Look(theme: custom ?? .standard(for: scheme), scale: scale))
            .equatable()
            // The second lock on the same door: whatever a link's text came to hold, only a web address leaves the app.
            .environment(\.openURL, OpenURLAction { url in
                guard isWebLink(url) else { return .discarded }
                outer(url)
                return .handled
            })
            .textSelection(.enabled)
    }
}

/// A run of blocks with §7's space between them. `level` counts the lists around.
struct BlockStack: View, Equatable {
    let blocks: [MarkdownBlock]
    let place: MarkdownPlace
    var afterText = false
    let tone: MarkdownTone
    var level = 0
    var bleed: CGFloat = 0
    let look: Look

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Markdown.spaced(blocks, in: place, afterText: afterText), id: \.index) { row in
                BlockView(block: row.block, tone: tone, level: level, bleed: place == .reply ? bleed : 0, look: look)
                    .equatable()
                    .padding(.top, CGFloat(row.gap))
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct BlockView: View, Equatable {
    let block: MarkdownBlock
    let tone: MarkdownTone
    let level: Int
    let bleed: CGFloat
    let look: Look

    var body: some View {
        let theme = look.theme
        switch block {
        case .paragraph(let text):
            InlineText(source: text, lines: true, spec: theme.body(tone), look: look)
        case .heading(let level, let text):
            InlineText(source: text, spec: theme.heading(level, tone), look: look)
                .accessibilityAddTraits(.isHeader)
        case .code(let lang, let text):
            CodeBlockView(lang: lang, text: text, look: look).equatable()
        case .quote(let blocks):
            // The bar is drawn over the padding, so it is as tall as what it quotes.
            BlockStack(blocks: blocks, place: .quote, tone: .secondary, level: level, look: look)
                .padding(.leading, 14)
                .overlay(alignment: .leading) { Rectangle().fill(theme.track).frame(width: 2) }
        case .list(let ordered, let start, let items):
            ListView(ordered: ordered, start: start, items: items, tone: tone, level: level, look: look)
        case .table(let head, let align, let rows):
            TableView(head: head, align: align, rows: rows, tone: tone, bleed: bleed, look: look).equatable()
        case .rule:
            // Not drawn: `Markdown.spaced` turned it into space.
            EmptyView()
        }
    }
}

extension MarkdownTheme {
    func body(_ tone: MarkdownTone) -> TextSpec {
        TextSpec(size: bodySize, lineHeight: bodyLineHeight, color: color(tone), codeSize: inlineCodeSize)
    }

    func heading(_ level: Int, _ tone: MarkdownTone) -> TextSpec {
        let large = level <= 2
        let size = headingSizes[large ? 0 : level == 3 ? 1 : 2]
        return TextSpec(
            size: size, weight: .semibold, lineHeight: headingLineHeight, color: level <= 3 ? color(tone) : muted,
            codeSize: large ? largeCodeSize : inlineCodeSize)
    }
}

private struct ListView: View {
    let ordered: Bool
    let start: Int
    let items: [MarkdownItem]
    let tone: MarkdownTone
    let level: Int
    let look: Look

    private var theme: MarkdownTheme { look.theme }
    private var scale: CGFloat { look.scale }

    var body: some View {
        // Two-digit numbers need a wider gutter, or they run into the text.
        let gutter: CGFloat = ordered && start + items.count - 1 >= 10 ? 28 : 20
        let loose = Markdown.looseList(items.map(\.text))
        VStack(alignment: .leading, spacing: loose ? 8 : 4) {
            ForEach(Array(items.enumerated()), id: \.offset) { n, item in
                // A finished task is muted with everything under it.
                let ink: MarkdownTone = item.task == .done ? .muted : tone
                HStack(alignment: .top, spacing: 0) {
                    marker(n, item)
                        // As high as the first line, so the marker sits beside it at any text size.
                        .frame(width: gutter, height: theme.bodySize * theme.bodyLineHeight * scale, alignment: item.task != nil ? .leading : .center)
                    VStack(alignment: .leading, spacing: 0) {
                        if !item.text.isEmpty { InlineText(source: item.text, lines: true, spec: theme.body(ink), look: look).equatable() }
                        BlockStack(blocks: item.children, place: .item, afterText: !item.text.isEmpty, tone: ink, level: level + 1, look: look).equatable()
                    }
                }
            }
        }
    }

    @ViewBuilder private func marker(_ n: Int, _ item: MarkdownItem) -> some View {
        if let task = item.task {
            IconView(
                icon: task == .done ? .boxChecked : .box, size: theme.bodySize * scale, color: theme.muted,
                label: task == .done ? theme.strings.taskDone : theme.strings.taskTodo)
        } else if ordered {
            Text(verbatim: "\(start + n).")
                .font(.system(size: theme.bodySize * scale))
                .monospacedDigit()
                .foregroundStyle(theme.muted)
                .lineLimit(1)
                // The line box of the text beside it, so the two share a baseline.
                .lineBox(height: theme.bodySize * theme.bodyLineHeight * scale, fontSize: theme.bodySize * scale)
                // At a large text size the number grows to the left, away from the text.
                .fixedSize()
                .frame(width: 0, alignment: .trailing)
                .padding(.trailing, 6)
                .frame(maxWidth: .infinity, alignment: .trailing)
                .textSelection(.disabled)
        } else {
            // Fonts for CJK draw • as a small centered dot, so the disc is drawn; from the second level it is a ring.
            Group {
                if level == 0 {
                    Circle().fill(theme.muted)
                } else {
                    Circle().strokeBorder(theme.muted, lineWidth: 1)
                }
            }
            .frame(width: 5, height: 5)
            .accessibilityHidden(true)
        }
    }
}
