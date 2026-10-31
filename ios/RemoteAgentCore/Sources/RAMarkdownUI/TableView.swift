import RAMarkdown
import SwiftUI

/// Where a cell's text stands in its column: 0 leading, 0.5 centered, 1 trailing.
private struct CellAlignment: LayoutValueKey {
    static let defaultValue: CGFloat = 0
}

/// A table's cells in rows, then the lines between its rows.
///
/// The subviews are `rows * columns` cells, row by row, followed by
/// `rows - 1` lines. A column is as wide as its widest cell, between
/// `minWidth` and `maxWidth`; a table a little wider than `viewport` has its
/// long columns narrowed by `Markdown.tableColumnWidths`, and one that still
/// does not fit keeps its widths for the scroll view around it. Every cell
/// of a column is then given the column's width, so rows line up, and a row
/// is as high as its highest cell.
struct TableLayout: Layout {
    let columns: Int
    let rows: Int
    /// The width a table should fit into when it can; infinite until it is known.
    let viewport: CGFloat
    let gap: CGFloat
    /// Above and below a row's text; the head has nothing above it.
    let pad: CGFloat
    let minWidth: CGFloat
    let maxWidth: CGFloat
    let readable: CGFloat

    struct Metrics {
        var widths: [CGFloat] = []
        var heights: [CGFloat] = []
        var size: CGSize = .zero
    }

    func makeCache(subviews: Subviews) -> Metrics? { nil }

    private func metrics(_ subviews: Subviews, _ cache: inout Metrics?) -> Metrics {
        if let cache { return cache }
        guard columns > 0, rows > 0, subviews.count >= rows * columns else { return Metrics() }
        // Whole points, as the other clients fit columns in whole pixels.
        let preferred = (0..<columns).map { ci -> Int in
            var widest: CGFloat = 0
            for ri in 0..<rows { widest = max(widest, subviews[ri * columns + ci].sizeThatFits(.unspecified).width) }
            return Int(min(max(widest, minWidth), maxWidth).rounded(.up))
        }
        let gaps = gap * CGFloat(columns - 1)
        let room = viewport.isFinite ? Int(max(0, viewport - gaps).rounded(.down)) : Int.max
        let widths = Markdown.tableColumnWidths(preferred: preferred, viewport: room, readable: Int(readable.rounded())).map { CGFloat($0) }
        let heights = (0..<rows).map { ri -> CGFloat in
            var highest: CGFloat = 0
            for ci in 0..<columns {
                highest = max(highest, subviews[ri * columns + ci].sizeThatFits(ProposedViewSize(width: widths[ci], height: nil)).height)
            }
            return highest + (ri == 0 ? pad : 2 * pad)
        }
        let m = Metrics(widths: widths, heights: heights, size: CGSize(width: widths.reduce(0, +) + gaps, height: heights.reduce(0, +)))
        cache = m
        return m
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout Metrics?) -> CGSize {
        metrics(subviews, &cache).size
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout Metrics?) {
        let m = metrics(subviews, &cache)
        guard !m.widths.isEmpty else { return }
        var y = bounds.minY
        for ri in 0..<rows {
            var x = bounds.minX
            for ci in 0..<columns {
                let cell = subviews[ri * columns + ci]
                let width = m.widths[ci]
                let size = cell.sizeThatFits(ProposedViewSize(width: width, height: nil))
                // Cells start at the top of their row.
                cell.place(
                    at: CGPoint(x: x + (width - size.width) * cell[CellAlignment.self], y: y + (ri == 0 ? 0 : pad)),
                    proposal: ProposedViewSize(width: width, height: nil))
                x += width + gap
            }
            y += m.heights[ri]
            // The line under a row, but not under the last.
            let line = rows * columns + ri
            if ri < rows - 1, line < subviews.count {
                subviews[line].place(at: CGPoint(x: bounds.minX, y: y - 1), proposal: ProposedViewSize(width: m.size.width, height: 1))
            }
        }
    }
}

