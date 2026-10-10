// 项目设置 (Android's NewSettingsSheet in ui/Project.kt, architecture §13):
// the machine a project's new sessions use, and for each CLI the machine
// has its own model and effort, context window (Claude Code) and approval
// mode, under a tab per CLI when there is more than one. Each is chosen on
// a page of the sheet. The folder is the project itself and is only
// shown. Which CLI a new session uses is not chosen here but next to the
// message box. Nothing is written until 完成; what the sheet holds
// meanwhile is a `ProjectSettingsDraft`.
import RACore
import RAProto
import SwiftUI

enum ProjectSettingsPage: String, Sendable {
    case main, machine, model, context, mode
}

/// The sheet as it was asked for: a page to open on, and a new one each time so that nothing of the last is left.
struct ProjectSettingsRequest: Identifiable {
    let id = UUID()
    var page = ProjectSettingsPage.main
}

struct ProjectSettingsSheet: View {
    private let cwd: String
    private let name: String
    /// The CLI selected next to the message box: the sheet opens on its tab, and 换机器 looks for it.
    private let agent: PBAgent
    /// Gets the change to store, or nil when nothing was changed.
    private let onDone: (PrefsOp.SetSettings?) -> Void
    private let onClose: () -> Void
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    /// The destination the sheet is over.
    @Environment(\.page) private var under
    @Environment(AccountContext.self) private var context
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var draft: ProjectSettingsDraft
    @State private var page: ProjectSettingsPage
    @State private var models: PBListModelsResponse?
    @State private var modelsError = ""
    @State private var modelsFor = ""
    @State private var settings: PBDaemonSettings?
    @State private var removing: String?

