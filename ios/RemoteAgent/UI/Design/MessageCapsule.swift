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

/// Positions `+` (index 0), text field (index 1), and trailing controls (index 2)
/// as either one capsule row or two rows without changing view hierarchy identity.
private struct CapsuleInputLayout: Layout {
    var twoRows: Bool

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard subviews.count == 3 else { return .zero }
        let maxW = max(0, proposal.width ?? 360)
        let plus = subviews[0].sizeThatFits(.unspecified)
        let trailing = subviews[2].sizeThatFits(.unspecified)
        if !twoRows {
            let gap: CGFloat = 2
            let textW = max(40, maxW - plus.width - trailing.width - gap * 2)
            let text = subviews[1].sizeThatFits(ProposedViewSize(width: textW, height: proposal.height))
            return CGSize(width: maxW, height: max(plus.height, max(text.height, trailing.height)))
        }
        let text = subviews[1].sizeThatFits(ProposedViewSize(width: maxW, height: proposal.height))
        let bottomH = max(plus.height, trailing.height)
        return CGSize(width: maxW, height: text.height + bottomH)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        guard subviews.count == 3 else { return }
        let plus = subviews[0].sizeThatFits(.unspecified)
        let trailing = subviews[2].sizeThatFits(.unspecified)
        if !twoRows {
            let gap: CGFloat = 2
            let textW = max(40, bounds.width - plus.width - trailing.width - gap * 2)
            let text = subviews[1].sizeThatFits(ProposedViewSize(width: textW, height: bounds.height))
            subviews[0].place(
                at: CGPoint(x: bounds.minX, y: bounds.maxY - plus.height),
                anchor: .topLeading,
                proposal: ProposedViewSize(plus)
            )
            subviews[1].place(
                at: CGPoint(x: bounds.minX + plus.width + gap, y: bounds.maxY - text.height),
                anchor: .topLeading,
                proposal: ProposedViewSize(width: textW, height: text.height)
            )
            subviews[2].place(
                at: CGPoint(x: bounds.maxX - trailing.width, y: bounds.maxY - trailing.height),
                anchor: .topLeading,
                proposal: ProposedViewSize(trailing)
            )
            return
        }
        let text = subviews[1].sizeThatFits(ProposedViewSize(width: bounds.width, height: proposal.height))
        let bottomH = max(plus.height, trailing.height)
        subviews[1].place(
            at: CGPoint(x: bounds.minX, y: bounds.minY),
            anchor: .topLeading,
            proposal: ProposedViewSize(width: bounds.width, height: text.height)
        )
        subviews[0].place(
            at: CGPoint(x: bounds.minX, y: bounds.minY + text.height + (bottomH - plus.height) / 2),
            anchor: .topLeading,
            proposal: ProposedViewSize(plus)
        )
        subviews[2].place(
            at: CGPoint(x: bounds.maxX - trailing.width, y: bounds.minY + text.height + (bottomH - trailing.height) / 2),
            anchor: .topLeading,
            proposal: ProposedViewSize(trailing)
        )
    }
}

/// The capsule. `tray` sits at the top of the card (`PendingTrayView`);
/// `top` stands above the text (a draft's images and files);
/// `plus` and `trailing` are the buttons at either end.
struct MessageCapsule<Tray: View, Top: View, Plus: View, Trailing: View>: View {
    @Binding var text: String
    let placeholder: String
    /// Names the field for VoiceOver; `identifier` for a script.
    let label: String
    var identifier = ""
    let focus: FocusState<Bool>.Binding
    /// Changes when the capsule's height will: its height follows in 200 ms (§4 动效) instead of jumping.
    var heightChanges: AnyHashable = 0
    var hasTray = false
    var twoRows = false
    @ViewBuilder let tray: Tray
    @ViewBuilder let top: Top
    @ViewBuilder let plus: Plus
    @ViewBuilder let trailing: Trailing

    init(
        text: Binding<String>,
        placeholder: String,
        label: String,
        identifier: String = "",
        focus: FocusState<Bool>.Binding,
        heightChanges: AnyHashable = 0,
        hasTray: Bool = false,
        twoRows: Bool = false,
        @ViewBuilder tray: () -> Tray,
        @ViewBuilder top: () -> Top,
        @ViewBuilder plus: () -> Plus,
        @ViewBuilder trailing: () -> Trailing
    ) {
        self._text = text
        self.placeholder = placeholder
        self.label = label
        self.identifier = identifier
        self.focus = focus
        self.heightChanges = heightChanges
        self.hasTray = hasTray
        self.twoRows = twoRows
        self.tray = tray()
        self.top = top()
        self.plus = plus()
        self.trailing = trailing()
    }

    var body: some View {
        let radius: CGFloat = (hasTray || twoRows) ? DS.Radius.card : 26
        let shape = RoundedRectangle(cornerRadius: radius, style: .continuous)
        VStack(spacing: 0) {
            tray
                .clipShape(
                    UnevenRoundedRectangle(
                        topLeadingRadius: radius,
                        bottomLeadingRadius: 0,
                        bottomTrailingRadius: 0,
                        topTrailingRadius: radius,
                        style: .continuous
                    )
                )
            top
            CapsuleInputLayout(twoRows: twoRows) {
                plus.dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                WrappingPlaceholder(text: placeholder, empty: text.isEmpty, font: .body, color: DS.ink3) { prompt in
                    TextField("", text: $text, prompt: prompt, axis: .vertical).lineLimit(1...6)
                        .focused(focus)
                        .accessibilityLabel(label).accessibilityIdentifier(identifier)
                }
                .font(.body)
                .foregroundStyle(DS.ink)
                .tint(DS.blue)
                .padding(.horizontal, twoRows ? 10 : 4)
                .padding(.top, twoRows ? 8 : 11)
                .padding(.bottom, twoRows ? 4 : 11)
                .frame(maxWidth: .infinity, minHeight: DS.minHit, alignment: .leading)
                .contentShape(Rectangle())
                .onTapGesture { focus.wrappedValue = true }
                trailing.dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            }
            .padding(4)
        }
        .animation(Motion.enter(), value: heightChanges)
        .glass(shape, interactive: false)
        .padding(.horizontal, DS.Space.m)
        .padding(.top, 4).padding(.bottom, 8)
    }
}

extension MessageCapsule where Tray == EmptyView {
    init(
        text: Binding<String>,
        placeholder: String,
        label: String,
        identifier: String = "",
        focus: FocusState<Bool>.Binding,
        heightChanges: AnyHashable = 0,
        twoRows: Bool = false,
        @ViewBuilder top: () -> Top,
        @ViewBuilder plus: () -> Plus,
        @ViewBuilder trailing: () -> Trailing
    ) {
        self.init(
            text: text,
            placeholder: placeholder,
            label: label,
            identifier: identifier,
            focus: focus,
            heightChanges: heightChanges,
            hasTray: false,
            twoRows: twoRows,
            tray: { EmptyView() },
            top: top,
            plus: plus,
            trailing: trailing
        )
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
