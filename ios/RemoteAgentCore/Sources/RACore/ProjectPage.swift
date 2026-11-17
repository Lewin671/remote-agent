// What a project's next session starts with (architecture §13 「初值」), and
// the words its page says about that: the part of Android's ui/Project.kt
// that draws nothing.
import Foundation
import RAProto

/// The modes a session can be set to start in besides the default: a
/// scheduled task's, and a machine's default for new tasks. Neither CLI starts
/// in plan. A new task has none of its own (architecture §13): it starts in
/// the machine's default.
public let newSessionModes: [Mode] = [.ask, .auto, .full]

/// How a new session starts; empty model, effort and mode mean the machine's
/// defaults. A new task's mode is always empty; a scheduled task's is its own.
public struct NewConfig: Equatable, Sendable {
    public var machineId: String
    public var cwd: String
    public var agent: PBAgent
    public var model: String
    public var effort: String
    public var mode: Mode?
    /// Claude Code: the standard context window instead of 1M.
    public var standardContext: Bool

    public init(
        machineId: String, cwd: String, agent: PBAgent = defaultAgent, model: String = "", effort: String = "", mode: Mode? = nil,
        standardContext: Bool = false
    ) {
        self.machineId = machineId
        self.cwd = cwd
        self.agent = agent
        self.model = model
        self.effort = effort
        self.mode = mode
        self.standardContext = standardContext
    }

    /// The configuration's CLI set, as project settings store it: never with
    /// an approval mode, which a new task does not have.
    public func agentSettings() -> PBAgentSettings {
        var s = PBAgentSettings()
        s.agent = agent
        s.model = model
        s.effort = effort
        s.standardContext = agentInfo(agent).standardContextOption && standardContext
        return s
    }
}

/// The project's stored settings, or nil while none were saved.
public func savedSettings(_ state: RemoteState, key: String) -> PBProjectSettings? {
    state.prefs?.prefs.settings.first { $0.path.jvmEquals(key) }
}

// Of the sessions held here: every one that has not ended, and the ended ones fetched so far (architecture §8 「会话列表分页」).
// In the order of the listing, live sessions before ended ones (architecture §8).
private func projectSessions(_ state: RemoteState, key: String) -> [SessionView] {
    state.sessions.values.filter { $0.info?.taskOwned != true && projectKey($0).jvmEquals(key) }.sorted(by: sessionOrder)
}

/// The machines a project has sessions on, by the sessions held here and the
/// machines' project summaries; every machine when nothing names it yet (by
/// ID: the account's machines have no order of their own).
public func projectMachines(_ state: RemoteState, key: String) -> [String] {
    let named = project(state, key: key)?.machineIds ?? []
    return named.isEmpty ? state.machines.keys.sorted(by: jvmLess) : named
}

/// Where a project's new sessions inherit from (architecture §13 「初值」): the
/// first session in the listing's order, of the project or of one CLI in it.
/// The listing comes a page at a time, in that order, so the answer is the
/// first such session among those within the pages fetched (`sure`): every
/// session before it is held. While none of them matches and the project has
/// ended sessions that were not fetched, the answer is not known: the session
/// may be among those, and one held past the pages fetched (kept from before
/// a reconnect, or fetched by its ID) may not be the first.
private final class Inheritance {
    private let all: [SessionView]
    private let sure: [SessionView]
    private let unfetched: Bool

    /// Set once a session was looked for whose place among those not fetched is not known.
    private(set) var unsettled = false

    init(_ state: RemoteState, key: String) {
        all = projectSessions(state, key: key)
        sure = all.filter { s in
            guard let next = state.sessionsNext else { return true }
            return !afterPlace(endedMs: s.listedEndedMs, sessionId: s.id, place: next)
        }
        unfetched = state.moreSessions && (project(state, key: key)?.ended ?? 0) > all.filter(exited).count
    }

    /// The first session that matches; when `unsettled` turns true, only the best guess of those held.
    func first(_ match: (SessionView) -> Bool = { _ in true }) -> SessionView? {
        if let s = sure.first(where: match) { return s }
        if unfetched { unsettled = true }
        return all.first(where: match)
    }
}

/// The CLI a project's next session is meant to use, before looking at what
/// the machine has: the one picked next to the message box, else the one the
/// latest new session used, else the latest session's. Nil when nothing says.
public func wantedAgent(_ state: RemoteState, key: String, pick: PBAgent? = nil) -> PBAgent? {
    wantedAgent(state, key: key, pick: pick, saved: savedSettings(state, key: key))
}

