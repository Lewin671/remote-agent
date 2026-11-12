// The bottom of the two first-level pages (design-system §6 「底部」): the
// two tabs, 会话 and 定时, in one capsule of glass at the left, and at the
// right the ink button that says what it makes, 新任务 or 新定时. There is
// no tab bar under them: the page runs to the screen's edge and fades out
// there. 设置 is not a tab; the gear on the home page opens it.
import SwiftUI

/// A tab's mark: how many wait for you, in orange, or the blue dot of new results.
struct TabBadge: Equatable, Sendable {
    var count = 0
    /// New results (定时 only), when nothing waits.
    var dot = false
    /// What waits is 件 on 定时 and 个 on 会话.
    enum Unit: Sendable { case sessions, tasks }
    var unit = Unit.sessions

    /// The number as drawn: 99+ past 99.
    var text: String { count > 99 ? "99+" : String(count) }

    /// What VoiceOver says after the tab's name.
    var spoken: String {
        if count > 0 { return "\(count) \(unit == .tasks ? "件" : "个")需要处理" }
        return dot ? "有新结果" : ""
    }
}

struct BottomBar: View {
    @Binding var tab: MainTab
    let badges: [MainTab: TabBadge]
    /// 新任务 or 新定时 can be started now: not while the server is out of reach, nor without a computer.
    let canCreate: Bool
    let onCreate: () -> Void

    var body: some View {
        HStack(alignment: .bottom, spacing: 12) {
            GlassGroup {
                HStack(spacing: 0) {
                    item(.home, "会话", "message")
                    item(.schedules, "定时", "clock")
                }
                .padding(4)
                .glass(Capsule(), interactive: false)
            }
            Spacer(minLength: 0)
            InkButton(tab == .schedules ? "新定时" : "新任务", symbol: tab == .schedules ? "plus" : "square.and.pencil", floating: true, action: onCreate)
                .disabled(!canCreate)
                .accessibilityIdentifier("new-button")
        }
        .padding(.horizontal, 20)
        .padding(.top, 8)
        .padding(.bottom, 4)
        // The bar keeps its size past the largest standard text size; a long press shows a tab's name large, as the system's tab bar does.
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
    }

    private func item(_ which: MainTab, _ label: String, _ symbol: String) -> some View {
        let on = tab == which
        let badge = badges[which] ?? TabBadge()
        return Button {
            if !on { tab = which }
        } label: {
            VStack(spacing: 2) {
                Image(systemName: symbol).font(.title3)
                Text(label).font(.system(size: 10, weight: .semibold))
            }
            .foregroundStyle(on ? DS.ink : DS.ink2)
            .frame(width: 76, height: 52)
            .background { if on { Capsule().fill(DS.fill2) } }
            .overlay(alignment: .topLeading) { BadgeMark(badge: badge).offset(x: 44, y: 5) }
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
        .accessibilityValue(badge.spoken)
        .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
        .accessibilityShowsLargeContentViewer {
            Image(systemName: symbol)
            Text(label)
        }
        .accessibilityIdentifier("tab-" + which.rawValue)
    }
}

/// The count on a tab: an orange capsule with the number (`orangeInk` under
/// white in the light theme, `orange` under dark text in the dark, for the
/// contrast), or a blue dot for new results.
private struct BadgeMark: View {
    let badge: TabBadge
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        if badge.count > 0 {
            Text(badge.text)
                .font(.system(size: 12, weight: .bold))
                .monospacedDigit()
                .foregroundStyle(scheme == .dark ? Color(white: 0.11) : .white)
                .padding(.horizontal, 5)
                .frame(minWidth: 18, minHeight: 18)
                .background(scheme == .dark ? DS.orange : DS.orangeInk, in: Capsule())
                .fixedSize()
        } else if badge.dot {
            Circle().fill(DS.blue).frame(width: 9, height: 9).offset(x: 3, y: 3)
        }
    }
}
