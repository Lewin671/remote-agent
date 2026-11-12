// 会话, the app's first page (client-v58 #home): one sentence of how things
// stand, then a timeline of sessions for a scope, a project or every
// project (architecture §8 「主页的范围」). What waits for you comes first and
// is not scoped, its most urgent approval as the dark card; then what goes
// on; then the ended sessions by day, a page at a time. The sentence and
// which lines there are is RACore's to say (`homeHeadline`, `homeList`);
// this file lays them out.
//
// The page runs under the screen's edges: the scope capsule and the gear
// float on glass at the top, the tabs and 新任务 at the bottom (UI/Shell).
import RACore
import SwiftUI

struct HomeScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var menu = ["scope", "browse"].contains(HomeLaunch.value("-homeSheet"))
    /// 看哪个项目 has turned into 新项目 · 选目录.
    @State private var browsing = HomeLaunch.value("-homeSheet") == "browse"

    var body: some View {
        let state = model.state
        let scopes = context.homeScopes
        let scope = resolveScope(remembered: scopes.remembered(state), state: state)
        // Without any project, removed ones included, there is nothing to scope by.
        let anyProject = !projects(state).isEmpty
        // What is typed and opened belongs to the scope: another scope starts over, at the top.
        HomeTimeline(state: state, scope: scope).id(scope)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(DS.paper.ignoresSafeArea())
            .floatingBar(.top) {
                HomeTopBar(scope: anyProject ? scope : nil) { menu = true }
            }
            .kitSheet(isPresented: $menu, size: browsing || listedProjects(state).count > filterOver ? .large : .fit, onDismiss: { browsing = false }) {
                HomeScopeSheet(state: state, scope: scope, browsing: $browsing) { menu = false }
            }
            .homeLaunchTweaks()
            // Chooses the scope `-homeScopeTarget` names as a tap in the menu would, for a script (debug builds).
            .debugSignal("home.scope") {
                if let target = HomeLaunch.value("-homeScopeTarget") { scopes.choose(target == "all" ? .all : .of(key: target)) }
            }
    }
}

/// The top of the page (design-system §6 「范围胶囊」): at the left the scope
/// on a capsule of glass, which opens 看哪个项目; none without any project.
/// At the right the gear, to 设置.
private struct HomeTopBar: View {
    let scope: HomeScope?
    let onScope: () -> Void
    @Environment(Navigator.self) private var nav
    @Environment(\.sidebarCreate) private var create

    var body: some View {
        GlassGroup {
            HStack(spacing: 10) {
                if let scope {
                    Button(action: onScope) {
                        HStack(spacing: 7) {
                            if case .of(let key) = scope { Avatar(key) } else { Avatar.all() }
                            Text(scopeName(scope)).font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink).lineLimit(1)
                            Image(systemName: "chevron.down").font(.footnote.weight(.semibold)).foregroundStyle(DS.ink2)
                        }
                        .padding(.leading, 8).padding(.trailing, 14)
                        .frame(minHeight: DS.minHit)
                        .contentShape(Capsule())
                    }
                    .buttonStyle(GlassPressStyle())
                    .glass(Capsule())
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(scopeName(scope))
                    .accessibilityHint(L10n.homeIosChangeScope())
                    .accessibilityAddTraits(.isButton)
                    .accessibilityIdentifier("scope-title")
                }
                Spacer(minLength: 0)
                // In the wide window's sidebar 新任务 is here, and the gear at the sidebar's bottom.
                if let create {
                    SidebarCreateButton(create: create, schedules: false)
                } else {
                    GlassIconButton("gearshape", label: L10n.commonSettings()) { nav.open(.settings) }
                        .accessibilityIdentifier("settings-button")
                }
            }
        }
        .padding(.horizontal, DS.Space.card)
        .padding(.top, 4).padding(.bottom, 8)
        // A bar: one line at any text size; a long press shows what it says large.
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
    }
}

