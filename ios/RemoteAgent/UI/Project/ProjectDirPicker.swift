// Choosing a folder for a new project (architecture §13 「远程新建会话」):
// a computer first, offline ones greyed, then its folders level by level
// as the daemon lists them (`ListDirectories`; hidden folders are not
// listed). Android's DirPicker in ui/Project.kt.
import RACore
import RAProto
import SwiftUI

/// Where a folder's listing comes from: the computer, or in sample mode a made-up tree.
@MainActor
struct DirectorySource {
    var list: @MainActor (_ machine: String, _ path: String) async throws -> PBListDirectoriesResponse

    static func remote(_ remote: Remote) -> DirectorySource {
        DirectorySource { machine, path in
            let r = try await remote.machineCommand(machine, listDirectoriesCommand(path: path))
            guard case .listDirectories(let dirs)? = r.kind else { throw CommandError("internal", L10nMessage.newSessionUnexpectedReply()) }
            return dirs
        }
    }
}

/// The picker's state: which computer and folder, what was listed, and how the last listing went.
private struct Browsing: Equatable {
    var machine: String
    var path = ""
    var attempt = 0
}

/// Choose a machine (only where the account has more than one), then browse to a folder; `onChosen` with the
/// folder. The computers and the path above the list and the button below it stay; only the list between
/// them scrolls, and it fades into the surface at the edge it passes under. It fills what it is given: a
/// sheet (which it pins, `sheetPinned()`) or a page (`onPage`).
struct ProjectDirPicker: View {
    private let state: RemoteState
    private let onPage: Bool
    private let onChosen: (_ machine: String, _ path: String) -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var at: Browsing
    @State private var dirs: PBListDirectoriesResponse?
    @State private var loading = false
    @State private var error = ""

    init(_ state: RemoteState, machine: String? = nil, onPage: Bool = false, onChosen: @escaping (_ machine: String, _ path: String) -> Void) {
        self.state = state
        self.onPage = onPage
        self.onChosen = onChosen
        _at = State(initialValue: Browsing(machine: machine ?? firstOnlineMachine(state), path: HomeLaunch.value("-dirPath") ?? ""))
    }

    private var source: DirectorySource {
        #if DEBUG
        if model.isDemo { return Demo.directories }
        #endif
        return .remote(context.remote)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // A computer is chosen only where the account has more than one (design-system §6 新任务).
            if state.machines.count > 1 { machines.padding(.bottom, 10) }
            listing
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .padding(.horizontal, onPage ? DS.Space.l : 0)
        .padding(.bottom, onPage ? 8 : 0)
        .readableColumn()
        .sheetPinned()
        .task(id: at) { await load() }
        // What a tap here would do, for a script (debug builds).
        .debugSignal("dir.first") { if let d = dirs, let name = d.dirs.first { at.path = childPath(d.path, name) } }
        .debugSignal("dir.up") { if let d = dirs, !d.parent.isEmpty { at.path = d.parent } }
        .debugSignal("dir.choose") { if let d = dirs { onChosen(at.machine, d.path) } }
        // The first computer to come online is browsed when none was at the start.
        .onChange(of: firstOnlineMachine(state)) { _, first in
            if at.machine.isEmpty && !first.isEmpty { at.machine = first }
        }
    }

    private func load() async {
        guard !at.machine.isEmpty else { return }
        loading = true
        error = ""
        do {
            let listed = try await source.list(at.machine, at.path)
            if Task.isCancelled { return }
            dirs = listed
        } catch is CancellationError {
            return
        } catch {
            if Task.isCancelled { return }
            self.error = errorText(error)
        }
        loading = false
    }

    private func choose(_ d: PBListDirectoriesResponse) -> some View {
        Btn(L10n.projectsUseDirectory(name: pathCrumbs(d.path) == ["/"] ? "/" : projectName(d.path)), kind: .primary, enabled: !loading) {
            Haptics.confirm()
            onChosen(at.machine, d.path)
        }
    }

    @ViewBuilder private var listing: some View {
        if at.machine.isEmpty {
            Banner(L10n.projectsIosNoOnline(), tone: .warn)
        } else if !error.isEmpty {
            Banner(L10n.projectsDirectoryFailed(reason: error), tone: .error, action: L10n.commonRetry(), onAction: { at.attempt += 1 })
        } else if let d = dirs {
            crumbs(d)
            if d.dirs.isEmpty { Text(L10n.projectsNoSubdirectories()).font(.subheadline).ink(DS.ink2).padding(.vertical, 8) }
            // The folder before stays, dimmed, until the next one is listed: no blank in between.
            ScrollView {
                VStack(spacing: 0) {
                    if !d.parent.isEmpty {
                        ListItem(L10n.projectsParentDirectory(), last: d.dirs.isEmpty, icon: "chevron.left") { at.path = d.parent }
                    }
                    ForEach(Array(d.dirs.enumerated()), id: \.element) { i, name in
                        ListItem(name, last: i == d.dirs.count - 1) { at.path = childPath(d.path, name) }
                            .trailing { Arrow(DS.ink2, style: .body) }
                    }
                }
                .opacity(loading ? 0.45 : 1)
                .disabled(loading)
            }
            .scrollEdgeFades()
            // The list takes what the button leaves, so the button stays at the bottom under a short list too.
            .frame(maxHeight: .infinity, alignment: .top)
            choose(d).padding(.top, 10)
        } else {
            LoadingRow(inset: 0)
        }
    }

    /// The computers as a segmented control, the chosen one a slider; an offline one cannot be chosen.
    private var machines: some View {
        MachineSegmented(
            items: listedMachines(state).map { .init(id: $0.id, name: machineLabel(state, id: $0.id), online: $0.online) },
            selected: Binding(get: { at.machine }, set: { if $0 != at.machine { at = Browsing(machine: $0) } }))
    }

    /// 「~ / Code / ai-bot」, the folder itself in the text colour.
    private func crumbs(_ d: PBListDirectoriesResponse) -> some View {
        let parts = pathCrumbs(d.path)
        var line = Text("")
        for (i, part) in parts.enumerated() {
            if i > 0 { line = line + Text(" / ") }
            line = line + (i == parts.count - 1 ? Text(part).fontWeight(.semibold).foregroundStyle(DS.ink) : Text(part))
        }
        return line.font(.footnote.monospaced()).ink(DS.ink2)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.bottom, 6)
            .accessibilityLabel(L10n.projectsIosCurrentDirectory(path: shortPath(d.path)))
    }
}
