// 会话, the app's first page (Android's HomeScreen in ui/Home.kt): one
// timeline of sessions for a scope, a project or every project
// (architecture §8 「主页的范围」). What needs you comes first and is not
// scoped, then what is running, then the ended sessions by day, a page at
// a time. Which lines there are is `homeList`'s to say; this file lays
// them out.
//
// The title is the scope and stays at the top while the list scrolls
// under it, as a navigation bar would: it is how the scope is changed and
// where a new session starts, and both should be in reach from anywhere in
// a long list.
import RACore
import SwiftUI

struct HomeScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var menu = ["scope", "browse"].contains(HomeLaunch.value("-homeSheet"))
    /// 看哪个项目 has turned into 新项目 · 选目录.
    @State private var browsing = HomeLaunch.value("-homeSheet") == "browse"
    @State private var pick = ["pick", "pickBrowse"].contains(HomeLaunch.value("-homeSheet"))
    @State private var pickBrowsing = HomeLaunch.value("-homeSheet") == "pickBrowse"

    var body: some View {
        let state = model.state
        let scopes = context.homeScopes
        let scope = resolveScope(remembered: scopes.remembered(state), state: state)
        // Without any project, removed ones included, there is nothing to scope by.
        let anyProject = !projects(state).isEmpty
        Page {
            VStack(spacing: 0) {
                HomeScopeTitle(scope: anyProject ? scope : nil, onOpen: { menu = true }) {
                    // A project as the scope is where the new session goes; every project asks which first.
                    if case .of(let key) = scope { nav.open(.project(key: key, focusNew: true)) } else { pick = true }
                }
                .readableColumn()
                ConnectionBar()
                // What is typed and opened belongs to the scope: another scope starts over, at the top.
                HomeTimeline(state: state, scope: scope).id(scope)
            }
        }
        // A folder's listing has no height to fit before it is read; the list of projects does.
        .kitSheet(isPresented: $menu, size: browsing || listedProjects(state).count > filterOver ? .large : .fit, onDismiss: { browsing = false }) {
            HomeScopeSheet(state: state, scope: scope, browsing: $browsing) { menu = false }
        }
        .kitSheet(isPresented: $pick, size: pickBrowsing || listedProjects(state).count > filterOver ? .large : .fit, onDismiss: { pickBrowsing = false }) {
            PickProjectSheet(state: state, browsing: $pickBrowsing) { pick = false }
        }
        .homeLaunchTweaks()
        // Chooses the scope `-homeScopeTarget` names as a tap in the menu would, for a script (debug builds).
        .debugSignal("home.scope") {
            if let target = HomeLaunch.value("-homeScopeTarget") { scopes.choose(target == "all" ? .all : .of(key: target)) }
        }
    }
}

/// The home page's title is its scope (design-system §6 范围标题): the
/// project's avatar and name, or 全 on grey and 全部项目, 22 with an arrow
/// down; a tap opens 看哪个项目. Without any project (`scope` nil) it is a
/// plain 会话 that does nothing. The new-session button stays at the right.
private struct HomeScopeTitle: View {
    let scope: HomeScope?
    let onOpen: () -> Void
    let onNew: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            if let scope {
                Button(action: onOpen) {
                    HStack(spacing: 10) {
                        if case .of(let key) = scope { Avatar(key, size: 28) } else { Avatar.all(size: 28) }
                        Text(scopeName(scope)).type(T.h22, Theme.fg).lineLimit(1)
                        Arrow(Theme.faint, style: T.h17, direction: .down)
                    }
                    .hitTarget(minWidth: 0, alignment: .leading)
                }
                .buttonStyle(PressStyle())
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(scopeName(scope))
                .accessibilityHint("换范围")
                .accessibilityAddTraits([.isHeader, .isButton])
                .accessibilityIdentifier("scope-title")
                Spacer(minLength: 0)
            } else {
                Text("会话").type(T.title30, Theme.fg).padding(.vertical, 3)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityAddTraits(.isHeader)
            }
            NewSessionButton(action: onNew)
        }
        .padding(.init(top: 4, leading: 16, bottom: 2, trailing: 12))
        // A bar: one line at any text size, the list below says the rest (design-system §6).
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
    }
}

/// The timeline of one scope, with what is typed in its filter and how much of it is opened.
private struct HomeTimeline: View {
    let state: RemoteState
    let scope: HomeScope
    @Environment(Navigator.self) private var nav
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    // What is typed stays in memory: it is part of a title, which is content.
    @State private var query = HomeLaunch.value("-homeFilter") ?? ""
    @State private var shown = HomeShown(
        allAttention: HomeLaunch.flag("-homeExpand"), allRunning: HomeLaunch.flag("-homeExpand"))
    @State private var now = nowMs()
    @ScaledMetric(relativeTo: .caption) private var dot: CGFloat = 8

    private struct Grow: Hashable {
        var shown: Int
        var held: Int
        var grows: Bool
    }

