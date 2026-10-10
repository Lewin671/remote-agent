// A session: its navigation bar, the 聊天 / 终端 tabs, and under them the
// chat or the terminal. Android's `SessionScreen` and `SessionTopBar` in
// ui/Session.kt; docs/design-system.md §6 「会话导航栏」「视图切换」.
//
// The bar's first line is the title alone; the second is the session's
// state and its approval mode, red only for full access. Everything else
// about the session (CLI, project, computer, folder, ID) heads the ⋯ menu,
// whose choices are the mode, the model and the context, then 重命名,
// 分叉会话, 中断本轮 and 结束会话…. A choice that cannot be made now is not
// hidden: it is greyed, with when it can be under it.
import RACore
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
                    Text(note).type(T.h17, Theme.fg).multilineTextAlignment(.center).accessibilityAddTraits(.isHeader)
                    // The server could not be asked: it is asked again by itself, and at once on a tap.
                    if case .failed = lookup { TextAction("重试", alignment: .center, action: onRetry) }
                    TextAction("返回", alignment: .center) { nav.back() }
                } else {
                    Mark(Lucide.loaderCircle, color: Theme.muted, size: 22, relativeTo: .body)
                    Text(online ? "读取中…" : "连上服务器后读取这个会话").type(T.sm, Theme.muted).padding(.top, 12)
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
    @State private var chat = SessionModel()
    @State private var tab: Int
    @State private var menu = false
    @State private var sheet: SessionSheet?
    @State private var rename = false
    @State private var projectSettings: ProjectSettingsRequest?
    @State private var confirmStop = false
    @State private var act = ActionRunner()
    @State private var fork = ActionRunner()
    @State private var forked: String?
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
        let term = tab == 1 && hasTerminal
        let live = status != .exited && status != .offline
        let mode = status == .exited ? nil : modeLabel(s)
        // Unknown context is left out, never shown as 0%.
        let percent = live ? contextPercent(s) : nil
        let high = (percent ?? 0) > 80
        let refusal = protocolMismatch(state.machines[s.machineId])
        let subtitle = sessionSubtitle(status: status, mode: mode)
        VStack(spacing: 0) {
            ConnectionBar()
            if let refusal {
                Banner(refusal, tone: .warn).padding(.horizontal, 12).padding(.vertical, 6)
            }
            if !act.error.isEmpty {
                Banner(act.error, tone: .error).padding(.horizontal, 12).padding(.vertical, 6)
            }
            ViewTabs(
                ["聊天", "终端"], selected: Binding(get: { term ? 1 : 0 }, set: { pick($0) }),
                enabled: { $0 == 0 || hasTerminal }, dot: status == .popup ? 1 : nil, dark: term)
            if term {
                TerminalPane(sessionId: sid, running: true) { pick(0) }
            } else {
                ChatPane(session: s, status: status, chat: chat) { pick(1) }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background((term ? Theme.Term.bg : Theme.card).ignoresSafeArea())
        .tint(Theme.blue)
        // The page's to do, not the chat's or its message box's: it may open on the terminal, and an ended
        // session or one on an offline computer has no box.
        .task(id: sid) { SessionImages.restore(session: sid, remote: context.remote, store: context.sessionStore) }
        // The second line is the state and the mode, which is red only when it is full access.
        .navBar(
            titleOf(s), subtitle: subtitle.danger || subtitle.mode.isEmpty ? subtitle.status : "\(subtitle.status) · \(subtitle.mode)",
            danger: subtitle.danger ? subtitle.mode : nil, dot: Theme.dot(StatusTone(status.tone)), dark: term,
            // Something in the menu wants a look: a context that is nearly full.
            moreDot: high, onMore: { menu = true })
        .toolbarBackground(term ? Theme.Term.bg : Theme.card, for: .navigationBar)
        .toolbarBackground(term ? .visible : .automatic, for: .navigationBar)
        .toolbarColorScheme(term ? .dark : nil, for: .navigationBar)
        .actionMenu(isPresented: $menu, actions: actions(status: status, running: running, live: live, mode: mode, percent: percent)) {
            VStack(spacing: 0) {
                Text("\(agentName(s.agent)) · \(projectName(projectKey(s))) · \(machineLabel(state, id: s.machineId))")
                    .type(T.meta, Theme.muted)
                Text("\(shortPath(s.info?.cwd ?? "")) · 会话 \(s.id.prefix(16))").type(T.mono, Theme.muted)
            }
            .multilineTextAlignment(.center)
        }
        .onChange(of: menu) { _, shown in if !shown { fork.error = "" } }
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
        .task(id: s.machineId) {
            if s.agent == .claude { models = try? await context.remote.listModels(s.machineId, agent: s.agent) }
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

    private func actions(status: Status, running: Bool, live: Bool, mode: ModeLabel?, percent: Int?) -> [SheetAction] {
        let state = model.state
        let s = session
        let sid = s.id
        let forking = fork.running
        // The title is kept on the computer: an ended session can be renamed too, but only while the computer is reachable.
        let reachable = state.conn == .online && state.machines[s.machineId]?.online == true
        let sent = sentMessage(s, events: [PBEvent]()) || chat.spoke
        let forkBlock = forkBlocked(status: status, running: running, sent: sent)
        let raw = s.info?.model ?? ""
        let name = models.flatMap { findModel($0.models, id: raw) { $0.slug }?.displayName } ?? raw
        let effort = s.info?.effort ?? ""
        var list: [SheetAction] = []
        if live {
            list.append(SheetAction("审批模式：\(mode?.text ?? "未知")", danger: mode?.mode == .full, enabled: !forking) {
                afterMenu { sheet = .mode }
            })
            list.append(SheetAction(name.isEmpty ? "会话设置" : "模型：\(name)" + (effort.isEmpty ? "" : " · \(effort)"), enabled: !forking) {
                afterMenu { sheet = .model }
            })
            if let percent {
                list.append(SheetAction("上下文：\(percent)%", warn: percent > 80, enabled: !forking) { afterMenu { sheet = .model } })
            }
        }
        list.append(SheetAction("项目设置", enabled: !forking && projectConfig(state, key: projectKey(s)) != nil) {
            afterMenu { projectSettings = ProjectSettingsRequest() }
        })
        if reachable {
            list.append(SheetAction("重命名", enabled: !forking) { afterMenu { rename = true } })
            // A fork starts at once, in the menu: its row shows the progress, and a failure under it.
            list.append(SheetAction(
                forking ? "正在分叉…" : "分叉会话", enabled: forkBlock == nil,
                note: forking ? "" : forkBlock ?? (fork.error.isEmpty ? "" : "没能分叉：\(fork.error)"), noteError: forkBlock == nil,
                busy: forking, keepOpen: true
            ) {
                let remote = context.remote
                fork.run({
                    var cmd = PBCommand()
                    cmd.forkSession = .with { $0.sessionID = sid }
                    guard let made = forkedSession(try await remote.command(sid, cmd)) else {
                        throw CommandError("internal", "意外的回复")
                    }
                    forked = made
                }, onDone: {
                    // The new session may not be in the list yet: its page waits for it, as after starting one.
                    menu = false
                    if let forked { afterMenu { nav.open(.session(id: forked), from: page) } }
                })
            })
        }
        if running {
            list.append(SheetAction("中断本轮", danger: true, enabled: !forking) {
                command(act) { $0.interrupt = .with { $0.sessionID = sid } }
            })
        }
        if status != .exited {
            list.append(SheetAction("结束会话…", danger: true, enabled: !forking) { afterMenu { confirmStop = true } })
        }
        return list
    }
}
