// The new-session box at the bottom of a project's page (Android's
// NewSessionBox in ui/Project.kt, architecture §13): the first message
// starts the session, there is no start without one. The plus adds
// images; the configuration comes from the project's settings and stays
// with a draft once one is begun, except the CLI, which is switched in the
// row above the box when the machine has more than one. An offline
// machine is replaced only by the user's choice for this start. What is
// said above the box is `newSessionNotices`.
//
// The input is a session's message box (`MessageCapsule`, design-system §6 输入框): it starts at one line and grows
// to six, ＋ at the left, the round send button at the right.
import RACore
import RAProto
import SwiftUI

struct NewSessionBox: View {
    let state: RemoteState
    /// The project's path: where the session starts, and what the draft is kept under.
    let key: String
    /// What the project says the next session starts with, for the CLI picked next to the box.
    let initial: NewConfig?
    /// The project's name, for the notices.
    let name: String
    /// Bring the keyboard up with the page.
    let focus: Bool
    /// Whether `initial` is more than a guess (`ProjectStart`). While it is
    /// not, the pages of the listing are fetched and nothing starts; what is
    /// written stays, and takes the settings once they are known.
    var settled = true
    /// The list of the CLI's models, once it has come: it says whether the machine's default mode can be started in.
    var models: PBListModelsResponse?
    /// Opens the machine choice of the project's settings.
    var onMachine: () -> Void = {}
    /// Opens the model's page of the project's settings.
    var onModel: () -> Void = {}
    /// Opens a computer's page in 设置, where its default mode is.
    var onMachineSettings: (String) -> Void = { _ in }
    /// Drawn as 新任务's whole page (`NewTaskScreen`) rather than as the box at the bottom of a project's page.
    var sheet: NewTaskChrome?
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @Environment(AccountContext.self) private var context
    @FocusState private var focused: Bool
    // Native menus can temporarily collapse the keyboard's prediction strip.
    @State private var cliViewport: CGFloat?
    @State private var keyboardVisible = false
    @State private var modelPanelHeight: CGFloat = 0

    private struct Follow: Equatable {
        var initial: NewConfig?
        var drafting: Bool
        var settled: Bool
    }

    var body: some View {
        let draft = context.newSessionDrafts.of(key, initial: initial, settled: settled)
        let config = draft.config.value
        let agents = config.map { machineAgents(state.machines[$0.machineId]) } ?? []
        // The first message may be images alone: they make a draft as text does.
        let images = attachments(config)
        let drafting = draft.drafting || images.hasImages
        let notices = newSessionNotices(
            state, config: config, name: name, pick: draft.agent, drafting: drafting, settled: settled, start: draft.progress, models: models)
        let canSend = canStart(draft)
        Group {
            if let sheet {
                taskPage(sheet, draft: draft, config: config, agents: agents, images: images, notices: notices, canSend: canSend)
            } else {
                box(draft: draft, config: config, agents: agents, images: images, notices: notices, canSend: canSend)
            }
        }
        // What the next session inherits may be a session not fetched yet.
        .fetchEarlier(state, page: !settled)
        // The draft follows the project until something is written or chosen.
        .onChange(of: Follow(initial: initial, drafting: drafting, settled: settled), initial: true) { _, now in
            draft.config.follow(now.initial, drafting: now.drafting, settled: now.settled)
        }
        // ↑ and the other CLI, for a script (debug builds).
        .debugSignal("project.start") { start(draft) }
        .debugSignal("project.agent") {
            if let c = draft.config.value, let other = agents.first(where: { $0 != c.agent }) { switchAgent(draft, c, to: other) }
        }
        .task {
            #if DEBUG
            if let text = HomeLaunch.value("-projectDraft"), draft.text.isEmpty { draft.text = text }
            switch HomeLaunch.value("-projectStart") {
            case "running": draft.show(running: true)
            case "failed": draft.show(error: "这个目录不存在")
            case "unknown": draft.show(error: "结果未知", frozen: true)
            case "expired": draft.show(frozen: true, expired: true)
            default: break
            }
            #endif
            guard focus else { return }
            // A page still sliding in does not take the keyboard.
            try? await Task.sleep(for: .milliseconds(450))
            focused = true
        }
    }

