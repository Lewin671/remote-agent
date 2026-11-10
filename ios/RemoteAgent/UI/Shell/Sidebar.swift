// The wide window's sidebar (design-system §6 「宽屏」): the first-level
// page drawn on the grey window, 新任务 or 新定时 as a small ink button at
// its top, and at its bottom 会话 / 定时 as a segmented control beside the
// gear. What it opens fills the main area beside it; with nothing chosen
// that area says what to do.
import SwiftUI

/// 新任务 or 新定时 at the top of the sidebar, in place of the phone's floating button.
struct SidebarCreate {
    var enabled: Bool
    var action: () -> Void
}

extension EnvironmentValues {
    /// Set on a first-level page drawn as the sidebar.
    @Entry var sidebarCreate: SidebarCreate?
}

/// The small ink button at the top of the sidebar.
struct SidebarCreateButton: View {
    let create: SidebarCreate
    let schedules: Bool

    var body: some View {
        Button(action: create.action) {
            Label(schedules ? "新定时" : "新任务", systemImage: schedules ? "plus" : "square.and.pencil")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(DS.btnFg)
                .padding(.horizontal, 14)
                .frame(minHeight: 36)
                .background(DS.btn, in: Capsule())
                .frame(minHeight: DS.minHit)
                .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .disabled(!create.enabled)
        .opacity(create.enabled ? 1 : 0.4)
        .accessibilityIdentifier("new-button")
    }
}

/// 会话 / 定时 and the gear at the bottom of the sidebar.
struct SidebarFooter: View {
    @Binding var tab: MainTab
    @Environment(Navigator.self) private var nav

    var body: some View {
        HStack(spacing: 10) {
            GlassSegmented(items: ["会话", "定时"], selected: Binding(get: { tab == .home ? 0 : 1 }, set: { i in
                let next: MainTab = i == 0 ? .home : .schedules
                guard next != tab else { return }
                tab = next
                nav.path.removeAll()
            }))
            Spacer(minLength: 0)
            GlassIconButton("gearshape", label: "设置") { nav.open(.settings) }
        }
        .padding(.horizontal, DS.Space.card).padding(.top, 4).padding(.bottom, 8)
    }
}

/// The main area with nothing chosen in the sidebar.
struct WideEmpty: View {
    let tab: MainTab

    var body: some View {
        Text(tab == .schedules ? "选一个任务查看结果" : "选一个会话")
            .font(.subheadline).foregroundStyle(DS.ink2)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(DS.paper.ignoresSafeArea())
            .toolbar(.hidden, for: .navigationBar)
    }
}
