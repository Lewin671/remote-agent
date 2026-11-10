// Buttons (docs/design-system.md §6). A screen has at most one solid
// button, the thing most worth doing there (§1).
import SwiftUI

enum BtnKind: Sendable {
    /// The screen's one main action: the ink capsule.
    case primary
    /// 取消, 再试一次: a `fill2` capsule, ink text.
    case secondary
    /// 拒绝: the secondary capsule with red text.
    case negative
    /// Solid red, only in a confirmation.
    case danger
}

/// A button: a capsule at least 48 tall, text 17 semibold. Not enabled, the whole of it dims to 40 %; its colours do not change.
struct Btn: View {
    private let text: String
    private let kind: BtnKind
    private let enabled: Bool
    private let background: Color?
    private let fill: Bool
    private let action: () -> Void

    /// `background` replaces a secondary button's surface, as `card` inside a grey card; `fill` takes the width offered.
    init(_ text: String, kind: BtnKind = .secondary, enabled: Bool = true, background: Color? = nil, fill: Bool = true, action: @escaping () -> Void) {
        self.text = text
        self.kind = kind
        self.enabled = enabled
        self.background = background
        self.fill = fill
        self.action = action
    }

    var body: some View {
        let (back, fore): (Color, Color) = switch kind {
        case .primary: (DS.btn, DS.btnFg)
        case .secondary: (DS.fill2, DS.ink)
        case .negative: (DS.fill2, DS.red)
        case .danger: (DS.red, Color.white)
        }
        Button(action: action) {
            // The words may wrap at large text sizes; the button grows with them (design-system §6).
            Text(text).font(.body.weight(.semibold)).ink(fore)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 20).padding(.vertical, 10)
                .frame(maxWidth: fill ? .infinity : nil, minHeight: 48)
                .background(background ?? back, in: Capsule())
                .contentShape(Capsule())
        }
        .buttonStyle(PressStyle())
        .disabled(!enabled)
        .opacity(enabled ? 1 : 0.4)
    }
}

/// Buttons sharing the width, 8 pt apart: side by side, or one under the other at accessibility text sizes.
struct Btns<Content: View>: View {
    private let content: Content
    @Environment(\.dynamicTypeSize) private var typeSize

    init(@ViewBuilder content: () -> Content) { self.content = content() }

    var body: some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(spacing: 8)) : AnyLayout(HStackLayout(spacing: 8))
        layout { content }
    }
}

/// A text action: blue, or red for 中断; 44 pt tall to the finger. One that goes to another page ends in an arrow.
struct TextAction: View {
    private let text: String
    private let color: Color
    private let style: Font
    private let enabled: Bool
    private let alignment: Alignment
    private let arrow: Bool
    private let action: () -> Void

    init(_ text: String, color: Color = DS.blue, style: Font = .subheadline.weight(.semibold), enabled: Bool = true, alignment: Alignment = .leading, arrow: Bool = false, action: @escaping () -> Void) {
        self.text = text
        self.color = color
        self.style = style
        self.enabled = enabled
        self.alignment = alignment
        self.arrow = arrow
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            Group {
                if arrow { ArrowText(text, color: color, style: style) } else { Text(text).font(style).ink(color) }
            }
            // A few words that keep their line: squeezed beside something long, they are never set a character to a line.
            .lineLimit(1).fixedSize()
            .hitTarget(alignment: alignment)
        }
        .buttonStyle(PressStyle())
        .disabled(!enabled)
        .opacity(enabled ? 1 : 0.45)
    }
}

/// An icon that is a button by itself: 44 × 44 to the finger, and `label` says what it does.
struct IconButton: View {
    private let icon: String
    private let label: String
    private let size: CGFloat
    private let color: Color
    private let action: () -> Void

    init(_ icon: String, label: String, size: CGFloat = 22, color: Color = DS.ink, action: @escaping () -> Void) {
        self.icon = icon
        self.label = label
        self.size = size
        self.color = color
        self.action = action
    }

    var body: some View {
        Button(action: action) { SymbolImage(icon, size: size, color: color).hitTarget() }
            .buttonStyle(PressStyle())
            .accessibilityLabel(label)
    }
}