    /// The box at the bottom of a project's page: the notices, the CLI, and the message capsule.
    private func box(
        draft: NewSessionDraft, config: NewConfig?, agents: [PBAgent], images: ComposerAttachments, notices: [NewSessionNotice], canSend: Bool
    ) -> some View {
        VStack(spacing: 0) {
            if !notices.isEmpty {
                VStack(spacing: 6) {
                    ForEach(notices) { notice(of: $0, draft, canStart: canSend) }
                }
                .padding(.horizontal, 12).padding(.top, 6).padding(.bottom, 2)
            }
            if let c = config, agents.count > 1 {
                // The CLI of the next session, in a row of its own above the box; it stays there with the keyboard up.
                SegPick(
                    agents.map(agentName), marks: agents,
                    selected: Binding(get: { agents.firstIndex(of: c.agent) ?? 0 }, set: { switchAgent(draft, c, to: agents[$0]) })
                )
                .accessibilityLabel("新会话使用的 CLI")
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, DS.Space.l)
            }
            MessageCapsule(
                text: Bindable(draft).text, placeholder: "描述任务…", label: "描述任务…", identifier: "new-session-text",
                focus: $focused
            ) {
                images.strip()
            } plus: {
                Button { images.pick() } label: { PlusCircle(dimmed: images.blocked != nil) }
                    .buttonStyle(.plain)
                    .accessibilityLabel("添加附件")
            } trailing: {
                Button { start(draft) } label: { InkCircle(symbol: "arrow.up", enabled: canSend) }
                    .buttonStyle(.plain)
                    .disabled(!canSend)
                    .accessibilityLabel("开始")
                    .accessibilityIdentifier("new-session-start")
            }
        }
        .readableColumn()
    }

    /// 新任务 as a whole page (design-system §6 「新任务」): where in the title, the notices in the middle, and at
    /// the bottom the box with ＋, the CLI, the model and ↑.
    private func taskPage(
        _ sheet: NewTaskChrome, draft: NewSessionDraft, config: NewConfig?, agents: [PBAgent], images: ComposerAttachments,
        notices: [NewSessionNotice], canSend: Bool
    ) -> some View {
        let modelOpen = sheet.picker.isOpen
        return GeometryReader { viewport in
            VStack(spacing: 0) {
                ScrollView {
                    VStack(spacing: 6) {
                        ForEach(notices) { notice(of: $0, draft, canStart: canSend) }
                    }
                    .padding(.horizontal, DS.Space.l).padding(.top, 8)
                }
                .scrollDismissesKeyboard(.interactively)
                MessageCard(text: Bindable(draft).text, placeholder: "要做什么？", label: "要做什么", identifier: "new-session-text", focus: $focused) {
                    images.strip()
                } accessories: {
                    AttachMenu(images: images)
                    if let c = config, let parts = sheet.parts {
                        let summary = newTaskModelSummary(sheet.picker.config ?? c, models: sheet.models.list,
                                                          settings: sheet.picker.settings(for: c), singleAgent: parts.cli == nil)
                        NewTaskToolsLayout {
                            if parts.cli != nil {
                                NewTaskCli(agents: agents, agent: c.agent) { switchAgent(draft, c, to: $0) }
                                    .anchorPreference(key: NewTaskPanelAnchors.self, value: .bounds) { ["cli": $0] }
                            }
                            NewTaskModel(summary: summary, active: modelOpen) { toggleModel(draft) }
                                .anchorPreference(key: NewTaskPanelAnchors.self, value: .bounds) { ["model": $0] }
                        }
                        .layoutPriority(1)
                    }
                    Spacer(minLength: 0)
                    Button { start(draft) } label: { InkCircle(symbol: "arrow.up", enabled: canSend) }
                        .buttonStyle(.plain)
                        .disabled(!canSend)
                        .accessibilityLabel("开始")
                        .accessibilityIdentifier("new-session-start")
                }
                .readableColumn()
                .transformAnchorPreference(key: NewTaskPanelAnchors.self, value: .bounds) { $0["composer"] = $1 }
            }
            .frame(maxWidth: .infinity)
            .frame(height: min(cliViewport ?? viewport.size.height, viewport.size.height))
            .frame(maxHeight: .infinity, alignment: .top)
            .background(DS.paper)
            .overlayPreferenceValue(NewTaskPanelAnchors.self) { anchors in
                GeometryReader { geometry in
                    if let composer = anchors["composer"], let chip = anchors["model"] {
                        let box = geometry[composer]
                        let height = min(600, max(0, box.minY - 8))
                        let width = min(420, max(0, box.width - 24))
                        let rect = CGRect(x: box.minX + 12, y: box.minY - 8 - height, width: width, height: height)
                        let panelRect = CGRect(x: rect.minX, y: rect.maxY - modelPanelHeight, width: width, height: modelPanelHeight)
                        NewTaskOutsideTouches(panel: modelOpen ? panelRect : nil, chip: geometry[chip],
                                              cli: anchors["cli"].map { geometry[$0] },
                                              onCLI: { if keyboardVisible { cliViewport = viewport.size.height } },
                                              onClose: { closeModel(draft) })
                            .allowsHitTesting(false)
                        if modelOpen {
                            NewTaskModelPanel(picker: sheet.picker, models: sheet.models, settled: settled) { closeModel(draft) }
                                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { modelPanelHeight = $0 }
                                .frame(width: width, height: height, alignment: .bottom)
                                .position(x: rect.midX, y: rect.midY)
                                .transition(.opacity)
                        }
                    }
                }
                .animation(Motion.enter(Motion.fast), value: modelOpen)
            }
            // Keep the pre-menu viewport until the prediction strip returns. The
            // menu and the keyboard stay native; a real keyboard dismissal is free
            // to resize the page as before.
            .onChange(of: viewport.size.height) { _, height in
                if let held = cliViewport, height <= held { cliViewport = nil }
            }
            .onChange(of: viewport.size.width) { _, _ in cliViewport = nil }
        }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in keyboardVisible = true }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillHideNotification)) { _ in
            keyboardVisible = false
            cliViewport = nil
        }
        .onChange(of: focused) { _, focused in if !focused { cliViewport = nil } }
        .onDisappear { closeModel(draft) }
        .onChange(of: config) { _, now in sheet.picker.follow(now) }
        .onChange(of: draft.text) { _, _ in closeModel(draft) }
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button { closeModel(draft); sheet.onClose() } label: { Image(systemName: "xmark") }.tint(DS.ink).accessibilityLabel("关闭")
            }
            ToolbarItem(placement: .principal) {
                NewTaskWhere(key: key, name: name, machine: sheet.parts?.machine ?? "", onProject: { closeModel(draft); sheet.onProject() }, onMachine: { closeModel(draft); sheet.onMachine() })
            }
        }
    }

    private func toggleModel(_ draft: NewSessionDraft) {
        guard let sheet else { onModel(); return }
        if sheet.picker.isOpen { closeModel(draft) }
        else if let c = draft.config.value { sheet.picker.open(c) }
    }

    private func closeModel(_ draft: NewSessionDraft) {
        guard let next = sheet?.picker.close(target: draft.config.value, settled: settled, blocked: prefsBlocked(model.state) != nil) else { return }
        let op = PrefsOp.SetSettings(path: key, machineId: next.machineId, sets: [next.agentSettings()])
        context.prefsChanges.run(context.remote, .setSettings(op))
        draft.config.choose(next)
        Haptics.confirm()
    }

    private func attachments(_ config: NewConfig?) -> ComposerAttachments {
        ComposerAttachments(state: state, key: ImageCenter.projectKey(key), machineId: config?.machineId, remote: context.remote)
    }

    /// Another CLI for the draft: the text stays, and the draft takes the project's set for that CLI.
    private func switchAgent(_ draft: NewSessionDraft, _ c: NewConfig, to agent: PBAgent) {
        draft.agent = agent
        let to = projectStart(state, key: key, pick: agent, machine: c.machineId)
        draft.config.retarget(RACore.switchAgent(c, project: to?.config, agent: agent), settled: to?.settled != false)
    }

    /// Whether a session can be started with the draft as it is: ↑ is drawn
    /// by it, and everything else that starts one (重试 after a failure, a
    /// script) is held to it too.
    private func canStart(_ draft: NewSessionDraft) -> Bool {
        let config = draft.config.value
        let images = attachments(config)
        // Every image must be on the machine first: never a start with some of them.
        return canStartSession(
            state, config: config, drafting: draft.drafting || images.hasImages, settled: settled, running: draft.running, models: models)
            && images.ready
    }

    private func start(_ draft: NewSessionDraft) {
        let refresh = attachments(draft.config.value)
        refresh.refresh()
        guard canStart(draft), let cfg = draft.config.value else { return }
        let machine = initial?.machineId
        let images = attachments(cfg)
        draft.start(
            context.remote, cfg, images: images.draft, onImagesUsed: { images.used($0) }
        ) { sid, used, prompt in
            // What the project stores of this start is read off the lists as they are now.
            let state = model.state
            let project = projectConfig(state, key: key, pick: used.agent, machine: machine) ?? used
            if let op = startSettings(saved: savedSettings(state, key: key), project: project, used: used) {
                context.prefsChanges.run(context.remote, .setSettings(op))
            }
            // The project remembers this CLI now; the next draft starts from it.
            draft.agent = nil
            // The session's page shows the first message as a pending bubble until the history has it.
            context.sessionStore.rememberPrompt(sid, prompt)
            Haptics.confirm()
            focused = false
            if let sheet { sheet.onStarted(sid) } else { nav.open(.session(id: sid), from: page) }
        }
    }

    @ViewBuilder private func notice(of notice: NewSessionNotice, _ draft: NewSessionDraft, canStart: Bool) -> some View {
        switch notice {
        case .reading(let text, let retry):
            EarlierTail(text, retry: retry).padding(.horizontal, 4).accessibilityIdentifier("settings-unread")
        case .offlineChoice(let title, let text, let choices, let none):
            VStack(alignment: .leading, spacing: 0) {
                Text(title).font(.subheadline).ink(DS.orangeInk)
                Text(text).font(.footnote).ink(DS.ink2)
                ForEach(choices) { choice in
                    Button {
                        Haptics.selection()
                        draft.config.choose(choice.config)
                    } label: {
                        HStack(spacing: 8) {
                            VStack(alignment: .leading, spacing: 0) {
                                Text(choice.title).font(.body.weight(.semibold)).ink(DS.ink)
                                Text(choice.note).font(.footnote).ink(DS.ink2)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            Arrow(DS.ink2, style: .body)
                        }
                        .padding(.vertical, 4)
                        .frame(minHeight: DS.minHit)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(PressStyle())
                }
                if let none { Text(none).font(.footnote).ink(DS.ink2) }
            }
            .padding(.horizontal, 12).padding(.vertical, 9)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
        case .offline(let text), .mismatch(let text):
            Banner(text, tone: .warn)
        case .missingAgent(let text):
            Banner(text, action: "换机器", onAction: onMachine)
        case .autoModel(let text):
            Banner(text, tone: .warn, action: "换模型", onAction: { toggleModel(draft) })
        case .autoAccount(let text):
            Banner(text, tone: .warn, action: "去改默认") {
                if let id = draft.config.value?.machineId { onMachineSettings(id) }
            }
        case .starting(let text):
            Banner(text, icon: Mark.progress)
        case .failed(let text):
            // 重试 is ↑ again: it is offered when ↑ is.
            Banner(text, tone: .error, action: canStart ? "重试" : nil, onAction: { start(draft) })
        case .unknown(let text):
            Banner(text, tone: .warn, action: "放弃", onAction: { draft.abandon() })
        }
    }
}

/// The new-session settings as a button that opens the project's settings
/// (a project's page): a `fill` capsule in `ink`, as wide as its words, `›`
/// at the end.
struct ProjectSummaryChip: View {
    let summary: String
    let action: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                Text(summary)
                    .font(.subheadline).ink(DS.ink).lineLimit(typeSize.isAccessibilitySize ? 4 : 2).multilineTextAlignment(.leading)
                    // Two lines before anything is cut: the model at the end is not the part to lose first.
                    .fixedSize(horizontal: false, vertical: true)
                Arrow(DS.ink3)
            }
            .padding(.horizontal, 12).padding(.vertical, 6)
            .frame(minHeight: 36)
            .background(DS.fill, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            // 44 pt to the finger without making the header taller.
            .padding(.vertical, 6).contentShape(Rectangle()).padding(.vertical, -6)
        }
        .buttonStyle(PressStyle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("新会话的设置：\(summary)")
        .accessibilityHint("项目设置")
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("project-settings-summary")
    }
}
