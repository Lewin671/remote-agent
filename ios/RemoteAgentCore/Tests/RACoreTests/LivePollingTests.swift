import Foundation
import RAProto
import Testing

@testable import RACore

private extension Remote {
    func finishEndedPollingForTest(_ sid: String) async {
        startLivePolling(sid)
        await liveJobs[sid]?.value
    }

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

extension ServerTests {
    @Suite struct ResumedLivePollingTests {
        @Test func resumedMetadataRestartsPollingWithoutResubscribing() async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("test-machine")
                var ended = session("/test-project", "test session", status: .exited)
                ended.endedAt.seconds = 1
                try await w.daemon.putSession("s1", ended, endedMs: 1000)
                try await w.daemon.connect()
            }) { w in
                let r = try await w.client()
                r.start()
                _ = try await r.awaitState("ended session online") {
                    $0.conn == .online && $0.sessions["s1"]?.info?.status == .exited
                        && $0.machines[w.daemon.id]?.online == true
                }
                let feed = r.events("s1")
                let updates = feed.updates()
                defer { withExtendedLifetime(updates) {} }
                await r.finishEndedPollingForTest("s1")
                #expect(feed.followers == 1)
                #expect(await r.liveJobs["s1"] == nil)
                #expect(await r.liveSnapshots["s1"] == nil)

                var approval = PBEvent()
                approval.approvalRequested.approvalID = "approval-1"
                var question = PBEvent()
                question.questionAsked.questionID = "question-1"
                try await w.daemon.upload("s1", approval, question)
                try await w.daemon.putSession("s1", session("/test-project", "test session", status: .running))
                _ = try await r.awaitState("resumed session metadata") {
                    $0.sessions["s1"]?.info?.status == .running
                }
                // Consume the original subscription: a new subscription would
                // restart polling itself and hide the missing metadata restart.
                let live = try await withTimeout(3000, sleep: realSleep) {
                    for await snapshot in updates {
                        if let live = snapshot.live, live.session.status == .running { return live }
                    }
                    throw TestFailure("resumed session did not publish a live snapshot")
                }
                #expect(live.actions.contains { $0.approvalRequested.approvalID == "approval-1" })
                #expect(live.actions.contains { $0.questionAsked.questionID == "question-1" })
            }
        }
    }
}