/// The sentence at the top (design-system §6 「大标题」): 34 / 700 with the
/// number of what waits in orange, and the line under it; 详情 there opens
/// why the server is out of reach.
private struct HomeHeadlineView: View {
    let headline: HomeHeadline
    let state: RemoteState
    @State private var details = false

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            title
                .font(.largeTitle.bold())
                .foregroundStyle(DS.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("home-headline")
            if !headline.sub.isEmpty || headline.details {
                HStack(alignment: .firstTextBaseline, spacing: 12) {
                    if !headline.sub.isEmpty {
                        Text(headline.sub)
                            .font(headline.subPath ? .subheadline.monospaced() : .body)
                            .foregroundStyle(headline.subError ? DS.red : DS.ink2)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    if headline.details {
                        LinkButton(L10n.commonDetails()) { details = true }.font(.body).padding(.vertical, -12)
                    }
                }
            }
        }
        .padding(.horizontal, DS.Space.page)
        .padding(.top, 6)
        .frame(maxWidth: .infinity, alignment: .leading)
        .kitSheet(isPresented: $details) {
            ServerDetails(state: state) { details = false }
        }
    }

    private var title: Text {
        if case .waiting(let n) = headline.kind {
            var title = AttributedString(L10n.homeWaitingCount(count: n))
            if let range = title.range(of: String(n)) { title[range].foregroundColor = DS.orange }
            return Text(title)
        }
        return Text(headline.title)
    }
}

/// The timeline of one scope, with what is typed in its filter and how much of it is opened.
private struct HomeTimeline: View {
    @Environment(AccountContext.self) private var context
    @Environment(AppModel.self) private var model
    let state: RemoteState
    let scope: HomeScope
    @Environment(Navigator.self) private var nav
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    // What is typed stays in memory: it is part of a title, which is content.
    @State private var query = HomeLaunch.value("-homeFilter") ?? ""
    @State private var shown = HomeShown(
        allAttention: HomeLaunch.flag("-homeExpand"), allRunning: HomeLaunch.flag("-homeExpand"))
    @State private var now = nowMs()

    private struct Grow: Hashable {
        var shown: Int
        var held: Int
        var grows: Bool
    }

    var body: some View {
        let list = homeList(state, scope: scope, query: query, shown: shown, now: now)
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                if let headline = homeHeadline(state, scope: scope) {
                    HomeHeadlineView(headline: headline, state: state)
                }
                HomeNotices(state: state).padding(.horizontal, DS.Space.card).padding(.top, 12)
                if list.showFilter {
                    FilterField(text: $query).padding(.horizontal, DS.Space.card).padding(.top, 18).padding(.bottom, 4)
                        .accessibilityIdentifier("title-filter")
                }
                if let empty = list.empty { emptyView(empty) }
                // One list of lines, each with an ID of its own: a session that changes parts moves, nothing else is redrawn.
                ForEach(list.items) { item in line(item, list) }
            }
            .padding(.bottom, 24)
            .frame(maxWidth: DS.column)
            .frame(maxWidth: .infinity)
            .animation(reduceMotion ? nil : Motion.enter(), value: list.items.map(\.id))
        }
        .scrollDismissesKeyboard(.immediately)
        .defaultScrollAnchor(HomeLaunch.flag("-homeEnd") ? .bottom : .top)
        // The ended sessions come a page at a time (architecture §8 「会话列表分页」):
        // another page when more are to be shown than were fetched, and every
        // page left once a filter is typed, which has to see every title.
        .fetchEarlier(state, page: list.wantPage, all: list.wantAll)
        .ticking($now)
    }

    @ViewBuilder private func line(_ item: HomeItem, _ list: HomeList) -> some View {
        switch item {
        case .label(let id, let title, _):
            SectionTitle(title: title, dated: id != "running")
        case .attentionCard(let s):
            HomeApprovalCard(state: state, session: s, scope: scope).padding(.top, 20)
        case .attention(let s, let first, let last):
            HomePendingRow(state: state, session: s, scope: scope, first: first, last: last)
                .padding(.top, first ? 10 : 0)
        case .moreAttention(let n):
            Button { shown.allAttention = true } label: { PendingMoreRow(text: L10n.homeMoreWaiting(count: n)) }
                .buttonStyle(RowHighlight())
        case .running(let s, let last):
            HomeSessionRow(state: state, session: s, now: now, meta: list.meta, last: last)
        case .moreRunning(let n):
            LinkButton(L10n.homeMoreLive(count: n), chevron: true) { shown.allRunning = true }
                .font(.subheadline)
                .padding(.horizontal, DS.Space.page)
        case .ended(let s):
            HomeEndedRow(session: s, now: now, project: list.meta.project)
        case .tail(let tail):
            Group {
                switch tail {
                case .text(let text): Text(text).font(.subheadline).foregroundStyle(DS.ink2).frame(maxWidth: .infinity, alignment: .leading)
                case .earlier(let text, let retry): EarlierTail(text, retry: retry)
                }
            }
            .padding(.horizontal, DS.Space.page).padding(.top, 13).padding(.bottom, 4)
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
            ProgressView().frame(maxWidth: .infinity).padding(.vertical, 48)
        case .scope:
            note(L10n.homeIosScopeStart())
        case .account(.noMachines):
            let access = AuthorityAccess(state: state, me: context.me)
            if access.isAuthority {
                // Only here is a button in the page the thing to do: 新任务 has no computer to run on yet.
                note(L10n.homeIosAddComputer())
                InkButton(L10n.homeAddDevice(), symbol: "plus") { nav.open(.addDevice()) }
                    .padding(.horizontal, DS.Space.page).padding(.top, 4)
            } else {
                note(access.emptyHint)
            }
        case .account(.noneOnline):
            note(L10n.homeIosNoOnline())
        case .account(.ready(let machine)):
            note(L10n.homeOnlineNewTask(name: machine))
        case .account(.unlinked):
            // Which computers are online is not known now; the sentence above says why.
            EmptyView()
        }
    }

    private func note(_ text: String) -> some View {
        Text(text).font(.body).foregroundStyle(DS.ink2)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, DS.Space.page).padding(.top, 10).padding(.bottom, 16)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// The notices under the sentence (design-system §6 「大标题」): the
