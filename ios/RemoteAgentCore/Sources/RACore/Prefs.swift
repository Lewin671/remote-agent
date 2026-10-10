// The account's project preferences (architecture §8, 项目列表偏好):
// pinned and hidden projects, sealed with the account-prefs key and
// replaced on the server only if its revision is still the one a change is
// based on. A conflict redoes the one operation on the newer object, so
// what other clients changed meanwhile stays.
import Foundation
import RAProto

public let projectPrefsID = "project-prefs"
public let recentOps = 64

/// One change to the project preferences. Each names the state it leads to, never a toggle, so redoing it on a newer object means the same.
public enum PrefsOp: Equatable, Sendable {
    case pin(path: String)
    case unpin(path: String)
    case hide(path: String)
    case unhide(path: String)
    /// A change to one project's settings (architecture §13). It names only
    /// what changes, so redone on a newer object the rest stays as another
    /// client left it.
    case setSettings(SetSettings)

    public struct SetSettings: Equatable, Sendable {
        public var path: String
        /// Stored when the project has no settings yet, or with `moved`.
        public var machineId: String
        /// The user chose another machine: every CLI starts over from that machine's defaults.
        public var moved = false
        /// The CLI a new session just used, or nil to keep the one remembered.
        public var agent: PBAgent?
        /// The CLIs' sets to store, each replacing that CLI's entry.
        public var sets: [PBAgentSettings] = []
        /// A start: `sets` only fill in a CLI that has no entry yet.
        public var keepExisting = false

        public static func == (a: SetSettings, b: SetSettings) -> Bool {
            a.path.jvmEquals(b.path) && a.machineId == b.machineId && a.moved == b.moved &&
                a.agent == b.agent && a.sets == b.sets && a.keepExisting == b.keepExisting
        }

        public init(
            path: String, machineId: String, moved: Bool = false, agent: PBAgent? = nil,
            sets: [PBAgentSettings] = [], keepExisting: Bool = false
        ) {
            self.path = path
            self.machineId = machineId
            self.moved = moved
            self.agent = agent
            self.sets = sets
            self.keepExisting = keepExisting
        }
    }

    public static func == (a: PrefsOp, b: PrefsOp) -> Bool {
        switch (a, b) {
        case (.pin(let x), .pin(let y)), (.unpin(let x), .unpin(let y)),
             (.hide(let x), .hide(let y)), (.unhide(let x), .unhide(let y)):
            return x.jvmEquals(y)
        case (.setSettings(let x), .setSettings(let y)):
            return x == y
        default:
            return false
        }
    }

    public var path: String {
        switch self {
        case .pin(let p), .unpin(let p), .hide(let p), .unhide(let p): return p
        case .setSettings(let s): return s.path
        }
    }
}

/// The CLIs a project keeps a set of settings for.
public let settingsAgents: [PBAgent] = [.codex, .claude]

/// `op` applied to a project's stored settings, or to none.
public func applySettings(stored: PBProjectSettings?, op: PrefsOp.SetSettings, now: Int64) -> PBProjectSettings {
    var b: PBProjectSettings
    if let stored {
        b = stored
    } else {
        b = PBProjectSettings()
        b.path = op.path
        b.machineID = op.machineId
    }
    var sets = [Int: PBAgentSettings]()
    for s in b.agents { sets[s.agent.rawValue] = s }
    if op.moved {
        b.machineID = op.machineId
        // Models are per machine: an empty entry means that machine's defaults.
        for a in settingsAgents {
            var empty = PBAgentSettings()
            empty.agent = a
            sets[a.rawValue] = empty
        }
    }
    if let agent = op.agent { b.agent = agent }
    for s in op.sets where !op.keepExisting || sets[s.agent.rawValue] == nil { sets[s.agent.rawValue] = s }
    b.agents = sets.keys.sorted().compactMap { sets[$0] }
    b.updatedMs = now
    return b
}

/// `op` applied to `base`. Pinning keeps the order projects were pinned in;
/// hiding also unpins, and restoring does not pin again (the project goes
/// back among the others). `opId` is remembered so a client whose reply was
/// lost can tell its change is in.
public func applyPrefsOp(base: PBProjectPrefs, op: PrefsOp, opId: String, now: Int64) -> PBProjectPrefs {
    // A copy keeps fields this client did not change, including unknown
    // protobuf fields written by a newer client.
    var out = base
    func pref(_ p: String) -> PBProjectPref {
        var x = PBProjectPref()
        x.path = p
        x.sinceMs = now
        return x
    }
    switch op {
    case .pin(let path):
        out.hidden.removeAll { $0.path.jvmEquals(path) }
        if !out.pinned.contains(where: { $0.path.jvmEquals(path) }) { out.pinned.append(pref(path)) }
    case .unpin(let path):
        out.pinned.removeAll { $0.path.jvmEquals(path) }
    case .hide(let path):
        out.pinned.removeAll { $0.path.jvmEquals(path) }
        if !out.hidden.contains(where: { $0.path.jvmEquals(path) }) { out.hidden.append(pref(path)) }
    case .unhide(let path):
        out.hidden.removeAll { $0.path.jvmEquals(path) }
    case .setSettings(let s):
        let stored = base.settings.first { $0.path.jvmEquals(s.path) }
        out.settings = base.settings.filter { !$0.path.jvmEquals(s.path) } + [applySettings(stored: stored, op: s, now: now)]
    }
    out.schema = 1
    out.recentOps = Array((base.recentOps + [opId]).suffix(recentOps))
    return out
}

