// The reader: the history lines as selectable text at the live screen's font,
// line height and colours (architecture §11 「回看历史」). It only reads — a
// tap never opens the keyboard, and output arriving never moves it. Reaching
// the top asks for the page before; carrying on past the very bottom leaves.
// Prepending keeps the line in sight fixed, by the distance from the end of
// the text, which is what a page and the status line above are prepended to.
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

    /// What is on screen, so a redraw that changes nothing re-lays out nothing.
    private var shown = ""
    private var placed = false
    private var updating = false
    private var leaving = false
    private var lineHeight: CGFloat = 16
    private var lastBounds = CGSize.zero

    init() {
        super.init(frame: .zero, textContainer: nil)
        isEditable = false
        isSelectable = true
        backgroundColor = TerminalHistoryText.background
        textColor = TerminalHistoryText.foreground
        // The live terminal's own margin, so a line lands where it was.
        textContainerInset = UIEdgeInsets(top: 4, left: 4, bottom: 12, right: 4)
        // Lines are the pane's width already: they do not wrap.
        textContainer.lineBreakMode = .byClipping
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
        let key = pages.map(\.id).joined(separator: "|") + "#\(content.status)#\(content.online)#\(points)/\(cell.height)"
        guard key != shown else { return }
        lineHeight = max(1, cell.height)
        let anchor = placed ? topAnchor() : nil
        let text = TerminalHistoryText.document(
            pages: pages, status: content.status, online: content.online, points: points, cell: cell)
        linkTextAttributes = [.foregroundColor: content.online ? TerminalHistoryText.link : TerminalHistoryText.status]
        updating = true
        attributedText = text
        layoutManager.ensureLayout(for: textContainer)
        layoutIfNeeded()
        if let anchor {
            restore(anchor)
        } else {
            placed = true
            // The newest line, a pull's worth up, so the gesture feels carried on and does not leave at once.
            scrollToBottom(up: max(content.entry, lineHeight * 3))
        }
        updating = false
        shown = key
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
        setContentOffset(CGPoint(x: 0, y: max(0, contentOffset.y + line.minY - anchor.y)), animated: false)
    }

    private func scrollToBottom(up: CGFloat) {
        let bottom = max(0, contentSize.height - bounds.height)
        setContentOffset(CGPoint(x: 0, y: max(0, bottom - up)), animated: false)
    }

    // MARK: Reading on and leaving

    func scrollViewDidScroll(_ scrollView: UIScrollView) {
        guard !updating, !leaving, scrollView.isDragging || scrollView.isDecelerating else { return }
        // Near the top: the page before, which keeps the line in sight in place.
        if scrollView.contentOffset.y < 80 { onLoadEarlier() }
        // Carried on down past the very bottom: back to the live screen.
        let bottom = max(0, scrollView.contentSize.height - scrollView.bounds.height)
        if scrollView.contentOffset.y - bottom > max(24, lineHeight) { leave() }
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
