import SwiftUI
import UIKit

/// A menu preview is an excerpt, not a second page of the reply.
struct MessageMenuPreview: View {
    let text: String

    var body: some View {
        Text(verbatim: text)
            .type(T.body, Theme.fg)
            .lineLimit(4)
            .frame(width: 260, alignment: .leading)
            .padding(16)
            .background(Theme.card)
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
                SheetHead("选择文字", action: "完成", onAction: onDone)
                Text("长按文字，再拖动两端选择要复制的内容")
                    .type(T.sm, Theme.muted)
                    .padding(.bottom, 10)
            }
            .padding(.horizontal, 16)
            .padding(.top, 26)
            MessageSelectableText(text: text)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .environment(\.onSoft, false)
        .presentationDetents([.large])
        .presentationContentInteraction(.scrolls)
        .presentationDragIndicator(.visible)
        .presentationCornerRadius(Theme.Radius.lg)
        .presentationBackground(Theme.card)
        .tint(Theme.blue)
    }
}

struct MessageSelectableText: UIViewRepresentable {
    let text: String

    func makeUIView(context: Context) -> UITextView {
        let view = UITextView()
        view.isEditable = false
        view.isSelectable = true
        view.isScrollEnabled = true
        view.alwaysBounceVertical = true
        view.backgroundColor = .clear
        view.adjustsFontForContentSizeCategory = true
        view.textContainerInset = UIEdgeInsets(top: 8, left: 16, bottom: 24, right: 16)
        view.textContainer.lineFragmentPadding = 0
        view.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        view.accessibilityLabel = "消息文字"
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        // Assigning even the same text resets UIKit's selection and offset.
        if view.text != text { view.text = text }
        let font = UIFont.preferredFont(forTextStyle: .body, compatibleWith: view.traitCollection)
        if view.font != font { view.font = font }
        view.textColor = UIColor(Theme.fg)
        view.tintColor = UIColor(Theme.blue)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UITextView, context: Context) -> CGSize? {
        // Use the available viewport, never the height of the whole message.
        guard let width = proposal.width, let height = proposal.height else { return nil }
        return CGSize(width: width, height: height)
    }
}
