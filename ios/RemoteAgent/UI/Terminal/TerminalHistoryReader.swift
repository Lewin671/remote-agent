// The reader: the history lines as selectable text at the live screen's font,
// line height and colours (architecture §11 「回看历史」). It only reads — a
// tap never opens the keyboard, and output arriving never moves it. Reaching
// the top asks for the page before; carrying on past the very bottom leaves,
// by a vertical gesture (a sideways pan only looks along a line).
// Prepending keeps the line in sight fixed, by the distance from the end of
// the text, which is what a page and the status line above are prepended to.
//
// A line is already the pane's width, so nothing wraps: a line wider than the
// area is panned sideways, as the live screen is, and the reader opens at the
// live screen's own sideways offset. It pans over at least the live grid's
// width, so that offset is not lost when the captured lines are shorter than
// the window. The live screen keeps its offset when reading is left and is
// asked for it again on the next open.
//
// The size is asked for again whenever the area is laid out: a rotation moves
// the live screen to another size, and the reader moves with it.
import RACore
import SwiftUI
import UIKit

struct TerminalHistoryReader: UIViewRepresentable {
    let pages: [TerminalHistory.Page]
    let status: TerminalHistory.Status
    let online: Bool
    /// How far the entry gesture had pulled: the reader opens this far above the newest line.
    let entry: CGFloat
    /// The live screen's shown point size and cell, so the two look like one picture moving.
    let metrics: () -> (points: CGFloat, cell: CGSize)
    /// How far the live screen is panned sideways, so the reader opens there.
    let offset: () -> CGFloat
    /// The width the live grid is shown at: the reader pans over at least this much.
    let gridWidth: () -> CGFloat
    let loadEarlier: () -> Void
    let retry: () -> Void
    let leave: () -> Void

    func makeUIView(context: Context) -> TerminalHistoryTextView {
        let view = TerminalHistoryTextView()
        configure(view)
        return view
    }

    func updateUIView(_ view: TerminalHistoryTextView, context: Context) {
        configure(view)
        view.content = TerminalHistoryTextView.Content(pages: pages, status: status, online: online, entry: entry)
        view.metrics = metrics
        view.liveOffsetX = offset()
        view.liveGridWidth = gridWidth()
        view.show()
    }

    private func configure(_ view: TerminalHistoryTextView) {
        view.onLoadEarlier = loadEarlier
        view.onRetry = retry
        view.onLeave = leave
    }
}

final class TerminalHistoryTextView: UITextView, UITextViewDelegate {
    /// What the reader is to show, kept so the size can be asked for again later.
    struct Content {
        var pages: [TerminalHistory.Page]
        var status: TerminalHistory.Status
        var online: Bool
        var entry: CGFloat
    }

    var onLoadEarlier: () -> Void = {}
    var onRetry: () -> Void = {}
    var onLeave: () -> Void = {}

    var content = Content(pages: [], status: .none, online: true, entry: 0)
    var metrics: () -> (points: CGFloat, cell: CGSize) = { (14, CGSize(width: 8.4, height: 17)) }
    /// How far the live screen is panned sideways: the reader opens at the same offset.
    var liveOffsetX: CGFloat = 0
    /// The width the live grid is shown at: the reader pans over at least this much,
    /// so the offset survives even when the captured lines are shorter than the window.
    var liveGridWidth: CGFloat = 0

    /// What is on screen, so a redraw that changes nothing re-lays out nothing.
    private var shown = ""
    private var placed = false
    private var updating = false
    private var leaving = false
    private var lineHeight: CGFloat = 16
    private var lastBounds = CGSize.zero
    /// Which way the drag in hand goes, decided once, so a sideways pan with a
    /// little drift is not read as a pull past the bottom.
    private var dragAxis: TerminalDrag?

