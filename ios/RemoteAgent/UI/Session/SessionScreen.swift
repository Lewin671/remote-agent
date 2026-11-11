// A session: its navigation bar, the 聊天 / 终端 tabs, and under them the
// chat or the terminal. Android's `SessionScreen` and `SessionTopBar` in
// ui/Session.kt; docs/design-system.md §6 「会话导航栏」「视图切换」.
//
// The bar's first line is the title alone; the second is the session's
// state and its approval mode, red only for full access. Everything else
// about the session (CLI, project, computer, folder) heads the ⋯ menu, a
// list whose rows are the mode, the model and the context (「名称 …… 值 ›」),
// then 项目设置, 重命名, 分叉会话, and in red 中断 and 结束会话…, in three groups of a system sheet. A
// choice that cannot be made now is not hidden: it is greyed, with when it
// can be under it.
import RACore
import RAMarkdownUI
import RAProto
import SwiftUI

/// A session: its chat and its terminal. `terminal` opens it on the terminal.
struct SessionScreen: View {
    let id: String
    let terminal: Bool

    @Environment(AppModel.self) private var model
    @State private var finder = SessionFinder()
    @State private var attempt = 0

    var body: some View {
        Group {
            if let session = model.state.sessions[id] {
                SessionPage(session: session, terminal: terminal)
            } else {
                // Not held here: asked for by its ID, and only the server saying it has none makes it missing.
                SessionAbsent(lookup: finder.lookup) { attempt += 1 }
                    .navBar("会话")
            }
        }
        .sessionLookup(id, attempt: attempt, into: finder)
        .sessionSamples(id)
    }
}

/// What a page about a session shows while the session is not held here.
struct SessionAbsent: View {
    let lookup: SessionLookup
    let onRetry: () -> Void
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav

    var body: some View {
        let online = model.state.conn == .online
        // Offline nothing was asked: only a server that answered "none" is believed.
        let note = online || lookup == .missing ? lookupNote(lookup) : nil
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                Spacer(minLength: 24)
                if let note {
                    Text(note).font(.headline).ink(DS.ink).multilineTextAlignment(.center).accessibilityAddTraits(.isHeader)
                    // The server could not be asked: it is asked again by itself, and at once on a tap.
                    if case .failed = lookup { TextAction("重试", alignment: .center, action: onRetry) }
                    TextAction("返回", alignment: .center) { nav.back() }
                } else {
                    Mark(Mark.progress, color: DS.ink2, size: 22, relativeTo: .body)
                    Text(online ? "读取中…" : "连上服务器后读取这个会话").font(.subheadline).ink(DS.ink2).padding(.top, 12)
                        .multilineTextAlignment(.center)
                }
                Spacer(minLength: 24)
            }
            .padding(.horizontal, 24)
        }
    }
}

private enum SessionSheet: String, Identifiable {
    case mode, model

    var id: String { rawValue }
}

/// The view last picked on this device; every session opens in it.
private let sessionTabKey = "session-view.tab"

