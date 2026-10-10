// What the project screens decide before they draw: the one list of
// projects (architecture §8 「项目列表偏好」), choosing a folder for a new
// one, a project's page, the notices above its new-session box, and the
// edits of 项目设置 (architecture §13). The parts of Android's ui/Manage.kt
// and ui/Project.kt that sit inside composables there.
import Foundation
import RAProto

// --- the list of projects ---

/// The projects as every list shows them (client-v34): 常用 then 其他,
/// titled only when there are both, a search above more than `filterOver`.
public struct ProjectListing: Equatable, Sendable {
    public var pinned: [Project]
    public var others: [Project]
    /// Both groups have rows: each gets its title.
    public var titled: Bool
    /// More projects than `filterOver`: the list offers a search.
    public var searchable: Bool
    /// Something is searched for; what stands above the projects (全部项目) is left out meanwhile.
    public var searching: Bool
    /// 「没有名字或路径里有「…」的项目」 when a search matches nothing.
    public var noMatch: String?

    public init(pinned: [Project], others: [Project], titled: Bool, searchable: Bool, searching: Bool, noMatch: String?) {
        self.pinned = pinned
        self.others = others
        self.titled = titled
        self.searchable = searchable
        self.searching = searching
        self.noMatch = noMatch
    }
}

public func projectListing(_ state: RemoteState, search: String = "") -> ProjectListing {
    let g = projectGroups(state)
    let q = search.jvmTrim()
    let match: (Project) -> Bool = { q.isEmpty || $0.name.jvmContainsIgnoringCase(q) || $0.key.jvmContainsIgnoringCase(q) }
    let pinned = g.pinned.filter(match)
    let others = g.others.filter(match)
    return ProjectListing(
        pinned: pinned, others: others, titled: !pinned.isEmpty && !others.isEmpty,
        searchable: g.pinned.count + g.others.count > filterOver, searching: !q.isEmpty,
        noMatch: !q.isEmpty && pinned.isEmpty && others.isEmpty ? "没有名字或路径里有「\(q)」的项目" : nil)
}

/// What choosing the folder at `path` on `machine` as a project writes
/// (client-v33): a folder removed from the list earlier is listed again;
/// a new one is stored with the machine chosen, so it is listed before its
/// first session, here and on the account's other clients; a folder that
/// is a project already changes nothing.
public func addProjectOp(_ state: RemoteState, machine: String, path: String) -> PrefsOp? {
    if isHidden(state, key: path) { return .unhide(path: path) }
    if project(state, key: path) == nil && savedSettings(state, key: path) == nil {
        return .setSettings(PrefsOp.SetSettings(path: path, machineId: machine))
    }
    return nil
}

// --- choosing a folder ---

/// The account's computers in the order the pickers list them: by name, then by ID.
public func listedMachines(_ state: RemoteState) -> [MachineView] {
    state.machines.values.sorted { a, b in
        let x = machineName(a, id: a.id), y = machineName(b, id: b.id)
        return x != y ? jvmLess(x, y) : jvmLess(a.id, b.id)
    }
}

/// The computer a folder is first browsed on: the first that is online; empty when none is.
public func firstOnlineMachine(_ state: RemoteState) -> String {
    listedMachines(state).first(where: \.online)?.id ?? ""
}

/// A folder's path as the picker writes it, piece by piece: ~ / Code / foo.
public func pathCrumbs(_ path: String) -> [String] {
    shortPath(path).split(ascii: UInt8(ascii: "/")).filter { !$0.isEmpty }
}

/// The folder `name` inside `dir`.
public func childPath(_ dir: String, _ name: String) -> String {
    var base = dir
    while base.hasSuffix("/") { base.removeLast() }
    return base + "/" + name
}

public func listDirectoriesCommand(path: String) -> PBCommand {
    var cmd = PBCommand()
    cmd.listDirectories = .with { $0.path = path }
    return cmd
}

public func getSettingsCommand() -> PBCommand {
    var cmd = PBCommand()
    cmd.getSettings = PBGetSettingsRequest()
    return cmd
}

// --- a project's page ---

/// A project's page (Android's ProjectScreen): its sessions that go on, the ended ones held here, and what its header says.
public struct ProjectPage: Equatable, Sendable {
    public var active: [SessionView]
    public var ended: [SessionView]
    /// The rows are one project's: never the project, the CLI only when its sessions used more than one.
    public var meta: RowMeta
    /// How many of its sessions ended, by the machines' summaries: they come a page at a time.
    public var endedTotal: Int
    /// 「~/Code/foo · MacBook · VPS-dev」.
    public var subtitle: String