    init() {
        super.init(frame: .zero, textContainer: nil)
        isEditable = false
        isSelectable = true
        backgroundColor = TerminalHistoryText.background
        textColor = TerminalHistoryText.foreground
        // The live terminal's own margin, so a line lands where it was.
        textContainerInset = UIEdgeInsets(top: 4, left: 4, bottom: 12, right: 4)
        // Lines are the pane's width already: they do not wrap (the width is set in `show`).
        textContainer.lineBreakMode = .byClipping
        showsHorizontalScrollIndicator = true
        alwaysBounceHorizontal = false
        // A drag that has begun one way keeps to it, so a sideways pan does not also move up and down.
        isDirectionalLockEnabled = true
        contentInsetAdjustmentBehavior = .never
        delegate = self
    }

    @available(*, unavailable) required init?(coder: NSCoder) { fatalError("not from a nib") }

    // MARK: What it shows

    /// The area was laid out again — a rotation, say. The live screen is at
    /// another size by now, and the reader is asked for it again.
    override func layoutSubviews() {
        super.layoutSubviews()
        guard bounds.size != lastBounds else { return }
        lastBounds = bounds.size
        show()
    }

    func show() {
        let (points, cell) = metrics()
        let pages = content.pages
        let key = pages.map(\.id).joined(separator: "|") + "#\(content.status)#\(content.online)#\(points)/\(cell.height)/\(Int(bounds.width))x\(Int(bounds.height))/\(Int(liveGridWidth))"
        guard key != shown else { return }
        // Not laid out yet: place nothing, and mark nothing done, so the first
        // real layout places the entry pull and the live offset.
        guard bounds.width > 0, bounds.height > 0 else { return }
        lineHeight = max(1, cell.height)
        let anchor = placed ? topAnchor() : nil
        let text = TerminalHistoryText.document(
            pages: pages, status: content.status, online: content.online, points: points, cell: cell)
        linkTextAttributes = [.foregroundColor: content.online ? TerminalHistoryText.link : TerminalHistoryText.status]
        updating = true
        // The container is sized before the text goes in, so the layout the text
        // view does on assignment already measures the whole, unwrapped line.
        attributedText = text
        sizeContainer(to: text)
        layoutManager.ensureLayout(for: textContainer)
        layoutIfNeeded()
        // With the width no longer tracking the view, the text view leaves the
        // scrollable size alone: it is the laid-out one, which the reader sets here.
        setScrollableSize()
        if let anchor {
            restore(anchor)
        } else {
            placed = true
            // The newest line, a pull's worth up, so the gesture feels carried on and does not leave at once.
            scrollToBottom(up: max(content.entry, lineHeight * 3))
            // The live screen's own sideways offset: the reader is the same picture, moved sideways.
            let most = max(0, contentSize.width - bounds.width)
            setContentOffset(CGPoint(x: min(max(0, liveOffsetX), most), y: contentOffset.y), animated: false)
        }
        updating = false
        shown = key
    }

    /// Gives the text container the whole width of the longest line, so nothing
    /// wraps; the height of the text is the container's too, so the reader scrolls.
    private func sizeContainer(to text: NSAttributedString) {
        guard bounds.width > 0 else { return }
        let insets = textContainerInset
        let room = max(1, bounds.width - insets.left - insets.right)
        // Measured with no bound on either axis: a line is drawn as long as it is,
        // and every line is laid out however many there are (100,000 tmux rows is
        // well past a million points, and a cap there would leave the newest of
        // them unreachable).
        let drawn = text.boundingRect(
            with: CGSize(width: 1_000_000, height: CGFloat.greatestFiniteMagnitude),
            options: [.usesLineFragmentOrigin, .usesFontLeading], context: nil
        ).size
        let padding = textContainer.lineFragmentPadding
        textContainer.widthTracksTextView = false
        textContainer.size = CGSize(
            width: max(room, ceil(drawn.width) + padding * 2 + 1),
            height: max(bounds.height - insets.top - insets.bottom, ceil(drawn.height) + 1))
    }

