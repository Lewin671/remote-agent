// The keys a phone keyboard lacks, two rows of 44 pt keys on a bar of
// dark glass, the same in either theme (design-system §4, §6 「终端按键栏」;
// Android's KeyBar in term/TerminalPane.kt). Which keys and what they send
// are the package's (`terminalKeyRows`).
//
// The bar stays put at any text size: its keys share the width evenly, so
// their words stop growing at the largest ordinary size and shrink to fit
// rather than wrap or push a key off the row.
import RACore
import SwiftUI

struct TerminalKeyBar: View {
    /// The terminal takes input; the keyboard key works either way.
    let enabled: Bool
    let onKey: (TerminalKey) -> Void
    let onKeyboard: () -> Void

    /// Two rows of keys, the space between them and around them; the same at any text size.
    static let height: CGFloat = 2 * DS.minHit + 4 + 2 * 6 + 2

    var body: some View {
        VStack(spacing: 4) {
            HStack(spacing: 4) {
                ForEach(terminalKeyRows[0], id: \.label) { key in
                    KeyButton(label: key.label, icon: icon(key), enabled: enabled) { onKey(key) }
                }
            }
            HStack(spacing: 4) {
                ForEach(terminalKeyRows[1], id: \.label) { key in
                    KeyButton(label: key.label, icon: icon(key), enabled: enabled) { onKey(key) }
                }
                KeyButton(label: L10n.terminalKeyboard(), icon: "keyboard", named: true, light: true, enabled: true, hint: L10n.terminalIosKeyboardHint(), action: onKeyboard)
            }
        }
        .padding(6)
        .glass(RoundedRectangle(cornerRadius: 24, style: .continuous), interactive: false)
        .environment(\.colorScheme, .dark)
        .padding(.horizontal, 8).padding(.bottom, 2)
        .frame(maxWidth: .infinity)
        .frame(height: Self.height)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(L10n.terminalIosKeys())
    }

    private func icon(_ key: TerminalKey) -> String? { key.icon }
}

private struct KeyButton: View {
    let label: String
    let icon: String?
    /// The icon goes before the label instead of standing for it.
    var named = false
    /// The white key, 键盘 (design-system §6 「终端按键栏」).
    var light = false
    let enabled: Bool
    var hint = ""
    let action: () -> Void

    var body: some View {
        let color = light ? Color.black : enabled ? TermColors.fg : TermColors.tool
        Button(action: action) {
            Group {
                if let icon, named {
                    HStack(spacing: 6) {
                        SymbolImage(icon, style: .subheadline, color: color)
                        Text(label).font(.subheadline).ink(color)
                    }
                } else if let icon {
                    SymbolImage(icon, size: 20, color: color, relativeTo: .footnote)
                } else {
                    Text(label).font(.subheadline).ink(color)
                }
            }
            .lineLimit(1)
            .minimumScaleFactor(0.6)
            .padding(.horizontal, 2)
            .frame(maxWidth: .infinity, minHeight: DS.minHit, maxHeight: DS.minHit)
            .background(light ? Color.white : Color.white.opacity(0.1), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .disabled(!enabled)
        .accessibilityLabel(label)
        .accessibilityHint(hint)
    }
}
