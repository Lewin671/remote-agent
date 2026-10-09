import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core OrderTest.
@Suite struct OrderTests {
    private func s(_ id: String, _ created: Int64, _ updated: Int64, _ extra: (inout PBSession) -> Void = { _ in }) -> SessionView {
        Mk.session(id, updatedMs: updated) {
            $0.createdAt = Mk.ts(created)
            extra(&$0)
        }
    }

    private func with(_ s: SessionView, updatedMs: Int64? = nil, _ change: (inout PBSession) -> Void = { _ in }) -> SessionView {
        var out = s
        if let updatedMs { out.updatedMs = updatedMs }
        var info = s.info!
        change(&info)
        out.info = info
        return out
    }

    private func keys(_ list: SessionView...) -> [String] { projects(Mk.state(sessions: list)).map(\.key) }

    @Test func outputAndStatusDoNotMoveLiveSessions() {
        let a = s("a", 1000, 99999) { $0.status = .running }
        let b = s("b", 2000, 1) { $0.status = .idle }
        #expect([a, b].sorted(by: liveOrder).map(\.id) == ["b", "a"])
        #expect([with(a, updatedMs: 999999), b].sorted(by: liveOrder).map(\.id) == ["b", "a"])
    }

    @Test func attentionUsesTypeThenOldestWait() {
        let a = s("a", 100, 99999) { $0.pendingApprovals = 1; $0.attentionSince = Mk.ts(2000) }
        let b = s("b", 200, 1) { $0.pendingApprovals = 1; $0.attentionSince = Mk.ts(1000) }
        let q = s("q", 1, 999999) { $0.pendingQuestions = 1; $0.attentionSince = Mk.ts(1) }
        #expect([q, a, b].sorted(by: attentionOrder(RemoteState())).map(\.id) == ["b", "a", "q"])
    }

    @Test func exitAndLegacyTimesIgnoreRelayTime() {
        let a = s("a", 100, 99999) { $0.endedAt = Mk.ts(1000) }
        let b = s("b", 200, 1) { $0.endedAt = Mk.ts(2000) }
        #expect([a, b].sorted(by: endedOrder).map(\.id) == ["b", "a"])
        let oldA = s("a", 100, 99999)
        let oldB = s("b", 100, 1)
        #expect([oldB, oldA].sorted(by: endedOrder).map(\.id) == ["a", "b"])
        #expect([oldB, oldA].sorted(by: liveOrder).map(\.id) == ["a", "b"])
    }

    @Test func projectPositionsIgnoreOutputExitAndReloadButFollowNewSessions() {
        let a = s("a", 1000, 99999) { $0.cwd = "/a" }
        let b = s("b", 2000, 1) { $0.cwd = "/b" }
        #expect(keys(a, b) == ["/b", "/a"])
        let changed = with(a, updatedMs: 999999) { $0.endedAt = Mk.ts(5000); $0.pendingApprovals = 1 }
        #expect(keys(b, changed) == ["/b", "/a"])
        let only = projects(Mk.state(sessions: [changed]))
        #expect(only.count == 1 && only[0].updatedMs == 999999)
        #expect(keys(b, changed, s("new", 3000, 0) { $0.cwd = "/a" }) == ["/a", "/b"])
    }

    @Test func projectTiesAndLegacyMetadataUsePathsInsteadOfUploadTimes() {
        let a = s("z", 1000, 99999) { $0.cwd = "/a" }
        let b = s("a", 1000, 1) { $0.cwd = "/b" }
        #expect(keys(b, a) == ["/a", "/b"])
        #expect(keys(with(b) { $0.clearCreatedAt() }, with(a) { $0.clearCreatedAt() }) == ["/a", "/b"])
    }

