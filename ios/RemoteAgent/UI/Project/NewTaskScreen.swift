// 新任务 (design-system §6 「新任务」): a whole-page sheet from the bottom,
// closed at its top left, started with the ink circle at its top right.
// The first line is a sentence, 「在 [项目] 用 [CLI]」, whose brackets are
// chips: the project opens the list to choose another, the CLI lists those
// its computer has. Under it what to do, in large type, with the keyboard
// up; above the keyboard ＋ and the summary of what the session starts with,
// which opens the project's settings. What is written stays with the
// project's draft (`NewSessionBox`, which this is the page layout of).
import RACore
import RAProto
import SwiftUI

/// What the page around `NewSessionBox` hands it to draw 新任务.
struct NewTaskChrome {
    var summary: String
    var full: Bool
    var onSettings: () -> Void
    var onProject: () -> Void
    var onClose: () -> Void
    var onStarted: (String) -> Void
}

struct NewTaskScreen: View {
    /// The project the task starts in; another can be chosen from the sentence.
    @State var key: String
    let onClose: () -> Void
    /// The session started: the sheet closes and its page opens.
    let onStarted: (String) -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var settings: ProjectSettingsRequest?
    @State private var picking = false
    @State private var summaryModels = SummaryModels()

    var body: some View {
        let state = model.state
        let seed = projectStart(state, key: key)
        let draft = context.newSessionDrafts.of(key, initial: seed?.config, settled: seed?.settled != false)
        let start = projectStart(state, key: key, pick: draft.agent)
        let initial = start?.config
        let selected = projectConfig(state, key: key, pick: newSessionPick(state, draft))
        let launch = launchConfig(draft: draft.config.value, project: selected, hidden: isHidden(state, key: key))
        let several = machineAgents(state.machines[draft.config.value?.machineId ?? ""]).count > 1
        NewSessionBox(
            state: state, key: key, initial: initial, name: projectName(key), focus: true, settled: start?.settled != false,
            onMachine: { settings = ProjectSettingsRequest(page: .machine) },
            sheet: NewTaskChrome(
                summary: launch.map { newTaskSummary(state, $0, models: summaryModels.list, cliInSentence: several) } ?? "",
                full: launch.map { isFullAccess(state, $0) } ?? false,
                onSettings: { settings = ProjectSettingsRequest() },
                onProject: { picking = true },
                onClose: onClose,
                onStarted: onStarted))
        .navigationBarTitleDisplayMode(.inline)
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
        .kitSheet(isPresented: $picking, size: .large) {
            SheetHead("在哪个项目", action: "取消", onAction: { picking = false })
            ProjectPickList(state: state, onPick: { choose($0, from: draft) }, onNew: {})
        }
        .task(id: launch.map { SummaryModels.Key(machine: $0.machineId, agent: $0.agent) }) {
            let remote = context.remote
            await summaryModels.load(launch.map { SummaryModels.Key(machine: $0.machineId, agent: $0.agent) }) { key in
                await HomeLaunch.settled(model)
                return try? await remote.listModels(key.machine, agent: key.agent)
            }
        }
    }

    /// Another project: what was written goes along when that project's draft is empty.
    private func choose(_ next: String, from draft: NewSessionDraft) {
        picking = false
        guard next != key else { return }
        let state = model.state
        let seed = projectStart(state, key: next)
        let other = context.newSessionDrafts.of(next, initial: seed?.config, settled: seed?.settled != false)
        if other.text.isEmpty { other.text = draft.text }
        key = next
    }

    /// Stores a change to the project's settings; the draft takes them at once, written or not.
    private func store(_ op: PrefsOp.SetSettings, _ draft: NewSessionDraft) {
        let state = model.state
        context.prefsChanges.run(context.remote, .setSettings(op))
        let saved = applySettings(stored: savedSettings(state, key: key), op: op, now: nowMs())
        if let c = projectConfig(state, key: key, pick: draft.agent, machine: nil, saved: saved) { draft.config.choose(c) }
    }
}

/// 「在 [项目] 用 [CLI]」: 17 `ink2` around chips of `fill`, 15 / 600 `ink`;
/// only 「在 [项目]」 when the computer has one CLI.
struct NewTaskSentence: View {
    let key: String
    let name: String
    let agents: [PBAgent]
    let agent: PBAgent?
    let onProject: () -> Void
    let onAgent: (PBAgent) -> Void

    var body: some View {
        let project = Button(action: onProject) {
            HStack(spacing: 6) {
                Avatar(key, size: 22)
                Text(name).lineLimit(1)
            }
            .chip()
        }
        .buttonStyle(PressStyle())
        .accessibilityLabel("项目：\(name)")
        .accessibilityHint("换一个项目")
        let cli = Menu {
            ForEach(agents, id: \.self) { a in
                Button { onAgent(a) } label: {
                    if a == agent { Label(agentName(a), systemImage: "checkmark") } else { Text(agentName(a)) }
                }
            }
        } label: {
            Text(agent.map(agentName) ?? "").lineLimit(1).chip()
        }
        .accessibilityLabel("CLI：\(agent.map(agentName) ?? "")")
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 6) {
                Text("在")
                project
                if agents.count > 1 {
                    Text("用")
                    cli
                }
            }
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 6) {
                    Text("在")
                    project
                }
                if agents.count > 1 {
                    HStack(spacing: 6) {
                        Text("用")
                        cli
                    }
                }
            }
        }
        .font(.body)
        .foregroundStyle(DS.ink2)
    }
}

extension View {
    /// A chip of the sentence: `fill`, 15 / 600 `ink`, a capsule at least 36 tall.
    fileprivate func chip() -> some View {
        font(.subheadline.weight(.semibold))
            .foregroundStyle(DS.ink)
            .padding(.horizontal, 12)
            .frame(minHeight: 36)
            .background(DS.fill, in: Capsule())
            .frame(minHeight: DS.minHit)
            .contentShape(Rectangle())
    }
}
