#if DEBUG
import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

@MainActor
@Suite struct DemoDataTests {
    let now: Int64 = 1_791_000_000_000

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
