// Text entry (docs/design-system.md §6): a field on a soft surface with a
// small label over what is typed, and the pill that filters a list.
import SwiftUI
import UIKit

/// The surface of an input field: soft, radius 10. `background` replaces it, as `card` for a field on a grey card.
struct FieldBox<Content: View>: View {
    private let background: Color?
    private let content: Content

    init(background: Color? = nil, @ViewBuilder content: () -> Content) {
        self.background = background
        self.content = content()
    }

    var body: some View {
        content
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 14).padding(.vertical, 11)
            .background(background ?? Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous))
    }
}

/// A text field in a `FieldBox`: an optional small label over what is
/// typed, a placeholder, one line or growing up to `lines`. `mono` is for a
/// key or a path; `code` for a pairing code, large, centred and checked
/// digit by digit. `focusOnAppear` brings the keyboard up with the field.
struct InputField: View {
    enum Look: Sendable { case text, mono, code }

    private let label: String?
    @Binding private var text: String
    private let placeholder: String
    private let look: Look
    private let lines: ClosedRange<Int>
    private let background: Color?
    private let keyboard: UIKeyboardType
    private let contentType: UITextContentType?
    private let submitLabel: SubmitLabel
    private let onSubmit: (() -> Void)?
    private let focusOnAppear: Bool
    @FocusState private var focused: Bool

    init(
        _ label: String? = nil, text: Binding<String>, placeholder: String = "", look: Look = .text,
        lines: ClosedRange<Int> = 1...1, background: Color? = nil, keyboard: UIKeyboardType = .default, contentType: UITextContentType? = nil,
        submitLabel: SubmitLabel = .done, focusOnAppear: Bool = false, onSubmit: (() -> Void)? = nil
    ) {
        self.label = label
        _text = text
        self.placeholder = placeholder
        self.look = look
        self.lines = lines
        self.background = background
        self.keyboard = keyboard
        self.contentType = contentType
        self.submitLabel = submitLabel
        self.focusOnAppear = focusOnAppear
        self.onSubmit = onSubmit
    }

    var body: some View {
        let style: TypeStyle = switch look {
        case .text: T.body
        case .mono: T.mono13
        case .code: T.code22
        }
        FieldBox(background: background) {
            VStack(alignment: .leading, spacing: 2) {
                if let label { Text(label).type(T.meta, Theme.muted) }
                TextField(
                    "", text: $text, prompt: Text(placeholder).foregroundStyle(Theme.muted),
                    axis: lines.upperBound > 1 ? .vertical : .horizontal
                )
                .lineLimit(lines)
                // The placeholder is a sentence in the text size; only what is typed takes the look.
                .type(text.isEmpty && look == .code ? T.body : style, Theme.fg)
                .multilineTextAlignment(look == .code ? .center : .leading)
                .keyboardType(keyboard)
                .textContentType(contentType)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(submitLabel)
                .onSubmit { onSubmit?() }
                .focused($focused)
                .tint(Theme.blue)
                .accessibilityLabel(label ?? placeholder)
            }
        }
        // The whole surface is the field to the finger, not just the line of text.
        .contentShape(Rectangle())
        .onTapGesture { focused = true }
        .task { if focusOnAppear { focused = true } }
    }
}

/// The filter of a list: a pill on `chip`, the search icon at the left, and an x to clear it once it has text.
struct FilterBox: View {
    @Binding private var text: String
    private let placeholder: String

    init(text: Binding<String>, placeholder: String = "按标题筛选") {
        _text = text
        self.placeholder = placeholder
    }

    var body: some View {
        HStack(spacing: 6) {
            Ic(Lucide.search, style: T.body, color: Theme.muted)
            TextField("", text: $text, prompt: Text(placeholder).foregroundStyle(Theme.muted))
                .type(T.body, Theme.fg)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.search)
                .tint(Theme.blue)
                .accessibilityLabel(placeholder)
        }
        .padding(.leading, 14).padding(.trailing, text.isEmpty ? 14 : 40)
        .frame(minHeight: 36)
        .background(Theme.chip, in: Capsule())
        .overlay(alignment: .trailing) {
            if !text.isEmpty { IconButton(Lucide.x, label: "清空筛选", size: 16, color: Theme.faint) { text = "" } }
        }
        .frame(minHeight: Theme.minHit)
        .padding(.horizontal, Theme.Space.page)
    }
}