    @Test func liveProjectsFirstUntilTheirLastLiveSessionExits() {
        let ended = s("ended", 9000, 999999) { $0.cwd = "/ended"; $0.status = .exited }
        var a = s("a", 1000, 0) { $0.cwd = "/a"; $0.status = .running }
        var older = s("older", 500, 0) { $0.cwd = "/a"; $0.status = .idle }
        var b = s("b", 2000, 0) { $0.cwd = "/b"; $0.status = .idle }
        func order() -> [String] { keys(ended, a, older, b) }
        #expect(order() == ["/b", "/a", "/ended"])
        for status in [PBSessionStatus.starting, .idle, .blocked] {
            b = with(b) { $0.status = status; $0.pendingApprovals = 1 }
            #expect(order() == ["/b", "/a", "/ended"])
        }
        a = with(a) { $0.status = .exited }
        #expect(order() == ["/b", "/a", "/ended"])
        older = with(older) { $0.status = .exited }
        #expect(order() == ["/b", "/ended", "/a"])
        b = with(b) { $0.status = .exited }
        #expect(order() == ["/ended", "/b", "/a"])
    }

    // --- not in Android's tests ---

    @Test func idsAndPathsBreakTiesByUtf16UnitAsOnTheJvm() {
        // U+FF5E sorts after an emoji in UTF-16 (the emoji's units start at D83D), before it by code point, which is Swift's order.
        let emoji = s("😀", 100, 0)
        let wide = s("～", 100, 0)
        #expect("～" < "😀")
        #expect([wide, emoji].sorted(by: liveOrder).map(\.id) == ["😀", "～"])
        #expect([wide, emoji].sorted(by: endedOrder).map(\.id) == ["😀", "～"])
        let p = s("1", 100, 0) { $0.cwd = "/～" }
        let q = s("2", 100, 0) { $0.cwd = "/😀" }
        #expect(keys(p, q) == ["/😀", "/～"])
    }

    @Test func theListingPutsSessionsThatGoOnBeforeEndedOnes() {
        let live = s("live", 10, 0) { $0.status = .idle }
        let newer = s("newer", 20, 0) { $0.status = .running }
        let done = s("done", 99, 0) { $0.status = .exited; $0.endedAt = Mk.ts(500) }
        let later = s("later", 1, 0) { $0.status = .exited; $0.endedAt = Mk.ts(900) }
        #expect([done, live, later, newer].sorted(by: sessionOrder).map(\.id) == ["newer", "live", "later", "done"])
    }

    @Test func aProjectIsKnownFromSummariesSessionsAndSettingsAlike() {
        let m = MachineView(
            id: "m", online: true, lastSeenMs: 0,
            meta: .with {
                $0.hostname = "mac.local"
                $0.projects = [.with { $0.path = "/old"; $0.ended = 3; $0.latestCreatedMs = 50; $0.latestEndedMs = 70 }]
            })
        let n = MachineView(
            id: "n", online: true, lastSeenMs: 0,
            meta: .with { $0.projects = [.with { $0.path = "/old"; $0.ended = 2; $0.latestCreatedMs = 60 }] })
        let prefs = PBProjectPrefs.with {
            $0.settings = [
                .with { $0.path = "/added"; $0.machineID = "m"; $0.updatedMs = 40 },
                .with { $0.path = "/elsewhere"; $0.machineID = "gone"; $0.updatedMs = 99 },
            ]
        }
        let held = s("x", 10, 5) { $0.cwd = "/held"; $0.status = .exited }
        let st = Mk.state(sessions: [held], machines: [m, n], prefs: prefs)
        let all = projects(st)
        // No live session anywhere: the latest creation first. A project whose machine is not the account's is not listed.
        #expect(all.map(\.key) == ["/old", "/added", "/held"])
        let old = all[0]
        // The machine with the newest session there first.
        #expect(old.machineIds == ["n", "m"])
        #expect(old.machines == ["n", "mac"])
        #expect(old.ended == 5)
        #expect(old.updatedMs == 70 && old.latestCreatedMs == 60)
        #expect(old.paths == ["/old"])
        #expect(all[1].machineIds == ["m"] && all[1].latestCreatedMs == 40)
        #expect(all[2].ended == 1)
        #expect(machineProjects(st, machineId: "m") == ["/old", "/held"])
    }
}
