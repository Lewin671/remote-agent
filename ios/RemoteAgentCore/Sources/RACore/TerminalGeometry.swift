// How many cells of a font fit an area, and the font size at which the
// remote grid fits (Android's term/TerminalFont.kt). The numbers are in
// whatever unit the view measures in, the same one throughout.
import Foundation

/// A font's cell: its width, the height of a line, and the space above the first line.
public struct TerminalGeometry: Sendable, Equatable {
    public var cellWidth: Double
    /// Not always whole: iOS shows a font's cell scaled.
    public var lineHeight: Double
    public var topOffset: Int

    public init(cellWidth: Double, lineHeight: Double, topOffset: Int = 0) {
        self.cellWidth = cellWidth
        self.lineHeight = lineHeight
        self.topOffset = topOffset
    }

    /// The columns and rows an area has room for.
    public func capacity(width: Int, height: Int) -> TerminalSize {
        guard cellWidth > 0, lineHeight > 0 else { return TerminalSize(0, 0) }
        return TerminalSize(
            Int((Double(max(width, 0)) / cellWidth).rounded(.down)), Int((Double(max(height - topOffset, 0)) / lineHeight).rounded(.down)))
    }

    public func contains(width: Int, height: Int, cols: Int, rows: Int) -> Bool {
        Double(cols) * cellWidth <= Double(width) && Double(rows) * lineHeight + Double(topOffset) <= Double(height)
    }
}

/// The size the font is never shown below, and the smallest 字号 there is
/// (architecture §11 「尺寸」): at 6 an unfitted window stays on the screen,
/// and what is still cut off is reached by panning sideways. In points (dp on
/// Android); a caller measuring in another unit converts it.
public let terminalFontFloor = 6

/// Keep the remote grid intact; only the explicit fit action changes its
/// size. The largest size from `preferred` down to `smallest` whose grid of
/// `cols` by `rows` fits; `measure` gives a size's geometry. `smallest` is in
/// the same unit as `preferred` — the screen's floor in that unit, e.g. the
/// floor in points times the display scale. At `smallest` it stays, even if
/// the grid is wider: the rest is panned into view.
public func terminalFontSize(
    preferred: Int, smallest: Int, width: Int, height: Int, cols: Int, rows: Int, measure: (Int) -> TerminalGeometry
) -> Int {
    if cols <= 0 || rows <= 0 { return preferred }
    var size = preferred
    while size >= smallest {
        if measure(size).contains(width: width, height: height, cols: cols, rows: rows) { return size }
        size -= 1
    }
    return smallest
}
