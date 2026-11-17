import Foundation
import RAProto

/// The command that starts a session with its first message. `mode` unspecified leaves it to the machine's default
/// (daemon settings, else the CLI's own configuration); a new task always sends it so.
public func createSessionCommand(_ c: NewConfig, prompt: String, mode: PBApprovalMode, imageIds: [String] = [], fileIds: [String] = []) -> PBCommand {
    var cmd = PBCommand()
    cmd.createSession = .with {
        $0.cwd = c.cwd
        $0.prompt = prompt
        $0.agent = c.agent
        $0.model = c.model
        $0.effort = c.effort
        $0.mode = mode
        $0.standardContext = agentInfo(c.agent).standardContextOption && c.standardContext
        $0.imageIds = imageIds
        $0.fileIds = fileIds
    }
    return cmd
}

/// The text a start sends: what is written, without the space around it.
public func promptText(_ text: String) -> String { text.jvmTrim() }

// --- 项目设置 ---

/// The edits of an open 项目设置 sheet (Android's NewSettingsSheet): the
/// machine, and the set of the one CLI the sheet was opened for (the one
/// selected next to the message box, a session's, a task's) as far as it
/// was changed here; the others stay as stored. Another CLI's set is
/// reached by selecting that CLI there, not in the sheet. Nothing is
/// written until 完成.
public struct ProjectSettingsDraft: Equatable, Sendable {
    public let cwd: String
    /// The machine the sheet opened on.
    public let from: String
    public private(set) var machine: String
    /// The CLI the sheet was opened for, whose set is shown.
    public let agent: PBAgent
    public private(set) var edited: [PBAgent: NewConfig] = [:]

    public init(cwd: String, machine: String, agent: PBAgent) {
        self.cwd = cwd
        from = machine
        self.machine = machine
        self.agent = agent
    }

    /// Models are per machine: on another one every CLI starts over.
    public var moved: Bool { machine != from }

    public func agents(_ state: RemoteState) -> [PBAgent] { machineAgents(state.machines[machine]) }

    /// The CLI shown: the one opened for, or the machine's first where it lacks that one.
    public func current(_ state: RemoteState) -> PBAgent {
        let all = agents(state)
        return all.contains(agent) ? agent : all[0]
    }

    /// The set shown: as changed here, else that machine's defaults after a move, else the project's own.
    public func config(_ state: RemoteState) -> NewConfig {
        let cur = current(state)
        if let e = edited[cur] { return e }
        if moved { return NewConfig(machineId: machine, cwd: cwd, agent: cur) }
        return agentConfig(machineId: machine, cwd: cwd, agentSet(state, key: cwd, agent: cur))
    }

    /// What this CLI's set starts from may be a session not fetched yet: it is fetched before the set can be changed here.
    public func unread(_ state: RemoteState) -> Bool {
        let cur = current(state)
        return !moved && edited[cur] == nil && !agentSetSettled(state, key: cwd, agent: cur)
    }

    public mutating func edit(_ next: NewConfig) { edited[next.agent] = next }

    /// Another machine: what was changed for the one before does not carry over.
    public mutating func move(to id: String) {
        guard id != machine else { return }
        machine = id
        edited.removeAll()
    }

    /// The change to store, or nil when nothing was changed.
    public func op() -> PrefsOp.SetSettings? {
        if !moved && edited.isEmpty { return nil }
        let sets = edited.keys.sorted { $0.rawValue < $1.rawValue }.compactMap { edited[$0]?.agentSettings() }
        return PrefsOp.SetSettings(path: cwd, machineId: machine, moved: moved, sets: sets)
    }
}

/// The 模型 row: the model's name and the effort, 默认模型 when neither is chosen.
public func settingsModelValue(_ c: NewConfig, shown: PBModelInfo?) -> String {
    [(shown?.displayName ?? "").ifEmpty(c.model), c.effort].filter { !$0.isEmpty }.joined(separator: " · ").ifEmpty(L10n.newSessionDefaultModel())
}

/// The 上下文 row (Claude Code): a model without a larger window has nothing to choose (`fixed`).
public func settingsContext(_ c: NewConfig, shown: PBModelInfo?) -> (text: String, fixed: Bool) {
    let fixed = shown.map { (1...standardWindow).contains($0.contextWindow) } ?? false
    return (fixed ? L10n.modelsStandardOnly() : c.standardContext ? L10n.modelsStandardWindow() : L10n.modelsExtendedWindow(), fixed)
}

