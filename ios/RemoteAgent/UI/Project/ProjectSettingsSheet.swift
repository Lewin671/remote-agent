// 项目设置 (architecture §13, design-system §6 「新任务」): the machine a
// project's new sessions use, where the account has more than one, and the
// model of the one CLI it was opened for, whose page also has the effort and
// the context window (Claude Code). A new task has no approval mode, so
// there is none here. Another CLI's set is reached by selecting that CLI
// next to the message box. With one machine the model is all there is and
// its page opens at once; a sheet opened on one page has no way back to the
// first. Nothing is written until 完成, or until a machine is picked on a
// sheet opened for that alone; what the sheet holds meanwhile is a
// `ProjectSettingsDraft`.
import RACore
import RAProto
import SwiftUI

enum ProjectSettingsPage: String, Sendable {
    case main, machine, model
}

/// The sheet as it was asked for: a page to open on, and a new one each time so that nothing of the last is left.
struct ProjectSettingsRequest: Identifiable {
    let id = UUID()
    var page = ProjectSettingsPage.main
}

struct ProjectSettingsSheet: View {
    private let cwd: String
    /// The CLI selected next to the message box (or the session's, or the task's): the sheet is its set, and 换机器 looks for it.
    private let agent: PBAgent
    /// Gets the change to store, or nil when nothing was changed.
    private let onDone: (PrefsOp.SetSettings?) -> Void
    private let onClose: () -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var draft: ProjectSettingsDraft
    @State private var page: ProjectSettingsPage
    @State private var models: PBListModelsResponse?
    @State private var modelsError = ""
    @State private var modelsFor = ""
    @State private var settings: PBDaemonSettings?
    /// The page the sheet was asked to open on.
    private let open: ProjectSettingsPage

    init(
        cwd: String, machine: String, agent: PBAgent, open: ProjectSettingsPage = .main,
        onDone: @escaping (PrefsOp.SetSettings?) -> Void, onClose: @escaping () -> Void
    ) {
        self.cwd = cwd
        self.agent = agent
        self.onDone = onDone
        self.onClose = onClose
        _draft = State(initialValue: ProjectSettingsDraft(cwd: cwd, machine: machine, agent: agent))
        _page = State(initialValue: open)
        self.open = open
    }

    private struct Load: Hashable {
        var machine: String
        var agent: PBAgent
        var picking: Bool
    }

    var body: some View {
        let state = model.state
        let c = draft.config(state)
        let blocked = prefsBlocked(state)
        // 完成 saves the whole of it from any page; without the connection there is nothing it could do.
        let done = blocked == nil ? save : nil
        // With one machine the first page would list the model alone: the model's page is the first.
        let root = open == .main && state.machines.count <= 1 ? ProjectSettingsPage.model : open
        let shown = page == .main ? root : page
        let back: (() -> Void)? = shown == root ? nil : { page = .main }
        Group {
            switch shown {
            case .main: main(state, c, blocked: blocked, done: done)
            case .machine: machines(state, done: done, back: back, alone: root == .machine)
            case .model: modelPage(state, c, done: done, back: back)
            }
        }
        // What this CLI's set starts from may be a session not fetched yet.
        .fetchEarlier(state, page: draft.unread(state))
        // The list is read when the sheet opens and again when the models are to be chosen from.
        .task(id: Load(machine: c.machineId, agent: c.agent, picking: shown == .model)) { await load(c) }
        // Chooses what `-settingsEdit` names (standard, effort=<e>, model=<slug>) and saves, for a script (debug builds).
        .debugSignal("settings.apply") {
            for word in (HomeLaunch.value("-settingsEdit") ?? "").split(separator: ",") {
                edit(draft.config(model.state)) { c in
                    switch word {
                    case "standard": c.standardContext = true
                    default:
                        if word.hasPrefix("effort=") { c.effort = String(word.dropFirst(7)) }
                        if word.hasPrefix("model=") { c.model = String(word.dropFirst(6)) }
                    }
                }
            }
            onDone(draft.op())
        }
    }

    private var save: () -> Void {
        {
            let op = draft.op()
            if op != nil { Haptics.confirm() }
            onDone(op)
        }
    }