private struct SessionPage: View {
    let session: SessionView
    let terminal: Bool

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.horizontalSizeClass) private var width
    @State private var chat = SessionModel()
    @State private var tab: Int
    @State private var menu = false
    @State private var sheet: SessionSheet?
    @State private var scheduleSending = false
    @State private var rename = false
    @State private var projectSettings: ProjectSettingsRequest?
    @State private var confirmStop = false
    @State private var act = ActionRunner()
    /// Claude Code records full model IDs; the list gives the alias to show.
    @State private var models: PBListModelsResponse?

    init(session: SessionView, terminal: Bool) {
        self.session = session
        self.terminal = terminal
        // Opening on the terminal, as after starting a session, is not picking it.
        _tab = State(initialValue: terminal ? 1 : UserDefaults.standard.integer(forKey: sessionTabKey))
    }

    private func pick(_ view: Int) {
        tab = view
        UserDefaults.standard.set(view, forKey: sessionTabKey)
    }

    /// A choice of the menu that opens something: only once the menu is out of the way.
    private func afterMenu(_ open: @escaping @MainActor () -> Void) {
        Task {
            try? await Task.sleep(nanoseconds: 220_000_000)
            open()
        }
    }

    private func command(_ runner: ActionRunner, _ build: @escaping @Sendable (inout PBCommand) -> Void) {
        let (remote, sid) = (context.remote, session.id)
        runner.run {
            var cmd = PBCommand()
            build(&cmd)
            try await remote.command(sid, cmd)
        }
    }

    private var params: ChatParams {
        let state = model.state
        let store = context.sessionStore
        return ChatParams(
            agent: session.agent, machine: machineLabel(state, id: session.machineId), cwd: session.info?.cwd ?? "",
            home: state.machines[session.machineId]?.meta?.home ?? "", me: context.me, devices: state.devices,
            texts: store.texts(session.id), prompt: store.prompt(session.id))
    }

    var body: some View {
        let state = model.state
        let s = session
        let sid = s.id
        let status = shownStatus(state, s)
        let running = chat.derived.running || status == .running
        let hasTerminal = terminalAvailable(status)
        // A wide window shows both side by side as a third choice (design-system §6 「宽屏」).
        let wide = width == .regular
        let both = wide && tab == 2 && hasTerminal
        let term = tab == 1 && hasTerminal
        let views = wide ? ["聊天", "终端", "并排"] : ["聊天", "终端"]
        let view = Binding(get: { both ? 2 : term ? 1 : 0 }, set: { pick($0) })
        let live = status != .exited && status != .offline
        let mode = status == .exited ? nil : modeLabel(s)
        // Unknown context is left out, never shown as 0%.
        let percent = live ? contextPercent(s) : nil
        let high = (percent ?? 0) > 80
        let refusal = protocolMismatch(state.machines[s.machineId])
        let subtitle = sessionSubtitle(status: status, mode: mode)
        Group {
            if both {
                // The terminal is a dark panel set into the page beside the chat.
                HStack(spacing: 0) {
                    ChatPane(session: s, status: status, chat: chat) { pick(1) }
                    TerminalPane(sessionId: sid, running: true)
                        .background(TermColors.bg)
                        .clipShape(RoundedRectangle(cornerRadius: DS.Radius.command, style: .continuous))
                        .padding(12)
                }
            } else if term {
                TerminalPane(sessionId: sid, running: true)
            } else {
                ChatPane(session: s, status: status, chat: chat) { pick(1) }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background((term ? TermColors.bg : DS.paper).ignoresSafeArea())
        .markdownTheme(DS.markdown)
        // Under the title: 聊天 / 终端 on glass, then the strips of what stands in the way, the content sliding under them.
        .floatingBar(.top) {
            VStack(spacing: 8) {
                // Wide, the choice is in the bar beside ⋯ and the top is one line.
                if !wide {
                    GlassSegmented(items: views, selected: view, enabled: { $0 == 0 || hasTerminal }, dot: status == .popup ? 1 : nil)
                        .accessibilityIdentifier("session-view")
                }
                ConnectionStrip()
                if let refusal { GlassStrip(text: refusal, symbol: "exclamationmark.triangle", tone: .attention) }
                if !act.error.isEmpty { GlassStrip(text: act.error, symbol: "xmark", tone: .error) }
                SessionScheduleBar(session: s)
            }
            .padding(.horizontal, DS.Space.m)
            .padding(.top, 4).padding(.bottom, 8)
            .frame(maxWidth: DS.column)
            .environment(\.colorScheme, term ? .dark : colorScheme)
        }
        .tint(DS.blue)
        .task(id: s.info?.scheduledTaskID) {
            guard s.info?.taskOwned == true, let task = s.info?.scheduledTaskID, !task.isEmpty,
                  nav.path.last == page, !nav.path.dropLast().contains(.schedule(machine: s.machineId, id: task)) else { return }
            nav.path.insert(.schedule(machine: s.machineId, id: task), at: max(0, nav.path.count - 1))
        }
        .task(id: sid) { SessionImages.restore(session: sid, remote: context.remote, store: context.sessionStore) }
        // The system's bar, with nothing drawn under it: the back button, the title with its line of state,
        // and ⋯; from iOS 26 they are on glass, and the content runs under them.
        .navigationBarTitleDisplayMode(.inline)
        .navigationTitle(titleOf(s))
        .toolbarRole(.editor)
        .toolbar {
            ToolbarItem(placement: .principal) {
                SessionTitle(
                    title: titleOf(s), mark: StatusKind(status), status: subtitle.status, mode: subtitle.mode, danger: subtitle.danger)
            }
            if wide {
                ToolbarItem(placement: .topBarTrailing) {
                    GlassSegmented(items: views, selected: view, enabled: { $0 == 0 || hasTerminal }, dot: status == .popup ? 1 : nil)
                        .accessibilityIdentifier("session-view")
                }
            }
            ToolbarItem(placement: .topBarTrailing) {
                Button { menu = true } label: {
                    Image(systemName: "ellipsis")
                        .foregroundStyle(DS.ink)
                        .overlay(alignment: .topTrailing) {
                            // Something in the menu wants a look: a context that is nearly full.
                            if high { Circle().fill(DS.orange).frame(width: 7, height: 7).offset(x: 5, y: -5) }
                        }
                }
                .accessibilityLabel("更多")
                .accessibilityValue(high ? "有需要注意的项" : "")
            }
        }
        .toolbarBackground(.hidden, for: .navigationBar)
        .toolbarColorScheme(term ? .dark : nil, for: .navigationBar)
        .menuSheet(isPresented: $menu, groups: groups(status: status, running: running, live: live, mode: mode, percent: percent)) {
            // What the menu is about: the CLI, the project, the computer, then the folder (design-system §6 「弹层与对话框」).
            VStack(alignment: .leading, spacing: 2) {
                // The computer is named only where the account has more than one.
                Text(([agentName(s.agent), projectName(projectKey(s))] + (state.machines.count > 1 ? [machineLabel(state, id: s.machineId)] : [])).joined(separator: " · "))
                    .font(.headline).foregroundStyle(DS.ink)
                Text(shortPath(s.info?.cwd ?? "")).font(.footnote.monospaced()).foregroundStyle(DS.ink2)
            }
        }
        .onChange(of: menu) { _, shown in if !shown { context.sessionStore.dismissForkError(sid) } }
        .kitSheet(item: $sheet) { which in
            switch which {
            case .mode: SessionModeSheet(session: s, startConfirm: SessionDebug.shows("confirmFull")) { sheet = nil }
            case .model: SessionModelSheet(session: s, running: running) { sheet = nil }
            }
        }
        // 项目设置 opens here, over the session, as on Android: on the session's own CLI, whose set this page is about.
        .kitSheet(item: $projectSettings) { _ in
            let key = projectKey(s)
            if let initial = projectConfig(state, key: key) {
                ProjectSettingsSheet(
                    cwd: key, machine: initial.machineId, agent: s.agent,
                    onDone: { op in
                        if let op { context.prefsChanges.run(context.remote, .setSettings(op)) }
                        projectSettings = nil
                    },
                    onClose: { projectSettings = nil })
            }
        }
        .sheet(isPresented: $scheduleSending) {
            NavigationStack {
                NewScheduleScreen(sessionID: sid, onCreated: { route in
                    guard scheduleSending else { return }
                    scheduleSending = false; nav.open(route, from: page)
                }, onClose: { scheduleSending = false })
            }.sheetSurface().presentationDetents([.large])
        }
        .sessionRename(isPresented: $rename, session: s)
        .confirm(
            isPresented: $confirmStop, title: "结束这个会话？",
            text: "\(machineLabel(state, id: s.machineId)) 上的 \(agentName(s.agent)) 会被关掉，聊天记录保留，之后可以恢复。",
            confirm: "结束", danger: true
        ) {
            command(act) { $0.stopSession = .with { $0.sessionID = sid } }
        }
        .task(id: sid) { await chat.follow(context.remote, sid) }
        .onChange(of: params, initial: true) { _, p in chat.use(p) }
        // The menu's model row says the model's name, so it reads the list when it opens too.
        .task(id: [s.machineId, menu ? "menu" : ""]) {
            if agentInfo(s.agent).sessionNeedsModelList || menu { models = try? await context.remote.listModels(s.machineId, agent: s.agent) }
        }
        .task {
            #if DEBUG
            if model.isDemo, HomeLaunch.value("-schedulePanel") == "session-schedule" { scheduleSending = true }
            #endif
        }
        .debugSignal("session.schedule") {
            if context.schedules.online(s.machineId) { scheduleSending = true }
        }
        .debugSignal("session.menu") { menu = true }
        .debugSignal("session.mode") { sheet = .mode }
        .debugSignal("session.model") { sheet = .model }
        .debugSignal("session.rename") { rename = true }
        .debugSignal("session.project") { projectSettings = ProjectSettingsRequest() }
        .debugSignal("session.close") {
            menu = false
            sheet = nil
            rename = false
            projectSettings = nil
        }
        .task {
            switch SessionDebug.shown {
            case "menu": menu = true
            case "mode", "confirmFull": sheet = .mode
            case "model": sheet = .model
            case "rename": rename = true
            case "project": projectSettings = ProjectSettingsRequest()
            case "stop": confirmStop = true
            default: break
            }
        }
    }

    /// The menu's three groups: the settings (审批模式, 模型, 上下文), what can be done, and 结束会话….
    private func groups(status: Status, running: Bool, live: Bool, mode: ModeLabel?, percent: Int?) -> [[SheetAction]] {
        let state = model.state
        let s = session
        let sid = s.id
        let store = context.sessionStore
        // Kept for the account: a fork whose outcome is unknown is still that fork after the menu or the page was left.
        let fork = store.fork(sid)
        let forking = fork.running
        // The title is kept on the computer: an ended session can be renamed too, but only while the computer is reachable.
        let reachable = state.conn == .online && state.machines[s.machineId]?.online == true
        let sent = sentMessage(s, events: [PBEvent]()) || chat.spoke
        let forkBlock = forkBlocked(status: status, running: running, sent: sent)
        let raw = s.info?.model ?? ""
        // The model's name, as the new-session summary says it; until the list is there, the name the CLI's slugs have.
        let name = models.flatMap { findModel($0.models, id: raw) { $0.slug }?.displayName }.flatMap { $0.isEmpty ? nil : $0 } ?? agentInfo(s.agent).modelLabel(raw)
        let effort = s.info?.effort ?? ""
        var settings: [SheetAction] = []
        var list: [SheetAction] = []
        var end: [SheetAction] = []
        if live {
            // A mode that could not be read has no row: there is nothing to say of it.
            for row in menuSettings(mode: mode, model: name, effort: effort, percent: percent) {
                settings.append(SheetAction(row.text, value: row.value, danger: row.danger, warn: row.warn, enabled: !forking) {
                    afterMenu { sheet = row.opens == .mode ? .mode : .model }
                })
            }
        }
        list.append(SheetAction("项目设置", enabled: !forking && projectConfig(state, key: projectKey(s)) != nil) {
            afterMenu { projectSettings = ProjectSettingsRequest() }
        })
        list.append(SheetAction("定时发送…", enabled: reachable && state.machines[s.machineId]?.meta?.capabilities.scheduledTasks == true, note: state.machines[s.machineId]?.meta?.capabilities.scheduledTasks == true ? "" : "需要更新 remote-agent") {
            afterMenu { scheduleSending = true }
        })
        if reachable {
            list.append(SheetAction("重命名", enabled: !forking) { afterMenu { rename = true } })
            if agentInfo(s.agent).forks {
                // A fork starts at once, in the menu: its row shows the progress, and a failure under it.
                let row = forkAction(blocked: forkBlock, running: forking, error: fork.error, frozen: fork.frozen, expired: fork.expired)
                list.append(SheetAction(
                    row.text, enabled: row.enabled, note: row.note, noteError: row.noteError, busy: row.busy, keepOpen: true
                ) {
                    if row.tap == .abandon { return store.abandonFork(sid) }
                    let made = store.forkSession(context.remote, machineId: s.machineId, sid: sid)
                    Task {
                        guard let forked = await made.value else { return }
                        // The new session may not be in the list yet: its page waits for it, as after starting one.
                        menu = false
                        afterMenu { nav.open(.session(id: forked), from: page) }
                    }
                })
            }
        }
        if running {
            list.append(SheetAction("中断", danger: true, enabled: !forking) {
                command(act) { $0.interrupt = .with { $0.sessionID = sid } }
            })
        }
        if status != .exited {
            end.append(SheetAction("结束会话…", danger: true, enabled: !forking) { afterMenu { confirmStop = true } })
        }
        return [settings, list, end]
    }
}

/// The middle of the bar: the title on one line, and under it the state and
/// the approval mode (12, `ink2`), the mode red only when it is full access.
/// The title, and under it the status's mark before its word, then the
/// mode: both lines start at the same left edge, next to the back button.
private struct SessionTitle: View {
    let title: String
    let mark: StatusKind?
    let status: String
    let mode: String
    let danger: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(title).font(.headline).foregroundStyle(DS.ink).lineLimit(1)
            HStack(spacing: 5) {
                if let mark { StatusMark(mark).scaleEffect(0.8) }
                Text("\(Text(status).foregroundStyle(DS.ink2))\(Text(mode.isEmpty ? "" : " · ").foregroundStyle(DS.ink2))\(Text(mode).foregroundStyle(danger ? DS.red : DS.ink2))")
                    .font(.caption).lineLimit(1)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        // The bar has one line's room at the largest sizes; a long press shows it large.
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }
}

