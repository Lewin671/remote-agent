// Core-driven project lists (`CoreProjectRow`, `CoreProjectList`) shared across
// Home scope menu, PickProject, and ManageProjects. `PrefsChanges` remains here
// for the un-migrated `SessionScreen` (`ProjectSettingsSheet`).
import Observation
import RACore
import RAProto
import SwiftUI

/// Changes to the project preferences in flight, per project path, kept for `SessionScreen`.
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
    }

    private(set) var states: [JvmKey: State] = [:]

    private struct Change {
        var op: PrefsOp
        var write: (PrefsOp) async throws -> Void
        var onDone: @MainActor () -> Void
    }

    private var queues: [JvmKey: [Change]] = [:]
    private var closed = false

    func run(_ remote: Remote, _ op: PrefsOp, onDone: @escaping @MainActor () -> Void = {}) {
        run(op, onDone: onDone) { try await remote.changePrefs($0) }
    }

    func run(_ op: PrefsOp, onDone: @escaping @MainActor () -> Void = {}, write: @escaping (PrefsOp) async throws -> Void) {
        guard !closed else { return }
        let path = JvmKey(op.path)
        let failed = states[path]?.saving == false
        var queue = queues[path] ?? []
        if failed, queue.first?.op == op {
        } else if queue.last?.op != op {
            queue.append(Change(op: op, write: write, onDone: onDone))
        }
        queues[path] = queue
        if states[path]?.saving != true { next(path) }
    }

    private func next(_ path: JvmKey) {
        guard let change = queues[path]?.first else {
            queues[path] = nil
            states[path] = nil
            return
        }
        states[path] = .saving(change.op)
        Task {
            do {
                try await change.write(change.op)
            } catch {
                guard !closed else { return }
                states[path] = .failed(change.op, errorText(error))
                return
            }
            guard !closed else { return }
            queues[path]?.removeFirst()
            change.onDone()
            next(path)
        }
    }

    func close() {
        closed = true
        queues.removeAll()
        states.removeAll()
    }
}

extension AccountContext {
    var prefsChanges: PrefsChanges { memory.value { PrefsChanges() } }
}

/// One project row rendered directly from `PBCoreProjectRowView`.
struct CoreProjectRow<Trailing: View, Accessory: View>: View {
    private let row: PBCoreProjectRowView
    private let hint: String?
    private let canRetry: Bool
    private let action: () -> Void
    private let onRetry: () -> Void
    private let trailing: Trailing
    private let accessory: Accessory
    @Environment(\.dynamicTypeSize) private var typeSize

    init(
        row: PBCoreProjectRowView,
        hint: String? = nil,
        canRetry: Bool = false,
        onRetry: @escaping () -> Void = {},
        action: @escaping () -> Void,
        @ViewBuilder trailing: () -> Trailing,
        @ViewBuilder accessory: () -> Accessory
    ) {
        self.row = row
        self.hint = hint
        self.canRetry = canRetry
        self.onRetry = onRetry
        self.action = action
        self.trailing = trailing()
        self.accessory = accessory()
    }

