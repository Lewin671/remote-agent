// A project's page (Android's ProjectScreen in ui/Project.kt): its
// sessions, and at the bottom the message box that starts a new one, which
// is what the page is for on a phone (architecture §8 「主页的范围」). Under
// the title a line says which CLI and settings the next session uses; it
// and ⋯ open 项目设置. The header folds into the navigation bar once it
// is scrolled away; the keyboard only shortens the list, so nothing jumps
// as it comes and goes (design-system §6 「输入框」).
import Observation
import RACore
import RAProto
import SwiftUI

/// The model list the settings summary reads a CLI's default model from,
/// for the machine and CLI the summary is about now. The lists are fetched
/// by tasks the page cannot stop (`Remote.listModels` shares one request
/// among its callers), so an answer may arrive after the page has moved on
/// to another CLI: it is only kept while it is still the one asked for.
@MainActor
@Observable
final class SummaryModels {
    struct Key: Hashable, Sendable {
        var machine: String
        var agent: PBAgent
    }

    private(set) var list: PBListModelsResponse?
    @ObservationIgnored private var wanted: Key?

    /// Shows nothing, then the list of `key` once `fetch` has it; nil shows nothing.
    func load(_ key: Key?, fetch: @MainActor (Key) async -> PBListModelsResponse?) async {
        wanted = key
        list = nil
        guard let key else { return }
        let got = await fetch(key)
        guard wanted == key else { return }
        list = got
    }
}

/// A project's page. `key` is the project's path; `focusNew` brings the keyboard up in the box.
struct ProjectScreen: View {
    let key: String
    let focusNew: Bool

    var body: some View {
        ProjectPageView(key: key, machine: nil, focus: focusNew)
    }
}

/// The CLI a draft is on, as the pick its project's configuration is read
/// with. A CLI the draft only fell back to is not one: the pick stays.
@MainActor
func newSessionPick(_ state: RemoteState, _ draft: NewSessionDraft) -> PBAgent? {
    if let c = draft.config.value, missingAgent(state, c, pick: draft.agent) == nil { return c.agent }
    return draft.agent
}

/// The page of a project, and of a folder just chosen for a new one
/// (`machine`: the computer it was chosen on, where its first session
/// starts unless the folder turns out to be a project with settings of its
/// own).
struct ProjectPageView: View {
    let key: String
    let machine: String?
    let focus: Bool
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var showEnded = HomeLaunch.flag("-projectEnded")
    /// The header is scrolled out of sight: the navigation bar says what it said.
    @State private var scrolledAway = false
    @State private var settings: ProjectSettingsRequest? = HomeLaunch.value("-projectSheet")
        .map { ProjectSettingsRequest(page: ProjectSettingsPage(rawValue: $0) ?? .main) }
    @State private var summaryModels = SummaryModels()
    @State private var now = nowMs()

    private static let scroll = "project-scroll"

    /// Where the header ends in the scroll view; nil while nothing measures it.
    private struct HeaderEnd: PreferenceKey {
        static let defaultValue: CGFloat? = nil
        static func reduce(value: inout CGFloat?, nextValue: () -> CGFloat?) { value = nextValue() ?? value }
    }

