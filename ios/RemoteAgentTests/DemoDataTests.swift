#if DEBUG
import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

@MainActor
@Suite struct DemoDataTests {
    let now: Int64 = 1_791_000_000_000

    @Test func everydayIsTheMockupsAccount() {
        let s = Demo.state(.everyday, now: now)
        #expect(s.machines.count == 3)
        #expect(s.machines["imac"]?.online == false)
        #expect(s.sessions.count == 10)
        #expect(s.devices[Demo.me]?.kind == .client)
        #expect(s.link == .online && s.loaded && s.synced && !s.removed)
        // One of each thing that waits, on computers that are online.
        let waiting = s.sessions.values.filter { needsAttention($0, machine: s.machines[$0.machineId]) }.map(\.id).sorted()
        #expect(waiting == ["api", "plan", "trust"])
        #expect(s.prefs?.pinned == [Demo.foo])
        #expect(!quotaGroups(s.machines["mac"]!, now: now).isEmpty)
    }

    @Test func crowdedHasManyOfEverythingAndStatesThatOverlap() throws {
        let s = Demo.state(.crowded, now: now)
        #expect(s.machines.count >= 6)
        #expect(s.sessions.count >= 80)
        #expect(s.devices.count >= 12)
        #expect(Set(s.sessions.values.compactMap(\.info?.cwd)).count >= 10)
        let all = try #require(s.sessions["all"]?.info)
        #expect(all.pendingApprovals > 1 && all.pendingQuestions > 1 && all.planOpen && all.title.count > 40)
        // Sessions wait on a computer that is offline, some have no title, and more can be fetched.
        #expect(s.sessions.values.contains { $0.info?.pendingApprovals ?? 0 > 0 && s.machines[$0.machineId]?.online == false })
        #expect(s.sessions.values.contains { ($0.info?.title ?? "").isEmpty })
        #expect(s.moreSessions)
        #expect(s.machines.values.contains { ($0.meta?.hostname ?? "").isEmpty })
    }

    @Test func emptyIsAnAccountWithOnlyThisClient() {
        let s = Demo.state(.empty, now: now)
        #expect(s.machines.isEmpty && s.sessions.isEmpty)
        #expect(Array(s.devices.keys) == [Demo.me])
        #expect(s.prefs == .empty)
        #expect(Demo.events(.empty, now: now).isEmpty)
    }

    @Test(arguments: DemoSet.allCases)
    func historiesBelongToSessionsAndAreNumberedFromOne(set: DemoSet) {
        let s = Demo.state(set, now: now)
        for (sid, events) in Demo.events(set, now: now) {
            #expect(s.sessions[sid] != nil, "\(sid)")
            #expect(events.map(\.seq) == Array(1...UInt64(events.count)), "\(sid)")
            #expect(events.allSatisfy { $0.kind != nil })
        }
    }

    @Test(arguments: DemoSet.allCases)
    func aRemoteShowsASetWithoutAServer(set: DemoSet) async {
        let remote = Demo.remote()
        let state = Demo.state(set, now: now)
        let events = Demo.events(set, now: now)
        await remote.showForTest(state: state, events: events, modelLists: Demo.models)
        #expect(remote.current == state)
        for (sid, list) in events {
            let shown = remote.events(sid).value
            #expect(shown.loaded && shown.events.count == list.count)
        }
        let model = AppModel(settings: SettingsStore(defaults: UserDefaults(suiteName: "demo-\(UUID().uuidString)")!))
        model.showDemo(account: Demo.account, remote: remote, initial: state) {}
        #expect(model.stage == .ready && model.isDemo && model.state == state)
        #expect(model.context?.me == Demo.me)
        // Leaving sample data stores and erases nothing.
        await model.logout()
        #expect(model.stage == .join)
    }
}
#endif