    var body: some View {
        let saveError = row.saveError.rendered()
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 0) {
                Button(action: action) {
                    HStack(spacing: 12) {
                        Avatar(row.key, size: 22)
                        VStack(alignment: .leading, spacing: 0) {
                            Text(row.name.rendered())
                                .font(.body.weight(.semibold))
                                .ink(DS.ink)
                                .lineLimit(typeSize.isAccessibilitySize ? 2 : 1)
                            Text(row.subtitle.rendered())
                                .font(.footnote)
                                .ink(DS.ink2)
                                .lineLimit(typeSize.isAccessibilitySize ? 2 : 1)
                                .truncationMode(.middle)
                            if row.syncing {
                                IconText(Mark.progress, L10n.projectsIosSyncing(), style: .footnote)
                            }
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
            if !saveError.isEmpty {
                HStack(alignment: .firstTextBaseline, spacing: 0) {
                    Text(saveError)
                        .font(.footnote)
                        .ink(DS.red)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    TextAction(L10n.commonRetry(), style: .footnote.weight(.semibold), enabled: canRetry, alignment: .trailing, action: onRetry)
                }
                .padding(.leading, 34)
                .padding(.top, -10)
            }
        }
        .bottomLine(startInset: 34)
    }
}

extension CoreProjectRow where Accessory == EmptyView {
    init(
        row: PBCoreProjectRowView,
        hint: String? = nil,
        canRetry: Bool = false,
        onRetry: @escaping () -> Void = {},
        action: @escaping () -> Void,
        @ViewBuilder trailing: () -> Trailing
    ) {
        self.init(row: row, hint: hint, canRetry: canRetry, onRetry: onRetry, action: action, trailing: trailing, accessory: { EmptyView() })
    }
}

/// Core-rendered project listing (`pinned`, `others`, `titled`, `canSearch`, `emptyMatch`).
struct CoreProjectList<First: View, Row: View>: View {
    private let listing: PBCoreProjectListingView
    private let onSearch: ((String) -> Void)?
    private let onNew: () -> Void
    private let first: First
    private let row: (PBCoreProjectRowView) -> Row
    @State private var search = ""

    init(
        listing: PBCoreProjectListingView,
        onSearch: ((String) -> Void)? = nil,
        onNew: @escaping () -> Void,
        @ViewBuilder first: () -> First,
        @ViewBuilder row: @escaping (PBCoreProjectRowView) -> Row
    ) {
        self.listing = listing
        self.onSearch = onSearch
        self.onNew = onNew
        self.first = first()
        self.row = row
    }

    var body: some View {
        let query = onSearch != nil ? listing.searchQuery : search
        let pinned = onSearch == nil && !query.isEmpty
            ? listing.pinned.filter { matches($0, query: query) }
            : listing.pinned
        let others = onSearch == nil && !query.isEmpty
            ? listing.others.filter { matches($0, query: query) }
            : listing.others
        let emptyMatch = listing.emptyMatch.rendered()
        if listing.canSearch {
            FilterBox(text: $search, placeholder: L10n.projectsSearch(), inset: 0)
                .padding(.bottom, 4)
                .onChange(of: search) { _, q in
                    onSearch?(q)
                }
        }
        if query.isEmpty { first }
        if listing.titled && !pinned.isEmpty && !others.isEmpty {
            FieldLabel(L10n.projectsPinned())
        }
        ForEach(pinned, id: \.key) { row($0) }
        if listing.titled && !pinned.isEmpty && !others.isEmpty {
            FieldLabel(L10n.projectsOthers())
        }
        ForEach(others, id: \.key) { row($0) }
        if !emptyMatch.isEmpty {
            Hint(emptyMatch).padding(.bottom, 8)
        } else if onSearch == nil && !query.isEmpty && pinned.isEmpty && others.isEmpty {
            Hint(L10n.projectsSearchEmpty(query: query)).padding(.bottom, 8)
        }
        Button(action: onNew) {
            HStack(spacing: 12) {
                SymbolImage("plus", size: 18, color: DS.blue, relativeTo: .subheadline).frame(width: 22)
                Text(L10n.projectsNew()).font(.body.weight(.semibold)).ink(DS.blue)
            }
            .frame(maxWidth: .infinity, minHeight: 52, alignment: .leading)
        }
        .buttonStyle(RowPressStyle())
    }

    private func matches(_ row: PBCoreProjectRowView, query: String) -> Bool {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return true }
        return row.name.rendered().localizedCaseInsensitiveContains(trimmed)
            || row.shortPath.localizedCaseInsensitiveContains(trimmed)
    }
}

extension CoreProjectList where First == EmptyView {
    init(
        listing: PBCoreProjectListingView,
        onSearch: ((String) -> Void)? = nil,
        onNew: @escaping () -> Void,
        @ViewBuilder row: @escaping (PBCoreProjectRowView) -> Row
    ) {
        self.init(listing: listing, onSearch: onSearch, onNew: onNew, first: { EmptyView() }, row: row)
    }
}

private struct RemoveProjectConfirm: ViewModifier {
    @Binding var key: String?
    let name: String
    let onConfirm: @MainActor (String) -> Void
    @State private var shownKey = ""
    @State private var shownName = ""

    func body(content: Content) -> some View {
        content
            .onChange(of: key, initial: true) { _, new in
                if let new {
                    shownKey = new
                    shownName = name.isEmpty ? new : name
                }
            }
            .confirm(
                isPresented: Binding(get: { key != nil }, set: { if !$0 { key = nil } }),
                title: L10n.projectsRemoveTitle(name: shownName),
                text: L10n.projectsRemoveExplanation(),
                confirm: L10n.commonRemove(),
                danger: true
            ) {
                onConfirm(shownKey)
            }
    }
}

extension View {
    func removeProjectConfirm(_ key: Binding<String?>, name: String = "", onConfirm: @escaping @MainActor (String) -> Void) -> some View {
        modifier(RemoveProjectConfirm(key: key, name: name, onConfirm: onConfirm))
    }
}
