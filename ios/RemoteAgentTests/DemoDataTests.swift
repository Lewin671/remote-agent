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
        let s = Demo.world(set, now: now)
        for (sid, events) in Demo.events(set, now: now) {
            #expect(s.sessions[sid] != nil, "\(sid)")
            #expect(events.map(\.seq) == Array(1...UInt64(events.count)), "\(sid)")
            #expect(events.allSatisfy { $0.kind != nil })
        }
    }

    @Test(arguments: DemoSet.allCases)
    func aRemoteShowsASetWithoutAServer(set: DemoSet) async {
        let remote = Demo.remote()
        let world = Demo.world(set, now: now)
        let events = Demo.events(set, now: now)
        let model = AppModel(settings: SettingsStore(defaults: UserDefaults(suiteName: "demo-\(UUID().uuidString)")!))
        model.showDemo(account: Demo.account, remote: remote, world: world, events: events)
        #expect(model.screens.appRoot.stage == .ready && model.isDemo)
        #expect(model.context?.me == Demo.me)
        if let context = model.context, let sid = world.sessions.keys.sorted().first {
            let token = await context.coreHost.acquireSessionWatch(sid)
            await eventually("demo core session rendered") { context.core.sessions[sid] != nil }
            await context.coreHost.releaseSessionWatch(token)
        }
        // Leaving sample data stores and erases nothing.
        await model.logout()
        #expect(model.screens.appRoot.stage == .onboarding)
    }
}
#endif
