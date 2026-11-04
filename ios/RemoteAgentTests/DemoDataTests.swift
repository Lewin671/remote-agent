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
        #expect(s.sessions.count == 12)
        #expect(s.devices[Demo.me]?.kind == .client)
        #expect(s.link == .online && s.loaded && s.synced && !s.removed)
        // One of each thing that waits, on computers that are online.
        let waiting = s.sessions.values.filter { needsAttention($0, machine: s.machines[$0.machineId]) }.map(\.id).sorted()
        #expect(waiting == ["api", "plan", "trust"])
        #expect(s.prefs?.pinned == [JvmKey(Demo.foo)])
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
    func everyComputerOfASetListsModelsForAllCLIs(set: DemoSet) {
        for id in Demo.state(set, now: now).machines.keys {
            for agent in [PBAgent.codex, .claude, .antigravity] {
                let list = Demo.models.first { $0.machineId == id && $0.agent == agent }?.models
                #expect(list?.models.isEmpty == false, "\(id) \(agent)")
                #expect(list?.models.contains { $0.slug == list?.defaultModel } == true)
            }
        }
        // Claude Code's have what its settings choose between: efforts, and a window larger than the standard one.
        let claude = Demo.models.first { $0.agent == .claude }?.models.models ?? []
        #expect(claude.contains { !$0.efforts.isEmpty && $0.contextWindow > 200_000 } && claude.contains { $0.efforts.isEmpty })
        let agy = Demo.models.first { $0.agent == .antigravity }?.models.models ?? []
        #expect(!agy.isEmpty && agy.allSatisfy { !$0.autoUnavailable })
    }

    @Test func antigravitySamplesExerciseTheMultiCliScreens() throws {
        let state = Demo.state(.everyday, now: now)
        #expect(machineAgents(state.machines["mac"]) == [.codex, .claude, .antigravity])
        #expect(state.machines.values.contains { machineAgents($0).count == 1 })
        let session = try #require(state.sessions["s-agy"])
        #expect(session.agent == .antigravity && session.info?.fullAccessUnavailable == true)
        #expect(session.info?.contextWindow == 1_048_000)
        #expect(state.sessions["s-agy-full"]?.info?.modeLocked == true)
        let machine = try #require(state.machines["mac"])
        let quota = try #require(quotaGroups(machine, now: now).first { $0.agent == .antigravity })
        #expect(quota.subgroups.count == 2 && quota.rows.count == 5)
        let models = try #require(Demo.models.first { $0.machineId == "mac" && $0.agent == .antigravity }?.models)
        #expect(!allAutoUnavailable(models.models))
        #expect(Set(models.models.map(\.slug)).count == models.models.count)
        #expect(models.models.first { $0.slug == "gpt-oss-120b" }?.efforts == ["medium"])
        #expect(models.models.contains { $0.slug == session.info?.model })
        #expect(SettingsDemo.daemonSettings.antigravity.model == "gemini-3.1-pro")
    }

    @Test func theMadeUpHomeIsListedFolderByFolder() {
        let home = Demo.directory("")
        #expect(home.path == Demo.home && home.home == Demo.home && home.parent == "/Users" && home.dirs.contains("Code"))
        let code = Demo.directory(Demo.home + "/Code")
        #expect(code.parent == Demo.home && code.dirs.contains("foo"))
        // A folder with nothing in it, and the root, which has nothing above it.
        #expect(Demo.directory(Demo.foo).dirs.isEmpty && Demo.directory(Demo.foo).parent == Demo.home + "/Code")
        #expect(Demo.directory("/").parent.isEmpty && Demo.directory("/Users").parent == "/")
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
