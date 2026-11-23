// 新会话 · 选项目: rendered from `manageProjects.listing`.
import RACore
import RAProto
import SwiftUI

/// The projects as a pick list, each with an arrow to open or select it.
struct CoreProjectPickList: View {
    let listing: PBCoreProjectListingView
    let onSearch: (String) -> Void
    let onPick: (String) -> Void
    let onNew: () -> Void

    var body: some View {
        CoreProjectList(listing: listing, onSearch: onSearch, onNew: onNew) { pr in
            CoreProjectRow(row: pr, action: { onPick(pr.key) }) { Arrow(DS.ink3, style: .body) }
        }
    }
}

/// 新会话 · 选项目 as a sheet from Home; 新项目… turns it into 新项目 · 选目录.
struct PickProjectSheet: View {
    let state: RemoteState
    @Binding var browsing: Bool
    let onClose: () -> Void
    let onPick: (NewTaskTarget) -> Void
    @Environment(AccountContext.self) private var context

    var body: some View {
        let listing = context.core.manageProjects.listing
        Group {
            if browsing {
                SheetHead(L10n.projectsNewChooseDirectory(), onBack: { browsing = false })
                CoreDirPicker(pickerKey: "pick-project", forNewTask: true) { machine, path in
                    onPick(NewTaskTarget(key: path, machine: machine))
                }
            } else {
                SheetHead(L10n.newSessionIosChooseProject(), action: L10n.commonCancel(), onAction: onClose)
                CoreProjectPickList(
                    listing: listing,
                    onSearch: { q in context.dispatch { $0.projectSetSearch.query = q } },
                    onPick: { onPick(NewTaskTarget(key: $0)) },
                    onNew: { browsing = true }
                )
            }
        }
        .task { await context.coreHost.setManageActive(true) }
        .onDisappear { Task { await context.coreHost.setManageActive(false) } }
    }
}

/// 新会话 · 选项目 as a pushed page.
struct PickProjectScreen: View {
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context

    var body: some View {
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        CoreProjectPickList(
                            listing: context.core.manageProjects.listing,
                            onSearch: { q in context.dispatch { $0.projectSetSearch.query = q } },
                            onPick: { nav.open(.project(key: $0, focusNew: true)) },
                            onNew: { nav.open(.newProject) }
                        )
                    }
                    .padding(.horizontal, DS.Space.l).padding(.bottom, 16)
                    .readableColumn()
                }
                .scrollDismissesKeyboard(.immediately)
            }
        }
        .navBar(L10n.newSessionIosChooseProject())
        .task { await context.coreHost.setManageActive(true) }
        .onDisappear { Task { await context.coreHost.setManageActive(false) } }
        .homeLaunchTweaks()
    }
}