/// Whether the 审批 row is red: full access chosen, or left to a default that is full access or could not be read.
public func settingsModeRed(_ c: NewConfig, default d: DefaultMode) -> Bool {
    c.mode == .full || (c.mode == nil && (d.mode == .full || !d.known))
}

/// The model 默认 stands for: the daemon's default for that CLI, else the list's.
public func defaultModelName(_ c: NewConfig, models: PBListModelsResponse, settings: PBDaemonSettings?) -> String {
    defaultsFor(settings, agent: c.agent).model.ifEmpty(models.defaultModel)
}

/// `c` with the model `m` chosen: its default effort, and auto given up where the model has none.
public func pickModel(_ c: NewConfig, _ m: PBModelInfo) -> NewConfig {
    var out = c
    out.model = m.slug
    out.effort = m.defaultEffort
    if m.autoUnavailable && c.mode == .auto { out.mode = nil }
    return out
}

/// The efforts to choose from and the one that is in force, or nil when the model shown has none.
public func effortChoices(_ c: NewConfig, models: PBListModelsResponse, settings: PBDaemonSettings?) -> (efforts: [String], selected: String)? {
    guard let m = projectModel(c, models: models, settings: settings), !m.efforts.isEmpty else { return nil }
    let own = c.model.isEmpty ? defaultsFor(settings, agent: c.agent).effort.ifEmpty(m.defaultEffort) : m.defaultEffort
    return (m.efforts, c.effort.ifEmpty(own))
}

/// One choice of 审批模式.
public struct ApprovalModeChoice: Equatable, Sendable, Identifiable {
    /// Nil is 默认: the CLI's own configuration.
    public var mode: Mode?
    public var title: String
    public var desc: String
    public var enabled: Bool
    public var checked: Bool

    public var id: String { mode.map { String($0.proto.rawValue) } ?? "default" }

    public init(mode: Mode?, title: String, desc: String, enabled: Bool, checked: Bool) {
        self.mode = mode
        self.title = title
        self.desc = desc
        self.enabled = enabled
        self.checked = checked
    }
}

/// The modes a new session can start in. The auto mode is dropped where the
/// model shown comes without it; the CLI's own list says which those are
/// (projectModel), so nothing is greyed out until the list has come.
public func modeChoices(_ c: NewConfig, models: PBListModelsResponse?, settings: PBDaemonSettings? = nil) -> [ApprovalModeChoice] {
    let name = agentName(c.agent)
    let allOff = allAutoUnavailable(models?.models)
    let off = allOff || (models.map { projectModel(c, models: $0, settings: settings)?.autoUnavailable == true } ?? false)
    let autoHelp = allOff ? L10n.modesAccountUnsupported() : L10n.modesModelUnsupported()
    return [ApprovalModeChoice(mode: nil, title: L10n.modelsDefault(), desc: L10n.newSessionFollowSConfiguration(value: String(describing: name)), enabled: true, checked: c.mode == nil)]
        + newSessionModes.map { m in
            let disabled = m == .auto && off
            return ApprovalModeChoice(
                mode: m, title: m.text, desc: disabled ? autoHelp : modeHelp(m, agent: name),
                enabled: !disabled, checked: c.mode == m)
        }
}

/// One machine of 项目设置 › 机器.
public struct ProjectMachineRow: Equatable, Sendable, Identifiable {
    public var id: String
    public var name: String
    public var online: Bool
    /// Why it cannot be chosen, or what it lacks; nil for nothing to say.
    public var note: String?

    public init(id: String, name: String, online: Bool, note: String?) {
        self.id = id
        self.name = name
        self.online = online
        self.note = note
    }
}

/// The machines the project used. `agent` is the CLI selected next to the message box: 换机器 comes here to find it.
public func machineRows(_ state: RemoteState, cwd: String, agent: PBAgent) -> [ProjectMachineRow] {
    projectMachines(state, key: cwd).map { id in
        let m = state.machines[id]
        let online = m?.pathState.reachable == true
        let installed = machineAgents(m).map(agentName).joined(separator: L10n.commonListSeparator())
        let has = machineAgents(m).contains(agent)
        let note: String
        if !online {
            note = L10n.newSessionIosMachineUnavailable()
        } else {
            let fallback = (!has && m != nil) ? switchMachine(NewConfig(machineId: "", cwd: cwd, agent: agent), machineId: id, agents: machineAgents(m)).agent : agent
            note = has ? L10n.newSessionInstalled(agents: installed) : L10n.newSessionInstalledFallback(agent: agentName(fallback), agents: installed, missing: agentName(agent))
        }
        return ProjectMachineRow(
            id: id, name: machineLabel(state, id: id), online: online,
            note: note)
    }
}
