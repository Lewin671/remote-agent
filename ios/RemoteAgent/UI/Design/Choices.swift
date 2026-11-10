// Choosing one of a few (docs/design-system.md §6): a segmented pill picks
// one value of a setting (自动 / 亮色 / 暗色). The view switch 聊天 / 终端 is
// `GlassSegmented`.
import SwiftUI

/// A segmented control for the values of one setting: a `fill2` capsule, the chosen one a light slider (`paper`, mid grey in the dark), never ink.
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
                    Text(items[i]).font(on ? .subheadline.weight(.semibold) : .subheadline).ink(DS.ink)
                        .lineLimit(1)
                        .padding(.horizontal, 10)
                        .frame(minWidth: 56, minHeight: 32)
                        .background {
                            if on {
                                Capsule()
                                    .fill(scheme == .dark ? Color(white: 0.39) : DS.paper)
                                    .shadow(color: .black.opacity(scheme == .dark ? 0 : 0.08), radius: 1.5, y: 1)
                            }
                        }
                        // The pill is 36 pt tall; each segment is 44 to the finger.
                        .frame(minHeight: DS.minHit)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
        }
        .padding(.horizontal, 2)
        .background {
            Capsule().fill(DS.fill2).padding(.vertical, 4)
        }
        // As wide as its words: beside a label it is never squeezed into dots; a row too narrow for both puts it underneath.
        .fixedSize()
        .accessibilityElement(children: .contain)
    }
}

/// Choices laid out three to a row (推理强度); one to a row at accessibility text sizes.
struct ChoiceGrid: View {
    private let items: [String]
    private let selected: String?
    private let enabled: Bool
    private let onPick: (String) -> Void
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.colorScheme) private var scheme

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
                let shape = Capsule()
                Button {
                    onPick(item)
                } label: {
                    Text(item).font(on ? .subheadline.weight(.semibold) : .subheadline).ink(enabled ? DS.ink : DS.ink2)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 6)
                        .frame(maxWidth: .infinity, minHeight: DS.minHit)
                        .background(on ? (scheme == .dark ? Color(white: 0.39) : DS.paper) : DS.fill, in: shape)
                        // The chosen one is the light slider of a segmented control, no coloured outline (design-system §4).
                        .shadow(color: .black.opacity(on && scheme != .dark ? 0.08 : 0), radius: 1.5, y: 1)
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
                Text("\(percent)%").font(.title2.bold()).ink(percent > 80 ? DS.orangeInk : DS.ink)
                Text(detail).font(.subheadline).ink(DS.ink2)
            }
            .accessibilityElement(children: .combine)
            Spacer(minLength: 0)
            if let action {
                TextAction(action, color: enabled ? DS.blue : DS.ink2, style: .body.weight(.semibold), enabled: enabled, alignment: .center, action: onAction)
            }
        }
        .padding(12)
        .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
    }
}

