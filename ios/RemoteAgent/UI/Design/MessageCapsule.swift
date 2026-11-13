// The message box (design-system §6 「输入框」): a capsule of glass floating
// at the bottom, ＋ on a `fill` circle at its left, the text from one line
// up to about six, and one ink circle at its right: send, or ■ to
// interrupt. What ＋ offers and what the circle does are the caller's.
import SwiftUI

/// The round ink button at the right of the capsule: ↑ to send, ■ to
/// interrupt. With nothing to send it is `fill2` with an `ink3` arrow: not
/// yet (design-system §6).
struct InkCircle: View {
    let symbol: String
    var enabled = true
    @ScaledMetric(relativeTo: .body) private var side: CGFloat = 36

    var body: some View {
        Image(systemName: symbol)
            .font(.body.weight(.semibold))
            .foregroundStyle(enabled ? DS.btnFg : DS.ink3)
            // ink and grey, and ↑ and ■, change by cross-fading (§4 动效, 快)
            .contentTransition(.opacity)
            .frame(width: side, height: side)
            .background(enabled ? DS.btn : DS.fill2, in: Circle())
            .animation(Motion.enter(Motion.fast), value: enabled)
            .animation(Motion.enter(Motion.fast), value: symbol)
            .frame(minWidth: DS.minHit, minHeight: DS.minHit)
            .contentShape(Rectangle())
    }
}

/// ＋ at the left of the capsule, on a `fill` circle; dimmed when it can only say why nothing can be added now.
struct PlusCircle: View {
    var dimmed = false
    @ScaledMetric(relativeTo: .body) private var side: CGFloat = 36

    var body: some View {
        Image(systemName: "plus")
            .font(.body.weight(.medium))
            .foregroundStyle(DS.ink)
            .frame(width: side, height: side)
            .background(DS.fill, in: Circle())
            .opacity(dimmed ? 0.4 : 1)
            .frame(minWidth: DS.minHit, minHeight: DS.minHit)
            .contentShape(Rectangle())
    }
}

/// The capsule. `top` stands inside it above the text (a draft's images
/// and files); `plus` and `trailing` are the buttons at either end.
struct MessageCapsule<Top: View, Plus: View, Trailing: View>: View {
    @Binding var text: String
    let placeholder: String
    /// Names the field for VoiceOver; `identifier` for a script.
    let label: String
    var identifier = ""
    let focus: FocusState<Bool>.Binding
    /// Changes when the capsule's height will: its height follows in 200 ms (§4 动效) instead of jumping.
    var heightChanges: AnyHashable = 0
    @ViewBuilder let top: Top
    @ViewBuilder let plus: Plus
    @ViewBuilder let trailing: Trailing

    var body: some View {
        VStack(spacing: 0) {
            top
            HStack(alignment: .bottom, spacing: 2) {
                // The two buttons keep a size a thumb knows; the words between them are what grows.
                plus.dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                WrappingPlaceholder(text: placeholder, empty: text.isEmpty, font: .body, color: DS.ink3) { prompt in
                    TextField("", text: $text, prompt: prompt, axis: .vertical).lineLimit(1...6)
                        .focused(focus)
                        .accessibilityLabel(label).accessibilityIdentifier(identifier)
                }
                .font(.body)
                .foregroundStyle(DS.ink)
                .tint(DS.blue)
                .padding(.horizontal, 4).padding(.vertical, 11)
                .frame(maxWidth: .infinity, minHeight: DS.minHit, alignment: .leading)
                // The whole of the capsule between the buttons is the field to the finger.
                .contentShape(Rectangle())
                .onTapGesture { focus.wrappedValue = true }
                trailing.dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            }
        }
        .animation(Motion.enter(), value: heightChanges)
        .padding(4)
        .glass(RoundedRectangle(cornerRadius: 26, style: .continuous), interactive: false)
        .padding(.horizontal, DS.Space.m)
        .padding(.top, 4).padding(.bottom, 8)
    }
}

/// The message box in its taller form (design-system §6 「新任务」): the
/// same glass as the capsule, with the text on top, what is attached under
/// it, and under that a row of `accessories` (＋, what the message goes
/// with, and the round send button). The text grows to about eight lines,
/// then scrolls inside the box.
struct MessageCard<Below: View, Accessories: View>: View {
    @Binding var text: String
    let placeholder: String
    /// Names the field for VoiceOver; `identifier` for a script.
    let label: String
    var identifier = ""
    let focus: FocusState<Bool>.Binding
    @ViewBuilder let below: Below
    @ViewBuilder let accessories: Accessories

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            TextField("", text: $text, prompt: Text(placeholder).foregroundStyle(DS.ink3), axis: .vertical)
                .lineLimit(1...8)
                .font(.body)
                .foregroundStyle(DS.ink)
                .tint(DS.blue)
                .focused(focus)
                .accessibilityLabel(label)
                .accessibilityIdentifier(identifier)
                .padding(.horizontal, 12).padding(.top, 12).padding(.bottom, 6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
                .onTapGesture { focus.wrappedValue = true }
            below
            HStack(spacing: 2) {
                accessories
            }
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        }
        .animation(Motion.enter(), value: text.isEmpty)
        .padding(4)
        .glass(RoundedRectangle(cornerRadius: 22, style: .continuous), interactive: false)
        .padding(.horizontal, DS.Space.m)
        .padding(.top, 4).padding(.bottom, 8)
    }
}