    /// The size the laid-out text needs, margins included: what can be scrolled to.
    /// Never narrower than the live grid, or the entry offset would be clamped to
    /// less than the live screen's own when the captured lines are shorter.
    private func setScrollableSize() {
        let insets = textContainerInset
        layoutManager.ensureLayout(for: textContainer)
        let used = layoutManager.usedRect(for: textContainer)
        contentSize = CGSize(
            width: max(used.width, liveGridWidth) + insets.left + insets.right,
            height: used.height + insets.top + insets.bottom)
    }

    /// The line at the top of what is in sight, as where it starts from the end of the text.
    private struct Anchor {
        let fromEnd: Int
        let y: CGFloat
    }

    private func topAnchor() -> Anchor? {
        guard let text = attributedText, text.length > 0 else { return nil }
        layoutManager.ensureLayout(for: textContainer)
        let point = CGPoint(x: textContainerInset.left + 1, y: max(0, contentOffset.y - textContainerInset.top) + 1)
        let glyph = layoutManager.glyphIndex(for: point, in: textContainer)
        let character = layoutManager.characterIndexForGlyph(at: glyph)
        let line = layoutManager.lineFragmentRect(forGlyphAt: glyph, effectiveRange: nil)
        return Anchor(fromEnd: text.length - character, y: line.minY)
    }

    private func restore(_ anchor: Anchor) {
        guard let text = attributedText, text.length > 0 else { return }
        layoutManager.ensureLayout(for: textContainer)
        let character = max(0, min(text.length - 1, text.length - anchor.fromEnd))
        let glyph = layoutManager.glyphIndexForCharacter(at: character)
        let line = layoutManager.lineFragmentRect(forGlyphAt: glyph, effectiveRange: nil)
        setContentOffset(CGPoint(x: contentOffset.x, y: max(0, contentOffset.y + line.minY - anchor.y)), animated: false)
    }

    private func scrollToBottom(up: CGFloat) {
        let bottom = max(0, contentSize.height - bounds.height)
        setContentOffset(CGPoint(x: contentOffset.x, y: max(0, bottom - up)), animated: false)
    }

    // MARK: Reading on and leaving

    func scrollViewWillBeginDragging(_ scrollView: UIScrollView) {
        dragAxis = nil
    }

    func scrollViewDidScroll(_ scrollView: UIScrollView) {
        guard !updating, !leaving, scrollView.isDragging || scrollView.isDecelerating else { return }
        // Which way this drag goes, once it has moved enough to say.
        if dragAxis == nil, scrollView.isDragging {
            let moved = scrollView.panGestureRecognizer.translation(in: scrollView)
            dragAxis = terminalDrag(dx: moved.x, dy: moved.y)
        }
        // Near the top: the page before, which keeps the line in sight in place.
        if scrollView.contentOffset.y < 80 { onLoadEarlier() }
        // Carried on down past the very bottom: back to the live screen — which a
        // sideways pan is not, it only came here to look along a wide line.
        let bottom = max(0, scrollView.contentSize.height - scrollView.bounds.height)
        if Self.leavesReading(
            offsetY: scrollView.contentOffset.y, bottom: bottom, lineHeight: lineHeight, sideways: dragAxis == .pan)
        {
            leave()
        }
    }

    /// Whether being past the bottom is a wish to leave: by a vertical gesture
    /// only, and only well past the last line.
    static func leavesReading(offsetY: CGFloat, bottom: CGFloat, lineHeight: CGFloat, sideways: Bool) -> Bool {
        !sideways && offsetY - bottom > max(24, lineHeight)
    }

    private func leave() {
        guard !leaving else { return }
        leaving = true
        onLeave()
    }

    /// The one link there is: 重试, after a failed page.
    func textView(_ textView: UITextView, primaryActionFor textItem: UITextItem, defaultAction: UIAction) -> UIAction? {
        guard case .link(let url) = textItem.content, url == TerminalHistoryText.retryURL else { return defaultAction }
        return UIAction { [weak self] _ in self?.onRetry() }
    }

    /// A tap does nothing else: the keyboard belongs to the live screen, not here.
    func textViewDidBeginEditing(_ textView: UITextView) { resignFirstResponder() }
}
