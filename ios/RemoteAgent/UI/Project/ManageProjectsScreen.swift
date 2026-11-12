// 项目管理 (Android's ManageProjectsScreen in ui/Manage.kt): the list of
// projects, with the two things that can be done to one, 常用 and 移除;
// only the entries change, never folders on the computer.
//
// The two are reached three ways that say the same words in the same
// order: the ⋯ at a row's end, as on Android; a long press on the row; and
// a swipe, which a list of things that can be removed has on this
// platform. 移除 asks once either way and cannot be taken back.
import RACore
import SwiftUI

struct ManageProjectsScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var menu: Project?
    @State private var menuShown = false
    @State private var removing: String? = HomeLaunch.value("-projectsRemove")
    @State private var adding = HomeLaunch.flag("-projectsAdd")
    // A change from another client while this page is open is said once.
    @State private var seenRevision: UInt64?
    @State private var elsewhere = HomeLaunch.flag("-projectsElsewhere")

    var body: some View {
        let state = model.state
        let blocked = prefsBlocked(state)
        let changes = context.prefsChanges
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                List {
                    Group {
                        if elsewhere {
                            Banner(L10n.projectsChangedElsewhere(), action: L10n.commonGotIt(), onAction: { elsewhere = false }).padding(.vertical, 4)
                        }
                        ProjectList(state, onNew: { adding = true }) { pr in
                            row(state, pr, blocked: blocked, saving: changes.states[JvmKey(pr.key)]?.saving == true)
                        }
                        Hint(L10n.projectsRemoveNote()).padding(.top, 4).padding(.bottom, 16)
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
        .navBar(L10n.projectsManage())
        .actionMenu(isPresented: $menuShown, actions: menu.map { actions(state, $0, blocked: blocked) } ?? []) {
            if let pr = menu {
                VStack(alignment: .leading, spacing: 2) {
                    Text(pr.name).font(.body.weight(.semibold)).ink(DS.ink)
                    Text(shortPath(pr.key)).font(.footnote.monospaced()).ink(DS.ink2)
                }
            }
        }
        .removeProjectConfirm($removing)
        .kitSheet(isPresented: $adding, size: .large) {
            SheetHead(L10n.projectsNewChooseDirectory())
            // Chosen, the folder is listed and the page stays: the project appears under 其他.
            ProjectDirPicker(state) { machine, path in
                context.addProject(model.state, machine: machine, path: path)
                adding = false
            }
        }
        .onChange(of: state.prefs?.revision, initial: true) { _, revision in
            if let seen = seenRevision, let revision, revision != seen, changes.states.isEmpty { elsewhere = true }
            seenRevision = revision
        }
        .task {
            // The menu of the project a launch argument names, for looking at it.
            if let key = HomeLaunch.value("-projectsMenu"), let pr = project(model.state, key: key) {
                menu = pr
                menuShown = true
            }
        }
        .homeLaunchTweaks()
        // What the menu's two choices do to the project `-projectsTarget` names, for a script (debug builds).
        .debugSignal("projects.pin") {
            if let key = HomeLaunch.value("-projectsTarget") { pin(key, !isPinned(model.state, key: key)) }
        }
        .debugSignal("projects.remove") {
            // As the menu's second choice does it: the menu closes, then the dialog asks.
            menuShown = false
            if let key = HomeLaunch.value("-projectsTarget") { askToRemove(key) }
        }
        .debugSignal("projects.remove.confirm") {
            if let key = HomeLaunch.value("-projectsTarget") { context.prefsChanges.run(context.remote, .hide(path: key)) }
        }
    }

    private func row(_ state: RemoteState, _ pr: Project, blocked: String?, saving: Bool) -> some View {
        let pinned = isPinned(state, key: pr.key)
        let can = blocked == nil && !saving
        // The name makes the project the home page's scope and goes there; it only changes this device.
        return ProjectRow(state, pr, hint: L10n.projectsIosViewHome()) {
            context.homeScopes.choose(.of(key: pr.key))
            nav.open(.home)
        } trailing: {
            EmptyView()
        } accessory: {
            IconButton("ellipsis", label: L10n.projectsManageNamed(name: pr.name), size: 20) {
                menu = pr
                menuShown = true
            }
            .disabled(saving)
            .opacity(saving ? 0.4 : 1)
        }
        .contextMenu {
            Button(pinned ? L10n.projectsUnpin() : L10n.projectsPin()) { pin(pr.key, !pinned) }.disabled(!can)
            Button(L10n.projectsRemoveAction(), role: .destructive) { removing = pr.key }.disabled(!can)
        }
        // Out of reach of the server the changes cannot be made: the menu says why, a swipe offers nothing.
        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
            if can {
                Button(L10n.projectsRemoveAction()) { removing = pr.key }.tint(DS.red)
                Button(pinned ? L10n.projectsUnpin() : L10n.projectsPin()) { pin(pr.key, !pinned) }.tint(DS.blue)
            }
        }
    }

    /// The dialog comes once the menu has gone: the two are covers of the same page, and one is shown at a time.
    private func askToRemove(_ key: String) {
        Task {
            try? await Task.sleep(for: .milliseconds(300))
            removing = key
        }
    }

    private func pin(_ key: String, _ on: Bool) {
        Haptics.confirm()
        context.prefsChanges.run(context.remote, on ? .pin(path: key) : .unpin(path: key))
    }

    private func actions(_ state: RemoteState, _ pr: Project, blocked: String?) -> [SheetAction] {
        let pinned = isPinned(state, key: pr.key)
        return [
            SheetAction(pinned ? L10n.projectsUnpin() : L10n.projectsPin(), enabled: blocked == nil, note: blocked ?? "") { pin(pr.key, !pinned) },
            SheetAction(L10n.projectsRemoveAction(), danger: true, enabled: blocked == nil) { askToRemove(pr.key) },
        ]
    }
}
