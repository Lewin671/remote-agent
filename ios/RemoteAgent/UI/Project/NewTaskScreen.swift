// 新任务 (design-system §6 「新任务」): a whole-page sheet from the bottom,
// laid out as a session's page. The title says where: the project, and
// under it the computer where the account has more than one, each opening
// its choice. The middle holds only the notices. At the bottom, above the
// keyboard, the message box: what to do, what is attached, and a row of ＋,
// the CLI (where the computer has more than one), the model, which opens
// an anchored model panel, and ↑. A new task has no
// approval mode: it starts in the computer's default. What is written stays
// with the project's draft (`NewSessionBox`, which this is the page layout of).
import RACore
import RAProto
import SwiftUI

/// What the page around `NewSessionBox` hands it to draw 新任务.
struct NewTaskChrome {
    /// The computer in the title, the CLI and the model in the box.
    var parts: NewTaskParts?
    var models: SummaryModels
    var picker: NewTaskModelPicker
    var onMachine: () -> Void
    var onProject: () -> Void
    var onClose: () -> Void
    var onStarted: (String) -> Void
}

struct NewTaskScreen: View {
    /// The project the task starts in; another can be chosen from the title.
    @State var key: String
    /// The computer of a folder just chosen, which has no session to say it; nil for a project.
    @State var machine: String?
    let onClose: () -> Void
    /// The session started: the sheet closes and its page opens.
    let onStarted: (String) -> Void
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var settings: ProjectSettingsRequest?
    @State private var picking = false
    /// The list of projects has turned into 新项目 · 选目录.
    @State private var browsing = false
    @State private var summaryModels = SummaryModels()
    @State private var modelPicker = NewTaskModelPicker()

