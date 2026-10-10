import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct PrefsTests {
    private let codec = PrefsCodec(key: Data(repeating: 7, count: 32), sender: "dev")

    /// The server's compare-and-set, as internal/server does it.
    private final class FakeServer: @unchecked Sendable {
        var obj: PBAccountPrefsObject = {
            var o = PBAccountPrefsObject()
            o.objectID = projectPrefsID
            return o
        }()
        var loseNextReply = false

        func put(_ expected: UInt64, _ env: PBEnvelope) throws -> PutResult {
            if expected != obj.revision { return PutResult(applied: false, current: obj) }
            var next = PBAccountPrefsObject()
            next.objectID = projectPrefsID
            next.revision = expected + 1
            next.envelope = env
            obj = next
            if loseNextReply {
                loseNextReply = false
                throw URLError(.timedOut)
            }
            return PutResult(applied: true, current: obj)
        }
    }

    private func write(_ server: FakeServer, _ base: PrefsObject, _ op: PrefsOp, _ id: String) async throws -> PrefsObject {
        try await writePrefsOp(
            codec: codec, base: base, op: op, opId: id, now: 1000, put: { try server.put($0, $1) }, get: { server.obj })
    }

    private func set(_ agent: PBAgent, _ model: String = "", _ mode: PBApprovalMode = .unspecified) -> PBAgentSettings {
        var s = PBAgentSettings()
        s.agent = agent
        s.model = model
        s.mode = mode
        return s
    }

    private func settings(_ path: String, _ machine: String, _ at: Int64, agents: [PBAgentSettings] = []) -> PBProjectSettings {
        var s = PBProjectSettings()
        s.path = path
        s.machineID = machine
        s.updatedMs = at
        s.agents = agents
        return s
    }

    @Test func operationEqualityKeepsCodeUnitDistinctPathsSeparate() {
        let a = "/work/caf\u{e9}"
        let b = "/work/cafe\u{301}"
        #expect(a == b && !a.jvmEquals(b))
        let left: [PrefsOp] = [.pin(path: a), .unpin(path: a), .hide(path: a), .unhide(path: a), .setSettings(.init(path: a, machineId: "mac"))]
        let right: [PrefsOp] = [.pin(path: b), .unpin(path: b), .hide(path: b), .unhide(path: b), .setSettings(.init(path: b, machineId: "mac"))]
        for (x, y) in zip(left, right) {
            #expect(x != y)
            #expect(x == x)
        }
        #expect(PrefsOp.SetSettings(path: a, machineId: "mac") != PrefsOp.SetSettings(path: b, machineId: "mac"))
        #expect(PrefsOp.pin(path: a) != .hide(path: a))
        #expect(PrefsOp.setSettings(.init(path: a, machineId: "mac")) != .setSettings(.init(path: a, machineId: "vps")))
    }

    @Test func aConflictRedoesTheOperationAndKeepsTheOtherClientsChange() async throws {
        let server = FakeServer()
        let a = try await write(server, .empty, .pin(path: "/p/foo"), "op1")
        #expect(a.pinned == ["/p/foo"])
        // Another client, still at revision 0, hides a project.
        let b = try await write(server, .empty, .hide(path: "/p/billing"), "op2")
        #expect(b.revision == 2)
        #expect(b.pinned == ["/p/foo"])
        #expect(b.hidden == ["/p/billing"])
        #expect(try codec.open(server.obj) == b)
    }

    @Test func settingsConflictRedoesOneProjectAndPreservesOtherSettingsAndUnknownFields() async throws {
        let server = FakeServer()
        let first = settings("/a", "mac", 1000)
        let second = settings("/b", "vps", 1000)
        _ = try await write(server, .empty, .setSettings(.init(path: "/a", machineId: "mac")), "s1")
        let after = try await write(server, .empty, .setSettings(.init(path: "/b", machineId: "vps")), "s2")
        #expect(after.prefs.settings == [first, second])
        let updated = settings("/a", "mac", 1000, agents: [set(.codex, "new-model")])
        let again = try await write(
            server, after, .setSettings(.init(path: "/a", machineId: "mac", sets: [set(.codex, "new-model")])), "s3")
        #expect(again.prefs.settings == [second, updated])
        let unknown = Data([0x9a, 0x06, 0x01, 0x51])
        let newer = try PBProjectPrefs(serializedBytes: try again.prefs.serializedData() + unknown)
        let pinned = applyPrefsOp(base: newer, op: .pin(path: "/c"), opId: "s4", now: 1000)
        let bytes: Data = try pinned.serializedData()
        #expect(bytes.range(of: unknown) != nil)
        #expect(pinned.settings == [second, updated])
    }

    @Test func aSettingsChangeRedoneOnANewerObjectKeepsWhatTheOtherClientChanged() async throws {
        let server = FakeServer()
        let base = try await write(
            server, .empty, .setSettings(.init(path: "/a", machineId: "mac", agent: .codex, sets: [set(.codex, "astra")])), "s1")
        // Another client edits Claude Code's set and a session there remembers Claude Code.
        _ = try await write(
            server, base, .setSettings(.init(path: "/a", machineId: "mac", agent: .claude, sets: [set(.claude, "sonnet")])), "s2")
        // This client, still on the older object, edits Codex's set only.
        let mine = try await write(
            server, base, .setSettings(.init(path: "/a", machineId: "other", sets: [set(.codex, "sol", .ask)])), "s3")
        #expect(mine.prefs.settings.count == 1)
        let s = try #require(mine.prefs.settings.first)
        #expect(s.machineID == "mac")
        #expect(s.agent == .claude)
        #expect(s.agents == [set(.codex, "sol", .ask), set(.claude, "sonnet")])
    }

    @Test func aStartFillsInOnlyWhatIsMissingAndAnotherMachineStartsEverySetOver() {
        let stored = applySettings(
            stored: nil, op: .init(path: "/a", machineId: "mac", agent: .codex, sets: [set(.codex, "astra")], keepExisting: true),
            now: 1)
        #expect(stored.machineID == "mac")
        #expect(stored.agents == [set(.codex, "astra")])
        // A later start with the other CLI: its set is added, the machine and Codex's set stay.
        let next = applySettings(
            stored: stored,
            op: .init(
                path: "/a", machineId: "vps", agent: .claude, sets: [set(.claude, "sonnet"), set(.codex, "other")],
                keepExisting: true),
            now: 2)
        #expect(next.machineID == "mac")
        #expect(next.agent == .claude)
        #expect(next.agents == [set(.codex, "astra"), set(.claude, "sonnet")])
        #expect(next.updatedMs == 2)
        // Another machine: both CLIs get an empty entry, the remembered CLI stays.
        let moved = applySettings(stored: next, op: .init(path: "/a", machineId: "vps", moved: true), now: 3)
        #expect(moved.machineID == "vps")
        #expect(moved.agent == .claude)
        #expect(moved.agents == [set(.codex), set(.claude)])
        // A set edited together with the machine is kept.
        let both = applySettings(
            stored: next, op: .init(path: "/a", machineId: "vps", moved: true, sets: [set(.claude, "opus")]), now: 3)
        #expect(both.agents == [set(.codex), set(.claude, "opus")])
    }

    @Test func pinsKeepTheirOrderAndHidingUnpins() {
        var p = PBProjectPrefs()
        p = applyPrefsOp(base: p, op: .pin(path: "/b"), opId: "1", now: 1)
        p = applyPrefsOp(base: p, op: .pin(path: "/a"), opId: "2", now: 2)
        p = applyPrefsOp(base: p, op: .pin(path: "/b"), opId: "3", now: 3)
        #expect(p.pinned.map(\.path) == ["/b", "/a"])
        p = applyPrefsOp(base: p, op: .hide(path: "/b"), opId: "4", now: 4)
        #expect(p.pinned.map(\.path) == ["/a"])
        p = applyPrefsOp(base: p, op: .unhide(path: "/b"), opId: "5", now: 5)
        // Restored among the others, not pinned again.
        #expect(p.pinned.map(\.path) == ["/a"])
        #expect(p.hidden.isEmpty)
    }

    // Two folders a Linux machine can have side by side: é as one character, and as e with a combining accent.
    private let composed = "/work/caf\u{e9}"
    private let decomposed = "/work/cafe\u{301}"

    private func units(_ paths: [String]) -> [[UInt8]] { paths.map { Array($0.utf8) } }

    @Test func pathsThatAreOnlyCanonicallyEqualAreDifferentProjects() {
        var p = PBProjectPrefs()
        p.settings = [settings(composed, "mac", 1), settings(decomposed, "vps", 1)]
        p = applyPrefsOp(base: p, op: .setSettings(.init(path: decomposed, machineId: "imac", moved: true)), opId: "1", now: 2)
        // The other folder's settings stay as they were.
        #expect(units(p.settings.map(\.path)) == units([composed, decomposed]))
        #expect(p.settings.map(\.machineID) == ["mac", "imac"])

        p = applyPrefsOp(base: p, op: .pin(path: composed), opId: "2", now: 3)
        p = applyPrefsOp(base: p, op: .pin(path: decomposed), opId: "3", now: 4)
        #expect(units(p.pinned.map(\.path)) == units([composed, decomposed]))
        p = applyPrefsOp(base: p, op: .hide(path: composed), opId: "4", now: 5)
        #expect(units(p.pinned.map(\.path)) == units([decomposed]))
        #expect(units(p.hidden.map(\.path)) == units([composed]))
        p = applyPrefsOp(base: p, op: .unpin(path: composed), opId: "5", now: 6)
        #expect(units(p.pinned.map(\.path)) == units([decomposed]))
        p = applyPrefsOp(base: p, op: .unhide(path: decomposed), opId: "6", now: 7)
        #expect(units(p.hidden.map(\.path)) == units([composed]))
    }

    @Test func aLostReplyIsSettledByTheOpId() async throws {
        let server = FakeServer()
        server.loseNextReply = true
        let r = try await write(server, .empty, .pin(path: "/p/foo"), "op1")
        #expect(r.revision == 1)
        // Redoing it finds it in and writes nothing more.
        let again = try await write(server, .empty, .pin(path: "/p/foo"), "op1")
        #expect(again.revision == 1)
        #expect(server.obj.revision == 1)
    }

    @Test func recentOpsAreBounded() {
        var p = PBProjectPrefs()
        for i in 0..<100 { p = applyPrefsOp(base: p, op: .pin(path: "/x\(i)"), opId: "op\(i)", now: 1) }
        #expect(p.recentOps.count == recentOps)
        #expect(p.recentOps.last == "op99")
    }

    @Test func aRevisionTheEnvelopeDoesNotSayIsRefused() throws {
        var lie = PBAccountPrefsObject()
        lie.objectID = projectPrefsID
        lie.revision = 4
        lie.envelope = try codec.seal(revision: 3, prefs: PBProjectPrefs())
        #expect(throws: OpenError.self) { try codec.open(lie) }
    }

    @Test func aPushNeverTakesTheListBack() {
        let newer = PrefsObject(revision: 5, prefs: PBProjectPrefs())
        #expect(newerPrefs(have: newer, got: PrefsObject(revision: 4, prefs: PBProjectPrefs())) == newer)
        #expect(newerPrefs(have: nil, got: newer) == newer)
    }
}