/// The same with the project's settings given, as they will be once a change is stored.
public func wantedAgent(_ state: RemoteState, key: String, pick: PBAgent?, saved: PBProjectSettings?) -> PBAgent? {
    wantedAgent(from: Inheritance(state, key: key), pick: pick, saved: saved)
}

private func wantedAgent(from: Inheritance, pick: PBAgent?, saved: PBProjectSettings?) -> PBAgent? {
    if let pick { return pick }
    if let a = saved?.agent, settingsAgents.contains(a) { return a }
    return from.first()?.agent
}

/// One CLI's set in a project: the stored entry (empty fields are the
/// machine's defaults), else the model and effort the project's latest
/// session on that CLI runs with, else the machine's defaults. No approval
/// mode is carried over: a new task starts in the machine's default.
public func agentSet(_ state: RemoteState, key: String, agent: PBAgent) -> PBAgentSettings {
    agentSet(state, key: key, agent: agent, saved: savedSettings(state, key: key))
}

public func agentSet(_ state: RemoteState, key: String, agent: PBAgent, saved: PBProjectSettings?) -> PBAgentSettings {
    agentSet(from: Inheritance(state, key: key), agent: agent, saved: saved)
}

/// Whether `agentSet` is settled for this CLI: see `ProjectStart.settled`.
public func agentSetSettled(_ state: RemoteState, key: String, agent: PBAgent) -> Bool {
    agentSetSettled(state, key: key, agent: agent, saved: savedSettings(state, key: key))
}

public func agentSetSettled(_ state: RemoteState, key: String, agent: PBAgent, saved: PBProjectSettings?) -> Bool {
    let from = Inheritance(state, key: key)
    _ = agentSet(from: from, agent: agent, saved: saved)
    return !from.unsettled
}

private func agentSet(from: Inheritance, agent: PBAgent, saved: PBProjectSettings?) -> PBAgentSettings {
    if let stored = saved?.agents.first(where: { $0.agent == agent }) { return stored }
    var b = PBAgentSettings()
    b.agent = agent
    if let last = from.first({ $0.agent == agent }) {
        b.model = last.info?.model ?? ""
        b.effort = last.info?.effort ?? ""
    }
    return b
}

/// What a project's next session starts with, and whether that is settled.
/// It is not while something in it would be inherited from a session and
/// which session that is cannot be told yet: `config` is then only the best
/// guess, no session is started with it, and the pages of the listing are
/// fetched until it is settled. This is the one place that answers both, for
/// the CLI the session would really start on, which is not the remembered
/// one where the machine lacks that.
public struct ProjectStart: Equatable, Sendable {
    public var config: NewConfig
    public var settled: Bool

    public init(config: NewConfig, settled: Bool) {
        self.config = config
        self.settled = settled
    }
}

/// What a project's next session starts with (architecture §13). The machine
/// is the explicitly chosen `machine`, else the stored one, else the latest session's,
/// else the one whose summary
/// has the project's newest session (its sessions may all be on pages not
/// fetched). The CLI is `wantedAgent`, or the machine's first
/// when it has none or lacks that one, with that CLI's own set. Nil while no
/// machine is known.
public func projectStart(_ state: RemoteState, key: String, pick: PBAgent? = nil, machine: String? = nil) -> ProjectStart? {
    projectStart(state, key: key, pick: pick, machine: machine, saved: savedSettings(state, key: key))
}

/// The same with the project's settings given, as they will be once a change is stored.
public func projectStart(_ state: RemoteState, key: String, pick: PBAgent?, machine: String?, saved: PBProjectSettings?) -> ProjectStart? {
    let from = Inheritance(state, key: key)
    let machineId: String
    if let machine {
        machineId = machine
    } else if let stored = saved?.machineID, !stored.isEmpty {
        machineId = stored
    } else if let latest = from.first()?.machineId {
        machineId = latest
    } else if let summed = project(state, key: key)?.machineIds.first {
        machineId = summed
    } else {
        return nil
    }
    let agents = machineAgents(state.machines[machineId])
    var agent = agents[0]
    if let wanted = wantedAgent(from: from, pick: pick, saved: saved), agents.contains(wanted) { agent = wanted }
    return ProjectStart(config: agentConfig(machineId: machineId, cwd: key, agentSet(from: from, agent: agent, saved: saved)), settled: !from.unsettled)
}

