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

/// Retains the native prompt where it fits, measuring a whole instruction only when it must wrap.
/// Measuring in the background leaves the field's width and focus independent of its hint.
struct WrappingPlaceholder<Field: View>: View {
    let text: String
    let empty: Bool
    let font: Font
    let color: Color
    @ViewBuilder let field: (Text?) -> Field
    @State private var width: CGFloat = 0
    @State private var naturalWidth: CGFloat = 0

    private var wraps: Bool { naturalWidth > width + 0.5 }

    var body: some View {
        ZStack(alignment: .topLeading) {
            if empty && wraps {
                Text(text).font(font).foregroundStyle(color)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityHidden(true).allowsHitTesting(false)
            }
            field(wraps ? nil : Text(text).foregroundStyle(color))
        }
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { width = $0 }
        .background {
            Text(text).font(font).fixedSize().hidden()
                .accessibilityHidden(true).allowsHitTesting(false)
                .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { naturalWidth = $0 }
        }
    }
}

/// A text field in a `FieldBox`: an optional small label over what is
/// typed, a placeholder, one line or growing up to `lines`. `mono` is for a
/// key or a path; `code` for a pairing code, large, centred and checked
/// digit by digit. `focusOnAppear` brings the keyboard up with the field.
/// `wrapPlaceholder` shows a whole instruction without changing the input's line limit.
struct InputField: View {
    enum Look: Sendable { case text, mono, code }