    private func load(_ c: NewConfig) async {
        let key = "\(c.machineId)/\(c.agent.rawValue)"
        if modelsFor != key {
            // Another machine or CLI: the list before says nothing of this one.
            models = nil
            settings = nil
            modelsFor = key
        }
        modelsError = ""
        let remote = context.remote
        await HomeLaunch.settled(model)
        do {
            // Sample data has its lists and no computer to ask for newer ones.
            let listed = try await remote.listModels(c.machineId, agent: c.agent, fresh: !model.isDemo)
            if Task.isCancelled { return }
            models = listed
        } catch {
            if Task.isCancelled { return }
            modelsError = errorText(error)
        }
        if case .getSettings(let r)? = try? await remote.machineCommand(c.machineId, getSettingsCommand()).kind, !Task.isCancelled {
            settings = r.settings
        }
    }

    private func edit(_ c: NewConfig, _ change: (inout NewConfig) -> Void) {
        var next = c
        change(&next)
        draft.edit(next)
    }

    // MARK: The first page

    /// The machine and the model; only where the account has more than one machine, else the model's page is the first.
    @ViewBuilder private func main(_ state: RemoteState, _ c: NewConfig, blocked: String?, done: (() -> Void)?) -> some View {
        let agents = draft.agents(state)
        let cur = draft.current(state)
        SheetHead("项目设置", action: done == nil ? nil : "完成", onAction: done)
        if let blocked { Banner(blocked).padding(.bottom, 8) }
        SetCard([
            AnyView(SetRow("机器", machineLabel(state, id: draft.machine), dot: state.machines[draft.machine]?.online == true ? DS.green : DS.ink3) {
                page = .machine
            })
        ])
        // The set under it is the CLI's, whose name is its title.
        if agents.count > 1 { FieldLabel(agentName(cur)) }
        // What a CLI's set starts from is read first where it is not known yet.
        if draft.unread(state) {
            SetCard([AnyView(reading(state, cur))])
        } else {
            let shown = models.flatMap { projectModel(c, models: $0, settings: settings) }
            SetCard([AnyView(SetRow("模型", settingsModelValue(c, shown: shown)) { page = .model })])
        }
    }

