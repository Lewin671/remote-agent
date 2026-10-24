// What a project's next session starts with (architecture §13 「初值」), and
// the words its page says about that: the part of Android's ui/Project.kt
// that draws nothing.
import Foundation
import RAProto

/// The modes a new session can be started in besides the default. Neither CLI
/// starts in plan, and full access is only ever the confirmed machine default.
public let newSessionModes: [Mode] = [.ask, .auto, .full]

/// How a new session starts; empty model, effort and mode mean the machine's defaults.
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
        machineId: String, cwd: String, agent: PBAgent = .codex, model: String = "", effort: String = "", mode: Mode? = nil,
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

    /// The configuration's CLI set, as project settings store it.
    public func agentSettings() -> PBAgentSettings {
        var s = PBAgentSettings()
        s.agent = agent
        s.model = model
        s.effort = effort
        s.mode = mode?.proto ?? .unspecified
        s.standardContext = agent == .claude && standardContext
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
        sure = all.filter { !pastFetched(state, $0) }
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
/// machine's defaults), else what the project's latest session on that CLI
/// runs with, else the machine's defaults. Plan is never carried over.
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
        if let m = modeOf(last), newSessionModes.contains(m) { b.mode = m.proto }
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

public func agentConfig(machineId: String, cwd: String, _ s: PBAgentSettings) -> NewConfig {
    NewConfig(
        machineId: machineId, cwd: cwd, agent: s.agent, model: s.model, effort: s.effort,
        mode: modeOf(s.mode).flatMap { newSessionModes.contains($0) ? $0 : nil }, standardContext: s.standardContext)
}

/// The CLI that was meant but that the machine of `c` does not have, so `c`
/// uses another: said above the message box, never silently.
public func missingAgent(_ state: RemoteState, _ c: NewConfig, pick: PBAgent? = nil) -> PBAgent? {
    guard let wanted = wantedAgent(state, key: c.cwd, pick: pick), wanted != c.agent,
        !machineAgents(state.machines[c.machineId]).contains(wanted)
    else { return nil }
    return wanted
}

/// What a started session leaves in the project's settings, or nil when
/// nothing changes. The first one establishes them; later ones only remember
/// the CLI and add its set if the project has none for it. `project` is the
/// project's own configuration for that start: a machine picked just for this
/// start is not stored, and neither are that machine's defaults.
public func startSettings(saved: PBProjectSettings?, project: NewConfig, used: NewConfig) -> PrefsOp.SetSettings? {
    if let saved, saved.agent == used.agent, saved.agents.contains(where: { $0.agent == used.agent }) { return nil }
    let set: NewConfig? = used.machineId == project.machineId ? used : project.agent == used.agent ? project : nil
    return PrefsOp.SetSettings(
        path: used.cwd, machineId: project.machineId, agent: used.agent, sets: set.map { [$0.agentSettings()] } ?? [], keepExisting: true)
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
public let unknownModeText = "跟随 CLI 设置"

/// The mode a start uses: chosen, or the CLI's effective default on that machine.
public func modeLabel(chosen: Mode?, default d: DefaultMode, agent: PBAgent) -> String {
    let text: String
    if let chosen {
        text = chosen.text
    } else if !d.known {
        text = unknownModeText
    } else if let m = d.mode {
        text = "默认：\(m.text)"
    } else {
        text = "默认：\(agentName(agent)) 自己的模式"
    }
    return text + (chosen == nil && d.varies ? "（项目设置可能不同）" : "")
}

/// A new session's configuration (architecture §8): it follows the project,
/// whose latest session or machines may change, until a draft is begun or the
/// user changes it, then it stays, so an update never retargets a draft.
public struct DraftConfig: Equatable, Sendable {
    public private(set) var value: NewConfig?
    private var chosen = false
    // What it holds was only a guess (`ProjectStart.settled`): what the project
    // says next replaces it even under a draft, whose text stays.
    private var guess: Bool

    public init(_ initial: NewConfig?, settled: Bool = true) {
        value = initial
        guess = !settled
    }

    /// A new suggestion from the project; taken while nothing is written or
    /// chosen, and always over a guess: a guess is never the user's choice,
    /// even after they chose for another CLI and then switched to this one.
    /// `settled`: whether the suggestion is more than a guess itself.
    public mutating func follow(_ initial: NewConfig?, drafting: Bool, settled: Bool = true) {
        if value == nil || guess || (!chosen && !drafting) {
            value = initial
            guess = !settled
        }
    }

    /// Another CLI for this draft: taken at once, written or not, and still following the project.
    public mutating func retarget(_ c: NewConfig, settled: Bool = true) {
        value = c
        guess = !settled
    }

    /// The user's own choice, kept from now on.
    public mutating func choose(_ c: NewConfig) {
        value = c
        chosen = true
        guess = false
    }
}

/// The configuration a project's page describes under its title: the one ↑
/// starts the next session with. That is the draft's own (`DraftConfig`),
/// which stops following the project once something is written or a machine
/// is picked for this start, so what the page says and what it sends are
/// one thing. `project` is what the project's settings say now; it stands
/// in while there is no draft, and for a project removed from the list,
/// whose page has no message box to keep a draft's configuration current.
public func launchConfig(draft: NewConfig?, project: NewConfig?, hidden: Bool) -> NewConfig? {
    hidden ? project : draft ?? project
}

/// Mode sent for a new session; unspecified lets the daemon use the CLI configuration.
public enum LaunchMode: Equatable, Sendable {
    /// Start in this mode; unspecified leaves it to the CLI's default, which the daemon checks again in the folder.
    case send(PBApprovalMode)
}

/// Resolves the mode of a start. A chosen mode is sent as it is. Without one
/// the CLI's effective default on the machine decides (AgentInfo): full
/// access or an unknown default is left to the daemon without a dialog.
public func launchMode(chosen: Mode?, default d: DefaultMode) -> LaunchMode { .send(chosen?.proto ?? .unspecified) }

/// The session a start created, or nil when it did not.
public func startedSession(_ r: PBCommandResult?) -> String? {
    guard let r, case .createSession(let c) = r.kind, c.hasSession, !c.session.id.isEmpty else { return nil }
    return c.session.id
}

private let summaryMachineMax = 16

/// The machine as the summary names it: only where there is more than one to mean, and cut short when long, so it is the name that gives way and not the model after it.
public func summaryMachine(_ state: RemoteState, id: String) -> String {
    if state.machines.count <= 1 { return "" }
    let name = machineLabel(state, id: id)
    return name.jvmLength > summaryMachineMax ? name.jvmTake(summaryMachineMax - 1) + "…" : name
}

/// Mode, machine and model; the effort and the context window are in the project's settings.
public func projectSummary(_ state: RemoteState, _ c: NewConfig, models: PBListModelsResponse? = nil) -> String {
    let d = defaultModeOf(state.machines[c.machineId], agent: c.agent)
    let mode = effectiveModeText(chosen: c.mode, default: d)
    let modelId = c.model.ifEmpty(models?.defaultModel ?? "")
    let chosen = models.flatMap { findModel($0.models, id: modelId) { $0.slug } }
    let model = chosen.map { $0.displayName.ifEmpty(modelId) } ?? modelId.ifEmpty("默认模型")
    return [agentName(c.agent), mode, summaryMachine(state, id: c.machineId), model].filter { !$0.isEmpty }.joined(separator: " · ")
}

public func effectiveModeText(chosen: Mode?, default d: DefaultMode) -> String {
    if let chosen { return chosen.text }
    if !d.known { return unknownModeText }
    return d.mode?.text ?? "CLI 自己的模式"
}

/// Another CLI for a draft (client-v26): the text and images stay, and the
/// draft takes the project's set for that CLI (`project`). On a machine
/// picked just for this start the model and effort are that machine's defaults.
public func switchAgent(_ c: NewConfig, project: NewConfig?, agent: PBAgent) -> NewConfig {
    var set = project.flatMap { $0.agent == agent ? $0 : nil } ?? NewConfig(machineId: c.machineId, cwd: c.cwd, agent: agent)
    if set.machineId != c.machineId {
        set.machineId = c.machineId
        set.model = ""
        set.effort = ""
    }
    return set
}

/// Another machine for a draft: the model and effort were that machine's
/// list, so they go, and the mode is that machine's default again. The CLI
/// stays when the machine has it; otherwise the caller must have the user
/// pick one (`machineAgents`), never silently. `agents` are that machine's
/// and never empty.
public func switchMachine(_ c: NewConfig, machineId: String, agents: [PBAgent]) -> NewConfig {
    var out = c
    out.machineId = machineId
    out.model = ""
    out.effort = ""
    out.mode = nil
    out.agent = agents.contains(c.agent) ? c.agent : agents.first ?? .codex
    return out
}

/// The two lines of the configuration: machine and mode, then model and effort, neither cut.
public func configLines(_ state: RemoteState, _ c: NewConfig, default d: DefaultMode) -> (String, String) {
    (
        "\(machineLabel(state, id: c.machineId)) · \(modeLabel(chosen: c.mode, default: d, agent: c.agent))",
        [c.model, c.effort].filter { !$0.isEmpty }.joined(separator: " · ").ifEmpty("默认模型")
    )
}

/// The model a new session would run: the one chosen, else the daemon's default for that CLI, else the list's.
public func projectModel(_ c: NewConfig, models: PBListModelsResponse, settings: PBDaemonSettings? = nil) -> PBModelInfo? {
    let defaultModel = (c.agent == .claude ? settings?.claude.model : settings?.defaultModel) ?? ""
    let id = c.model.ifEmpty(defaultModel.ifEmpty(models.defaultModel))
    return findModel(models.models, id: id) { $0.slug }
}