/// phone's clock ahead of the account's, and a recovery request waiting
/// for the management client. They stay under it while the server is out
/// of reach.
private struct HomeNotices: View {
    let state: RemoteState
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav

    var body: some View {
        let authority = AuthorityAccess(state: state, me: context.me).isAuthority
        VStack(spacing: 8) {
            if !state.clockWarning.isEmpty {
                Notice(state.clockWarning, tone: .attention, symbol: "clock")
            }
            if authority && state.recoveryPending {
                Notice(
                    L10n.securitySomeoneIsRequestingManagementTakeoverWithARecoveryCode(value: String(describing: securityTime(state.recoveryReadyMs))),
                    tone: .attention, action: L10n.dockHandle()) { nav.open(.accountSecurity) }
            } else if authority && state.recoveryStatusStale {
                Notice(L10n.errorsIosRecoveryRequestStateMayBeOutOfDateConnect(), tone: .attention)
            }
        }
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
            SheetHead(L10n.projectsNewChooseDirectory(), onBack: { browsing = false })
            ProjectDirPicker(state) { machine, path in
                onClose()
                nav.open(.newProjectAt(machine: machine, path: path))
            }
        } else {
            SheetHead(L10n.homeChooseScope(), action: L10n.commonCancel(), onAction: onClose)
            ProjectList(state, onNew: { browsing = true }) {
                // 全部项目, when there is more than one project to choose between.
                if let all = scopeRows(state, current: scope).first(where: { $0.scope == .all }) {
                    Button {
                        choose(.all)
                    } label: {
                        HStack(spacing: 12) {
                            Avatar.all()
                            VStack(alignment: .leading, spacing: 0) {
                                Text(all.name).font(.headline).foregroundStyle(DS.ink).lineLimit(1)
                                Text(all.detail).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            check(all.current)
                        }
                        .padding(.vertical, 6)
                        .frame(minHeight: 52)
                    }
                    .buttonStyle(RowHighlight())
                    .overlay(alignment: .bottom) { RowSeparator().padding(.leading, 40) }
                    .accessibilityAddTraits(all.current ? .isSelected : [])
                }
            } row: { pr in
                let current = scope == .of(key: pr.key)
                ProjectRow(state, pr, action: { choose(.of(key: pr.key)) }) { check(current) }
                    .accessibilityAddTraits(current ? .isSelected : [])
            }
            LinkButton(L10n.homeManageProjects(), chevron: true) {
                onClose()
                nav.open(.manageProjects)
            }
            .font(.body)
            .padding(.leading, 40)
        }
    }

    private func choose(_ scope: HomeScope) {
        Haptics.selection()
        context.homeScopes.choose(scope)
        onClose()
    }

    /// The check of the current scope; its room is kept on the other rows so the names line up.
    private func check(_ on: Bool) -> some View {
        Image(systemName: "checkmark").font(.body.weight(.semibold)).foregroundStyle(DS.blue).opacity(on ? 1 : 0)
            .accessibilityHidden(true)
    }
}