    @ViewBuilder private func reading(_ state: RemoteState, _ agent: PBAgent) -> some View {
        Group {
            if state.conn != .online || !state.sessionsError.isEmpty {
                EarlierTail(state)
            } else {
                Text("正在读取 \(agentName(agent)) 上次的设置…").font(.subheadline).ink(DS.ink2).frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(14)
    }

    // MARK: The pages

    /// `alone`: the sheet was opened for the machine only, and picking one is done with it.
    @ViewBuilder private func machines(_ state: RemoteState, done: (() -> Void)?, back: (() -> Void)?, alone: Bool) -> some View {
        let rows = machineRows(state, cwd: cwd, agent: agent)
        SheetHead("机器", action: done == nil ? nil : "完成", onAction: done, onBack: back)
        ForEach(Array(rows.enumerated()), id: \.element.id) { i, m in
            ListItem(m.name, desc: m.note, checked: draft.machine == m.id, enabled: m.online, last: i == rows.count - 1) {
                Haptics.selection()
                draft.move(to: m.id)
                if alone, let done { done() } else { page = .main }
            }
            .trailing { Dot(m.online ? DS.green : DS.ink3) }
            .accessibilityValue(m.online ? "在线" : "不在线")
        }
        Hint("换机器后，各 CLI 的模型从那台机器的默认设置开始。")
    }

    @ViewBuilder private func modelPage(_ state: RemoteState, _ c: NewConfig, done: (() -> Void)?, back: (() -> Void)?) -> some View {
        SheetHead("模型", action: done == nil ? nil : "完成", onAction: done, onBack: back)
        if let blocked = prefsBlocked(state), back == nil { Banner(blocked).padding(.bottom, 8) }
        if draft.unread(state) {
            reading(state, draft.current(state))
        } else {
            ModelSettingsBody(config: c, models: models, modelsError: modelsError, settings: settings,
                              machineName: machineLabel(state, id: c.machineId), onEdit: { draft.edit($0) })
        }
    }
}

/// Shared by project/scheduling sheets and the new-task panel. Presentation owns
/// loading, scrolling, focus and commit; these are the same choices everywhere.
struct ModelSettingsBody: View {
    let config: NewConfig
    let models: PBListModelsResponse?
    var modelsError = ""
    let settings: PBDaemonSettings?
    let machineName: String
    let onEdit: (NewConfig) -> Void
    @State private var allModels = false

    var body: some View {
        let c = config
        if let ms = models {
            let standing = defaultModelName(c, models: ms, settings: settings)
            ListItem("默认" + (standing.isEmpty ? "" : "（\(standing)）"), checked: c.model.isEmpty) {
                Haptics.selection()
                edit {
                    $0.model = ""
                    $0.effort = ""
                }
            }
            let shown = shownModels(ms.models, picked: ms.models.first { $0.slug == c.model }, all: allModels)
            let few = shown.count == ms.models.count
            ForEach(Array(shown.enumerated()), id: \.element.slug) { i, m in
                ListItem(m.displayName.isEmpty ? m.slug : m.displayName, checked: c.model == m.slug, last: few && i == shown.count - 1) {
                    Haptics.selection()
                    onEdit(pickModel(c, m))
                }
            }
            if !few { TextAction("全部模型", style: .body, arrow: true) { allModels = true } }
            // With the default model an effort can still be chosen; choosing one does not fix the model.
            if let efforts = effortChoices(c, models: ms, settings: settings) {
                FieldLabel("推理强度")
                ChoiceGrid(efforts.efforts, selected: efforts.selected) { effort in
                    Haptics.selection()
                    edit { $0.effort = effort }
                }
            }
            if agentInfo(c.agent).standardContextOption { context(c, shown: projectModel(c, models: ms, settings: settings)) }
        } else if !modelsError.isEmpty {
            Banner("没能读取模型列表，会用 \(agentName(c.agent)) 自己的默认模型：\(modelsError)", tone: .warn)
        } else {
            LoadingRow("读取 \(machineName) 的设置…", inset: 0)
        }
    }

    private func edit(_ change: (inout NewConfig) -> Void) {
        var next = config
        change(&next)
        onEdit(next)
    }

    /// The context window (Claude Code), under the model: a model without a larger window has nothing to choose.
    @ViewBuilder private func context(_ c: NewConfig, shown: PBModelInfo?) -> some View {
        let window = settingsContext(c, shown: shown)
        FieldLabel("上下文窗口")
        if window.fixed {
            Hint(window.text)
        } else {
            let options = ["1M", "标准 200k"]
            ChoiceGrid(options, selected: c.standardContext ? options[1] : options[0]) { picked in
                Haptics.selection()
                edit { $0.standardContext = picked == options[1] }
            }
        }
    }
}

/// The rows of a setting on a soft card, a line between each two.
private struct SetCard: View {
    private let rows: [AnyView]

    init(_ rows: [AnyView]) { self.rows = rows }

    var body: some View {
        VStack(spacing: 0) {
            ForEach(rows.indices, id: \.self) { i in
                if i > 0 { Rectangle().fill(DS.sep).frame(height: 1).accessibilityHidden(true) }
                rows[i]
            }
        }
        .background(DS.gbg)
        .clipShape(RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
    }
}

/// A project-setting row: a short label, the value at the right, an arrow when it leads to a page.
private struct SetRow: View {
    private let label: String
    private let value: String
    private let dot: Color?
    private let valueColor: Color?
    private let action: (() -> Void)?
    @Environment(\.dynamicTypeSize) private var typeSize

    init(_ label: String, _ value: String, dot: Color? = nil, valueColor: Color? = nil, action: (() -> Void)?) {
        self.label = label
        self.value = value
        self.dot = dot
        self.valueColor = valueColor
        self.action = action
    }

    var body: some View {
        let shown = Group {
            if typeSize.isAccessibilitySize {
                // The value goes under the label, at the right, instead of squeezing it.
                VStack(alignment: .leading, spacing: 0) {
                    Text(label).font(.subheadline).ink(DS.ink2)
                    HStack(spacing: 8) {
                        Spacer(minLength: 0)
                        if let dot { Dot(dot) }
                        Text(value).font(.subheadline).ink(valueColor ?? DS.ink2).multilineTextAlignment(.trailing)
                        if action != nil { Arrow(DS.ink3) }
                    }
                }
                .padding(.vertical, 8)
            } else {
                HStack(spacing: 10) {
                    Text(label).font(.subheadline).ink(DS.ink2).lineLimit(1).fixedSize()
                    Spacer(minLength: 0)
                    if let dot { Dot(dot) }
                    Text(value).font(.subheadline).ink(valueColor ?? DS.ink2).lineLimit(2).multilineTextAlignment(.trailing)
                    if action != nil { Arrow(DS.ink3) }
                }
            }
        }
        .padding(.horizontal, 14)
        .frame(maxWidth: .infinity, minHeight: DS.minHit, alignment: .leading)
        if let action {
            Button(action: action) { shown }.buttonStyle(RowPressStyle())
        } else {
            shown.accessibilityElement(children: .combine)
        }
    }
}
