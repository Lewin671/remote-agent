// A project's page (Android's ProjectScreen in ui/Project.kt): its
// sessions, and at the bottom the message box that starts a new one, which
// is what the page is for on a phone (architecture §8 「主页的范围」). Under
// the title a line says which CLI and settings the next session uses; it
// and ⋯ open 项目设置. With the keyboard up the header folds into the
// navigation bar and the rows lose their previews, so that the sessions
// still fit above the box.
import RACore
import RAProto
import SwiftUI

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
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var showEnded = HomeLaunch.flag("-projectEnded")
    /// The message box has the keyboard.
    @State private var typing = false
    @State private var settings: ProjectSettingsRequest? = HomeLaunch.value("-projectSheet")
        .map { ProjectSettingsRequest(page: ProjectSettingsPage(rawValue: $0) ?? .main) }
    @State private var summaryModels: PBListModelsResponse?
    @State private var now = nowMs()

    private struct Models: Hashable {
        var machine: String
        var agent: PBAgent
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
        // The summary describes the CLI selected next to the message box.
        let selected = machine == nil ? projectConfig(state, key: key, pick: newSessionPick(state, draft)) : draft.config.value ?? initial
        let hidden = isHidden(state, key: key)
        // A folder just chosen that has no session yet: there is nothing to list, the page is its message box.
        let fresh = machine != nil && page.active.isEmpty && page.endedTotal == 0
        let folded = typing || fresh
        let name = projectName(key)
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        if fresh {
                            if let selected { chip(state, selected).padding(.horizontal, Theme.Space.page).padding(.vertical, 4) }
                        } else if !typing {
                            header(state, page, selected, name: name)
                        }
                        changeNotice(state)
                        if hidden { HiddenProjectNotice(state: state, projectKey: key) }
                        sessions(state, page, compact: typing)
                        Color.clear.frame(height: 12)
                    }
                    .readableColumn()
                    .animation(reduceMotion ? nil : .easeInOut(duration: 0.2), value: typing)
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
                        onFocus: { typing = $0 }, onMachine: { settings = ProjectSettingsRequest(page: .machine) })
                }
            }
        }
        .navBar(
            folded ? name : "", subtitle: !folded ? nil : fresh ? shortPath(key) : selected.map { foldedSummary(state, $0) } ?? shortPath(key),
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
        .task(id: selected.map { Models(machine: $0.machineId, agent: $0.agent) }) {
            summaryModels = nil
            guard let selected else { return }
            await HomeLaunch.settled(model)
            summaryModels = try? await context.remote.listModels(selected.machineId, agent: selected.agent)
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

    private func chip(_ state: RemoteState, _ selected: NewConfig) -> some View {
        ProjectSummaryChip(summary: projectSummary(state, selected, models: summaryModels), full: isFullAccess(state, selected)) {
            settings = ProjectSettingsRequest()
        }
    }

    /// The avatar, the name, where the project is, how many of its sessions go on and have ended, and the settings summary.
    private func header(_ state: RemoteState, _ page: ProjectPage, _ selected: NewConfig?, name: String) -> some View {
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
                if let selected { chip(state, selected).padding(.top, 6) }
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

    @ViewBuilder private func sessions(_ state: RemoteState, _ page: ProjectPage, compact: Bool) -> some View {
        if !page.active.isEmpty {
            SectionLabel("进行中", count: page.active.count).accessibilityElement(children: .combine)
            ForEach(page.active) { s in
                HomeSessionRow(state: state, session: s, now: now, meta: page.meta, last: s.id == page.active.last?.id, preview: !compact)
            }
        }
        let recent = page.recent(all: showEnded)
        let earlier = state.moreSessions && page.ended.count < page.endedTotal
        if (!recent.isEmpty || (showEnded && earlier)) && !compact {
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
