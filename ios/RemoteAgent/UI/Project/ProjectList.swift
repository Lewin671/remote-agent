// The account's projects as a list, the same in every place that shows
// them (design-system §6 项目行, architecture §8 「项目列表偏好」): the scope
// menu of the home page, choosing a project for a new session, and 项目管理.
// Only what stands at a row's end differs. With it, the changes to the
// project preferences that are on their way to the server. Counterparts of
// Android's ui/Manage.kt.
import Observation
import RACore
import SwiftUI

/// Changes to the project preferences in flight, per project path, for
/// this run: the row says 正在同步 until the server has it, and a failure
/// leaves the list as it was with a retry.
@MainActor
@Observable
final class PrefsChanges {
    enum State: Equatable {
        case saving(PrefsOp)
        case failed(PrefsOp, String)

        var saving: Bool {
            if case .saving = self { return true }
            return false
        }

        /// The change is to the project's settings, not to where it is listed.
        var ofSettings: Bool {
            switch self {
            case .saving(.setSettings), .failed(.setSettings, _): true
            default: false
            }
        }
    }

    private(set) var states: [String: State] = [:]

    /// Writes `op`; nothing while another change to the same project is on its way.
    func run(_ remote: Remote, _ op: PrefsOp, onDone: @escaping @MainActor () -> Void = {}) {
        if states[op.path]?.saving == true { return }
        states[op.path] = .saving(op)
        Task {
            do {
                try await remote.changePrefs(op)
                states[op.path] = nil
                onDone()
            } catch {
                states[op.path] = .failed(op, errorText(error))
            }
        }
    }

    #if DEBUG
    /// A state to look at, for screenshots of sample data.
    func show(_ state: State, for path: String) { states[path] = state }
    #endif
}

extension AccountContext {
    var prefsChanges: PrefsChanges { memory.value { PrefsChanges() } }

    /// Lists the folder at `path` on `machine` as a project before its first
    /// session, here and on the account's other clients; a folder removed
    /// from the list earlier is listed again (`addProjectOp`).
    func addProject(_ state: RemoteState, machine: String, path: String) {
        if let op = addProjectOp(state, machine: machine, path: path) { prefsChanges.run(remote, op) }
    }
}

/// One project in a list: its mark, its name, and under it `projectDetail`.
/// `trailing` is what the place puts at the row's end and is part of the
/// row to the finger (a check, an arrow); `accessory` is a control of its
/// own there (the ⋯ of 项目管理).
struct ProjectRow<Trailing: View, Accessory: View>: View {
    private let state: RemoteState
    private let project: Project
    private let hint: String?
    private let action: () -> Void
    private let trailing: Trailing
    private let accessory: Accessory
    @Environment(AccountContext.self) private var context
    @Environment(\.dynamicTypeSize) private var typeSize

    /// `hint` says to VoiceOver what a tap does when it is not the obvious thing.
    init(
        _ state: RemoteState, _ project: Project, hint: String? = nil, action: @escaping () -> Void,
        @ViewBuilder trailing: () -> Trailing, @ViewBuilder accessory: () -> Accessory
    ) {
        self.state = state
        self.project = project
        self.hint = hint
        self.action = action
        self.trailing = trailing()
        self.accessory = accessory()
    }

    var body: some View {
        let change = context.prefsChanges.states[project.key]
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 0) {
                Button(action: action) {
                    HStack(spacing: 12) {
                        Avatar(project.key, size: 22)
                        VStack(alignment: .leading, spacing: 0) {
                            // At the largest text sizes a line holds too little of either: both may take a second one.
                            Text(project.name).type(T.bodyB, Theme.fg).lineLimit(typeSize.isAccessibilitySize ? 2 : 1)
                            // A long path gives way in its middle: the folder's own name and what runs there stay.
                            Text(projectDetail(state, project)).type(T.meta, Theme.muted)
                                .lineLimit(typeSize.isAccessibilitySize ? 2 : 1).truncationMode(.middle)
                            if change?.saving == true { IconText(Lucide.loaderCircle, "正在同步…", style: T.meta) }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        trailing
                    }
                    .padding(.vertical, 6)
                    .frame(minHeight: 52)
                }
                .buttonStyle(RowPressStyle())
                .accessibilityHint(hint ?? "")
                accessory
            }
            if case .failed(let op, let error)? = change {
                HStack(alignment: .firstTextBaseline, spacing: 0) {
                    Text("没能保存，列表保持原样：\(error)").type(T.meta, Theme.red).frame(maxWidth: .infinity, alignment: .leading)
                    TextAction("重试", style: T.metaB, enabled: prefsBlocked(state) == nil, alignment: .trailing) {
                        context.prefsChanges.run(context.remote, op)
                    }
                }
                .padding(.leading, 34).padding(.top, -10)
            }
        }
        // The line between rows starts where the text does.
        .bottomLine(startInset: 34)
    }
}

