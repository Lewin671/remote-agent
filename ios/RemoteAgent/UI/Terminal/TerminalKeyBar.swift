// The keys a phone keyboard lacks, two rows of 44 pt keys (client-v7;
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
    static let height: CGFloat = 2 * Theme.minHit + 4 + 6 + 8

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
                KeyButton(label: "键盘", icon: Lucide.keyboard, named: true, enabled: true, hint: "显示或收起键盘", action: onKeyboard)
            }
        }
        .padding(EdgeInsets(top: 6, leading: 8, bottom: 8, trailing: 8))
        .frame(maxWidth: .infinity)
        .frame(height: Self.height)
        .background(Theme.Term.bar)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("终端按键")
    }

    private func icon(_ key: TerminalKey) -> LucideIcon? {
        key.icon.flatMap { name in Lucide.all.first { $0.name == name } }
    }
}

private struct KeyButton: View {
    let label: String
    let icon: LucideIcon?
    /// The icon goes before the label instead of standing for it.
    var named = false
    let enabled: Bool
    var hint = ""
    let action: () -> Void

    var body: some View {
        let color = enabled ? Theme.Term.fg : Theme.Term.tool
        Button(action: action) {
            Group {
                if let icon, named {
                    HStack(spacing: 6) {
                        Ic(icon, style: T.sm, color: color)
                        Text(label).type(T.sm, color)
                    }
                } else if let icon {
                    Ic(icon, size: 20, color: color, relativeTo: .footnote)
                } else {
                    Text(label).type(T.sm, color)
                }
            }
            .lineLimit(1)
            .minimumScaleFactor(0.6)
            .padding(.horizontal, 2)
            .frame(maxWidth: .infinity, minHeight: Theme.minHit, maxHeight: Theme.minHit)
            .background(Theme.Term.key, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
            .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .disabled(!enabled)
        .accessibilityLabel(label)
        .accessibilityHint(hint)
    }
}
