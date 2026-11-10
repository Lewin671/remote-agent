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
    /// Opens the machine choice of the project's settings.
    var onMachine: () -> Void = {}
    /// Drawn as 新任务's whole page (`NewTaskScreen`) rather than as the box at the bottom of a project's page.
    var sheet: NewTaskChrome?
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @Environment(AccountContext.self) private var context
    @FocusState private var focused: Bool

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
            state, config: config, name: name, pick: draft.agent, drafting: drafting, settled: settled, start: draft.progress)
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
                    agents.map(agentName),
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

    /// 新任务 as a whole page (design-system §6 「新任务」): the sentence 「在 [项目] 用 [CLI]」,
    /// what to do in large type, and above the keyboard ＋ and the settings summary.
    private func taskPage(
        _ sheet: NewTaskChrome, draft: NewSessionDraft, config: NewConfig?, agents: [PBAgent], images: ComposerAttachments,
        notices: [NewSessionNotice], canSend: Bool
    ) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            NewTaskSentence(key: key, name: name, agents: agents, agent: config?.agent, onProject: sheet.onProject) { agent in
                if let c = config { switchAgent(draft, c, to: agent) }
            }
            .padding(.horizontal, 20).padding(.top, 8)
            ScrollView {
                VStack(alignment: .leading, spacing: 8) {
                    if !notices.isEmpty {
                        VStack(spacing: 6) {
                            ForEach(notices) { notice(of: $0, draft, canStart: canSend) }
                        }
                    }
                    TextField(
                        "", text: Bindable(draft).text,
                        prompt: Text("要做什么？").font(.title2.bold()).foregroundStyle(DS.ink3), axis: .vertical
                    )
                    .font(.body)
                    .foregroundStyle(DS.ink)
                    .tint(DS.blue)
                    .focused($focused)
                    .accessibilityLabel("要做什么")
                    .accessibilityIdentifier("new-session-text")
                    .frame(maxWidth: .infinity, minHeight: 120, alignment: .topLeading)
                }
                .padding(.horizontal, 20).padding(.top, 16)
            }
            .scrollDismissesKeyboard(.interactively)
            // Above the keyboard: what is attached, ＋, and what the session starts with.
            VStack(alignment: .leading, spacing: 6) {
                images.strip()
                HStack(spacing: 8) {
                    Button { images.pick() } label: { PlusCircle(dimmed: images.blocked != nil) }
                        .buttonStyle(.plain)
                        .accessibilityLabel("添加附件")
                    ProjectSummaryChip(summary: sheet.summary, full: sheet.full, action: sheet.onSettings)
                    Spacer(minLength: 0)
                }
            }
            .padding(.horizontal, 14).padding(.vertical, 6)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(DS.paper)
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button(action: sheet.onClose) { Image(systemName: "xmark") }.tint(DS.ink).accessibilityLabel("关闭")
            }
            ToolbarItem(placement: .topBarTrailing) {
                Button { start(draft) } label: { InkCircle(symbol: "arrow.up", enabled: canSend) }
                    .buttonStyle(.plain)
                    .disabled(!canSend)
                    .accessibilityLabel("开始")
                    .accessibilityIdentifier("new-session-start")
            }
        }
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
            state, config: config, drafting: draft.drafting || images.hasImages, settled: settled, running: draft.running)
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
/// (design-system §6 「新任务」): a `fill` capsule in `ink`, as wide as its
/// words, `›` at the end; only 完全放行 is red.
struct ProjectSummaryChip: View {
    let summary: String
    let full: Bool
    let action: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let parts = summaryParts(summary, full: full)
        Button(action: action) {
            HStack(spacing: 6) {
                (Text(parts.before) + Text(parts.full).foregroundStyle(DS.red) + Text(parts.after))
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