    var body: some View {
        let state = model.state
        let page = projectPage(state, key: key)
        // What the next session starts with, and whether the session it inherits from is known
        // yet (`ProjectStart`): the message box fetches pages until it is, and starts nothing before.
        let seed = projectStart(state, key: key, machine: machine)
        let draft = context.newSessionDrafts.of(key, initial: seed?.config, settled: seed?.settled != false)
        let start = projectStart(state, key: key, pick: draft.agent, machine: machine)
        let initial = start?.config ?? machine.map { NewConfig(machineId: $0, cwd: key) }
        // 项目设置 is about the project: its machine and its set for the CLI selected next to the message box.
        let selected = machine == nil ? projectConfig(state, key: key, pick: newSessionPick(state, draft)) : draft.config.value ?? initial
        let hidden = isHidden(state, key: key)
        // The summary says what ↑ starts with: the draft's own configuration, which the project's settings no
        // longer change once a draft is begun or a machine was picked for this start.
        let launch = launchConfig(draft: draft.config.value, project: selected, hidden: hidden)
        // A folder just chosen that has no session yet: there is nothing to list, the page is its message box.
        let fresh = machine != nil && page.active.isEmpty && page.endedTotal == 0
        let folded = scrolledAway || fresh
        let name = projectName(key)
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        if fresh {
                            if let launch { chip(state, launch).padding(.horizontal, Theme.Space.page).padding(.vertical, 4) }
                        } else {
                            header(state, page, launch, name: name)
                                .background {
                                    GeometryReader { g in
                                        Color.clear.preference(key: HeaderEnd.self, value: g.frame(in: .named(Self.scroll)).maxY)
                                    }
                                }
                                // A lazy stack lets go of what is far off the screen, and then nothing measures it.
                                .onDisappear { scrolledAway = true }
                        }
                        changeNotice(state)
                        if hidden { HiddenProjectNotice(state: state, projectKey: key) }
                        sessions(state, page)
                        Color.clear.frame(height: 12)
                    }
                    .readableColumn()
                }
                .coordinateSpace(.named(Self.scroll))
                .onPreferenceChange(HeaderEnd.self) { end in
                    if let end { scrolledAway = end <= 0 }
                }
                .scrollDismissesKeyboard(.interactively)
                // 最近结束 wants three; the whole list asks for a page each time its end is reached. Either
                // way what is shown must not reach past the pages fetched.
                .fetchEarlier(state, page: needsEndedPage(state, held: page.ended, want: page.wanted(all: showEnded), total: page.endedTotal))
            }
            // The box stays above the keyboard, and the list ends above the box.
            .safeAreaInset(edge: .bottom, spacing: 0) {
                // A hidden project stays reachable from its sessions and notifications; adding it again comes before a new session there.
                if !hidden {
                    NewSessionBox(
                        state: state, key: key, initial: initial, name: name, focus: focus, settled: start?.settled != false,
                        onMachine: { settings = ProjectSettingsRequest(page: .machine) })
                }
            }
        }
        .navBar(
            folded ? name : "", subtitle: !folded ? nil : fresh ? shortPath(key) : launch.map { foldedSummary(state, $0) } ?? shortPath(key),
            onMore: selected == nil ? nil : { settings = ProjectSettingsRequest() }
        )
        .kitSheet(item: $settings) { request in
            if let selected {
                ProjectSettingsSheet(
                    cwd: key, machine: selected.machineId, agent: selected.agent, open: request.page,
                    onDone: { op in
                        if let op { store(op, draft) }
                        settings = nil
                    },
                    onClose: { settings = nil })
            }
        }
        .task(id: launch.map { SummaryModels.Key(machine: $0.machineId, agent: $0.agent) }) {
            let remote = context.remote
            await summaryModels.load(launch.map { SummaryModels.Key(machine: $0.machineId, agent: $0.agent) }) { key in
                await HomeLaunch.settled(model)
                return try? await remote.listModels(key.machine, agent: key.agent)
            }
        }
        .task(id: key) {
            // The folder is stored with the machine chosen, so it is listed before its first session.
            if let machine { context.addProject(model.state, machine: machine, path: key) }
        }
        .ticking($now)
        .homeLaunchTweaks()
    }

    /// Stores a change to the project's settings; the draft takes them at once, written or not.
    private func store(_ op: PrefsOp.SetSettings, _ draft: NewSessionDraft) {
        let state = model.state
        context.prefsChanges.run(context.remote, .setSettings(op))
        let saved = applySettings(stored: savedSettings(state, key: key), op: op, now: nowMs())
        if let c = projectConfig(state, key: key, pick: draft.agent, machine: machine, saved: saved) { draft.config.choose(c) }
    }

    private func chip(_ state: RemoteState, _ launch: NewConfig) -> some View {
        ProjectSummaryChip(summary: projectSummary(state, launch, models: summaryModels.list), full: isFullAccess(state, launch)) {
            settings = ProjectSettingsRequest()
        }
    }

    /// The avatar, the name, where the project is, how many of its sessions go on and have ended, and the settings summary.
    private func header(_ state: RemoteState, _ page: ProjectPage, _ launch: NewConfig?, name: String) -> some View {
        HStack(alignment: .top, spacing: 14) {
            Avatar(key, size: 52)
            VStack(alignment: .leading, spacing: 0) {
                Text(name).type(T.h22, Theme.fg).lineLimit(2).accessibilityAddTraits(.isHeader)
                Text(page.subtitle).type(T.sm, Theme.muted).lineLimit(2)
                HStack(spacing: 0) {
                    Text("\(page.active.count) 个进行中 · ").type(T.sm, Theme.muted)
                    Button {
                        showEnded.toggle()
                    } label: {
                        // With none there is nothing to open: it is said as the count before it is.
                        Group {
                            if page.endedTotal == 0 {
                                Text("已结束 0").type(T.sm, Theme.muted)
                            } else {
                                ArrowText("已结束 \(page.endedTotal)", style: T.smB, direction: showEnded ? .down : .right)
                            }
                        }
                            // 44 pt to the finger without making the header taller.
                            .padding(.vertical, 12).contentShape(Rectangle()).padding(.vertical, -12)
                    }
                    .buttonStyle(PressStyle())
                    .disabled(page.endedTotal == 0)
                    .accessibilityValue(showEnded ? "已展开" : "已收起")
                }
                .padding(.top, 2)
                if let launch { chip(state, launch).padding(.top, 6) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, Theme.Space.page).padding(.bottom, 2)
    }

    /// A change to the project's settings on its way, or why it did not arrive.
    @ViewBuilder private func changeNotice(_ state: RemoteState) -> some View {
        if let change = context.prefsChanges.states[key], change.ofSettings {
            Group {
                switch change {
                case .saving:
                    Banner("正在保存项目设置…", icon: Lucide.loaderCircle)
                case .failed(let op, let error):
                    Banner("项目设置没能保存：\(error)", tone: .error, action: "重试", onAction: { context.prefsChanges.run(context.remote, op) })
                }
            }
            .padding(.horizontal, 12).padding(.vertical, 4)
        }
    }

    @ViewBuilder private func sessions(_ state: RemoteState, _ page: ProjectPage) -> some View {
        if !page.active.isEmpty {
            SectionLabel("进行中", count: page.active.count).accessibilityElement(children: .combine)
            ForEach(page.active) { s in
                HomeSessionRow(state: state, session: s, now: now, meta: page.meta, last: s.id == page.active.last?.id)
            }
        }
        let recent = page.recent(all: showEnded)
        let earlier = state.moreSessions && page.ended.count < page.endedTotal
        if !recent.isEmpty || (showEnded && earlier) {
            SectionLabel(showEnded ? "已结束" : "最近结束", count: showEnded ? page.endedTotal : nil).accessibilityElement(children: .combine)
            ForEach(recent) { HomeEndedRow(session: $0, now: now) }
            // On screen when the end of the list is reached: that is when the next page is fetched.
            if showEnded && earlier {
                EarlierTail(state)
                    .padding(.init(top: 12, leading: 35, bottom: 4, trailing: Theme.Space.page))
                    .fetchEarlier(state, page: true)
                    .accessibilityIdentifier("project-earlier")
            }
        }
        if page.active.isEmpty && page.endedTotal == 0 && !isHidden(state, key: key) {
            Text("这个项目还没有会话。").type(T.sm, Theme.muted).padding(Theme.Space.page)
        }
    }
}

/// 这个项目已从列表移除 · 重新添加: a notice, not a button in the way (design-system §1).
private struct HiddenProjectNotice: View {
    let state: RemoteState
    let projectKey: String
    @Environment(AccountContext.self) private var context

    var body: some View {
        let change = context.prefsChanges.states[projectKey]
        let saving = change?.saving == true
        let blocked = prefsBlocked(state)
        VStack(alignment: .leading, spacing: 0) {
            Banner(
                saving ? "正在添加到项目列表…" : "这个项目已从列表移除", action: blocked == nil && !saving ? "重新添加" : nil,
                onAction: {
                    Haptics.confirm()
                    context.prefsChanges.run(context.remote, .unhide(path: projectKey))
                })
            if let blocked { Hint(blocked, color: Theme.orangeInk) }
            if case .failed(_, let error)? = change { Hint("没能保存：\(error)", color: Theme.red) }
        }
        .padding(.horizontal, Theme.Space.page).padding(.vertical, 4)
    }
}
