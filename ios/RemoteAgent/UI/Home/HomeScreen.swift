// 会话, the app's first page (client-v58 #home): renders `HomeViewModel`
// produced by the shared Go core and dispatches user actions back as `Event`s.
import RACore
import RAProto
import SwiftUI

private let attentionMax = 3
private let runningMax = 5
private let endedPage = 30

struct HomeScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var menu = ["scope", "browse"].contains(HomeLaunch.value("-homeSheet"))
    /// 看哪个项目 has turned into 新项目 · 选目录.
    @State private var browsing = HomeLaunch.value("-homeSheet") == "browse"
    @State private var allAttention = HomeLaunch.flag("-homeExpand")
    @State private var allRunning = HomeLaunch.flag("-homeExpand")
    @State private var filterText = HomeLaunch.value("-homeFilter") ?? ""

    var body: some View {
        let home = context.core.home
        let state = model.state
        let authority = AuthorityAccess(state: state, me: context.me).isAuthority
        HomeContent(
            home: home,
            diskReminders: context.core.settings.diskReminders,
            state: state,
            isAuthority: authority,
            authorityHint: AuthorityAccess(state: state, me: context.me).emptyHint,
            allAttention: $allAttention,
            allRunning: $allRunning,
            filterText: $filterText,
            onOpenScope: { menu = true },
            onRetryLoad: { context.dispatch { $0.homeRetryLoad = PBCoreHomeRetryLoadAction() } },
            onLoadMoreEnded: { context.dispatch { $0.homeLoadMoreEnded.step = Int32(endedPage) } },
            onOpenSession: { sid in nav.open(.session(id: sid)) },
            onOpenAddDevice: { nav.open(.addDevice()) },
            onOpenSecurity: { nav.open(.accountSecurity) }
        )
        .onChange(of: home.scopeKey) { _, _ in
            allAttention = false
            allRunning = false
        }
        .onChange(of: filterText, initial: true) { _, query in
            if query != home.filterQuery {
                context.dispatch { $0.homeSetFilter.query = query }
            }
        }
        .task {
            await context.coreHost.showHome()
        }
        .kitSheet(isPresented: $menu, size: browsing || home.sidebarProjects.canSearch ? .large : .fit, onDismiss: {
            browsing = false
        }) {
            HomeScopeSheet(
                home: home,
                browsing: $browsing,
                onClose: { menu = false }
            )
        }
        .homeLaunchTweaks()
        .debugSignal("home.scope") {
            if let target = HomeLaunch.value("-homeScopeTarget") {
                context.dispatch { $0.homeSetScope.scopeKey = target == "all" ? "" : target }
            }
        }
    }
}