/// The preferences as the account has them, decrypted, at a revision.
public struct PrefsObject: Equatable, Sendable {
    public var revision: UInt64
    public var prefs: PBProjectPrefs

    public init(revision: UInt64, prefs: PBProjectPrefs) {
        self.revision = revision
        self.prefs = prefs
    }

    // Paths by their code units, as Kotlin compares them.
    public var pinned: [JvmKey] { prefs.pinned.map { JvmKey($0.path) } }
    public var hidden: Set<JvmKey> { Set(prefs.hidden.map { JvmKey($0.path) }) }

    public static let empty: PrefsObject = {
        var p = PBProjectPrefs()
        p.schema = 1
        return PrefsObject(revision: 0, prefs: p)
    }()
}

public struct PrefsConflictError: Error, LocalizedError, Equatable {
    public init() {}
    public var errorDescription: String? { "项目偏好一直在被其他客户端修改，没能保存" }
}

/// Seals and opens the prefs object; the header binds its ID and revision.
public struct PrefsCodec: Sendable {
    private let key: Data
    private let sender: String

    public init(key: Data, sender: String) {
        self.key = key
        self.sender = sender
    }

    public func seal(revision: UInt64, prefs: PBProjectPrefs) throws -> PBEnvelope {
        var h = PBHeader()
        h.kind = .accountPrefs
        h.requestID = projectPrefsID
        h.seq = revision
        h.sender = sender
        return try Envelopes.seal(key: key, header: h, message: prefs)
    }

    /// The object's content; its sealed revision must be the one the server says it is.
    public func open(_ o: PBAccountPrefsObject) throws -> PrefsObject {
        if o.revision == 0 || !o.hasEnvelope { return .empty }
        let (h, p) = try Envelopes.openMessage(
            key: key, envelope: o.envelope, want: Place(.accountPrefs, requestId: projectPrefsID),
            as: PBProjectPrefs.self)
        if h.seq != o.revision { throw OpenError("项目偏好的修订号被改动") }
        return PrefsObject(revision: o.revision, prefs: p)
    }
}

/// The server's side of a write: applied, or the object as stored now.
public struct PutResult: Sendable {
    public var applied: Bool
    public var current: PBAccountPrefsObject

    public init(applied: Bool, current: PBAccountPrefsObject) {
        self.applied = applied
        self.current = current
    }
}

/// Writes one operation: seal it on `base`, and on a conflict redo it on
/// the object the server returned. A write whose outcome is unknown (the
/// reply was lost) is settled by reading the object back: its op ID in
/// recent_ops means it went in. Returns what the account has afterwards.
public func writePrefsOp(
    codec: PrefsCodec,
    base: PrefsObject,
    op: PrefsOp,
    opId: String,
    now: Int64,
    put: (_ expected: UInt64, _ envelope: PBEnvelope) async throws -> PutResult,
    get: () async throws -> PBAccountPrefsObject,
    tries: Int = 5
) async throws -> PrefsObject {
    var cur = base
    for _ in 0..<tries {
        if cur.prefs.recentOps.contains(opId) { return cur }
        let next = applyPrefsOp(base: cur.prefs, op: op, opId: opId, now: now)
        let env = try codec.seal(revision: cur.revision + 1, prefs: next)
        let r: PutResult
        do {
            r = try await put(cur.revision, env)
        } catch {
            // Did it go in? Only the stored object can say.
            let stored = try codec.open(try await get())
            if stored.prefs.recentOps.contains(opId) { return stored }
            throw error
        }
        if r.applied { return PrefsObject(revision: cur.revision + 1, prefs: next) }
        let latest = try codec.open(r.current)
        // Never go back to an older revision than the one known here.
        if latest.revision < cur.revision { throw OpenError("服务器返回了更旧的项目偏好") }
        cur = latest
    }
    throw PrefsConflictError()
}

/// Keeps the value with the higher revision: a push or reply never takes the list back.
public func newerPrefs(have: PrefsObject?, got: PrefsObject) -> PrefsObject {
    guard let have, got.revision < have.revision else { return got }
    return have
}
