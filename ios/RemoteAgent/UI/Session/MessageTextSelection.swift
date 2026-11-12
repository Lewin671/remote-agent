import RACore
import SwiftUI
import UIKit

/// A menu preview is an excerpt, not a second page of the reply.
struct MessageMenuPreview: View {
    let text: String

    var body: some View {
        Text(verbatim: text)
            .font(.body).ink(DS.ink)
            .lineLimit(4)
            .frame(width: 260, alignment: .leading)
            .padding(16)
            .background(DS.paper)
            .textSelection(.disabled)
    }
}

/// One continuous text storage lets a selection span paragraphs, lists
/// and code. Keep the header outside it and let UITextView own scrolling:
/// a non-scrolling text view inside kitSheet's ScrollView cannot scroll
/// its viewport while a selection handle is dragged past the edge.
struct MessageTextSelection: View {
    let text: String
    let onDone: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 0) {
                SheetHead(L10n.sessionIosSelectText(), action: L10n.commonDone(), onAction: onDone)
                Text(L10n.sessionIosSelectTextHint())
                    .font(.subheadline).ink(DS.ink2)
                    .padding(.bottom, 10)
            }
            .padding(.horizontal, 16)
            .padding(.top, 26)
            MessageSelectableText(text: text)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .presentationDetents([.large])
        .presentationContentInteraction(.scrolls)
        .sheetSurface()
        .tint(DS.blue)
    }
}

struct MessageSelectableText: UIViewRepresentable {
    let text: String

    func makeUIView(context: Context) -> UITextView {
        let view = MessageSelectionTextView()
        view.isEditable = false
        view.isSelectable = true
        view.isScrollEnabled = true
        view.alwaysBounceVertical = true
        view.backgroundColor = .clear
        view.adjustsFontForContentSizeCategory = true
        view.textContainerInset = UIEdgeInsets(top: 8, left: 16, bottom: 24, right: 16)
        view.textContainer.lineFragmentPadding = 0
        view.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        // Assigning even the same text resets UIKit's selection and offset.
        if view.text != text { view.text = text }
        let font = UIFont.preferredFont(forTextStyle: .body, compatibleWith: view.traitCollection)
        if view.font != font { view.font = font }
        view.textColor = UIColor(DS.ink)
        view.tintColor = UIColor(DS.blue)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UITextView, context: Context) -> CGSize? {
        // Use the available viewport, never the height of the whole message.
        guard let width = proposal.width, let height = proposal.height else { return nil }
        return CGSize(width: width, height: height)
    }
}

/// UIKit may retain the view across language updates; its default label is not a saved sentence.
final class MessageSelectionTextView: UITextView {
    private var suppliedAccessibilityLabel: String?
    override var accessibilityLabel: String? {
        get { suppliedAccessibilityLabel ?? L10n.sessionIosMessageText() }
        set { suppliedAccessibilityLabel = newValue }
    }
}
