// The message pill (docs/design-system.md §6 「输入框」): ＋ at the left, the
// text from one line up to six, and one round button at the right. A
// session's message box and a project's new-session box are both this
// pill; what the button at the right is and does is theirs.
import SwiftUI

/// The round button at the right of the pill: a 32 pt circle, 44 pt to the
/// finger. The circle keeps its size; what is drawn in it does not outgrow it.
struct ComposerCircle<Icon: View>: View {
    private let fill: Color
    private let enabled: Bool
    private let icon: Icon

    init(_ fill: Color, enabled: Bool, @ViewBuilder icon: () -> Icon) {
        self.fill = fill
        self.enabled = enabled
        self.icon = icon()
    }

    var body: some View {
        icon
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .frame(width: 32, height: 32)
            .background(fill, in: Circle())
            // Nothing to send looks as any button that is not enabled does (design-system §6).
            .opacity(enabled ? 1 : 0.4)
            .frame(width: Theme.minHit, height: Theme.minHit)
            .contentShape(Rectangle())
    }
}

extension ComposerCircle where Icon == Ic {
    /// The send button: the arrow on the one solid blue of the screen.
    static func send(enabled: Bool) -> ComposerCircle {
        ComposerCircle(Theme.blueBg, enabled: enabled) { Ic(Lucide.arrowUp, size: 20, color: Theme.onSolid) }
    }
}

/// The pill. `top` stands inside it above the text, as the images of a
/// draft; `trailing` is the button at the right.
struct ComposerPill<Top: View, Trailing: View>: View {
    /// How the plus stands: `dimmed` still answers a tap (to say why it cannot add now), `off` does not.
    enum Plus: Sendable { case on, dimmed, off }

    @Binding private var text: String
    private let placeholder: String
    private let label: String
    private let identifier: String
    private let plus: Plus
    private let focus: FocusState<Bool>.Binding
    private let onPlus: () -> Void
    private let top: Top
    private let trailing: Trailing

    /// `label` names the field for VoiceOver; `identifier` for a script.
    init(
        text: Binding<String>, placeholder: String, label: String, identifier: String = "", plus: Plus = .on,
        focus: FocusState<Bool>.Binding, onPlus: @escaping () -> Void, @ViewBuilder top: () -> Top,
        @ViewBuilder trailing: () -> Trailing
    ) {
        _text = text
        self.placeholder = placeholder
        self.label = label
        self.identifier = identifier
        self.plus = plus
        self.focus = focus
        self.onPlus = onPlus
        self.top = top()
        self.trailing = trailing()
    }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: 24, style: .continuous)
        VStack(spacing: 0) {
            top
            HStack(alignment: .bottom, spacing: 0) {
                Button(action: onPlus) { Ic(Lucide.plus, size: 22, color: Theme.muted).hitTarget() }
                    .buttonStyle(PressStyle())
                    .disabled(plus == .off)
                    .opacity(plus == .on ? 1 : 0.4)
                    // The two buttons keep a size a thumb knows; the words between them are what grows.
                    .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                    .accessibilityLabel("添加附件")
                TextField("", text: $text, prompt: Text(placeholder).foregroundStyle(Theme.muted), axis: .vertical)
                    .lineLimit(1...6)
                    .type(T.body, Theme.fg)
                    .tint(Theme.blue)
                    .focused(focus)
                    .accessibilityLabel(label)
                    .accessibilityIdentifier(identifier)
                    .padding(.horizontal, 4).padding(.vertical, 10)
                    .frame(maxWidth: .infinity, minHeight: Theme.minHit, alignment: .leading)
                    // The whole of the pill between the buttons is the field to the finger, not just the line of text.
                    .contentShape(Rectangle())
                    .onTapGesture { focus.wrappedValue = true }
                trailing
            }
        }
        .padding(3)
        .background(Theme.card, in: shape)
        .overlay { shape.strokeBorder(Theme.line, lineWidth: 1) }
        .padding(.init(top: 4, leading: 10, bottom: 8, trailing: 10))
    }
}

extension ComposerPill where Top == EmptyView {
    init(
        text: Binding<String>, placeholder: String, label: String, identifier: String = "", plus: Plus = .on,
        focus: FocusState<Bool>.Binding, onPlus: @escaping () -> Void, @ViewBuilder trailing: () -> Trailing
    ) {
        self.init(
            text: text, placeholder: placeholder, label: label, identifier: identifier, plus: plus, focus: focus, onPlus: onPlus,
            top: { EmptyView() }, trailing: trailing)
    }
}