/// The configuration of `projectStart`, settled or not.
public func projectConfig(_ state: RemoteState, key: String, pick: PBAgent? = nil, machine: String? = nil) -> NewConfig? {
    projectStart(state, key: key, pick: pick, machine: machine)?.config
}

public func projectConfig(_ state: RemoteState, key: String, pick: PBAgent?, machine: String?, saved: PBProjectSettings?) -> NewConfig? {
    projectStart(state, key: key, pick: pick, machine: machine, saved: saved)?.config
}

/// A stored set as a new task's configuration. A mode stored by an earlier
/// version is not read: a new task starts in the machine's default.
public func agentConfig(machineId: String, cwd: String, _ s: PBAgentSettings) -> NewConfig {
    NewConfig(machineId: machineId, cwd: cwd, agent: s.agent, model: s.model, effort: s.effort, standardContext: s.standardContext)
}

/// The mode a CLI gives a session started without one, as the daemon reads
/// it (AgentInfo.default_mode): not `known` when it could not read the
/// configuration.
public struct DefaultMode: Equatable, Sendable {
    /// One of the four, or nil for a mode outside them (Claude Code's accept edits).
    public var mode: Mode?
    public var known: Bool
    /// The folder's project settings may give another default (Claude Code).
    public var varies: Bool

    public init(mode: Mode?, known: Bool, varies: Bool) {
        self.mode = mode
        self.known = known
        self.varies = varies
    }
}

public func defaultModeOf(_ m: MachineView?, agent: PBAgent) -> DefaultMode {
    let info = m?.meta?.agents.first { effectiveAgent($0.agent) == effectiveAgent(agent) }
    var known = false
    if let info {
        switch info.defaultModeSource {
        case .unspecified, .UNRECOGNIZED: known = false
        default: known = true
        }
    }
    return DefaultMode(mode: known ? info.flatMap { modeOf($0.defaultMode) } : nil, known: known, varies: info?.defaultModeVaries == true)
}

/// A mode the machine could not read: the start leaves it to the CLI's own settings.
public var unknownModeText: String { L10n.newSessionFollowCli() }

/// The mode a start uses: chosen, or the CLI's effective default on that machine.
public func modeLabel(chosen: Mode?, default d: DefaultMode, agent: PBAgent, includeDefault: Bool = true) -> String {
    let text: String
    if let chosen {
        text = chosen.text
    } else if !d.known {
        text = unknownModeText
    } else if let m = d.mode {
        text = includeDefault ? L10n.modesDefaultNamed(mode: String(describing: m.text)) : m.text
    } else {
        text = includeDefault ? L10n.modesDefaultAgent(agent: String(describing: agentName(agent))) : L10n.modesAgentOwn(agent: String(describing: agentName(agent)))
    }
    return text + (chosen == nil && d.varies ? L10n.errorsIosProjectSettingsMayDiffer() : "")
}

public func effectiveModeText(chosen: Mode?, default d: DefaultMode) -> String {
    if let chosen { return chosen.text }
    if !d.known { return unknownModeText }
    return d.mode?.text ?? L10n.errorsIosCliSOwnMode()
}

/// Another CLI for a draft (retained for un-migrated Schedules).
public func switchAgent(_ c: NewConfig, project: NewConfig?, agent: PBAgent) -> NewConfig {
    var set = project.flatMap { $0.agent == agent ? $0 : nil } ?? NewConfig(machineId: c.machineId, cwd: c.cwd, agent: agent)
    if set.machineId != c.machineId {
        set.machineId = c.machineId
        set.model = ""
        set.effort = ""
    }
    return set
}

/// Another machine for a draft (retained for un-migrated Schedules).
public func switchMachine(_ c: NewConfig, machineId: String, agents: [PBAgent]) -> NewConfig {
    var out = c
    out.machineId = machineId
    out.model = ""
    out.effort = ""
    out.mode = nil
    out.agent = agents.contains(c.agent) ? c.agent : agents.first ?? defaultAgent
    return out
}

/// The model a new session would run: the one chosen, else the daemon's default for that CLI, else the list's.
public func projectModel(_ c: NewConfig, models: PBListModelsResponse, settings: PBDaemonSettings? = nil) -> PBModelInfo? {
    let defaultModel = defaultsFor(settings, agent: c.agent).model
    let id = c.model.ifEmpty(defaultModel.ifEmpty(models.defaultModel))
    return findModel(models.models, id: id) { $0.slug }
}
