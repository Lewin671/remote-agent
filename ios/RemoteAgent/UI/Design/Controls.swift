import RACore
// Buttons and notices of design-system §6, built on the system's controls:
// the screen's one ink button, a blue text action, the grey notice.
import SwiftUI

/// The screen's one solid ink button (design-system §1, §6): 新任务, 新定时,
/// 添加设备 on an account without computers. A system button in an ink
/// capsule: disabled, it keeps its colours at 40 % (design-system §6), and
/// VoiceOver says it is dimmed.
struct InkButton: View {
    private let title: String
    private let symbol: String?
    private let floating: Bool
    private let action: () -> Void

    /// `floating`: it floats over the page, with the shadow of what floats.
    init(_ title: String, symbol: String? = nil, floating: Bool = false, action: @escaping () -> Void) {
        self.title = title
        self.symbol = symbol
        self.floating = floating
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                if let symbol { Image(systemName: symbol).accessibilityHidden(true) }
                Text(title)
            }
        }
        .buttonStyle(InkStyle(floating: floating))
    }
}

private struct InkStyle: ButtonStyle {
    let floating: Bool
    @Environment(\.isEnabled) private var enabled
    @Environment(\.colorScheme) private var scheme

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.headline)
            .foregroundStyle(DS.btnFg)
            .padding(.horizontal, 20)
            .frame(minHeight: 48)
            .background(DS.btn, in: Capsule())
            .contentShape(Capsule())
            .shadow(color: .black.opacity(floating && enabled ? (scheme == .dark ? 0.6 : 0.18) : 0), radius: 20, y: 16)
            .opacity(enabled ? (configuration.isPressed ? 0.75 : 1) : 0.4)
            .scaleEffect(configuration.isPressed ? 0.97 : 1)
            .animation(Motion.enter(Motion.fast), value: configuration.isPressed)
    }
}

/// A blue text action: 重试, 详情, 「还有 3 件」. At least 44 tall to the finger; `danger` makes it red.
struct LinkButton: View {
    private let title: String
    private let chevron: Bool
    private let danger: Bool
    private let action: () -> Void

    init(_ title: String, chevron: Bool = false, danger: Bool = false, action: @escaping () -> Void) {
        self.title = title
        self.chevron = chevron
        self.danger = danger
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            HStack(spacing: 2) {
                Text(title)
                if chevron { Image(systemName: "chevron.right").imageScale(.small).accessibilityHidden(true) }
            }
            .foregroundStyle(danger ? DS.red : DS.blue)
            .frame(minHeight: DS.minHit)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}

/// How a notice reads (design-system §6 「提示条」).
enum NoticeTone: Sendable {
    /// `ink2` on grey.
    case plain
    /// A failure: red text on grey.
    case error
    /// Wants you to act: `orangeInk` on `orangeSoft`.
    case attention
}

/// A notice in a page: 13, on grey with a radius of 18, at most one text action at its right.
struct Notice: View {
    private let text: String
    private let tone: NoticeTone
    private let symbol: String?
    private let action: String?
    private let onAction: (() -> Void)?

    init(_ text: String, tone: NoticeTone = .plain, symbol: String? = nil, action: String? = nil, onAction: (() -> Void)? = nil) {
        self.text = text
        self.tone = tone
        self.symbol = symbol
        self.action = action
        self.onAction = onAction
    }

    var body: some View {
        let ink: Color = switch tone {
        case .plain: DS.ink2
        case .error: DS.red
        case .attention: DS.orangeInk
        }
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 8) { message(ink); act }
            VStack(alignment: .trailing, spacing: 0) { message(ink); act }
        }
        .font(.footnote)
        .padding(.leading, 14).padding(.trailing, action == nil ? 14 : 6)
        .padding(.vertical, action == nil ? 10 : 0)
        .frame(maxWidth: .infinity, minHeight: 40, alignment: .leading)
        .background(tone == .attention ? DS.orangeSoft : DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.notice, style: .continuous))
    }

    private func message(_ ink: Color) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            if let symbol { Image(systemName: symbol).accessibilityHidden(true) }
            Text(text).fixedSize(horizontal: false, vertical: true)
        }
        .foregroundStyle(ink)
        .padding(.vertical, action == nil ? 0 : 10)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private var act: some View {
        if let action, let onAction {
            LinkButton(action, action: onAction).fontWeight(.semibold).padding(.horizontal, 8).fixedSize()
        }
    }
}

/// The title filter (design-system §6 「筛选框」): a capsule on `fill`, ⌕ at the left, ✕ at the right once something is typed.
struct FilterField: View {
    @Binding var text: String
    var prompt: String? = nil
    var resolvedPrompt: String { prompt ?? L10n.homeFilterTitle() }
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass").foregroundStyle(DS.ink3).accessibilityHidden(true)
            TextField(resolvedPrompt, text: $text)
                .focused($focused)
                .submitLabel(.search)
                .autocorrectionDisabled()
                .foregroundStyle(DS.ink)
            if !text.isEmpty {
                Button {
                    text = ""
                } label: {
                    Image(systemName: "xmark.circle.fill").foregroundStyle(DS.ink3).frame(minWidth: 28, minHeight: 40)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(L10n.homeClearFilter())
            }
        }
        .font(.body)
        .padding(.horizontal, 14)
        .frame(minHeight: 40)
        .background(DS.fill, in: Capsule())
        .contentShape(Capsule())
        .onTapGesture { focused = true }
    }
}
