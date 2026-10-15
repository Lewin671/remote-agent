import SwiftUI
import UIKit

/// Read-only text, separate from the live emulator so history never changes
/// terminal modes or sends keystrokes. Prepending keeps the visible text fixed.
struct TerminalHistoryPanel: View {
    let history: TerminalHistory
    let online: Bool
    let font: Int
    let load: () -> Void
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                if !history.pending.isEmpty { Text("读取历史…") }
                else if !history.error.isEmpty { Text(history.error); Button("重试", action: load).disabled(!online) }
                else if !history.cursor.isEmpty { Button("读取更早历史", action: load).disabled(!online) }
                else { Text(history.pages.isEmpty ? "暂无历史" : "已到保留历史的开头") }
                Spacer(minLength: 0)
            }.font(.caption).foregroundStyle(Theme.Term.link).padding(12)
            HistoryText(pages: history.pages, font: font, load: {
                if online && history.pending.isEmpty && history.error.isEmpty && !history.cursor.isEmpty { load() }
            })
        }.background(Theme.Term.bg)
    }
}

private struct HistoryText: UIViewRepresentable {
    let pages: [TerminalHistory.Page]
    let font: Int
    let load: () -> Void
    func makeUIView(context: Context) -> TerminalHistoryTextView { TerminalHistoryTextView() }
    func updateUIView(_ view: TerminalHistoryTextView, context: Context) {
        view.load = load
        view.show(pages: pages, font: font)
    }
}

final class TerminalHistoryTextView: UITextView, UITextViewDelegate {
    private var first: String?
    private var updating = false
    var load: () -> Void = {}
    init() {
        super.init(frame: .zero, textContainer: nil)
        isEditable = false; isSelectable = true
        backgroundColor = UIColor(Theme.Term.bg)
        textColor = UIColor(Theme.Term.text)
        textContainer.lineBreakMode = .byCharWrapping
        textContainerInset = UIEdgeInsets(top: 8, left: 8, bottom: 8, right: 8)
        delegate = self
    }
    @available(*, unavailable) required init?(coder: NSCoder) { fatalError("not from a nib") }
    func show(pages: [TerminalHistory.Page], font: Int) {
        let desired = UIFont.monospacedSystemFont(ofSize: CGFloat(font), weight: .regular)
        if self.font != desired { self.font = desired }
        guard pages.first?.id != first else { return }
        updating = true
        let previous = contentOffset
        let initial = first == nil
        let oldLength = (text as NSString).length
        // Use a text anchor rather than contentSize: UITextView may still
        // estimate the heights of lines outside its viewport.
        layoutManager.ensureLayout(for: textContainer)
        let oldGlyph = layoutManager.glyphIndex(for: CGPoint(x: 0, y: max(0, previous.y - textContainerInset.top)), in: textContainer)
        let oldCharacter = oldLength > 0 ? layoutManager.characterIndexForGlyph(at: oldGlyph) : 0
        let oldY = oldLength > 0 ? layoutManager.lineFragmentRect(forGlyphAt: oldGlyph, effectiveRange: nil).minY : 0
        text = pages.map { $0.text + "\n" }.joined()
        layoutManager.ensureLayout(for: textContainer)
        layoutIfNeeded()
        if initial {
            scrollRangeToVisible(NSRange(location: (text as NSString).length, length: 0))
        } else if !text.isEmpty {
            let added = max(0, (text as NSString).length - oldLength)
            let newGlyph = layoutManager.glyphIndexForCharacter(at: min((text as NSString).length - 1, oldCharacter + added))
            let newY = layoutManager.lineFragmentRect(forGlyphAt: newGlyph, effectiveRange: nil).minY
            setContentOffset(CGPoint(x: previous.x, y: max(0, previous.y + newY - oldY)), animated: false)
        }
        first = pages.first?.id
        updating = false
    }
    func scrollViewDidScroll(_ scrollView: UIScrollView) {
        if !updating && scrollView.contentOffset.y < 80 && (scrollView.isDragging || scrollView.isDecelerating) { load() }
    }
}
