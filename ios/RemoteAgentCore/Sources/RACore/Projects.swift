// The account's projects (architecture §8): where they come from, how the
// lists arrange them, and how a project and a computer are named. From
// Android's core/Model.kt.
import Foundation
import RAProto

public struct Project: Equatable, Sendable, Identifiable {
    public var key: String
    public var name: String
    /// The project's sessions held here, recent activity first, used for new-session defaults. UI lists sort separately.
    public var sessions: [SessionView]
    public var updatedMs: Int64
    public var hasLive: Bool
    public var latestCreatedMs: Int64
    public var paths: [String]
    public var machines: [String]
    /// The machines it has sessions on: those of the sessions held here, recent first, then the others whose summary names it.
    public var machineIds: [String]
    /// How many of its sessions have ended, on all machines: by their summaries, and no fewer than are held here.
    public var ended: Int

    /// The key by its code units: the identity Kotlin gives a path.
    public var id: JvmKey { JvmKey(key) }

    public init(
        key: String, name: String, sessions: [SessionView], updatedMs: Int64, hasLive: Bool, latestCreatedMs: Int64,
        paths: [String], machines: [String], machineIds: [String] = [], ended: Int = 0
    ) {
        self.key = key
        self.name = name
        self.sessions = sessions
        self.updatedMs = updatedMs
        self.hasLive = hasLive
        self.latestCreatedMs = latestCreatedMs
        self.paths = paths
        self.machines = machines
        self.machineIds = machineIds
        self.ended = ended
    }
}

/// The project a session belongs to: its working directory's absolute path, the same on every machine.
public func projectKey(_ s: SessionView) -> String { s.info?.cwd ?? "" }

/// A project's name: the last folder of its path.
public func projectName(_ key: String) -> String {
    key.split(ascii: UInt8(ascii: "/")).last { !$0.isEmpty } ?? "未知项目"
}

/// What the machines say of a project (MachineMeta.projects), added up.
private struct ProjectSums {
    var endedBy = [String: Int]()
    var latestCreatedMs: Int64 = 0
    var latestEndedMs: Int64 = 0
    var machineIds = [String]()
}

/// The account's projects (architecture §8 「项目从哪来」): the machines'
/// project summaries together with the sessions held here. Sessions come a
/// page at a time, so a project whose sessions all ended long ago is known
/// from the summaries alone. Whether one has a session that has not ended is
/// read off the sessions, which are all held; the latest creation is the
/// later of the two; its machines are those of both. A project added with
/// 新项目… is known from its settings alone until its first session: it is
/// listed while the machine chosen for it is one of the account's, as created
/// when it was added.
public func projects(_ state: RemoteState) -> [Project] {
    var byKey = [JvmKey: [SessionView]]()
    for s in state.sessions.values where s.info?.taskOwned != true { byKey[JvmKey(projectKey(s)), default: []].append(s) }
    var sums = [JvmKey: ProjectSums]()
    // The machine whose newest session there is the latest comes first. A
    // machine's own summaries keep the order it listed them in.
    let summaries = state.machines.values.sorted { jvmLess($0.id, $1.id) }
        .flatMap { m in (m.meta?.projects ?? []).map { (machineId: m.id, summary: $0) } }
        .stableSorted { a, b in
            if a.summary.latestCreatedMs != b.summary.latestCreatedMs { return a.summary.latestCreatedMs > b.summary.latestCreatedMs }
            return jvmLess(a.machineId, b.machineId)
        }
    for (machineId, p) in summaries {
        var sum = sums[JvmKey(p.path)] ?? ProjectSums()
        sum.endedBy[machineId] = Int(p.ended)
        sum.latestCreatedMs = max(sum.latestCreatedMs, p.latestCreatedMs)
        sum.latestEndedMs = max(sum.latestEndedMs, p.latestEndedMs)
        if !sum.machineIds.contains(machineId) { sum.machineIds.append(machineId) }
        sums[JvmKey(p.path)] = sum
    }
    var added = [JvmKey: PBProjectSettings]()
    for s in state.prefs?.prefs.settings ?? [] {
        let key = JvmKey(s.path)
        if !s.path.isEmpty && byKey[key] == nil && sums[key] == nil && state.machines[s.machineID] != nil { added[key] = s }
    }
    let keys = (Array(byKey.keys) + Array(sums.keys) + Array(added.keys)).distinct()
    return keys.map { id -> Project in
        let key = id.text
        let sorted = (byKey[id] ?? []).sorted { a, b in
            a.updatedMs != b.updatedMs ? a.updatedMs > b.updatedMs : jvmLess(a.id, b.id)
        }
        let sum = sums[id]
        let own = added[id]
        let machineIds = (sorted.map(\.machineId) + (sum?.machineIds ?? []) + (own.map { [$0.machineID] } ?? [])).distinct()
        var paths = sorted.compactMap { s -> String? in
            guard let c = s.info?.cwd, !c.isEmpty else { return nil }
            return c
        }.distinct()
        if paths.isEmpty { paths = [key].filter { !$0.isEmpty } }
        return Project(
            key: key,
            name: projectName(key),
            sessions: sorted,
            updatedMs: max(sorted.map(\.updatedMs).max() ?? 0, sum?.latestCreatedMs ?? 0, sum?.latestEndedMs ?? 0, own?.updatedMs ?? 0),
            hasLive: sorted.contains { !exited($0) },
            latestCreatedMs: max(sorted.map(createdMs).max() ?? 0, sum?.latestCreatedMs ?? 0, own?.updatedMs ?? 0),
            paths: paths,
            machines: machineIds.map { machineName(state.machines[$0], id: $0) }.distinct(),
            machineIds: machineIds,
            // Counted machine by machine: each machine's count, or what is held of its
            // sessions where that is more (its metadata may not have arrived, or be behind).
            ended: machineIds.reduce(0) { n, m in
                n + max(sum?.endedBy[m] ?? 0, sorted.filter { $0.machineId == m && exited($0) }.count)
            }
        )
    }.sorted { a, b in
        if a.hasLive != b.hasLive { return a.hasLive }
        if a.latestCreatedMs != b.latestCreatedMs { return a.latestCreatedMs > b.latestCreatedMs }
        return jvmLess(a.key, b.key)
    }
}

