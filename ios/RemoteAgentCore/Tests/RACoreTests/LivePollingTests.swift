import Foundation
import RAProto
import Testing

@testable import RACore

private extension Remote {
    func finishEndedPollingForTest(_ sid: String) async {
        startLivePolling(sid)
        await liveJobs[sid]?.value
    }

    func seedStaleLiveStateForTest() {
        var live = PBLiveSnapshot()
        live.running = true
        var approval = PBEvent()
        approval.approvalRequested.approvalID = "old-approval"
        var question = PBEvent()
        question.questionAsked.questionID = "old-question"
        live.actions = [approval, question]
        liveSnapshots["s"] = live
        liveErrors["s"] = "old snapshot error"
        let buffer = Buffer()
        buffer.loaded = true
        buffers["s"] = buffer
        publishEvents("s", buffer)
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

    @Test(arguments: [false, true])
    func exitedSessionsClearCachedLiveStateAndRepublish(timestamp: Bool) async throws {
        let r = try remote()
        defer { r.close() }
        let updates = await r.preparePollingTest(status: timestamp ? .idle : .exited, ended: timestamp)
        defer { withExtendedLifetime(updates) {} }
        await r.seedStaleLiveStateForTest()
        let feed = r.eventFeed("s")
        #expect(currentState(feed.value).running)
        #expect(currentState(feed.value).approvals.count == 1)
        #expect(currentState(feed.value).questions.count == 1)
        #expect(await r.livePollingTick("s") == false)
        #expect(await r.liveSnapshots["s"] == nil)
        #expect(await r.liveErrors.isEmpty)
        #expect(feed.value.live == nil)
        let state = currentState(feed.value)
        #expect(!state.running)
        #expect(state.approvals.isEmpty)
        #expect(state.questions.isEmpty)
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
        @Test func snapshotReturningAfterExitIsClearedAndResumeStillPolls() async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("test-machine")
                try await w.daemon.putSession("s1", session("/test-project", "test session", status: .running))
                var started = PBEvent()
                started.turnStarted = PBTurnStarted()
                var approval = PBEvent()
                approval.approvalRequested.approvalID = "approval-1"
                var question = PBEvent()
                question.questionAsked.questionID = "question-1"
                try await w.daemon.upload("s1", started, approval, question)
                try await w.daemon.connect()
            }) { w in
                let entered = Signal(), release = Signal()
                w.onClose { release.set() }
                let http = Intercept.http(for: w.server.url, answered: { request in
                    if !entered.isSet && request.url?.path.hasSuffix("/Forward") == true {
                        entered.set()
                        await release.wait()
                    }
                }) { $0 }
                let r = try w.remote(try await w.join(), http: http)
                r.start()
                _ = try await r.awaitState("active session online") { $0.synced && $0.sessions["s1"]?.info?.status == .running }
                let feed = r.events("s1")
                let updates = feed.updates()
                let observed = Feed(SessionEvents())
                let watching = Task { for await snapshot in updates { observed.set(snapshot) } }
                defer { watching.cancel() }
                #expect(await entered.wait())
                let polling = try #require(await r.liveJobs["s1"])
                var ended = PBEvent()
                ended.turnEnded = PBTurnEnded()
                try await w.daemon.upload("s1", ended)
                try await w.daemon.putSession("s1", session("/test-project", "test session", status: .exited), endedMs: 1000)
                _ = try await r.awaitState("exited session metadata") { $0.sessions["s1"]?.info?.status == .exited }
                release.set()
                await polling.value
                _ = try await observed.await("ended transcript") { $0.events.count == 4 }
                #expect(await r.liveSnapshots["s1"] == nil)
                #expect(feed.value.live == nil)
                #expect(!currentState(feed.value).running)
                #expect(currentState(feed.value).approvals.isEmpty)
                #expect(currentState(feed.value).questions.isEmpty)
                #expect(feed.followers == 1)

                try await w.daemon.putSession("s1", session("/test-project", "test session", status: .running))
                _ = try await observed.await("resumed live snapshot") { $0.live?.session.status == .running }
            }
        }

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
