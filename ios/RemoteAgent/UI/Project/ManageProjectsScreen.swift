// 项目管理: rendered directly from `PBCoreManageProjectsView` (`context.core.manageProjects`).
import RACore
import RAProto
import SwiftUI

struct ManageProjectsScreen: View {
    var initialMachineId: String = ""
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var menu: PBCoreProjectRowView?
    @State private var menuShown = false
    @State private var removingKey: String? = HomeLaunch.value("-projectsRemove")
    @State private var adding = HomeLaunch.flag("-projectsAdd")
    @State private var elsewhereDismissed = false

    var body: some View {
        let manage = context.core.manageProjects
        let subtitle: String? = if !manage.filterMachineID.isEmpty {
            L10n.projectsFilterMachineSubtitle(name: manage.filterMachineName.or(manage.filterMachineID))
        } else {
            nil
        }
        ManageProjectsContent(
            manage: manage,
            showElsewhere: (manage.changedElsewhere && !elsewhereDismissed) || HomeLaunch.flag("-projectsElsewhere"),
            onDismissElsewhere: { elsewhereDismissed = true },
            onShowAllMachines: {
                context.selectProjectsMachine("")
            },
            onSearch: { q in context.dispatch { $0.projectSetSearch.query = q } },
            onOpenProject: { key in
                context.dispatch { $0.homeSetScope.scopeKey = key }
                nav.open(.home)
            },
            onOpenMenu: { pr in
                menu = pr
                menuShown = true
            },
            onPin: { key in
                Haptics.confirm()
                context.dispatch { $0.projectTogglePin.projectKey = key }
            },
            onAskRemove: { key in removingKey = key },
            onStartAdd: { adding = true }
        )
        .navBar(L10n.projectsManage(), subtitle: subtitle)
        .actionMenu(isPresented: $menuShown, actions: menu.map { actions(manage, $0) } ?? []) {
            if let pr = menu {
                VStack(alignment: .leading, spacing: 2) {
                    Text(pr.name.rendered()).font(.body.weight(.semibold)).ink(DS.ink)
                    Text(pr.shortPath).font(.footnote.monospaced()).ink(DS.ink2)
                }
            }
        }
        .removeProjectConfirm($removingKey, name: projectName(for: removingKey, in: manage)) { key in
            context.dispatch { $0.projectToggleHide.projectKey = key }
        }
        .kitSheet(isPresented: $adding, size: .large) {
            SheetHead(L10n.projectsNewChooseDirectory())
            CoreDirPicker { _, path in
                context.dispatch { $0.projectAdd.path = path }
                adding = false
            }
        }
        .task(id: initialMachineId) {
            await context.coreHost.setManageActive(true, machineID: initialMachineId)
            if let key = HomeLaunch.value("-projectsMenu"),
               let pr = (manage.listing.pinned + manage.listing.others).first(where: { $0.key == key }) {
                menu = pr
                menuShown = true
            }
        }
        .onDisappear {
            Task { await context.coreHost.setManageActive(false) }
        }
        .homeLaunchTweaks()
        .debugSignal("projects.pin") {
            if let key = HomeLaunch.value("-projectsTarget") {
                Haptics.confirm()
                context.dispatch { $0.projectTogglePin.projectKey = key }
            }
        }
        .debugSignal("projects.remove") {
            menuShown = false
            if let key = HomeLaunch.value("-projectsTarget") {
                Task {
                    try? await Task.sleep(for: .milliseconds(300))
                    removingKey = key
                }
            }
        }
        .debugSignal("projects.remove.confirm") {
            if let key = HomeLaunch.value("-projectsTarget") {
                context.dispatch { $0.projectToggleHide.projectKey = key }
            }
        }
    }

    private func projectName(for key: String?, in manage: PBCoreManageProjectsView) -> String {
        guard let key else { return "" }
        let rows = manage.listing.pinned + manage.listing.others
        return rows.first(where: { $0.key == key })?.name.rendered() ?? ""
    }

