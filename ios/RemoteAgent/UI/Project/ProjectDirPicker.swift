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
            guard case .listDirectories(let dirs)? = r.kind else { throw CommandError("internal", "意外的回复") }
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

/// Choose a machine, then browse to a folder; `onChosen` with the folder.
///
/// In a sheet its lines join the sheet's column and scroll with it. As a
/// page (`pinned`) it scrolls by itself and keeps the button that chooses
/// the folder in reach under a long listing.
struct ProjectDirPicker: View {
    private let state: RemoteState
    private let pinned: Bool
    private let onChosen: (_ machine: String, _ path: String) -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var at: Browsing
    @State private var dirs: PBListDirectoriesResponse?
    @State private var loading = false
    @State private var error = ""

    init(_ state: RemoteState, pinned: Bool = false, onChosen: @escaping (_ machine: String, _ path: String) -> Void) {
        self.state = state
        self.pinned = pinned
        self.onChosen = onChosen
        _at = State(initialValue: Browsing(machine: firstOnlineMachine(state), path: HomeLaunch.value("-dirPath") ?? ""))
    }

    private var source: DirectorySource {
        #if DEBUG
        if model.isDemo { return Demo.directories }
        #endif
        return .remote(context.remote)
    }

    var body: some View {
        Group {
            if pinned {
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) { listing }
                        .padding(.horizontal, DS.Space.l).padding(.bottom, 16)
                        .readableColumn()
                }
                .safeAreaInset(edge: .bottom, spacing: 0) {
                    if let dirs, error.isEmpty {
                        choose(dirs).padding(.horizontal, DS.Space.l).padding(.vertical, 8).readableColumn()
                            .background(DS.gbg).topLine()
                    }
                }
            } else {
                listing
                if let dirs, error.isEmpty { choose(dirs).padding(.top, 10) }
            }
        }
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
        Btn("用 \(pathCrumbs(d.path) == ["/"] ? "/" : projectName(d.path)) 这个目录", kind: .primary, enabled: !loading) {
            Haptics.confirm()
            onChosen(at.machine, d.path)
        }
    }

    @ViewBuilder private var listing: some View {
        // A computer is chosen only where the account has more than one (design-system §6 新任务).
        if state.machines.count > 1 { machines }
        if at.machine.isEmpty {
            Banner("没有在线的电脑", tone: .warn)
        } else if !error.isEmpty {
            Banner("读不了这个目录：\(error)", tone: .error, action: "重试", onAction: { at.attempt += 1 })
        } else if let d = dirs {
            // The folder before stays, dimmed, until the next one is listed: no blank in between.
            Group {
                crumbs(d)
                if d.dirs.isEmpty { Text("没有子目录").font(.subheadline).ink(DS.ink2).padding(.vertical, 8) }
                if !d.parent.isEmpty {
                    ListItem("上一级", last: d.dirs.isEmpty, icon: "chevron.left") { at.path = d.parent }
                }
                ForEach(Array(d.dirs.enumerated()), id: \.element) { i, name in
                    ListItem(name, last: i == d.dirs.count - 1) { at.path = childPath(d.path, name) }
                        .trailing { Arrow(DS.ink2, style: .body) }
                }
            }
            .opacity(loading ? 0.45 : 1)
            .disabled(loading)
        } else {
            LoadingRow(inset: 0)
        }
    }

    /// 机器, then each computer as a pill; the chosen one is outlined, an offline one cannot be chosen.
    private var machines: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                Text("机器").font(.footnote).ink(DS.ink2)
                ForEach(listedMachines(state)) { m in
                    let on = at.machine == m.id
                    Button {
                        if !on {
                            Haptics.selection()
                            at = Browsing(machine: m.id)
                        }
                    } label: {
                        HStack(spacing: 5) {
                            Dot(m.online ? DS.green : DS.ink3)
                            Text(machineLabel(state, id: m.id)).font(.subheadline).ink(on ? DS.blue : DS.ink2).lineLimit(1)
                        }
                        .padding(.horizontal, 10).padding(.vertical, 4)
                        .background(on ? DS.fill : DS.fill, in: Capsule())
                        .overlay { if on { Capsule().strokeBorder(DS.blue, lineWidth: 1) } }
                        .hitTarget(minWidth: 0)
                    }
                    .buttonStyle(PressStyle())
                    .disabled(!m.online)
                    .opacity(m.online ? 1 : 0.45)
                    .accessibilityLabel(machineLabel(state, id: m.id))
                    .accessibilityValue(m.online ? "在线" : "不在线")
                    .accessibilityAddTraits(on ? .isSelected : [])
                }
            }
        }
        .padding(.bottom, 6)
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
            .accessibilityLabel("当前目录 \(shortPath(d.path))")
    }
}
