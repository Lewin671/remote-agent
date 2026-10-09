// How many cells of a font fit an area, and the font size at which the
// remote grid fits (Android's term/TerminalFont.kt). The numbers are in
// whatever unit the view measures in, the same one throughout.
import Foundation

/// A font's cell: its width, the height of a line, and the space above the first line.
public struct TerminalGeometry: Sendable, Equatable {
    public var cellWidth: Double
    public var lineHeight: Int
    public var topOffset: Int

    public init(cellWidth: Double, lineHeight: Int, topOffset: Int = 0) {
        self.cellWidth = cellWidth
        self.lineHeight = lineHeight
        self.topOffset = topOffset
    }

    /// The columns and rows an area has room for.
    public func capacity(width: Int, height: Int) -> TerminalSize {
        guard cellWidth > 0, lineHeight > 0 else { return TerminalSize(0, 0) }
        return TerminalSize(Int((Double(max(width, 0)) / cellWidth).rounded(.down)), max(height - topOffset, 0) / lineHeight)
    }

    public func contains(width: Int, height: Int, cols: Int, rows: Int) -> Bool {
        Double(cols) * cellWidth <= Double(width) && rows * lineHeight + topOffset <= height
    }
}

/// Keep the remote grid intact; only the explicit fit action changes its
/// size. The largest size from `preferred` down to `smallest` whose grid of
/// `cols` by `rows` fits; `measure` gives a size's geometry.
public func terminalFontSize(
    preferred: Int, smallest: Int = 5, width: Int, height: Int, cols: Int, rows: Int, measure: (Int) -> TerminalGeometry
) -> Int {
    if cols <= 0 || rows <= 0 { return preferred }
    var size = preferred
    while size >= smallest {
        if measure(size).contains(width: width, height: height, cols: cols, rows: rows) { return size }
        size -= 1
    }
    return smallest
}