    public init(active: [SessionView], ended: [SessionView], meta: RowMeta, endedTotal: Int, subtitle: String) {
        self.active = active
        self.ended = ended
        self.meta = meta
        self.endedTotal = endedTotal
        self.subtitle = subtitle
    }

    /// The ended sessions under the header: the latest three, or all that are held once 已结束 was opened.
    public func recent(all: Bool) -> [SessionView] { all ? ended : Array(ended.prefix(3)) }

    /// How many ended sessions the page wants held: three for 最近结束, else all it holds, each end reached asking for a page.
    public func wanted(all: Bool) -> Int { all ? ended.count : 3 }
}

public func projectPage(_ state: RemoteState, key: String) -> ProjectPage {
    let sessions = state.sessions.values.filter { projectKey($0) == key }.sorted(by: sessionOrder)
    let ended = sessions.filter(exited)
    let pr = project(state, key: key)
    let machines = (pr?.machineIds ?? []).map { machineLabel(state, id: $0) }.distinct()
    return ProjectPage(
        active: sessions.filter { !exited($0) }, ended: ended, meta: rowMeta(state, sessions: sessions, project: false),
        endedTotal: max(pr?.ended ?? 0, ended.count), subtitle: ([shortPath(key)] + machines).joined(separator: " · "))
}

// --- the settings summary ---

/// Whether a start with `c` runs without approvals: chosen so, or the CLI's own default on that machine.
public func isFullAccess(_ state: RemoteState, _ c: NewConfig) -> Bool {
    c.mode == .full || (c.mode == nil && defaultModeOf(state.machines[c.machineId], agent: c.agent).mode == .full)
}

/// The summary cut around 完全放行, the one part of it written in red; `full` is empty when it is not to be.
public func summaryParts(_ summary: String, full: Bool) -> (before: String, full: String, after: String) {
    guard full, let r = summary.range(of: Mode.full.text) else { return (summary, "", "") }
    return (String(summary[..<r.lowerBound]), Mode.full.text, String(summary[r.upperBound...]))
}

/// The summary as the navigation bar's second line has it once the keyboard is up: CLI, mode and machine.
public func foldedSummary(_ state: RemoteState, _ c: NewConfig) -> String {
    [
        agentName(c.agent), effectiveModeText(chosen: c.mode, default: defaultModeOf(state.machines[c.machineId], agent: c.agent)),
        summaryMachine(state, id: c.machineId),
    ].filter { !$0.isEmpty }.joined(separator: " · ")
}

// --- the new-session box ---

/// Another machine offered while the project's own is offline.
public struct OfflineMachineChoice: Equatable, Sendable, Identifiable {
    public var id: String
    /// 「改用 VPS-dev」.
    public var title: String
    /// What that machine offers for this draft.
    public var note: String
    /// The draft's configuration once this machine is chosen.
    public var config: NewConfig

    public init(id: String, title: String, note: String, config: NewConfig) {
        self.id = id
        self.title = title
        self.note = note
        self.config = config
    }
}

/// One notice above the new-session box.
public enum NewSessionNotice: Equatable, Sendable, Identifiable {
    /// What the next session inherits is not read yet; `retry` when a tap fetches again.
    case reading(String, retry: Bool)
    /// The machine went offline under a draft: the machines to choose from for this start, or that there is none.
    case offlineChoice(title: String, text: String, choices: [OfflineMachineChoice], none: String?)
    /// The machine is offline and nothing is written yet.
    case offline(String)
    /// The machine runs another protocol version: which side to update.
    case mismatch(String)
    /// The CLI meant is not on this machine, so another is used; 换机器.
    case missingAgent(String)
    case starting(String)
    /// The start failed for certain; 重试.
    case failed(String)
    /// Whether the last start ran is not known; 放弃.
    case unknown(String)

    public var id: String {
        switch self {
        case .reading: return "reading"
        case .offlineChoice: return "offline-choice"
        case .offline: return "offline"
        case .mismatch: return "mismatch"
        case .missingAgent: return "missing-agent"
        case .starting: return "starting"
        case .failed: return "failed"
        case .unknown: return "unknown"
        }
    }
}

/// How a start stands, for the notices.
public struct NewSessionProgress: Equatable, Sendable {
    public var running: Bool
    public var error: String
    /// The last start's outcome is unknown (`Attempt.frozen`), and expired (`Attempt.expired`).
    public var frozen: Bool
    public var expired: Bool
    /// The first message of the start whose outcome is unknown.
    public var prompt: String

    public init(running: Bool = false, error: String = "", frozen: Bool = false, expired: Bool = false, prompt: String = "") {
        self.running = running
        self.error = error
        self.frozen = frozen
        self.expired = expired
        self.prompt = prompt
    }
}

