// Choosing one of a few (docs/design-system.md §6): underlined tabs switch
// a view (聊天 / 终端), a segmented pill picks one value of a setting (自动 /
// 亮色 / 暗色), and the tab bar switches between the first-level pages.
import SwiftUI

/// Tabs that switch a view: underlined, on phones and wide screens alike.
struct ViewTabs: View {
    private let items: [String]
    @Binding private var selected: Int
    private let enabled: (Int) -> Bool
    private let dot: Int?
    private let dark: Bool
    private let inset: CGFloat

    /// `dot` marks a tab that needs you; `dark` is for the terminal's own colours; `inset` is the space at the sides, none inside a sheet.
    init(_ items: [String], selected: Binding<Int>, enabled: @escaping (Int) -> Bool = { _ in true }, dot: Int? = nil, dark: Bool = false, inset: CGFloat = 16) {
        self.items = items
        _selected = selected
        self.enabled = enabled
        self.dot = dot
        self.dark = dark
        self.inset = inset
    }

    var body: some View {
        HStack(spacing: 0) {
            ForEach(items.indices, id: \.self) { i in
                let on = i == selected
                let ok = enabled(i)
                let ink = dark ? (on ? Theme.Term.fg : Theme.Term.dim) : (on ? Theme.fg : Theme.muted)
                Button {
                    if !on { selected = i }
                } label: {
                    HStack(spacing: 3) {
                        Text(items[i]).type(on ? T.smB : T.sm, ink)
                        if i == dot { Dot(Theme.orange) }
                    }
                    .frame(maxWidth: .infinity, minHeight: Theme.minHit)
                    .overlay(alignment: .bottom) {
                        if on { Rectangle().fill(dark ? Theme.Term.fg : Theme.fg2).frame(height: 2) }
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .disabled(!ok)
                .opacity(ok ? 1 : 0.4)
                .accessibilityAddTraits(on ? .isSelected : [])
                .accessibilityValue(i == dot ? "需要处理" : "")
            }
        }
        .bottomLine(dark ? Theme.Term.line : Theme.line)
        .padding(.horizontal, inset).padding(.bottom, 4)
        .accessibilityElement(children: .contain)
    }
}

/// A segmented pill for the values of one setting: the groove is `seg`, the chosen one a raised card.
struct SegPick: View {
    private let items: [String]
    @Binding private var selected: Int
    @Environment(\.colorScheme) private var scheme

    init(_ items: [String], selected: Binding<Int>) {
        self.items = items
        _selected = selected
    }

    var body: some View {
        HStack(spacing: 2) {
            ForEach(items.indices, id: \.self) { i in
                let on = i == selected
                Button {
                    if !on {
                        selected = i
                        Haptics.selection()
                    }
                } label: {
                    Text(items[i]).type(on ? T.smB : T.sm, on ? Theme.fg : Theme.muted)
                        .lineLimit(1)
                        .padding(.horizontal, 10)
                        .frame(minWidth: 56, minHeight: 32)
                        .background {
                            if on {
                                RoundedRectangle(cornerRadius: 8, style: .continuous)
                                    .fill(scheme == .dark ? Theme.track : Theme.card)
                                    .shadow(color: .black.opacity(scheme == .dark ? 0 : 0.08), radius: 1.5, y: 1)
                            }
                        }
                        // The pill is 36 pt tall; each segment is 44 to the finger.
                        .frame(minHeight: Theme.minHit)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
        }
        .padding(.horizontal, 2)
        .background {
            RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous).fill(Theme.seg).padding(.vertical, 4)
        }
        // As wide as its words: beside a label it is never squeezed into dots; a row too narrow for both puts it underneath.
        .fixedSize()
        .accessibilityElement(children: .contain)
    }
}

/// One first-level page in the tab bar; `badge` is the count of sessions waiting for you.
struct TabBarItem: Identifiable, Sendable {
    let id: String
    let label: String
    let icon: LucideIcon
    var badge = 0

    init(id: String, label: String, icon: LucideIcon, badge: Int = 0) {
        self.id = id
        self.label = label
        self.icon = icon
        self.badge = badge
    }
}

/// 会话 and 设置 at the bottom of the two first-level pages: 11 labels, blue when chosen, the waiting count in orange.
struct TabBar: View {
    private let items: [TabBarItem]
    @Binding private var selected: String
    /// The icon's side as `Ic` scales it, for placing the count on its corner.
    @ScaledMetric(relativeTo: .body) private var iconSize: CGFloat = 22

    init(_ items: [TabBarItem], selected: Binding<String>) {
        self.items = items
        _selected = selected
    }

    var body: some View {
        HStack(spacing: 0) {
            ForEach(items) { item in
                let on = item.id == selected
                Button {
                    if !on { selected = item.id }
                } label: {
                    VStack(spacing: 2) {
                        Ic(item.icon, size: 22, color: on ? Theme.blue : Theme.muted, relativeTo: .body)
                            // The count sits on the icon's top right corner, 5 pt in and 4 pt up, at any text size.
                            .overlay(alignment: .topLeading) {
                                if item.badge > 0 {
                                    Text("\(item.badge)").type(T.tab.with(weight: .bold), Theme.onSolid)
                                        .padding(.horizontal, 5).frame(minHeight: 16)
                                        .background(Theme.orange, in: Capsule())
                                        .fixedSize()
                                        .offset(x: iconSize - 5, y: -4)
                                }
                            }
                        Text(item.label).type(T.tab, on ? Theme.blue : Theme.muted)
                    }
                    // Room above for the count, which stands 4 pt over the icon.
                    .padding(.top, 2)
                    .frame(maxWidth: .infinity, minHeight: Theme.minHit)
                    .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(item.label)
                .accessibilityValue(item.badge > 0 ? "\(item.badge) 个需要处理" : "")
                .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
            }
        }
        .padding(.top, 6)
        .background(Theme.card.ignoresSafeArea(edges: .bottom))
        .topLine()
    }
}

/// Choices laid out three to a row (推理强度); one to a row at accessibility text sizes.
struct ChoiceGrid: View {
    private let items: [String]
    private let selected: String?
    private let enabled: Bool
    private let onPick: (String) -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    init(_ items: [String], selected: String?, enabled: Bool = true, onPick: @escaping (String) -> Void) {
        self.items = items
        self.selected = selected
        self.enabled = enabled
        self.onPick = onPick
    }

    var body: some View {
        let columns = Array(repeating: GridItem(.flexible(), spacing: 6), count: typeSize.isAccessibilitySize ? 1 : 3)
        LazyVGrid(columns: columns, spacing: 6) {
            ForEach(items, id: \.self) { item in
                let on = item == selected
                let shape = RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous)
                Button {
                    onPick(item)
                } label: {
                    Text(item).type(T.sm, on ? Theme.blue : enabled ? Theme.fg : Theme.muted)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 6)
                        .frame(maxWidth: .infinity, minHeight: Theme.minHit)
                        .background(on ? Theme.blueSoft : Theme.soft, in: shape)
                        // The chosen one: blue-soft with a 1 pt blue outline (design-system §4).
                        .overlay { if on { shape.strokeBorder(Theme.blue, lineWidth: 1) } }
                        .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .disabled(!enabled)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
        }
    }
}

/// The context box of 会话设置: the percent, what it is of, and one action (压缩).
struct ContextBox: View {
    private let percent: Int
    private let detail: String
    private let action: String?
    private let enabled: Bool
    private let onAction: () -> Void

    init(percent: Int, detail: String, action: String? = nil, enabled: Bool = true, onAction: @escaping () -> Void = {}) {
        self.percent = percent
        self.detail = detail
        self.action = action
        self.enabled = enabled
        self.onAction = onAction
    }

    var body: some View {
        HStack(spacing: 8) {
            VStack(alignment: .leading, spacing: 0) {
                // Nearly full is the one case a count turns orange (design-system §1).
                Text("\(percent)%").type(T.h22, percent > 80 ? Theme.orangeInk : Theme.fg)
                Text(detail).type(T.sm, Theme.muted)
            }
            .accessibilityElement(children: .combine)
            Spacer(minLength: 0)
            if let action {
                TextAction(action, color: enabled ? Theme.blue : Theme.muted, style: T.bodyB, enabled: enabled, alignment: .center, action: onAction)
            }
        }
        .padding(12)
        .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
    }
}
