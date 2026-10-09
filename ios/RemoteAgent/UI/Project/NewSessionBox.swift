// The new-session box at the bottom of a project's page (Android's
// NewSessionBox in ui/Project.kt, architecture §13): the first message
// starts the session, there is no start without one. The plus adds
// images; the configuration comes from the project's settings and stays
// with a draft once one is begun, except the CLI, which is switched in the
// row above the box when the machine has more than one. An offline
// machine is replaced only by the user's choice for this start. What is
// said above the box is `newSessionNotices`.
//
// The input has the layout of a session's message box (design-system §6
// 输入框): a pill that starts at one line and grows to six, ＋ at the left,
// the round send button at the right.
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
    var onFocus: (Bool) -> Void = { _ in }
    /// Opens the machine choice of the project's settings.
    var onMachine: () -> Void = {}
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
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
        let images = ProjectImageContext(state: state, key: key, config: config, remote: context.remote)
        let drafting = draft.drafting || ProjectImages.hasImages(images)
        let notices = newSessionNotices(
            state, config: config, name: name, pick: draft.agent, drafting: drafting, settled: settled, start: draft.progress)
        VStack(spacing: 0) {
            if !notices.isEmpty {
                VStack(spacing: 6) {
                    ForEach(notices) { notice(of: $0, draft) }
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
                .padding(.horizontal, Theme.Space.page)
            }
            NewSessionInput(
                text: Bindable(draft).text,
                // Every image must be on the machine first: never a start with some of them.
                canSend: canStartSession(state, config: config, drafting: drafting, settled: settled, running: draft.running)
                    && ProjectImages.ready(images),
                plusEnabled: ProjectImages.available(images),
                focused: $focused,
                onPlus: { ProjectImages.pick(images) },
                onSend: { start(draft) }
            ) {
                ProjectImages.Strip(context: images)
            }
        }
        .readableColumn()
        .background(Theme.card)
        // What the next session inherits may be a session not fetched yet.
        .fetchEarlier(state, page: !settled)
        // The draft follows the project until something is written or chosen.
        .onChange(of: Follow(initial: initial, drafting: drafting, settled: settled), initial: true) { _, now in
            draft.config.follow(now.initial, drafting: now.drafting, settled: now.settled)
        }
        .onChange(of: focused) { _, on in onFocus(on) }
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

    /// Another CLI for the draft: the text stays, and the draft takes the project's set for that CLI.
    private func switchAgent(_ draft: NewSessionDraft, _ c: NewConfig, to agent: PBAgent) {
        draft.agent = agent
        let to = projectStart(state, key: key, pick: agent, machine: c.machineId)
        draft.config.retarget(RACore.switchAgent(c, project: to?.config, agent: agent), settled: to?.settled != false)
    }

    private func start(_ draft: NewSessionDraft) {
        guard settled, let cfg = draft.config.value else { return }
        let machine = initial?.machineId
        let images = ProjectImageContext(state: state, key: key, config: cfg, remote: context.remote)
        draft.start(
            context.remote, cfg, images: ProjectImages.draft(images), onImagesUsed: { ProjectImages.used(images, $0) }
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
            nav.open(.session(id: sid))
        }
    }

    @ViewBuilder private func notice(of notice: NewSessionNotice, _ draft: NewSessionDraft) -> some View {
        switch notice {
        case .reading(let text, let retry):
            EarlierTail(text, retry: retry).padding(.horizontal, 4).accessibilityIdentifier("settings-unread")
        case .offlineChoice(let title, let text, let choices, let none):
            VStack(alignment: .leading, spacing: 0) {
                Text(title).type(T.sm, Theme.orangeInk)
                Text(text).type(T.meta, Theme.muted)
                ForEach(choices) { choice in
                    Button {
                        Haptics.selection()
                        draft.config.choose(choice.config)
                    } label: {
                        HStack(spacing: 8) {
                            VStack(alignment: .leading, spacing: 0) {
                                Text(choice.title).type(T.bodyB, Theme.fg)
                                Text(choice.note).type(T.meta, Theme.muted)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            Arrow(Theme.muted, style: T.body)
                        }
                        .padding(.vertical, 4)
                        .frame(minHeight: Theme.minHit)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(PressStyle())
                }
                if let none { Text(none).type(T.meta, Theme.muted) }
            }
            .padding(.horizontal, 12).padding(.vertical, 9)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
        case .offline(let text), .mismatch(let text):
            Banner(text, tone: .warn)
        case .missingAgent(let text):
            Banner(text, action: "换机器", onAction: onMachine)
        case .starting(let text):
            Banner(text, icon: Lucide.loaderCircle)
        case .failed(let text):
            Banner(text, tone: .error, action: "重试", onAction: { start(draft) })
        case .unknown(let text):
            Banner(text, tone: .warn, action: "放弃", onAction: { draft.abandon() })
        }
    }
}

/// The pill: ＋, the text from one line to six, and ↑.
private struct NewSessionInput<Top: View>: View {
    @Binding var text: String
    let canSend: Bool
    /// Whether ＋ can add images now; when not it is dimmed, and a tap says why.
    let plusEnabled: Bool
    var focused: FocusState<Bool>.Binding
    let onPlus: () -> Void
    let onSend: () -> Void
    /// The attached images, over the text inside the pill.
    @ViewBuilder var top: Top
    /// The send button's circle; its arrow grows a little with the text, the circle does not.
    private let circle: CGFloat = 32

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: 24, style: .continuous)
        VStack(spacing: 0) {
            top
            row
        }
        .padding(3)
        .background(Theme.card, in: shape)
        .overlay { shape.strokeBorder(Theme.line, lineWidth: 1) }
        .padding(.init(top: 4, leading: 10, bottom: 8, trailing: 10))
    }

    private var row: some View {
        HStack(alignment: .bottom, spacing: 0) {
            Button(action: onPlus) { Ic(Lucide.plus, size: 22, color: Theme.muted).hitTarget() }
                .buttonStyle(PressStyle())
                .opacity(plusEnabled ? 1 : 0.4)
                // The two buttons keep a size a thumb knows; the words between them are what grows.
                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                .accessibilityLabel("添加图片")
            TextField("", text: $text, prompt: Text("描述任务…").foregroundStyle(Theme.muted), axis: .vertical)
                .lineLimit(1...6)
                .type(T.body, Theme.fg)
                .focused(focused)
                .tint(Theme.blue)
                .accessibilityLabel("描述任务…")
                .accessibilityIdentifier("new-session-text")
                .padding(.horizontal, 4).padding(.vertical, 10)
                .frame(maxWidth: .infinity, minHeight: Theme.minHit, alignment: .leading)
                // The whole of the pill between the buttons is the field to the finger.
                .contentShape(Rectangle())
                .onTapGesture { focused.wrappedValue = true }
            Button(action: onSend) {
                Ic(Lucide.arrowUp, size: 20, color: Theme.onSolid)
                    .frame(width: circle, height: circle)
                    .background(Theme.blueBg, in: Circle())
                    // Nothing to send looks as any button that is not enabled does (design-system §6).
                    .opacity(canSend ? 1 : 0.4)
                    .hitTarget()
            }
            .buttonStyle(PressStyle())
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .disabled(!canSend)
            .accessibilityLabel("开始")
            .accessibilityIdentifier("new-session-start")
        }
    }
}

/// The new-session settings as a button that opens the project's settings
/// (design-system §6): chip grey, as wide as its words, an arrow at the
/// end; only 完全放行 is red.
struct ProjectSummaryChip: View {
    let summary: String
    let full: Bool
    let action: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let parts = summaryParts(summary, full: full)
        Button(action: action) {
            HStack(spacing: 6) {
                (Text(parts.before) + Text(parts.full).foregroundStyle(Theme.red) + Text(parts.after))
                    .type(T.sm, Theme.fg2).lineLimit(typeSize.isAccessibilitySize ? 4 : 2).multilineTextAlignment(.leading)
                    // Two lines before anything is cut: the model at the end is not the part to lose first.
                    .fixedSize(horizontal: false, vertical: true)
                Arrow(Theme.faint)
            }
            .padding(.horizontal, 10).padding(.vertical, 4)
            .frame(minHeight: 32)
            .background(Theme.chip, in: RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous))
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