/// One project of `projects`, or nil when no summary, session held here or stored settings name it.
public func project(_ state: RemoteState, key: String) -> Project? { projects(state).first { $0.key.jvmEquals(key) } }

/// The projects a machine has sessions in: by its summary and by the
/// sessions held here, in that order. The sessions are gone through by ID,
/// their dictionary having no order to go by.
public func machineProjects(_ state: RemoteState, machineId: String) -> [String] {
    let summed = (state.machines[machineId]?.meta?.projects ?? []).map(\.path)
    let held = state.sessions.values.filter { $0.machineId == machineId }.sorted { jvmLess($0.id, $1.id) }
        .compactMap { $0.info?.cwd }
    return (summed + held).distinct()
}

/// The project list as the account arranges it (architecture §8): pinned
/// projects first in the order they were pinned, the others with live
/// projects first, then by latest session creation; hidden ones apart. Hiding only takes a project off these lists:
/// its sessions, pending items and notifications are not filtered here.
public struct ProjectGroups: Equatable, Sendable {
    public var pinned: [Project]
    public var others: [Project]
    public var hidden: [Project]

    public init(pinned: [Project], others: [Project], hidden: [Project]) {
        self.pinned = pinned
        self.others = others
        self.hidden = hidden
    }
}

public func projectGroups(_ state: RemoteState) -> ProjectGroups {
    let all = projects(state)
    let prefs = state.prefs ?? .empty
    let hidden = prefs.hidden
    var byKey = [JvmKey: Project]()
    for p in all { byKey[p.id] = p }
    let pinned = prefs.pinned.filter { !hidden.contains($0) }.compactMap { byKey[$0] }
    let pinnedKeys = Set(pinned.map(\.id))
    return ProjectGroups(
        pinned: pinned,
        others: all.filter { !pinnedKeys.contains($0.id) && !hidden.contains($0.id) },
        hidden: all.filter { hidden.contains($0.id) }
    )
}

public func isHidden(_ state: RemoteState, key: String) -> Bool { state.prefs?.hidden.contains(JvmKey(key)) == true }

public func isPinned(_ state: RemoteState, key: String) -> Bool {
    guard let p = state.prefs else { return false }
    return p.pinned.contains(JvmKey(key)) && !p.hidden.contains(JvmKey(key))
}

/// A computer's name: its host name without .local, else the start of its ID.
public func machineName(_ m: MachineView?, id: String = "") -> String {
    if let host = m?.meta?.hostname.removingSuffix(".local"), !host.isEmpty { return host }
    if let m { return m.id.jvmTake(8) }
    return id.jvmTake(8).ifEmpty("未知电脑")
}

/// Avatar color index 0..3, stable per project.
public func avatarIndex(_ key: String) -> Int {
    var h: UInt64 = 0
    for c in key.utf16 { h = (h &* 31 &+ UInt64(c)) & 0xffff_ffff }
    return Int(h % 4)
}