/// The machines a draft can move to while its own is offline (Android's OfflineChoice): the others the project used that are online.
public func offlineChoices(_ state: RemoteState, _ c: NewConfig) -> [OfflineMachineChoice] {
    projectMachines(state, key: c.cwd).filter { $0 != c.machineId && state.machines[$0]?.online == true }.map { id in
        let agents = machineAgents(state.machines[id])
        let note = agents.contains(c.agent)
            ? "有 \(agentName(c.agent))；模型和模式会按这台机器重新选择"
            : "只有 \(agents.map(agentName).joined(separator: "、"))；会改用 \(agentName(agents[0]))，模型和模式按这台机器重新选择"
        return OfflineMachineChoice(
            id: id, title: "改用 \(machineLabel(state, id: id))", note: note, config: switchMachine(c, machineId: id, agents: agents))
    }
}

/// The notices above the new-session box, in order (Android's NewSessionBox).
/// `name` is the project's; `pick` the CLI chosen next to the box; `drafting`
/// whether something is written. Out of reach of the server nothing is said
/// of the machine: every computer reads as offline then, and the connection
/// bar has said why (architecture §9 「断线时显示什么」).
public func newSessionNotices(
    _ state: RemoteState, config c: NewConfig?, name: String, pick: PBAgent?, drafting: Bool, settled: Bool, start: NewSessionProgress
) -> [NewSessionNotice] {
    var out = [NewSessionNotice]()
    // Why nothing can start yet; a page that failed says so, with the tap that tries again.
    if !settled {
        if state.conn != .online || !state.sessionsError.isEmpty {
            out.append(.reading(earlierTail(state), retry: state.conn == .online && !state.sessionsError.isEmpty))
        } else {
            out.append(.reading("正在读取这个项目上次的设置…", retry: false))
        }
    }
    if let c {
        let machine = state.machines[c.machineId]
        let label = machineLabel(state, id: c.machineId)
        if machine?.online != true {
            if state.unlinked {
                // The connection bar says it.
            } else if drafting {
                let choices = offlineChoices(state, c)
                out.append(.offlineChoice(
                    title: "\(label) 已离线，任务尚未开始", text: "草稿已保留，请选择在线机器；不自动换机器或 CLI。", choices: choices,
                    none: choices.isEmpty ? "\(name) 用过的其他电脑也都不在线。" : nil))
            } else {
                out.append(.offline("\(label) 不在线，现在不能新建"))
            }
        } else if let refusal = protocolMismatch(machine) {
            out.append(.mismatch(refusal))
        }
        if let missing = missingAgent(state, c, pick: pick) {
            out.append(.missingAgent("\(label) 没有 \(agentName(missing))，这次用 \(agentName(c.agent))"))
        }
        if start.running { out.append(.starting("正在 \(label) 上启动…")) }
    }
    // An unknown start has its own notice below.
    if !start.error.isEmpty && !start.frozen && !start.running { out.append(.failed("启动失败：\(start.error)")) }
    if !start.running && start.frozen {
        out.append(.unknown(
            start.expired
                ? "不确定「\(start.prompt.jvmTake(40))」有没有启动，它已过期，不会再重发。请先看看进行中的会话"
                : "上次的启动结果不确定。再点 ↑ 会重发同一个请求，电脑收到过就不会再启动一次"))
    }
    return out
}

/// Whether ↑ starts a session now: something is written, what it starts with is settled, and the machine is there to take it.
public func canStartSession(_ state: RemoteState, config c: NewConfig?, drafting: Bool, settled: Bool, running: Bool) -> Bool {
    guard let c, settled, !running, drafting, state.conn == .online, let machine = state.machines[c.machineId], machine.online else {
        return false
    }
    return protocolMismatch(machine) == nil
}

/// The command that starts a session with its first message. `mode` unspecified leaves it to the CLI's own configuration.
public func createSessionCommand(_ c: NewConfig, prompt: String, mode: PBApprovalMode, imageIds: [String] = []) -> PBCommand {
    var cmd = PBCommand()
    cmd.createSession = .with {
        $0.cwd = c.cwd
        $0.prompt = prompt
        $0.agent = c.agent
        $0.model = c.model
        $0.effort = c.effort
        $0.mode = mode
        $0.standardContext = c.agent == .claude && c.standardContext
        $0.imageIds = imageIds
    }
    return cmd
}

/// The text a start sends: what is written, without the space around it.
public func promptText(_ text: String) -> String { text.jvmTrim() }

// --- 项目设置 ---