/// Pure presentation body for `HomeScreen`, driven solely by `PBCoreHomeViewModel`.
struct HomeContent: View {
    let home: PBCoreHomeViewModel
    var diskReminders: [PBDiskReminderView] = []
    let state: RemoteState
    var isAuthority: Bool = true
    var authorityHint: String = ""
    @Binding var allAttention: Bool
    @Binding var allRunning: Bool
    @Binding var filterText: String
    var onOpenScope: () -> Void = {}
    var onRetryLoad: () -> Void = {}
    var onLoadMoreEnded: () -> Void = {}
    var onOpenSession: (String) -> Void = { _ in }
    var onOpenAddDevice: () -> Void = {}
    var onOpenSecurity: () -> Void = {}

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let timelineIDs = timelineAnimationIDs
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                HomeHeadlineView(headline: home.headline, state: state)
                HomeNotices(
                    state: state,
                    isAuthority: isAuthority,
                    onOpenSecurity: onOpenSecurity
                )
                .padding(.horizontal, DS.Space.card)
                .padding(.top, 12)
                HomeDiskReminders(reminders: diskReminders)
                if home.canFilter {
                    FilterField(text: $filterText)
                        .padding(.horizontal, DS.Space.card)
                        .padding(.top, 18)
                        .padding(.bottom, 4)
                        .accessibilityIdentifier("title-filter")
                }
                emptySection
                attentionSection
                runningSection
                endedSections
                tailSection
            }
            .padding(.bottom, 24)
            .frame(maxWidth: DS.column)
            .frame(maxWidth: .infinity)
            .animation(reduceMotion ? nil : Motion.enter(), value: timelineIDs)
        }
        .scrollDismissesKeyboard(.immediately)
        .defaultScrollAnchor(HomeLaunch.flag("-homeEnd") ? .bottom : .top)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(DS.paper.ignoresSafeArea())
        .floatingBar(.top) {
            HomeTopBar(
                scopeSelectable: home.scopeSelectable,
                scopeKey: home.scopeKey,
                scopeTitle: home.scopeTitle.rendered(),
                onScope: onOpenScope
            )
        }
    }

    private var timelineAnimationIDs: [String] {
        var ids: [String] = []
        if home.topAttentionActionable && !home.topAttentionCard.approvalID.isEmpty {
            ids.append("card:\(home.topAttentionCard.sessionID)")
        }
        ids += home.attention.map { "att:\($0.sessionID)" }
        ids += home.running.map { "run:\($0.sessionID)" }
        for g in home.endedGroups {
            ids.append("grp:\(g.groupKey)")
            ids += g.items.map { "end:\($0.sessionID)" }
        }
        return ids
    }

    @ViewBuilder private var emptySection: some View {
        let empty = home.empty
        switch empty.kind {
        case .noMachines:
            if isAuthority {
                note(empty.message.rendered())
                InkButton(L10n.homeAddDevice(), symbol: "plus", action: onOpenAddDevice)
                    .padding(.horizontal, DS.Space.page)
                    .padding(.top, 4)
            } else {
                note(authorityHint.isEmpty ? empty.message.rendered() : authorityHint)
            }
        case .noMachinesMember, .noOnline, .readyToStart, .scopedProject, .noNetwork:
            note(empty.message.rendered())
        case .listFailed:
            note(empty.message.rendered())
            if empty.canRetry {
                let retrySymbol = "arrow.clockwise"
                InkButton(L10n.commonRetry(), symbol: retrySymbol, action: onRetryLoad)
                    .padding(.horizontal, DS.Space.page)
                    .padding(.top, 4)
            }
        case .loading:
            ProgressView().frame(maxWidth: .infinity).padding(.vertical, 48)
        case .none, .unspecified, .UNRECOGNIZED:
            EmptyView()
        }
    }

    @ViewBuilder private var attentionSection: some View {
        if home.topAttentionActionable && !home.topAttentionCard.approvalID.isEmpty {
            HomeApprovalCard(card: home.topAttentionCard)
                .id(home.topAttentionCard.approvalID)
                .padding(.top, 20)
        }
        let rows = allAttention || !home.filterQuery.isEmpty
            ? home.attention
            : Array(home.attention.prefix(attentionMax))
        let hidden = max(0, home.attention.count - rows.count)
        ForEach(Array(rows.enumerated()), id: \.element.sessionID) { index, row in
            HomePendingRow(
                row: row,
                first: index == 0,
                last: index == rows.count - 1 && hidden == 0
            )
            .padding(.top, index == 0 ? 10 : 0)
        }
        if hidden > 0 {
            Button { allAttention = true } label: {
                PendingMoreRow(text: L10n.homeMoreWaiting(count: hidden))
            }
            .buttonStyle(RowHighlight())
        }
    }

    @ViewBuilder private var runningSection: some View {
        if !home.running.isEmpty {
            SectionTitle(title: L10n.homeLive(), dated: false)
            let rows = allRunning || !home.filterQuery.isEmpty
                ? home.running
                : Array(home.running.prefix(runningMax))
            ForEach(Array(rows.enumerated()), id: \.element.sessionID) { index, row in
                HomeSessionRow(row: row, last: index == rows.count - 1)
            }
            if rows.count < home.running.count {
                LinkButton(L10n.homeMoreLive(count: home.running.count - rows.count), chevron: true) {
                    allRunning = true
                }
                .font(.subheadline)
                .padding(.horizontal, DS.Space.page)
            }
        }
    }

    @ViewBuilder private var endedSections: some View {
        ForEach(home.endedGroups, id: \.groupKey) { group in
            SectionTitle(title: group.label.rendered(), dated: true)
            ForEach(group.items, id: \.sessionID) { row in
                HomeEndedRow(row: row)
            }
        }
    }

    @ViewBuilder private var tailSection: some View {
        let filterText = home.filterSummary.rendered()
        let tail = home.endedTail
        let tailText = tail.text.rendered()
        if !filterText.isEmpty || (tail.kind != .unspecified && !tailText.isEmpty) {
            VStack(alignment: .leading, spacing: 6) {
                if !filterText.isEmpty {
                    Text(filterText)
                        .font(.subheadline)
                        .foregroundStyle(DS.ink2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                if tail.kind == .loading || tail.kind == .offline || tail.kind == .failed {
                    EarlierTail(tailText, retry: tail.retry, onRetry: onRetryLoad)
                } else if !tailText.isEmpty {
                    Text(tailText)
                        .font(.subheadline)
                        .foregroundStyle(DS.ink2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .padding(.horizontal, DS.Space.page)
            .padding(.top, 13)
            .padding(.bottom, 4)
            .accessibilityIdentifier("timeline-tail")
            .task(id: "\(home.endedTotal):\(home.hasMoreEnded_p)") {
                if tail.kind == .moreEnded || tail.kind == .moreOnServer || home.hasMoreEnded_p {
                    onLoadMoreEnded()
                }
            }
        }
    }

    private func note(_ text: String) -> some View {
        Text(text).font(.body).foregroundStyle(DS.ink2)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, DS.Space.page).padding(.top, 10).padding(.bottom, 16)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// The top of the page (design-system §6 「范围胶囊」).
private struct HomeTopBar: View {
    let scopeSelectable: Bool
    let scopeKey: String
    let scopeTitle: String
    let onScope: () -> Void
    @Environment(Navigator.self) private var nav
    @Environment(\.sidebarCreate) private var create

    var body: some View {
        GlassGroup {
            HStack(spacing: 10) {
                if scopeSelectable {
                    Button(action: onScope) {
                        HStack(spacing: 7) {
                            if !scopeKey.isEmpty { Avatar(scopeKey) } else { Avatar.all() }
                            Text(scopeTitle).font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink).lineLimit(1)
                            Image(systemName: "chevron.down").font(.footnote.weight(.semibold)).foregroundStyle(DS.ink2)
                        }
                        .padding(.leading, 8).padding(.trailing, 14)
                        .frame(minHeight: DS.minHit)
                        .contentShape(Capsule())
                    }
                    .buttonStyle(GlassPressStyle())
                    .glass(Capsule())
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(scopeTitle)
                    .accessibilityHint(L10n.homeIosChangeScope())
                    .accessibilityAddTraits(.isButton)
                    .accessibilityIdentifier("scope-title")
                }
                Spacer(minLength: 0)
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
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
    }
}

/// The sentence at the top (design-system §6 「大标题」).
private struct HomeHeadlineView: View {
    let headline: PBCoreHomeHeadlineView
    let state: RemoteState
    @State private var details = false

    var body: some View {
        let subText = headline.sub.rendered()
        VStack(alignment: .leading, spacing: 7) {
            title
                .font(.largeTitle.bold())
                .foregroundStyle(DS.ink)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("home-headline")
            if !subText.isEmpty || headline.detailsAction {
                HStack(alignment: .firstTextBaseline, spacing: 12) {
                    if !subText.isEmpty {
                        Text(subText)
                            .font(headline.subPath ? .subheadline.monospaced() : .body)
                            .foregroundStyle(headline.subError ? DS.red : DS.ink2)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    if headline.detailsAction {
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
        let rendered = headline.title.rendered()
        if headline.kind == .waiting && headline.highlightCount > 0 {
            var attributed = AttributedString(rendered)
            if let range = attributed.range(of: String(headline.highlightCount)) {
                attributed[range].foregroundColor = DS.orange
            }
            return Text(attributed)
        }
        return Text(rendered)
    }
}

/// Notices under the headline: device clock skew and recovery requests.
private struct HomeNotices: View {
    let state: RemoteState
    let isAuthority: Bool
    let onOpenSecurity: () -> Void

    var body: some View {
        VStack(spacing: 8) {
            if !state.clockWarning.isEmpty {
                Notice(state.clockWarning, tone: .attention, symbol: "clock")
            }
            if isAuthority && state.recoveryPending {
                Notice(
                    L10n.securitySomeoneIsRequestingManagementTakeoverWithARecoveryCode(
                        value: String(describing: securityTime(state.recoveryReadyMs))
                    ),
                    tone: .attention,
                    action: L10n.dockHandle(),
                    onAction: onOpenSecurity
                )
            } else if isAuthority && state.recoveryStatusStale {
                Notice(L10n.errorsIosRecoveryRequestStateMayBeOutOfDateConnect(), tone: .attention)
            }
        }
    }
}

/// 看哪个项目: rendered directly from `home.scopeMenu` and `home.sidebarProjects`.
private struct HomeScopeSheet: View {
    let home: PBCoreHomeViewModel
    @Binding var browsing: Bool
    let onClose: () -> Void
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context

    var body: some View {
        if browsing {
            SheetHead(L10n.projectsNewChooseDirectory(), onBack: { browsing = false })
            CoreDirPicker(pickerKey: "home-scope") { machine, path in
                onClose()
                nav.open(.newProjectAt(machine: machine, path: path))
            }
        } else {
            SheetHead(L10n.homeChooseScope(), action: L10n.commonCancel(), onAction: onClose)
            CoreProjectList(listing: home.sidebarProjects, onNew: { browsing = true }) {
                if let all = home.scopeMenu.first(where: { $0.allProjects }) {
                    Button {
                        choose("")
                    } label: {
                        HStack(spacing: 12) {
                            Avatar.all()
                            VStack(alignment: .leading, spacing: 0) {
                                Text(all.name.rendered()).font(.headline).foregroundStyle(DS.ink).lineLimit(1)
                                Text(all.subtitle.rendered()).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            check(all.checked)
                        }
                        .padding(.vertical, 6)
                        .frame(minHeight: 52)
                    }
                    .buttonStyle(RowHighlight())
                    .overlay(alignment: .bottom) { RowSeparator().padding(.leading, 40) }
                    .accessibilityAddTraits(all.checked ? .isSelected : [])
                }
            } row: { pr in
                let current = pr.key == home.scopeKey
                CoreProjectRow(row: pr, action: { choose(pr.key) }) { check(current) }
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

    private func choose(_ scopeKey: String) {
        Haptics.selection()
        context.dispatch { $0.homeSetScope.scopeKey = scopeKey }
        onClose()
    }

    private func check(_ on: Bool) -> some View {
        let checkSymbol = "checkmark"
        return Image(systemName: checkSymbol).font(.body.weight(.semibold)).foregroundStyle(DS.blue).opacity(on ? 1 : 0)
            .accessibilityHidden(true)
    }
}

#if DEBUG
#Preview("Home Timeline") {
    HomeContent(
        home: ScreenGoldens.home("home_timeline", index: 0),
        state: RemoteState(),
        allAttention: .constant(false),
        allRunning: .constant(false),
        filterText: .constant("")
    )
}
#endif
