import Foundation
import RAProto
import Testing

@testable import RACore

private extension Remote {
    func preparePollingTest(status: PBSessionStatus = .idle, ended: Bool = false,
        connection: ConnState = .online, machineOnline: Bool = true, active: Bool = true,
        followed: Bool = true) -> AsyncStream<SessionEvents>? {
        running = active
        conn = connection
        token = "test-token"
        var info = PBSession()
        info.status = status
        if ended { info.endedAt.seconds = 1 }
        sessions["s"] = SessionView(id: "s", machineId: "m", info: info)
        machines["m"] = MachineView(id: "m", online: machineOnline, lastSeenMs: 0)
        // No lifecycle callbacks: the tests drive one tick at a time.
        eventFeeds.withLock { $0["s"] = Feed(SessionEvents()) }
        return followed ? eventFeed("s").updates() : nil
    }
}

@Suite struct LivePollingTests {
    private func remote() throws -> Remote {
        try Remote(account: Account(server: "https://example.com", root: Data(count: 32),
            deviceSeed: Data(count: 32), deviceId: "2222222222222222"), http: HTTP())
    }

    @Test(arguments: [false, true])
    func endedSessionsStopBeforeFetching(timestamp: Bool) async throws {
        let r = try remote()
        defer { r.close() }
        let updates = await r.preparePollingTest(status: timestamp ? .idle : .exited, ended: timestamp)
        defer { withExtendedLifetime(updates) {} }
        #expect(await r.livePollingTick("s") == false)
        #expect(await r.liveErrors.isEmpty)
    }

    @Test(arguments: [ConnState.connecting, .offline])
    func disconnectedSessionsPause(connection: ConnState) async throws {
        let r = try remote()
        defer { r.close() }
        let updates = await r.preparePollingTest(connection: connection)
        defer { withExtendedLifetime(updates) {} }
        #expect(await r.livePollingTick("s"))
        #expect(await r.liveErrors.isEmpty)
    }

    @Test func offlineMachinePausesAndOnlineMachineResumes() async throws {
        let r = try remote()
        defer { r.close() }
        let paused = await r.preparePollingTest(machineOnline: false)
        defer { withExtendedLifetime(paused) {} }
        #expect(await r.livePollingTick("s"))
        #expect(await r.liveErrors.isEmpty)
        let resumed = await r.preparePollingTest()
        defer { withExtendedLifetime(resumed) {} }
        #expect(await r.livePollingTick("s"))
        // The online tick attempted authorization; this fixture has no security store.
        #expect(await r.liveErrors["s"] != nil)
    }

    @Test(arguments: [false, true])
    func stoppedOrUnfollowedSessionsFinish(unfollowed: Bool) async throws {
        let r = try remote()
        defer { r.close() }
        let updates = await r.preparePollingTest(active: unfollowed, followed: !unfollowed)
        defer { withExtendedLifetime(updates) {} }
        #expect(await r.livePollingTick("s") == false)
        #expect(await r.liveErrors.isEmpty)
    }
}