/// A table: lines between the rows and nothing else, as wide as what it
/// holds. One too wide for the reply scrolls sideways, across `bleed` to the
/// edge of the screen, its trailing edge fading while there is more.
struct TableView: View, Equatable {
    let head: [String]
    let align: [MarkdownTableAlign?]
    let rows: [[String]]
    let tone: MarkdownTone
    let bleed: CGFloat
    let look: Look

    private var theme: MarkdownTheme { look.theme }
    private var scale: CGFloat { look.scale }
    /// The width the reply has for the table, measured from the scroll view once it is laid out.
    @State private var viewport = CGFloat.infinity
    @State private var more = false

    nonisolated static func == (a: TableView, b: TableView) -> Bool {
        a.head == b.head && a.align == b.align && a.rows == b.rows && a.tone == b.tone && a.bleed == b.bleed && a.look == b.look
    }

    var body: some View {
        let columns = head.count
        let all = [head] + rows
        // The separator row's alignment wins; without one, quantities go right.
        let numeric = (0..<columns).map { k in
            (k >= align.count || align[k] == nil) && Markdown.numericColumn(rows.map { k < $0.count ? $0[k] : "" })
        }
        let scroll = ScrollView(.horizontal, showsIndicators: false) {
            TableLayout(
                columns: columns, rows: all.count, viewport: viewport, gap: theme.tableColumnGap, pad: theme.tableRowPadding,
                minWidth: theme.tableCellMinWidth, maxWidth: theme.tableCellMaxWidth, readable: theme.tableReadableWidth * scale
            ) {
                ForEach(all.indices, id: \.self) { ri in
                    ForEach(0..<columns, id: \.self) { ci in
                        cell(ri, ci < all[ri].count ? all[ri][ci] : "", ci < align.count ? align[ci] : nil, numeric[ci])
                    }
                }
                ForEach(0..<all.count - 1, id: \.self) { _ in
                    Rectangle().fill(theme.line).frame(height: 1).accessibilityHidden(true)
                }
            }
            .padding(.horizontal, bleed)
        }
        .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { viewport = max(0, $0 - 2 * bleed) }
        Group {
            if #available(iOS 18.0, macOS 15.0, *) {
                scroll
                    .onScrollGeometryChange(for: Bool.self) { $0.contentOffset.x + $0.containerSize.width < $0.contentSize.width - 1 } action: { more = $1 }
                    .mask { fade }
            } else {
                // Without the scroll position there is no telling when the end is reached, and a fade left there would hide the last column.
                scroll
            }
        }
        .padding(.horizontal, -bleed)
    }

    /// Fades the last 32 points out, whatever is behind: there is more to scroll to.
    private var fade: some View {
        HStack(spacing: 0) {
            Rectangle()
            LinearGradient(colors: [.black, .black.opacity(more ? 0 : 1)], startPoint: .leading, endPoint: .trailing).frame(width: 32)
        }
    }

    private func cell(_ row: Int, _ text: String, _ align: MarkdownTableAlign?, _ numeric: Bool) -> some View {
        let side: MarkdownTableAlign = align ?? (numeric ? .right : .left)
        let spec = row == 0
            ? TextSpec(
                size: theme.tableHeadSize, lineHeight: 1.5, color: theme.muted, codeSize: theme.inlineCodeSize,
                alignment: side.text, tabular: numeric)
            : TextSpec(
                size: theme.tableCellSize, lineHeight: theme.tableLineHeight, color: theme.color(tone), codeSize: theme.inlineCodeSize,
                alignment: side.text, tabular: numeric)
        return InlineText(source: text, lines: true, spec: spec, look: look)
            .equatable()
            .layoutValue(key: CellAlignment.self, value: side == .left ? 0 : side == .center ? 0.5 : 1)
            .accessibilityAddTraits(row == 0 ? .isHeader : [])
    }
}

extension MarkdownTableAlign {
    fileprivate var text: TextAlignment {
        switch self {
        case .left: .leading
        case .center: .center
        case .right: .trailing
        }
    }
}
