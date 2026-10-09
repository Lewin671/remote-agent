// Pages (docs/design-system.md §6). Moving between them is the system's:
// a NavigationStack with its bar, its back button and the swipe from the
// edge, safe areas and keyboard avoidance as SwiftUI gives them. What the
// design adds is the surface (card, or soft for grouped settings pages),
// what the bar's middle says, and the large title of a first-level page.
import SwiftUI

/// A page's surface: card, or soft for grouped settings pages (`gray`), whose rows keep a readable column on a wide screen.
struct Page<Content: View>: View {
    private let gray: Bool
    private let content: Content

    init(gray: Bool = false, @ViewBuilder content: () -> Content) {
        self.gray = gray
        self.content = content()
    }

    var body: some View {
        Group {
            if gray { content.readableColumn() } else { content }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background((gray ? Theme.soft : Theme.card).ignoresSafeArea())
        .environment(\.onSoft, gray)
        .tint(Theme.blue)
    }
}

/// The middle of the navigation bar: the title on one line, the CLI beside
/// it, and a second line for the status. `danger` is a last part of that
/// line set in red after a " · ", as the 完全放行 of a session's mode
/// (design-system §6 「会话导航栏」); `dark` is the bar over a terminal.
struct NavTitle: View {
    private let title: String
    private let agent: String?
    private let subtitle: String?
    private let danger: String?
    private let dot: Color?
    private let subtitleColor: Color
    private let dark: Bool

    init(
        _ title: String, agent: String? = nil, subtitle: String? = nil, danger: String? = nil, dot: Color? = nil,
        subtitleColor: Color? = nil, dark: Bool = false
    ) {
        self.title = title
        self.agent = agent
        self.subtitle = subtitle
        self.danger = danger.flatMap { $0.isEmpty ? nil : $0 }
        self.dot = dot
        self.subtitleColor = subtitleColor ?? (dark ? Theme.Term.dim : Theme.muted)
        self.dark = dark
    }

    /// The second line as it is read out and drawn: the status, then the part in red.
    private var line: AttributedString? {
        guard subtitle != nil || danger != nil else { return nil }
        var line = AttributedString(subtitle ?? "")
        if let danger {
            if subtitle?.isEmpty == false { line.append(AttributedString(" · ")) }
            var red = AttributedString(danger)
            red.foregroundColor = Theme.red
            line.append(red)
        }
        return line
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 6) {
                // Always one line; a title that does not fit is cut (design-system §6).
                Text(title).type(T.bodyB, dark ? Theme.Term.fg : Theme.fg).lineLimit(1)
                if let agent { Text(agent).type(T.metaM, dark ? Theme.Term.dim : Theme.muted).lineLimit(1).layoutPriority(1) }
            }
            if let line {
                HStack(spacing: 5) {
                    if let dot { Dot(dot) }
                    Text(line).type(T.meta, subtitleColor).lineLimit(1)
                }
            }
        }
        // The bar has one line's room at the largest sizes; the page below says the rest.
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }
}

/// ⋯ at the right of a navigation bar; `dot` marks that something in its menu wants a look.
struct MoreButton: View {
    private let dot: Bool
    private let action: () -> Void

    init(dot: Bool = false, action: @escaping () -> Void) {
        self.dot = dot
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            Ic(Lucide.ellipsis, size: 22, color: Theme.blue)
                .overlay(alignment: .topTrailing) {
                    // Something in the menu wants a look, as a context that is nearly full.
                    if dot { Circle().fill(Theme.orange).frame(width: 5, height: 5).offset(x: 3, y: -1) }
                }
        }
        .accessibilityLabel("更多")
        .accessibilityValue(dot ? "有需要注意的项" : "")
    }
}

extension View {
    /// The navigation bar of a pushed page: the title, an optional status
    /// line under it whose last part may be red (`danger`), and ⋯ when it has
    /// more actions. `dark`: over a terminal, in the terminal's own text
    /// colours; the bar's surface there is the page's to set.
    func navBar(
        _ title: String, agent: String? = nil, subtitle: String? = nil, danger: String? = nil, dot: Color? = nil,
        subtitleColor: Color? = nil, dark: Bool = false, moreDot: Bool = false, onMore: (() -> Void)? = nil
    ) -> some View {
        navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    NavTitle(title, agent: agent, subtitle: subtitle, danger: danger, dot: dot, subtitleColor: subtitleColor, dark: dark)
                }
                if let onMore {
                    ToolbarItem(placement: .topBarTrailing) { MoreButton(dot: moreDot, action: onMore) }
                }
            }
    }
}

/// A first-level page's title: 设置, 加入账户, with the new-session button at its right on the home page.
struct LargeTitle: View {
    private let text: String
    private let actionLabel: String
    private let action: (() -> Void)?

    init(_ text: String, actionLabel: String = "新会话", action: (() -> Void)? = nil) {
        self.text = text
        self.actionLabel = actionLabel
        self.action = action
    }

    var body: some View {
        HStack(spacing: 8) {
            Text(text).type(T.title30, Theme.fg).padding(.vertical, 3)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityAddTraits(.isHeader)
            if let action { NewSessionButton(label: actionLabel, action: action) }
        }
        .padding(.init(top: 4, leading: 16, bottom: 2, trailing: 12))
    }
}