    var body: some View {
        let list = homeList(state, scope: scope, query: query, shown: shown, now: now)
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                if list.showFilter { FilterBox(text: $query).accessibilityIdentifier("title-filter") }
                if let empty = list.empty { emptyView(empty) }
                // One list of lines, each with an ID of its own: a session that changes parts moves, nothing else is redrawn.
                ForEach(list.items) { item in line(item, list) }
                Color.clear.frame(height: 24)
            }
            .readableColumn()
            .animation(reduceMotion ? nil : .snappy(duration: 0.28), value: list.items.map(\.id))
        }
        // The tab bar stays under the keyboard's place, so the list does not shrink for it: a drag puts it away instead.
        .scrollDismissesKeyboard(.immediately)
        // The ended sessions come a page at a time (architecture §8 「会话列表分页」):
        // another page when more are to be shown than were fetched, and every
        // page left once a filter is typed, which has to see every title.
        .fetchEarlier(state, page: list.wantPage, all: list.wantAll)
        .ticking($now)
    }

    @ViewBuilder private func line(_ item: HomeItem, _ list: HomeList) -> some View {
        switch item {
        case .label(_, let title, let count):
            SectionLabel(title, count: count).accessibilityElement(children: .combine)
        case .attention(let s):
            HomeAttentionCard(state: state, session: s, now: now, scope: scope)
        case .moreAttention(let n):
            TextAction("还有 \(n) 项待处理", arrow: true) { shown.allAttention = true }.padding(.horizontal, Theme.Space.page)
        case .running(let s, let last):
            HomeSessionRow(state: state, session: s, now: now, meta: list.meta, last: last)
        case .moreRunning(let n):
            TextAction("还有 \(n) 个进行中", arrow: true) { shown.allRunning = true }.padding(.horizontal, Theme.Space.page)
        case .ended(let s):
            HomeEndedRow(session: s, now: now, project: list.meta.project)
        case .tail(let tail):
            Group {
                switch tail {
                case .text(let text): Text(text).type(T.meta, Theme.muted).frame(maxWidth: .infinity, alignment: .leading)
                case .earlier(let text, let retry): EarlierTail(text, retry: retry)
                }
            }
            .padding(.init(top: 12, leading: Theme.Space.page + dot + 11, bottom: 4, trailing: Theme.Space.page))
            .accessibilityIdentifier("timeline-tail")
            // Reaching the end shows the next thirty, once the ones before them are all rendered.
            .task(id: Grow(shown: shown.ended, held: list.endedHeld, grows: list.grows)) {
                if list.grows { shown.ended += endedPage }
            }
        }
    }

    @ViewBuilder private func emptyView(_ empty: HomeEmpty) -> some View {
        switch empty {
        case .loading:
            Skeleton()
        case .scope:
            emptyNote("这个项目还没有会话", "点右上角的新建按钮开始一个")
        case .account(.noMachines):
            emptyNote("还没有会话", noMachinesHint)
        case .account(.noneOnline):
            emptyNote("还没有会话", "没有在线的电脑。在电脑上运行 remote-agent daemon。")
        case .account(.ready(let machine)):
            emptyNote("还没有会话", "\(machine) 在线，可以直接开始", action: "开始第一个会话") { nav.open(.newProject) }
        case .account(.unlinked):
            // Which computers are online is not known now; the bar above says why.
            emptyNote("还没有会话", "")
        }
    }

    private func emptyNote(_ title: String, _ text: String, action: String? = nil, onAction: @escaping () -> Void = {}) -> some View {
        EmptyState(title, text: text, action: action, onAction: action == nil ? nil : onAction).padding(.vertical, 24)
    }
}

/// 看哪个项目: 全部项目 when there is more than one project, the pinned
/// ones, then the others, each with its path and what runs there, a check
/// on the current scope; a search above more than eight. 新项目… picks a
/// machine and a folder in the same sheet; 管理项目 goes to 项目管理.
private struct HomeScopeSheet: View {
    let state: RemoteState
    let scope: HomeScope
    @Binding var browsing: Bool
    let onClose: () -> Void
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context

    var body: some View {
        if browsing {
            SheetHead("新项目 · 选目录", action: "取消", onAction: onClose, onBack: { browsing = false })
            ProjectDirPicker(state) { machine, path in
                onClose()
                nav.open(.newProjectAt(machine: machine, path: path))
            }
        } else {
            SheetHead("看哪个项目", action: "取消", onAction: onClose)
            ProjectList(state, onNew: { browsing = true }) {
                // 全部项目, when there is more than one project to choose between.
                if let all = scopeRows(state, current: scope).first(where: { $0.scope == .all }) {
                    Button {
                        choose(.all)
                    } label: {
                        HStack(spacing: 12) {
                            Avatar.all(size: 22)
                            VStack(alignment: .leading, spacing: 0) {
                                Text(all.name).type(T.bodyB, Theme.fg).lineLimit(1)
                                Text(all.detail).type(T.meta, Theme.muted).lineLimit(1)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            check(all.current)
                        }
                        .padding(.vertical, 6)
                        .frame(minHeight: 52)
                    }
                    .buttonStyle(RowPressStyle())
                    .bottomLine(startInset: 34)
                    .accessibilityAddTraits(all.current ? .isSelected : [])
                }
            } row: { pr in
                let current = scope == .of(key: pr.key)
                ProjectRow(state, pr, action: { choose(.of(key: pr.key)) }) { check(current) }
                    .accessibilityAddTraits(current ? .isSelected : [])
            }
            TextAction("管理项目", style: T.body, arrow: true) {
                onClose()
                nav.open(.manageProjects)
            }
            .padding(.leading, 34)
        }
    }

    private func choose(_ scope: HomeScope) {
        Haptics.selection()
        context.homeScopes.choose(scope)
        onClose()
    }

    /// The check of the current scope; its room is kept on the other rows so the names line up.
    private func check(_ on: Bool) -> some View {
        Ic(Lucide.check, size: 16, color: Theme.blue).opacity(on ? 1 : 0)
    }
}