    private let label: String?
    @Binding private var text: String
    private let placeholder: String
    private let wrapPlaceholder: Bool
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
        _ label: String? = nil, text: Binding<String>, placeholder: String = "", wrapPlaceholder: Bool = false, look: Look = .text,
        lines: ClosedRange<Int> = 1...1, background: Color? = nil, keyboard: UIKeyboardType = .default, contentType: UITextContentType? = nil,
        submitLabel: SubmitLabel = .done, focusOnAppear: Bool = false, onSubmit: (() -> Void)? = nil
    ) {
        self.label = label
        _text = text
        self.placeholder = placeholder
        self.wrapPlaceholder = wrapPlaceholder
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
        let field = { (prompt: Text?) in
            TextField(
                "", text: $text, prompt: prompt,
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

        return FieldBox(background: background) {
            VStack(alignment: .leading, spacing: 2) {
                if let label { Text(label).font(.footnote).ink(DS.ink2) }
                if wrapPlaceholder {
                    WrappingPlaceholder(text: placeholder, empty: text.isEmpty, font: style, color: DS.ink2) { field($0) }
                } else {
                    field(Text(placeholder).foregroundStyle(DS.ink2))
                }
            }
        }
        // The whole surface is the field to the finger, not just the line of text.
        .contentShape(Rectangle())
        .onTapGesture { focused = true }
        .task { if focusOnAppear { focused = true } }
    }
}

/// Maps a UTF-16 selection range from raw edited pairing-code text to Core's formatted
/// pairing-code text (`1234 5678`) by preserving the count of ASCII digits before each endpoint.
func mapPairingCodeSelection(
    editedText: String,
    editedSelection: (start: Int, end: Int),
    formattedText: String
) -> (start: Int, end: Int) {
    func countDigits(in text: String, upToUTF16 offset: Int) -> Int {
        let clamped = max(0, min(offset, text.utf16.count))
        var count = 0
        var i = 0
        for unit in text.utf16 {
            if i >= clamped { break }
            if unit >= 48 && unit <= 57 { count += 1 }
            i += 1
        }
        return count
    }
    func offsetForDigitCount(_ target: Int, in formatted: String) -> Int {
        if target <= 0 { return 0 }
        var seen = 0
        var offset = 0
        for unit in formatted.utf16 {
            offset += 1
            if unit >= 48 && unit <= 57 {
                seen += 1
                if seen == target { return offset }
            }
        }
        return formatted.utf16.count
    }
    let startDigits = countDigits(in: editedText, upToUTF16: editedSelection.start)
    let endDigits = countDigits(in: editedText, upToUTF16: editedSelection.end)
    return (
        offsetForDigitCount(startDigits, in: formattedText),
        offsetForDigitCount(endDigits, in: formattedText)
    )
}

/// Core-driven 8-digit pairing code field: sends raw user edits to Go `clientcore`, renders Core's
/// `formattedCode`, and preserves cursor position by digit count across async round-trips.
struct PairingCodeField: View {
    let formattedCode: String
    let settledEditSeq: UInt64
    let placeholder: String
    let onEdit: (String) -> UInt64
    let onSubmit: () -> Void

    var body: some View {
        FieldBox {
            PairingCodeTextFieldRepresentable(
                formattedCode: formattedCode,
                settledEditSeq: settledEditSeq,
                placeholder: placeholder,
                onEdit: onEdit,
                onSubmit: onSubmit
            )
            .frame(minHeight: 34)
        }
        .accessibilityElement(children: .contain)
    }
}

struct PairingCodeTextFieldRepresentable: UIViewRepresentable {
    let formattedCode: String
    let settledEditSeq: UInt64
    let placeholder: String
    let onEdit: (String) -> UInt64
    let onSubmit: () -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(onEdit: onEdit, onSubmit: onSubmit)
    }

    func makeUIView(context: Context) -> UITextField {
        let tf = UITextField()
        tf.delegate = context.coordinator
        tf.keyboardType = .numberPad
        tf.autocorrectionType = .no
        tf.autocapitalizationType = .none
        tf.spellCheckingType = .no
        tf.textAlignment = .center
        tf.returnKeyType = .next
        tf.tintColor = UIColor(DS.blue)
        tf.textColor = UIColor(DS.ink)
        tf.attributedPlaceholder = NSAttributedString(
            string: placeholder,
            attributes: [
                .foregroundColor: UIColor(DS.ink2),
                .font: UIFont.preferredFont(forTextStyle: .body),
            ]
        )
        tf.accessibilityLabel = placeholder
        tf.setContentHuggingPriority(.defaultLow, for: .horizontal)
        tf.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        tf.addTarget(context.coordinator, action: #selector(Coordinator.editingChanged(_:)), for: .editingChanged)
        applyText(tf, text: formattedCode, coordinator: context.coordinator, mapCursor: false)
        return tf
    }

    func applyCoreUpdate(_ uiView: UITextField, coordinator: Coordinator) {
        coordinator.onEdit = onEdit
        coordinator.onSubmit = onSubmit
        uiView.accessibilityLabel = placeholder
        uiView.accessibilityValue = formattedCode.filter(\.isASCII).filter(\.isNumber).map(String.init).joined(separator: " ")
        guard settledEditSeq >= coordinator.lastDispatchedSeq else { return }
        if (uiView.text ?? "") != formattedCode {
            applyText(uiView, text: formattedCode, coordinator: coordinator, mapCursor: true)
        } else {
            updateFont(uiView, empty: formattedCode.isEmpty)
        }
    }

    func updateUIView(_ uiView: UITextField, context: Context) {
        applyCoreUpdate(uiView, coordinator: context.coordinator)
    }

    private func updateFont(_ tf: UITextField, empty: Bool) {
        if empty {
            tf.font = UIFont.preferredFont(forTextStyle: .body)
        } else {
            let base = UIFont.preferredFont(forTextStyle: .title2)
            tf.font = UIFont.monospacedSystemFont(ofSize: base.pointSize, weight: .semibold)
        }
    }

    private func applyText(_ tf: UITextField, text: String, coordinator: Coordinator, mapCursor: Bool) {
        let oldText = tf.text ?? ""
        let oldSelection = currentUTF16Selection(in: tf)
        coordinator.isUpdatingFromCore = true
        tf.text = text
        updateFont(tf, empty: text.isEmpty)
        if mapCursor, tf.isFirstResponder {
            let mapped = mapPairingCodeSelection(
                editedText: oldText,
                editedSelection: oldSelection,
                formattedText: text
            )
            setUTF16Selection(mapped, in: tf)
        }
        coordinator.isUpdatingFromCore = false
    }

    private func currentUTF16Selection(in tf: UITextField) -> (Int, Int) {
        guard let range = tf.selectedTextRange else {
            let end = (tf.text ?? "").utf16.count
            return (end, end)
        }
        let start = tf.offset(from: tf.beginningOfDocument, to: range.start)
        let end = tf.offset(from: tf.beginningOfDocument, to: range.end)
        return (start, end)
    }

    private func setUTF16Selection(_ sel: (start: Int, end: Int), in tf: UITextField) {
        let count = (tf.text ?? "").utf16.count
        let s = max(0, min(sel.start, count))
        let e = max(s, min(sel.end, count))
        if let startPos = tf.position(from: tf.beginningOfDocument, offset: s),
           let endPos = tf.position(from: tf.beginningOfDocument, offset: e) {
            tf.selectedTextRange = tf.textRange(from: startPos, to: endPos)
        }
    }

    final class Coordinator: NSObject, UITextFieldDelegate {
        var onEdit: (String) -> UInt64
        var onSubmit: () -> Void
        var lastDispatchedSeq: UInt64 = 0
        var isUpdatingFromCore = false

        init(onEdit: @escaping (String) -> UInt64, onSubmit: @escaping () -> Void) {
            self.onEdit = onEdit
            self.onSubmit = onSubmit
        }

        @objc func editingChanged(_ sender: UITextField) {
            guard !isUpdatingFromCore else { return }
            let raw = sender.text ?? ""
            lastDispatchedSeq = onEdit(raw)
        }

        func textFieldShouldReturn(_ textField: UITextField) -> Bool {
            onSubmit()
            return false
        }
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