extension ProjectRow where Accessory == EmptyView {
    init(_ state: RemoteState, _ project: Project, hint: String? = nil, action: @escaping () -> Void, @ViewBuilder trailing: () -> Trailing) {
        self.init(state, project, hint: hint, action: action, trailing: trailing, accessory: { EmptyView() })
    }
}

/// The projects on the list: 常用 then 其他, titled only when there are
/// both, a search above more than eight, and 新项目… at the end. `first`
/// goes above the projects while nothing is searched for.
///
/// Its lines are not wrapped in a stack of their own, so the same list
/// stands in a sheet's column and, as rows, in a `List`.
struct ProjectList<First: View, Row: View>: View {
    private let state: RemoteState
    private let onNew: () -> Void
    private let first: First
    private let row: (Project) -> Row
    @State private var search = ""

    init(_ state: RemoteState, onNew: @escaping () -> Void, @ViewBuilder first: () -> First, @ViewBuilder row: @escaping (Project) -> Row) {
        self.state = state
        self.onNew = onNew
        self.first = first()
        self.row = row
    }

    var body: some View {
        let l = projectListing(state, search: search)
        if l.searchable {
            // The kit's pill brings the page's side margin, which the list already has.
            FilterBox(text: $search, placeholder: "搜索项目", inset: 0).padding(.bottom, 4)
        }
        if !l.searching { first }
        if l.titled { FieldLabel("常用") }
        ForEach(l.pinned) { row($0) }
        if l.titled { FieldLabel("其他") }
        ForEach(l.others) { row($0) }
        if let none = l.noMatch { Hint(none).padding(.bottom, 8) }
        Button(action: onNew) {
            HStack(spacing: 12) {
                Ic(Lucide.plus, size: 18, color: Theme.blue, relativeTo: .subheadline).frame(width: 22)
                Text("新项目…").type(T.bodyB, Theme.blue)
            }
            .frame(maxWidth: .infinity, minHeight: 52, alignment: .leading)
        }
        .buttonStyle(RowPressStyle())
    }
}

extension ProjectList where First == EmptyView {
    init(_ state: RemoteState, onNew: @escaping () -> Void, @ViewBuilder row: @escaping (Project) -> Row) {
        self.init(state, onNew: onNew, first: { EmptyView() }, row: row)
    }
}

private struct RemoveProjectConfirm: ViewModifier {
    @Binding var key: String?
    let onRemoved: @MainActor () -> Void
    @Environment(AccountContext.self) private var context
    /// The dialog fades out after `key` is cleared; its words stay until it has.
    @State private var shown = ""

    func body(content: Content) -> some View {
        content
            .onChange(of: key, initial: true) { _, new in if let new { shown = new } }
            .confirm(
                isPresented: Binding(get: { key != nil }, set: { if !$0 { key = nil } }),
                title: "移除 \(projectName(shown))？", text: "只从项目列表里移除，会话、历史和电脑上的目录都不动。以后要用，再用「新项目」添加这个目录。",
                confirm: "移除", danger: true
            ) {
                context.prefsChanges.run(context.remote, .hide(path: shown), onDone: onRemoved)
            }
    }
}

extension View {
    /// Asks before the project `key` names leaves the list: there is no undo, only adding it again.
    func removeProjectConfirm(_ key: Binding<String?>, onRemoved: @escaping @MainActor () -> Void = {}) -> some View {
        modifier(RemoveProjectConfirm(key: key, onRemoved: onRemoved))
    }
}