    var body: some View {
        let state = model.state
        // As on a project's page (`ProjectPageView`): a folder's computer is the one it was chosen on.
        let seed = projectStart(state, key: key, machine: machine)
        let draft = context.newSessionDrafts.of(key, initial: seed?.config, settled: seed?.settled != false)
        let start = projectStart(state, key: key, pick: draft.agent, machine: machine)
        let initial = start?.config ?? machine.map { NewConfig(machineId: $0, cwd: key) }
        let selected = machine == nil ? projectConfig(state, key: key, pick: newSessionPick(state, draft)) : draft.config.value ?? initial
        let launch = launchConfig(draft: draft.config.value, project: selected, hidden: isHidden(state, key: key))
        let models = summaryModels.list
        NewSessionBox(
            state: state, key: key, initial: initial, name: projectName(key), focus: true, settled: start?.settled != false,
            models: models,
            onMachine: { settings = ProjectSettingsRequest(page: .machine) },
            onMachineSettings: { id in
                onClose()
                nav.open(.machine(id: id))
            },
            sheet: NewTaskChrome(
                parts: launch.map { newTaskParts(state, $0, models: models) },
                models: summaryModels, picker: modelPicker,
                onMachine: { settings = ProjectSettingsRequest(page: .machine) },
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
        .kitSheet(isPresented: $picking, size: .large, onDismiss: { browsing = false }) {
            if browsing {
                SheetHead("新项目 · 选目录", onBack: { browsing = false })
                ProjectDirPicker(state) { machine, path in choose(path, on: machine, from: draft) }
            } else {
                SheetHead("在哪个项目", action: "取消", onAction: { picking = false })
                ProjectPickList(state: state, onPick: { choose($0, from: draft) }, onNew: { browsing = true })
            }
        }
        .task(id: launch.map { SummaryModels.Key(machine: $0.machineId, agent: $0.agent) }) {
            let remote = context.remote
            await summaryModels.load(launch.map { SummaryModels.Key(machine: $0.machineId, agent: $0.agent) }) { key in
                await HomeLaunch.settled(model)
                return try? await remote.listModels(key.machine, agent: key.agent)
            }
        }
        .task(id: launch?.machineId) {
            guard let machine = launch?.machineId else { return }
            await HomeLaunch.settled(model)
            if case .getSettings(let r)? = try? await context.remote.machineCommand(machine, getSettingsCommand()).kind,
               !Task.isCancelled { modelPicker.received(r.settings, machine: machine) }
        }
        #if DEBUG
        .task {
            // The settings sheet as `-newTaskSheet` names it (model, machine), for screenshots of sample data.
            if let page = HomeLaunch.value("-newTaskSheet").flatMap(ProjectSettingsPage.init(rawValue:)) {
                try? await Task.sleep(for: .milliseconds(900))
                if page == .model, let c = draft.config.value { modelPicker.open(c) }
                else { settings = ProjectSettingsRequest(page: page) }
            }
        }
        #endif
    }

    /// Another project, or a folder chosen on `machine`: what was written goes along when its draft is empty.
    private func choose(_ next: String, on machine: String? = nil, from draft: NewSessionDraft) {
        picking = false
        guard next != key || machine != self.machine else { return }
        let state = model.state
        let seed = projectStart(state, key: next, machine: machine)
        let other = context.newSessionDrafts.of(next, initial: seed?.config, settled: seed?.settled != false)
        if other.text.isEmpty { other.text = draft.text }
        key = next
        self.machine = machine
    }

    /// Stores a change to the project's settings; the draft takes them at once, written or not.
    private func store(_ op: PrefsOp.SetSettings, _ draft: NewSessionDraft) {
        let state = model.state
        context.prefsChanges.run(context.remote, .setSettings(op))
        let saved = applySettings(stored: savedSettings(state, key: key), op: op, now: nowMs())
        if let c = projectConfig(state, key: key, pick: draft.agent, machine: nil, saved: saved) { draft.config.choose(c) }
    }
}

/// The title of 新任务: the project (avatar, 17 / 600 name, a small ⌄),
/// and under it the computer in 12 `ink2` with its ⌄ where the account has
/// more than one; neither on a capsule.
struct NewTaskWhere: View {
    let key: String
    let name: String
    let machine: String
    let onProject: () -> Void
    let onMachine: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            Button(action: onProject) {
                HStack(spacing: 6) {
                    Avatar(key, size: 22)
                    Text(name).font(.headline).ink(DS.ink).lineLimit(1)
                    Arrow(DS.ink3, style: .caption, direction: .down)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(PressStyle())
            .accessibilityLabel("项目：\(name)")
            .accessibilityHint("换一个项目")
            .accessibilityIdentifier("new-task-project")
            if !machine.isEmpty {
                Button(action: onMachine) {
                    HStack(spacing: 2) {
                        Text(machine).font(.caption).ink(DS.ink2).lineLimit(1)
                        Arrow(DS.ink3, style: .caption2, direction: .down)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .accessibilityLabel("电脑：\(machine)")
                .accessibilityHint("换一台电脑")
                .accessibilityIdentifier("new-task-machine")
            }
        }
        .padding(.horizontal, 8)
    }
}

/// The CLI in the box: its mark, its name and a small ⌄, in `ink2` on no
/// capsule; a menu of the CLIs the computer has, the chosen one checked.
struct NewTaskCli: View {
    let agents: [PBAgent]
    let agent: PBAgent
    let onAgent: (PBAgent) -> Void

    var body: some View {
        Menu {
            // The mark before each name, as an image: a system menu takes no other view.
            Picker("CLI", selection: Binding(get: { agent }, set: { onAgent($0) })) {
                ForEach(agents, id: \.self) { a in
                    if let glyph = AgentGlyph(a) {
                        Label { Text(agentName(a)) } icon: { Image(uiImage: glyph.image(side: 17)) }.tag(a)
                    } else {
                        Text(agentName(a)).tag(a)
                    }
                }
            }
        } label: {
            ViewThatFits(in: .horizontal) {
                cliLabel(named: true).fixedSize()
                cliLabel(named: false).fixedSize()
            }
            .newTaskAccessory()
        }
        .accessibilityLabel("CLI：\(agentName(agent))")
        .accessibilityIdentifier("new-task-cli")
    }
    private func cliLabel(named: Bool) -> some View {
        HStack(spacing: 4) {
            AgentMark(agent, style: .subheadline, color: DS.ink2, named: true)
            if named { Text(agentName(agent)).lineLimit(1) }
            Arrow(DS.ink3, style: .caption2, direction: .down)
        }
    }

}

/// The model in the box, with a small ⌄: opens an anchored model panel.
struct NewTaskModel: View {
    let summary: NewTaskModelSummary
    let active: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 4) {
                HStack(spacing: 0) {
                    if !summary.prefix.isEmpty { Text(summary.prefix).fixedSize() }
                    Text(summary.model).lineLimit(1).truncationMode(.tail)
                    if !summary.effort.isEmpty { Text(" · " + summary.effort).fixedSize() }
                }
                Arrow(DS.ink3, style: .caption2, direction: .down)
            }
            .newTaskAccessory()
            .background(active ? DS.fill : .clear, in: RoundedRectangle(cornerRadius: 12))
        }
        .buttonStyle(.plain)
        .accessibilityLabel(summary.accessibilityLabel)
        .accessibilityHint("选择模型和推理强度")
        .accessibilityIdentifier("new-task-model")
    }
}

extension View {
    /// The CLI and the model in the box: 15 / 500 `ink2`, no capsule, 44 pt to the finger.
    fileprivate func newTaskAccessory() -> some View {
        font(.subheadline.weight(.medium))
            .foregroundStyle(DS.ink2)
            .padding(.horizontal, 6)
            .frame(minHeight: DS.minHit)
            .contentShape(Rectangle())
    }
}

/// Measure both complete chips before allocating space: the CLI loses its name
/// first; the model receives all remaining width and keeps the effort intact.
struct NewTaskToolsLayout: Layout {
    static func widths(available: CGFloat, cli: CGFloat, compactCLI: CGFloat, model: CGFloat) -> (cli: CGFloat, model: CGFloat) {
        let gap: CGFloat = cli > 0 ? 2 : 0
        let used = cli + gap + model <= available ? cli : compactCLI
        return (used, min(model, max(0, available - used - gap)))
    }
    private func widths(_ proposal: ProposedViewSize, _ views: Subviews) -> (cli: CGFloat, model: CGFloat) {
        let cli = views.count == 2 ? views[0].sizeThatFits(.unspecified).width : 0
        let compact = views.count == 2 ? views[0].sizeThatFits(.init(width: 0, height: nil)).width : 0
        let model = views[views.count - 1].sizeThatFits(.unspecified).width
        return Self.widths(available: proposal.width ?? cli + model + 2, cli: cli, compactCLI: compact, model: model)
    }
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let sizes = widths(proposal, subviews)
        return CGSize(width: sizes.cli + sizes.model + (subviews.count == 2 ? 2 : 0), height: DS.minHit)
    }
    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let sizes = widths(.init(width: bounds.width, height: bounds.height), subviews)
        if subviews.count == 2 {
            subviews[0].place(at: bounds.origin, anchor: .topLeading, proposal: .init(width: sizes.cli, height: bounds.height))
        }
        subviews[subviews.count - 1].place(at: CGPoint(x: bounds.minX + sizes.cli + (subviews.count == 2 ? 2 : 0), y: bounds.minY),
                                         anchor: .topLeading, proposal: .init(width: sizes.model, height: bounds.height))
    }
}
