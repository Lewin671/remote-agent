// What floats under a session's title (design-system §6 「会话顶部与视图切换」
// 「提示条」): the glass segmented control 聊天 / 终端, and the strips of
// glass under it, for the connection, a schedule, the terminal's size.
import SwiftUI

/// A segmented control on glass: the chosen one is a light slider (`paper`,
/// mid grey in the dark), never ink. A choice that cannot be made now is
/// greyed; `dot` marks one that wants a look (a popup in the terminal).
struct GlassSegmented: View {
    let items: [String]
    @Binding var selected: Int
    var enabled: (Int) -> Bool = { _ in true }
    var dot: Int?
    @Namespace private var slider
    @Environment(\.colorScheme) private var scheme
    @Environment(\.accessibilityReduceMotion) private var still

    var body: some View {
        GlassGroup {
            HStack(spacing: 0) {
                ForEach(items.indices, id: \.self) { i in
                    let on = i == selected
                    let can = enabled(i)
                    Button {
                        guard can, !on else { return }
                        withAnimation(still ? nil : .snappy(duration: 0.25)) { selected = i }
                    } label: {
                        HStack(spacing: 4) {
                            Text(items[i])
                            if dot == i && !on { StatusMark(.waiting) }
                        }
                        .font(.footnote.weight(on ? .semibold : .regular))
                        .foregroundStyle(can ? DS.ink : DS.ink3)
                        .padding(.horizontal, 18)
                        .frame(minWidth: 64, minHeight: 32)
                        .background {
                            if on {
                                Capsule().fill(scheme == .dark ? Color(white: 0.39) : DS.paper)
                                    .shadow(color: .black.opacity(0.08), radius: 2, y: 1)
                                    .matchedGeometryEffect(id: "slider", in: slider)
                            }
                        }
                        .contentShape(Capsule())
                    }
                    .buttonStyle(.plain)
                    .disabled(!can)
                    .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
                }
            }
            .padding(4)
            .glass(Capsule(), interactive: false)
        }
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
    }
}

/// A strip of glass under the title: an icon, one sentence (13, `ink2`), at
/// most one text action. `tone` `.attention` is the orange one (a security
/// reminder); `.error` writes the sentence red.
struct GlassStrip: View {
    let text: String
    var symbol: String?
    /// Draws the system's spinner in place of an icon: 正在重连….
    var spinning = false
    var tone: NoticeTone = .plain
    var action: String?
    var onAction: (() -> Void)?

    var body: some View {
        let ink: Color = switch tone {
        case .plain: DS.ink2
        case .error: DS.red
        case .attention: DS.orangeInk
        }
        HStack(spacing: 8) {
            if spinning {
                ProgressView().controlSize(.mini)
            } else if let symbol {
                Image(systemName: symbol).foregroundStyle(ink).accessibilityHidden(true)
            }
            Text(text).foregroundStyle(ink).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
            if let action, let onAction {
                LinkButton(action, action: onAction).fontWeight(.semibold).fixedSize()
            }
        }
        .font(.footnote)
        .padding(.leading, 14).padding(.trailing, action == nil ? 14 : 10)
        .frame(minHeight: 36)
        .background {
            if tone == .attention { Capsule().fill(DS.orangeSoft) }
        }
        .glass(Capsule(), interactive: false)
        .accessibilityElement(children: .combine)
    }
}
