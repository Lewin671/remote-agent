import RACore
// Text entry (docs/design-system.md §6): a field on a `fill` surface with a
// small label over what is typed, and the pill that filters a list.
import SwiftUI
import UIKit

/// The surface of an input field: `fill`, radius 18. `background` replaces it.
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
            .background(background ?? DS.fill, in: RoundedRectangle(cornerRadius: DS.Radius.notice, style: .continuous))
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
        let style: Font = switch look {
        case .text: .body
        case .mono: .footnote.monospaced()
        case .code: .title2.monospaced().weight(.semibold)
        }
        FieldBox(background: background) {
            VStack(alignment: .leading, spacing: 2) {
                if let label { Text(label).font(.footnote).ink(DS.ink2) }
                TextField(
                    "", text: $text, prompt: Text(placeholder).foregroundStyle(DS.ink2),
                    axis: lines.upperBound > 1 ? .vertical : .horizontal
                )
                .lineLimit(lines)
                // The placeholder is a sentence in the text size; only what is typed takes the look.
                .font(text.isEmpty && look == .code ? .body : style).ink(DS.ink)
                .multilineTextAlignment(look == .code ? .center : .leading)
                .keyboardType(keyboard)
                .textContentType(contentType)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(submitLabel)
                .onSubmit { onSubmit?() }
                .focused($focused)
                .tint(DS.blue)
                .accessibilityLabel(label ?? placeholder)
            }
        }
        // The whole surface is the field to the finger, not just the line of text.
        .contentShape(Rectangle())
        .onTapGesture { focused = true }
        .task { if focusOnAppear { focused = true } }
    }
}

/// The filter of a list: a pill on `chip`, the search icon at the left, and
/// an x to clear it once it has text. `inset` is what it keeps from the
/// sides: the page's margin on a page, nothing inside a sheet, which has its own.
struct FilterBox: View {
    @Binding private var text: String
    private let suppliedPlaceholder: String?
    var resolvedPlaceholder: String { suppliedPlaceholder ?? L10n.homeFilterTitle() }
    private let inset: CGFloat

    init(text: Binding<String>, placeholder: String? = nil, inset: CGFloat = DS.Space.l) {
        _text = text
        self.suppliedPlaceholder = placeholder
        self.inset = inset
    }

    var body: some View {
        HStack(spacing: 6) {
            SymbolImage("magnifyingglass", style: .body, color: DS.ink2)
            TextField("", text: $text, prompt: Text(resolvedPlaceholder).foregroundStyle(DS.ink2))
                .font(.body).ink(DS.ink)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.search)
                .tint(DS.blue)
                .accessibilityLabel(resolvedPlaceholder)
        }
        .padding(.leading, 14).padding(.trailing, text.isEmpty ? 14 : 40)
        .frame(minHeight: 36)
        .background(DS.fill, in: Capsule())
        .overlay(alignment: .trailing) {
            if !text.isEmpty { IconButton("xmark", label: L10n.homeClearFilter(), size: 16, color: DS.ink3) { text = "" } }
        }
        .frame(minHeight: DS.minHit)
        .padding(.horizontal, inset)
    }
}
