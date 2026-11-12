// 新会话 · 选项目: which project a new session goes to, when the home page
// lists every project (Android's PickProjectSheet and PickProjectScreen in
// ui/Project.kt). The list is the one every place shows (`ProjectList`),
// each row leading to that project's new-session box.
import RACore
import SwiftUI

/// The projects as every list shows them, each with an arrow to its new-session box.
struct ProjectPickList: View {
    let state: RemoteState
    let onPick: (String) -> Void
    let onNew: () -> Void

    var body: some View {
        ProjectList(state, onNew: onNew) { pr in
            ProjectRow(state, pr, action: { onPick(pr.key) }) { Arrow(DS.ink3, style: .body) }
        }
    }
}

/// 新会话 · 选项目 from the new-session button on the home page, as a
/// sheet; 新项目… turns it into 新项目 · 选目录. Choosing a project, or a
/// folder there, opens 新任务 in it.
struct PickProjectSheet: View {
    let state: RemoteState
    @Binding var browsing: Bool
    let onClose: () -> Void
    /// The project or folder chosen: 新任务 opens in it.
    let onPick: (NewTaskTarget) -> Void

    var body: some View {
        if browsing {
            SheetHead(L10n.projectsNewChooseDirectory(), onBack: { browsing = false })
            ProjectDirPicker(state) { machine, path in onPick(NewTaskTarget(key: path, machine: machine)) }
        } else {
            SheetHead(L10n.newSessionIosChooseProject(), action: L10n.commonCancel(), onAction: onClose)
            ProjectPickList(
                state: state,
                onPick: { onPick(NewTaskTarget(key: $0)) },
                onNew: { browsing = true })
        }
    }
}

/// 新会话 · 选项目 as a page, for links that name the route.
struct PickProjectScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav

    var body: some View {
        let state = model.state
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        ProjectPickList(
                            state: state, onPick: { nav.open(.project(key: $0, focusNew: true)) }, onNew: { nav.open(.newProject) })
                    }
                    .padding(.horizontal, DS.Space.l).padding(.bottom, 16)
                    .readableColumn()
                }
                .scrollDismissesKeyboard(.immediately)
            }
        }
        .navBar(L10n.newSessionIosChooseProject())
        .homeLaunchTweaks()
    }
}