/// The edits of an open 项目设置 sheet (Android's NewSettingsSheet): the
/// machine, shared by the CLIs, and each CLI's own set as far as it was
/// changed here; the others stay as stored. Nothing is written until 完成.
public struct ProjectSettingsDraft: Equatable, Sendable {
    public let cwd: String
    /// The machine the sheet opened on.
    public let from: String
    public private(set) var machine: String
    /// The CLI whose set is shown.
    public var tab: PBAgent
    public private(set) var edited: [PBAgent: NewConfig] = [:]

    public init(cwd: String, machine: String, agent: PBAgent) {
        self.cwd = cwd
        from = machine
        self.machine = machine
        tab = agent
    }

    /// Models are per machine: on another one every CLI starts over.
    public var moved: Bool { machine != from }

    public func agents(_ state: RemoteState) -> [PBAgent] { machineAgents(state.machines[machine]) }

    /// The CLI shown: the tab's, or the machine's first where it lacks that one.
    public func current(_ state: RemoteState) -> PBAgent {
        let all = agents(state)
        return all.contains(tab) ? tab : all[0]
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
    [(shown?.displayName ?? "").ifEmpty(c.model), c.effort].filter { !$0.isEmpty }.joined(separator: " · ").ifEmpty("默认模型")
}

/// The 上下文 row (Claude Code): a model without a larger window has nothing to choose (`fixed`).
public func settingsContext(_ c: NewConfig, shown: PBModelInfo?) -> (text: String, fixed: Bool) {
    let fixed = shown.map { (1...standardWindow).contains($0.contextWindow) } ?? false
    return (fixed ? "标准 200k · 这个模型只有标准窗口" : c.standardContext ? "标准 200k" : "1M", fixed)
}

/// Whether the 审批 row is red: full access chosen, or left to a default that is full access or could not be read.
public func settingsModeRed(_ c: NewConfig, default d: DefaultMode) -> Bool {
    c.mode == .full || (c.mode == nil && (d.mode == .full || !d.known))
}

/// The model 默认 stands for: the daemon's default for that CLI, else the list's.
public func defaultModelName(_ c: NewConfig, models: PBListModelsResponse, settings: PBDaemonSettings?) -> String {
    ((c.agent == .claude ? settings?.claude.model : settings?.defaultModel) ?? "").ifEmpty(models.defaultModel)
}

/// Claude Code's haiku has no auto mode.
public func noAutoModel(_ agent: PBAgent, model: String) -> Bool {
    agent == .claude && model.jvmContainsIgnoringCase("haiku")
}

/// `c` with the model `m` chosen: its default effort, and auto given up where the model has none.
public func pickModel(_ c: NewConfig, _ m: PBModelInfo) -> NewConfig {
    var out = c
    out.model = m.slug
    out.effort = m.defaultEffort
    if noAutoModel(c.agent, model: m.slug) && c.mode == .auto { out.mode = nil }
    return out
}

/// The efforts to choose from and the one that is in force, or nil when the model shown has none.
public func effortChoices(_ c: NewConfig, models: PBListModelsResponse, settings: PBDaemonSettings?) -> (efforts: [String], selected: String)? {
    guard let m = projectModel(c, models: models, settings: settings), !m.efforts.isEmpty else { return nil }
    let own = c.model.isEmpty ? ((c.agent == .claude ? settings?.claude.effort : settings?.defaultEffort) ?? "").ifEmpty(m.defaultEffort) : m.defaultEffort
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

    public var id: String { mode?.text ?? "default" }

    public init(mode: Mode?, title: String, desc: String, enabled: Bool, checked: Bool) {
        self.mode = mode
        self.title = title
        self.desc = desc
        self.enabled = enabled
        self.checked = checked
    }
}

public func modeChoices(_ c: NewConfig) -> [ApprovalModeChoice] {
    let name = agentName(c.agent)
    return [ApprovalModeChoice(mode: nil, title: "默认", desc: "跟随 \(name) 自己的配置", enabled: true, checked: c.mode == nil)]
        + newSessionModes.map { m in
            let off = m == .auto && noAutoModel(c.agent, model: c.model)
            return ApprovalModeChoice(
                mode: m, title: m.text, desc: off ? "当前 Haiku 模型不支持" : modeHelp(m).replacingOccurrences(of: "CLI", with: name),
                enabled: !off, checked: c.mode == m)
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
        let online = state.machines[id]?.online == true
        let has = machineAgents(state.machines[id]).contains(agent)
        return ProjectMachineRow(
            id: id, name: machineLabel(state, id: id), online: online,
            note: !online ? "不在线，现在不能新建" : !has ? "没有 \(agentName(agent))" : nil)
    }
}