    private func actions(_ manage: PBCoreManageProjectsView, _ pr: PBCoreProjectRowView) -> [SheetAction] {
        let blocked = manage.blockedReason.rendered()
        return [
            SheetAction(
                pr.pinned ? L10n.projectsUnpin() : L10n.projectsPin(),
                enabled: manage.canEdit,
                note: blocked
            ) {
                Haptics.confirm()
                context.dispatch { $0.projectTogglePin.projectKey = pr.key }
            },
            SheetAction(
                L10n.projectsRemoveAction(),
                danger: true,
                enabled: manage.canEdit
            ) {
                Task {
                    try? await Task.sleep(for: .milliseconds(300))
                    removingKey = pr.key
                }
            },
        ]
    }
}

/// Pure presentation body for `ManageProjectsScreen`.
struct ManageProjectsContent: View {
    let manage: PBCoreManageProjectsView
    var showElsewhere = false
    var onDismissElsewhere: () -> Void = {}
    var onShowAllMachines: () -> Void = {}
    var onSearch: (String) -> Void = { _ in }
    var onOpenProject: (String) -> Void = { _ in }
    var onOpenMenu: (PBCoreProjectRowView) -> Void = { _ in }
    var onPin: (String) -> Void = { _ in }
    var onAskRemove: (String) -> Void = { _ in }
    var onStartAdd: () -> Void = {}

    var body: some View {
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                List {
                    Group {
                        if !manage.filterMachineID.isEmpty {
                            Banner(
                                L10n.projectsFilterMachineSubtitle(name: manage.filterMachineName.or(manage.filterMachineID)),
                                action: L10n.projectsFilterShowAll(),
                                onAction: onShowAllMachines
                            )
                            .padding(.vertical, 4)
                        }
                        if showElsewhere {
                            Banner(L10n.projectsChangedElsewhere(), action: L10n.commonGotIt(), onAction: onDismissElsewhere)
                                .padding(.vertical, 4)
                        }
                        CoreProjectList(listing: manage.listing, onSearch: onSearch, onNew: onStartAdd) { pr in
                            row(pr)
                        }
                        Hint(L10n.projectsRemoveNote())
                            .padding(.top, 4)
                            .padding(.bottom, 16)
                    }
                    .listRowInsets(EdgeInsets(top: 0, leading: DS.Space.l, bottom: 0, trailing: DS.Space.l))
                    .listRowSeparator(.hidden)
                    .listRowBackground(DS.gbg)
                }
                .listStyle(.plain)
                .scrollContentBackground(.hidden)
                .environment(\.defaultMinListRowHeight, 0)
                .scrollDismissesKeyboard(.immediately)
                .readableColumn()
            }
        }
    }

    private func row(_ pr: PBCoreProjectRowView) -> some View {
        let can = manage.canEdit && !pr.syncing
        return CoreProjectRow(
            row: pr,
            hint: L10n.projectsIosViewHome(),
            canRetry: manage.canEdit,
            onRetry: { onPin(pr.key) },
            action: { onOpenProject(pr.key) },
            trailing: { EmptyView() },
            accessory: {
                IconButton("ellipsis", label: L10n.projectsManageNamed(name: pr.name.rendered()), size: 20) {
                    onOpenMenu(pr)
                }
                .disabled(pr.syncing)
                .opacity(pr.syncing ? 0.4 : 1)
            }
        )
        .contextMenu {
            Button(pr.pinned ? L10n.projectsUnpin() : L10n.projectsPin()) { onPin(pr.key) }.disabled(!can)
            Button(L10n.projectsRemoveAction(), role: .destructive) { onAskRemove(pr.key) }.disabled(!can)
        }
        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
            if can {
                Button(L10n.projectsRemoveAction()) { onAskRemove(pr.key) }.tint(DS.red)
                Button(pr.pinned ? L10n.projectsUnpin() : L10n.projectsPin()) { onPin(pr.key) }.tint(DS.blue)
            }
        }
    }
}

#if DEBUG
#Preview("Manage Projects") {
    ManageProjectsContent(manage: ScreenGoldens.manageProjects("project_manage_detail"))
}
#endif
