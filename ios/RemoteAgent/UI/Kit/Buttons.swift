// Buttons (docs/design-system.md §6). A screen has at most one solid
// button, the thing most worth doing there (§1).
import SwiftUI

enum BtnKind: Sendable {
    /// The screen's one main action: `blue-bg` solid, white text.
    case primary
    /// 取消, 再试一次: `chip`, text colour.
    case secondary
    /// 拒绝: the secondary surface with red text.
    case negative
    /// Solid red, only in a confirmation.
    case danger
}

/// A button: 44 pt, radius 10, text 15 semibold. Not enabled, the whole of it dims; its colours do not change.
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
        case .primary: (Theme.blueBg, Theme.onSolid)
        case .secondary: (Theme.chip, Theme.fg)
        case .negative: (Theme.chip, Theme.red)
        case .danger: (Theme.redBg, Theme.onSolid)
        }
        Button(action: action) {
            // The words may wrap at large text sizes; the button grows with them (design-system §6).
            Text(text).type(T.bodyB, fore)
                .multilineTextAlignment(.center)
                .padding(10)
                .frame(maxWidth: fill ? .infinity : nil, minHeight: Theme.minHit)
                .background(background ?? back, in: RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous))
                .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .disabled(!enabled)
        .opacity(enabled ? 1 : 0.45)
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
    private let style: TypeStyle
    private let enabled: Bool
    private let alignment: Alignment
    private let arrow: Bool
    private let action: () -> Void

    init(_ text: String, color: Color = Theme.blue, style: TypeStyle = T.smB, enabled: Bool = true, alignment: Alignment = .leading, arrow: Bool = false, action: @escaping () -> Void) {
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
                if arrow { ArrowText(text, color: color, style: style) } else { Text(text).type(style, color) }
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
    private let icon: LucideIcon
    private let label: String
    private let size: CGFloat
    private let color: Color
    private let action: () -> Void

    init(_ icon: LucideIcon, label: String, size: CGFloat = 22, color: Color = Theme.blue, action: @escaping () -> Void) {
        self.icon = icon
        self.label = label
        self.size = size
        self.color = color
        self.action = action
    }

    var body: some View {
        Button(action: action) { Ic(icon, size: size, color: color).hitTarget() }
            .buttonStyle(PressStyle())
            .accessibilityLabel(label)
    }
}

/// The new-session button at the right of a first-level title: a pencil on a sheet.
struct NewSessionButton: View {
    private let label: String
    private let action: () -> Void

    init(label: String = "新会话", action: @escaping () -> Void) {
        self.label = label
        self.action = action
    }

    var body: some View { IconButton(Lucide.squarePen, label: label, action: action) }
}