    init(
        cwd: String, machine: String, agent: PBAgent, open: ProjectSettingsPage = .main,
        onDone: @escaping (PrefsOp.SetSettings?) -> Void, onClose: @escaping () -> Void
    ) {
        self.cwd = cwd
        name = projectName(cwd)
        self.agent = agent
        self.onDone = onDone
        self.onClose = onClose
        _draft = State(initialValue: ProjectSettingsDraft(cwd: cwd, machine: machine, agent: agent))
        _page = State(initialValue: open)
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
        Group {
            switch page {
            case .main: main(state, c, blocked: blocked, done: done)
            case .machine: machines(state, done: done)
            case .model: modelPage(state, c, done: done)
            case .context: contextPage(c, done: done)
            case .mode: modePage(c, done: done)
            }
        }
        // What this CLI's set starts from may be a session not fetched yet.
        .fetchEarlier(state, page: draft.unread(state))
        // The list is read when the sheet opens and again when the models are to be chosen from.
        .task(id: Load(machine: c.machineId, agent: c.agent, picking: page == .model)) { await load(c) }
        .removeProjectConfirm($removing) {
            onClose()
            nav.open(.home, from: under)
        }
        // Chooses what `-settingsEdit` names (full, auto, ask, standard, effort=<e>, model=<slug>) and saves, for a script (debug builds).
        .debugSignal("settings.apply") {
            for word in (HomeLaunch.value("-settingsEdit") ?? "").split(separator: ",") {
                edit(draft.config(model.state)) { c in
                    switch word {
                    case "full": c.mode = .full
                    case "auto": c.mode = .auto
                    case "ask": c.mode = .ask
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

    @ViewBuilder private func main(_ state: RemoteState, _ c: NewConfig, blocked: String?, done: (() -> Void)?) -> some View {
        let agents = draft.agents(state)
        let cur = draft.current(state)
        let unread = draft.unread(state)
        let pinned = isPinned(state, key: cwd)
        SheetHead("项目设置", action: done == nil ? nil : "完成", onAction: done)
        if let blocked { Banner(blocked).padding(.bottom, 8) }
        HStack(spacing: 10) {
            Avatar(cwd, size: 22)
            VStack(alignment: .leading, spacing: 0) {
                Text(name).type(T.bodyB, Theme.fg)
                Text(shortPath(cwd)).type(T.mono, Theme.muted).lineLimit(2)
            }
        }
        .accessibilityElement(children: .combine)
        .padding(.bottom, 12)
        FieldLabel("新会话")
        let machineRow = SetRow(
            "机器", machineLabel(state, id: draft.machine), dot: state.machines[draft.machine]?.online == true ? Theme.green : Theme.gray
        ) { page = .machine }
        // What a CLI's set starts from is read first where it is not known yet.
        let own = unread ? [AnyView(reading(state, cur))] : rows(state, c)
        if agents.count > 1 {
            // The machine is shared; each CLI has its own set, seen under its tab.
            SetCard([AnyView(machineRow)])
            ViewTabs(
                agents.map(agentName), selected: Binding(get: { agents.firstIndex(of: cur) ?? 0 }, set: { draft.tab = agents[$0] }), inset: 0
            )
            .padding(.top, 6)
            SetCard(own)
            if !unread { Hint("两个 CLI 各存一套，新建时用哪个在输入框旁选。只影响之后新建的会话，进行中的不变；在你的设备之间同步。") }
        } else {
            SetCard([AnyView(machineRow)] + own)
            if !unread { Hint("只影响之后新建的会话，进行中的不变；在你的设备之间同步。") }
        }
        // The same two things 项目管理 offers for a project, under the same names.
        TextAction(pinned ? "取消常用" : "设为常用", enabled: blocked == nil) {
            Haptics.confirm()
            context.prefsChanges.run(context.remote, pinned ? .unpin(path: cwd) : .pin(path: cwd))
        }
        .padding(.top, 4)
        TextAction("移除项目…", color: Theme.red, enabled: blocked == nil) { removing = cwd }
    }

    private func rows(_ state: RemoteState, _ c: NewConfig) -> [AnyView] {
        let shown = models.flatMap { projectModel(c, models: $0, settings: settings) }
        let d = defaultModeOf(state.machines[c.machineId], agent: c.agent)
        var out = [AnyView(SetRow("模型", settingsModelValue(c, shown: shown)) { page = .model })]
        if c.agent == .claude {
            // A model without a larger window has nothing to choose.
            let window = settingsContext(c, shown: shown)
            out.append(AnyView(SetRow("上下文", window.text, action: window.fixed ? nil : { page = .context })))
        }
        out.append(AnyView(
            SetRow("审批", effectiveModeText(chosen: c.mode, default: d), valueColor: settingsModeRed(c, default: d) ? Theme.red : nil) {
                page = .mode
            }))
        return out
    }

    @ViewBuilder private func reading(_ state: RemoteState, _ agent: PBAgent) -> some View {
        Group {
            if state.conn != .online || !state.sessionsError.isEmpty {
                EarlierTail(state)
            } else {
                Text("正在读取 \(agentName(agent)) 上次的设置…").type(T.sm, Theme.muted).frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(14)
    }

    // MARK: The pages

    @ViewBuilder private func machines(_ state: RemoteState, done: (() -> Void)?) -> some View {
        let rows = machineRows(state, cwd: cwd, agent: agent)
        SheetHead("机器", action: done == nil ? nil : "完成", onAction: done, onBack: { page = .main })
        ForEach(Array(rows.enumerated()), id: \.element.id) { i, m in
            ListItem(m.name, desc: m.note, checked: draft.machine == m.id, enabled: m.online, last: i == rows.count - 1) {
                Haptics.selection()
                draft.move(to: m.id)
                page = .main
            }
            .trailing { Dot(m.online ? Theme.green : Theme.gray) }
            .accessibilityValue(m.online ? "在线" : "不在线")
        }
        Hint("换机器后，各 CLI 的模型和审批从那台机器的默认设置开始。")
    }

    @ViewBuilder private func modelPage(_ state: RemoteState, _ c: NewConfig, done: (() -> Void)?) -> some View {
        SheetHead("模型", action: done == nil ? nil : "完成", onAction: done, onBack: { page = .main })
        if let ms = models {
            let standing = defaultModelName(c, models: ms, settings: settings)
            ListItem("默认" + (standing.isEmpty ? "" : "（\(standing)）"), checked: c.model.isEmpty) {
                Haptics.selection()
                edit(c) {
                    $0.model = ""
                    $0.effort = ""
                }
            }
            ForEach(Array(ms.models.enumerated()), id: \.element.slug) { i, m in
                ListItem(m.displayName.isEmpty ? m.slug : m.displayName, checked: c.model == m.slug, last: i == ms.models.count - 1) {
                    Haptics.selection()
                    draft.edit(pickModel(c, m))
                }
            }
            // With the default model an effort can still be chosen; choosing one does not fix the model.
            if let efforts = effortChoices(c, models: ms, settings: settings) {
                FieldLabel("推理强度")
                ChoiceGrid(efforts.efforts, selected: efforts.selected) { effort in
                    Haptics.selection()
                    edit(c) { $0.effort = effort }
                }
            }
        } else if !modelsError.isEmpty {
            Banner("没能读取模型列表，会用 \(agentName(c.agent)) 自己的默认模型：\(modelsError)", tone: .warn)
        } else {
            LoadingRow("读取 \(machineLabel(state, id: c.machineId)) 的设置…", inset: 0)
        }
    }

    @ViewBuilder private func contextPage(_ c: NewConfig, done: (() -> Void)?) -> some View {
        SheetHead("上下文窗口", action: done == nil ? nil : "完成", onAction: done, onBack: { page = .main })
        ListItem("1M", desc: "Claude Code 的默认窗口", checked: !c.standardContext) {
            Haptics.selection()
            edit(c) { $0.standardContext = false }
            page = .main
        }
        ListItem("标准 200k", desc: "超过 200k 前自动压缩", checked: c.standardContext, last: true) {
            Haptics.selection()
            edit(c) { $0.standardContext = true }
            page = .main
        }
        Hint("窗口在会话开始时确定，改了只影响之后新建的会话。")
    }

    @ViewBuilder private func modePage(_ c: NewConfig, done: (() -> Void)?) -> some View {
        let choices = modeChoices(c)
        SheetHead("审批模式", action: done == nil ? nil : "完成", onAction: done, onBack: { page = .main })
        ForEach(Array(choices.enumerated()), id: \.element.id) { i, choice in
            // Full access is the one choice that takes the asking away: red, as wherever it is written.
            ListItem(
                choice.title, desc: choice.desc, checked: choice.checked, enabled: choice.enabled, danger: choice.mode == .full,
                last: i == choices.count - 1
            ) {
                Haptics.selection()
                edit(c) { $0.mode = choice.mode }
                page = .main
            }
        }
        Hint("完全放行的新会话直接按此设置启动。")
    }
}

/// The rows of a setting on a soft card, a line between each two.
private struct SetCard: View {
    private let rows: [AnyView]

    init(_ rows: [AnyView]) { self.rows = rows }

    var body: some View {
        VStack(spacing: 0) {
            ForEach(rows.indices, id: \.self) { i in
                if i > 0 { Rectangle().fill(Theme.line).frame(height: 1).accessibilityHidden(true) }
                rows[i]
            }
        }
        .background(Theme.soft)
        .clipShape(RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
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
                    Text(label).type(T.sm, Theme.muted)
                    HStack(spacing: 8) {
                        Spacer(minLength: 0)
                        if let dot { Dot(dot) }
                        Text(value).type(T.sm, valueColor ?? Theme.fg2).multilineTextAlignment(.trailing)
                        if action != nil { Arrow(Theme.faint) }
                    }
                }
                .padding(.vertical, 8)
            } else {
                HStack(spacing: 10) {
                    Text(label).type(T.sm, Theme.muted).lineLimit(1).fixedSize()
                    Spacer(minLength: 0)
                    if let dot { Dot(dot) }
                    Text(value).type(T.sm, valueColor ?? Theme.fg2).lineLimit(2).multilineTextAlignment(.trailing)
                    if action != nil { Arrow(Theme.faint) }
                }
            }
        }
        .padding(.horizontal, 14)
        .frame(maxWidth: .infinity, minHeight: Theme.minHit, alignment: .leading)
        if let action {
            Button(action: action) { shown }.buttonStyle(RowPressStyle())
        } else {
            shown.accessibilityElement(children: .combine)
        }
    }
}
