import Foundation
import RAProto
import Testing

@testable import RACore

private let base: Int64 = 1_700_000_000_000
private func endedId(_ i: Int) -> String { String(format: "e%03d", i) }
private func endedAt(_ i: Int) -> Int64 { base + Int64(i) }

private func live(_ cwd: String, _ title: String) -> PBSession { session(cwd, title) }

private func ended(_ cwd: String, _ title: String, _ at: Int64) -> PBSession {
    var s = session(cwd, title, status: .exited)
    s.endedAt.seconds = at / 1000
    s.endedAt.nanos = Int32(at % 1000) * 1_000_000
    return s
}

private func exited(_ s: SessionView) -> Bool { s.info?.status == .exited }

/// When the session ended by its sealed metadata, which is what the lists sort by.
private func endedMs(_ s: SessionView) -> Int64 {
    guard let i = s.info, i.hasEndedAt else { return 0 }
    return i.endedAt.seconds * 1000 + Int64(i.endedAt.nanos) / 1_000_000
}

extension RemoteState {
    /// The ended sessions held, the latest ended first, by ID where those are equal.
    fileprivate var endedIds: [String] {
        sessions.values.filter(exited).sorted {
            let (a, b) = (endedMs($0), endedMs($1))
            return a != b ? a > b : $0.id < $1.id
        }.map(\.id)
    }

    fileprivate var liveIds: [String] { sessions.values.filter { !exited($0) }.map(\.id).sorted() }
}

private let pageOneEnd = SessionPlace(endedMs: endedAt(71), sessionId: endedId(71))
private func endedDown(_ from: Int, _ to: Int) -> [String] {
    stride(from: from, through: to, by: -1).map(endedId)
}

extension Remote {
    /// Connected, with the first page: every session that has not ended and the fifty ended last.
    fileprivate func started() async throws -> RemoteState {
        start()
        return try await awaitState("the first page", ms: 15_000) {
            $0.conn == .online && $0.loaded && $0.sessionsNext == pageOneEnd
                && $0.sessions.count == 52
        }
    }
}

/// An HTTP stack whose listing of machines fails while `down` is set, with the stream and everything else working.
private final class Flaky: @unchecked Sendable {
    private let lock = NSLock()
    private var _down = false
    var down: Bool {
        get { lock.withLock { _down } }
        set { lock.withLock { _down = newValue } }
    }

    func http(for origin: String) -> HTTP {
        Intercept.http(for: origin) { [self] request in
            if down, request.url?.path.hasSuffix("ListMachines") == true {
                throw URLError(.networkConnectionLost)
            }
            return request
        }
    }
}

extension ServerTests {
    /// The session listing a page at a time (architecture §8 「会话列表分页」),
    /// against the real Go server: 120 ended sessions and two that have not
    /// ended, so the first page and two more.
    @Suite struct SessionPagingTests {
        private func world(_ body: (World) async throws -> Void) async throws {
            try await World.run(
                setUp: { w in
                    try await w.daemon.putMachine("fake-mac.local")
                    try await w.daemon.putSession("live1", live("/Users/me/Code/foo", "进行中一"))
                    try await w.daemon.putSession("live2", live("/Users/me/Code/bar", "进行中二"))
                    // e120 ended last: the first page is e120 down to e071.
                    for i in 1...120 {
                        try await w.daemon.putSession(
                            endedId(i), ended("/Users/me/Code/foo", "结束 \(i)", endedAt(i)),
                            endedMs: endedAt(i))
                    }
                    try await w.daemon.connect()
                }, body)
        }

        @Test func theFirstPageIsEverySessionNotEndedAndTheLatestEndedThenPagesMerge() async throws
        {
            try await world { w in
                let r = try await w.client()
                let st = try await r.started()
                #expect(st.liveIds == ["live1", "live2"])
                #expect(st.endedIds == endedDown(120, 71))
                #expect(st.moreSessions)
                #expect(!st.loadingSessions)
                // What is shown and sorted by is the sealed metadata; the server's time only places it.
                let e120 = try #require(st.sessions[endedId(120)])
                #expect(e120.listedEndedMs == endedAt(120))
                #expect(endedMs(e120) == endedAt(120))
                #expect(st.sessions["live1"]?.listedEndedMs == 0)

                #expect(try await r.loadMoreSessions())
                var next = r.current
                #expect(next.endedIds == endedDown(120, 21))
                #expect(
                    next.sessionsNext == SessionPlace(endedMs: endedAt(21), sessionId: endedId(21)))
                #expect(next.liveIds == ["live1", "live2"])

                // The last page: nothing is left, and asking again fetches nothing.
                #expect(try await !r.loadMoreSessions())
                next = r.current
                #expect(next.endedIds == endedDown(120, 1))
                #expect(next.sessionsNext == nil)
                #expect(!next.moreSessions)
                #expect(!next.loadingSessions)
                #expect(try await !r.loadMoreSessions())
                #expect(r.current.sessions.count == 122)
            }
        }

        @Test func callsForAPageWhileOneIsInFlightAreOneFetch() async throws {
            try await world { w in
                let r = try await w.client()
                _ = try await r.started()
                // Three calls at once fetch one page, not three: fifty more, with one page left.
                async let a = r.loadMoreSessions()
                async let b = r.loadMoreSessions()
                async let c = r.loadMoreSessions()
                #expect(try await [a, b, c] == [true, true, true])
                let st = r.current
                #expect(st.endedIds == endedDown(120, 21))
                #expect(
                    st.sessionsNext == SessionPlace(endedMs: endedAt(21), sessionId: endedId(21)))
                #expect(!st.loadingSessions)
            }
        }

        @Test func everyPageLeftIsFetchedForAFilter() async throws {
            try await world { w in
                let r = try await w.client()
                _ = try await r.started()
                // Asked twice at once, with a single page asked for as well: one run, every page once.
                async let a: Void = r.loadAllSessions()
                async let b: Void = r.loadAllSessions()
                async let c = r.loadMoreSessions()
                _ = try await (a, b, c)
                let st = r.current
                #expect(st.endedIds == endedDown(120, 1))
                #expect(!st.moreSessions)
                #expect(!st.loadingSessions)
                #expect(st.sessionsError == "")
                // With nothing left it returns at once.
                try await r.loadAllSessions()
                #expect(r.current.sessions.count == 122)
            }
        }

        @Test func aSessionNotHeldIsFetchedByItsIdAndOnlyTheServerSaysItIsMissing() async throws {
            try await world { w in
                let r = try await w.client()
                let st = try await r.started()
                #expect(st.sessions[endedId(3)] == nil)
                #expect(try await r.fetchSession(endedId(3)))
                let got = try #require(r.current.sessions[endedId(3)])
                #expect(got.info?.title == "结束 3")
                #expect(got.listedEndedMs == endedAt(3))
                // The cursor does not move: the pages between are still to come.
                #expect(r.current.sessionsNext == pageOneEnd)
                #expect(r.current.sessions.count == 53)
                // One held already is not asked for; one the server does not have is missing.
                #expect(try await r.fetchSession("live1"))
                #expect(try await !r.fetchSession("no-such-session"))
                #expect(r.current.sessions["no-such-session"] == nil)
                // Asked for twice at once, it is one session.
                async let a = r.fetchSession(endedId(5))
                async let b = r.fetchSession(endedId(5))
                #expect(try await [a, b] == [true, true])
                // Its events load like any other session's.
                try await w.daemon.upload(endedId(3), agent("旧会话的内容"))
                let ev = try await r.events(endedId(3)).await("the old session's event") {
                    $0.events.count == 1
                }
                #expect(ev.events[0].agentMessage.text == "旧会话的内容")
            }
        }

        @Test func afterAReconnectEndedSessionsPastTheFirstPageStay() async throws {
            try await world { w in
                let r = try await w.client()
                _ = try await r.started()
                #expect(try await r.loadMoreSessions())
                #expect(try await r.fetchSession(endedId(2)))
                r.stop()
                _ = try await r.awaitState("offline") { $0.conn == .offline }
                // A session ends while this client is away: it leads the next first page, which now ends at e072.
                try await w.daemon.putSession(
                    "live2", ended("/Users/me/Code/bar", "进行中二", endedAt(500)),
                    endedMs: endedAt(500))
                r.start()
                let end = SessionPlace(endedMs: endedAt(72), sessionId: endedId(72))
                let st = try await r.awaitState("the first page again", ms: 15_000) {
                    $0.conn == .online && $0.sessionsNext == end
                }
                // The pages start over from the first, and what was fetched of the later ones is kept:
                // e071 to e021 from the second page, and e002 fetched on its own.
                #expect(st.endedIds == ["live2"] + endedDown(120, 21) + [endedId(2)])
                #expect(st.liveIds == ["live1"])
                // Going on from there ends with every session, each once.
                try await r.loadAllSessions()
                #expect(r.current.endedIds == ["live2"] + endedDown(120, 1))
                #expect(r.current.sessions.count == 122)
            }
        }

        @Test func afterAReconnectASessionOfAMachineThatIsGoneGoes() async throws {
            try await world { w in
                let otherAccount = try await approvedClient(
                    authorizer: w.daemon.account, http: w.http, kind: .machine)
                let other = try FakeDaemon(account: otherAccount, http: w.http)
                try await other.login()
                try await other.putMachine("other-mac.local")
                try await other.putSession("there", live("/Users/me/Code/baz", "另一台上的"))
                // Ended before everything else: far past the first page, where an ended session would stay.
                try await other.putSession(
                    "old-there", ended("/Users/me/Code/baz", "另一台上的旧会话", base - 10),
                    endedMs: base - 10)
                try await other.upload("old-there", agent("旧内容"))
                let cache = MemoryCache()
                let r = try await w.client(cache: cache)
                r.start()
                _ = try await r.awaitState("both machines", ms: 15_000) {
                    $0.conn == .online && $0.sessionsNext == pageOneEnd
                        && $0.sessions["there"] != nil
                }
                #expect(try await r.fetchSession("old-there"))
                _ = try await r.events("old-there").await("its event") { $0.events.count == 1 }
                try await eventually("the event to be cached") {
                    await cache.events(sessionId: "old-there").count == 1
                }
                r.stop()
                _ = try await r.awaitState("offline") { $0.conn == .offline }
                // Another client removes that machine meanwhile.
                let admin = try await w.client()
                admin.start()
                _ = try await admin.awaitState("admin online", ms: 15_000) {
                    $0.conn == .online && $0.machines[other.id] != nil
                }
                await admin.refreshDevices()
                try await admin.removeDevice(other.id)
                r.start()
                let id = w.daemon.id
                let st = try await r.awaitState("the machine gone", ms: 15_000) {
                    $0.conn == .online && $0.machines[other.id] == nil
                        && $0.machines[id]?.online == true
                }
                #expect(st.sessions["there"] == nil)
                #expect(st.sessions["old-there"] == nil)
                #expect(st.sessions.count == 52)
                // Known not to exist any more: its cached events go too.
                try await eventually("its cached events to go", ms: 5_000) {
                    await cache.events(sessionId: "old-there").isEmpty
                }
                #expect(await !cache.sessions().contains { $0.machineID == other.id })
            }
        }

        @Test func afterAReconnectASessionThePageLacksAndThatIsNotALaterPagesGoes() async throws {
            try await world { w in
                let cache = MemoryCache()
                let r = try await w.client(cache: cache)
                _ = try await r.started()
                try await w.daemon.upload("live1", agent("进行中的内容"))
                _ = try await r.events("live1").await("its event") { $0.events.count == 1 }
                try await eventually("the event to be cached") {
                    await cache.events(sessionId: "live1").count == 1
                }
                r.stop()
                _ = try await r.awaitState("offline") { $0.conn == .offline }
                // It ended while this client was away, at a time before every other: it is on the last
                // page now. Here it is still held as not ended, so nothing says it is a later page's.
                try await w.daemon.putSession(
                    "live1", ended("/Users/me/Code/foo", "进行中一", base - 5), endedMs: base - 5)
                r.start()
                let st = try await r.awaitState("the stale session gone", ms: 15_000) {
                    $0.conn == .online && $0.sessions["live1"] == nil
                }
                #expect(st.liveIds == ["live2"])
                #expect(st.sessionsNext == pageOneEnd)
                // It was not known to be gone, so its events stay cached; it is back, as ended, with its page.
                #expect(await cache.events(sessionId: "live1").count == 1)
                try await r.loadAllSessions()
                let back = try #require(r.current.sessions["live1"])
                #expect(exited(back))
                #expect(back.listedEndedMs == base - 5)
                #expect(r.current.endedIds.last == "live1")
            }
        }

        @Test func aPageThatFailsLeavesTheListingReadyToGoOn() async throws {
            try await world { w in
                let r = try await w.client()
                _ = try await r.started()
                w.server.stop()
                await #expect(throws: (any Error).self) { try await r.loadMoreSessions() }
                var st = r.current
                #expect(!st.loadingSessions)
                #expect(!st.sessionsError.isEmpty)
                // Nothing moved: the same page is still the next one.
                #expect(st.sessionsNext == pageOneEnd)
                #expect(st.sessions.count == 52)
                // Every page left fails the same way, and does not hang.
                let all = Task { try await r.loadAllSessions() }
                let limit = Task {
                    await sleepMs(20_000)
                    if Task.isCancelled { return }
                    all.cancel()
                    Issue.record("loading every page hung")
                }
                await #expect(throws: (any Error).self) { try await all.value }
                limit.cancel()
                #expect(!r.current.loadingSessions)
                try await w.server.start()
                try await w.daemon.login()
                try await w.daemon.connect()
                r.reconnectNow()
                _ = try await r.awaitState("online again", ms: 15_000) {
                    $0.conn == .online && $0.sessionsError.isEmpty
                }
                #expect(try await r.loadMoreSessions())
                st = r.current
                #expect(st.sessionsError == "")
                #expect(st.endedIds == endedDown(120, 21))
            }
        }

        @Test func theCacheKeepsTheFirstPageAndEventsOfAnySessionOpened() async throws {
            try await world { w in
                let cache = MemoryCache()
                let r = try await w.client(cache: cache)
                _ = try await r.started()
                try await r.loadAllSessions()
                #expect(try await r.fetchSession(endedId(1)))
                try await w.daemon.upload(endedId(1), agent("最早的会话"))
                _ = try await r.events(endedId(1)).await("its event") { $0.events.count == 1 }
                try await eventually("the event to be cached") {
                    await cache.events(sessionId: endedId(1)).count == 1
                }
                // Only the first page is listed in the cache, whatever else was fetched.
                let firstPage = (["live1", "live2"] + (71...120).map(endedId)).sorted()
                #expect(await cache.sessions().map(\.sessionID).sorted() == firstPage)
                // A session that ends now is on the first page; one whose metadata comes again from a later page is not.
                try await w.daemon.putSession(
                    "live2", ended("/Users/me/Code/bar", "进行中二", endedAt(500)),
                    endedMs: endedAt(500))
                _ = try await r.awaitState("live2 ended") {
                    $0.sessions["live2"].map(exited) == true
                }
                try await w.daemon.putSession(
                    endedId(1), ended("/Users/me/Code/foo", "结束 1 改名", endedAt(1)),
                    endedMs: endedAt(1))
                _ = try await r.awaitState("e001 renamed") {
                    $0.sessions[endedId(1)]?.info?.title == "结束 1 改名"
                }
                #expect(await !cache.sessions().contains { $0.sessionID == endedId(1) })
                #expect(await cache.sessions().contains { $0.sessionID == "live2" })
                r.stop()
                _ = try await r.awaitState("offline") { $0.conn == .offline }

                // The next run shows the cached first page before it connects, and the events are still there.
                let again = try w.remote(r.account, cache: cache)
                w.server.stop()
                again.start()
                let st = try await again.awaitState("the cached page") {
                    $0.loaded && $0.sessions.count == 52
                }
                // What the cache kept is one page of an earlier run: whether there are more is not known until the first page comes.
                #expect(!st.sessionsListed)
                #expect(st.moreSessions)
                #expect(st.sessionsNext == nil)
                #expect(st.sessions[endedId(1)] == nil)
                #expect(await cache.events(sessionId: endedId(1)).count == 1)
                // After reconnecting the cache is the new first page: e071 left it for live2.
                try await w.server.start()
                try await w.daemon.login()
                try await w.daemon.connect()
                again.reconnectNow()
                let end = SessionPlace(endedMs: endedAt(72), sessionId: endedId(72))
                _ = try await again.awaitState("online", ms: 15_000) {
                    $0.conn == .online && $0.sessionsNext == end
                }
                try await eventually("e071 to leave the cached page", ms: 5_000) {
                    await !cache.sessions().contains { $0.sessionID == endedId(71) }
                }
                #expect(
                    await cache.sessions().map(\.sessionID).sorted()
                        == (["live1", "live2"] + (72...120).map(endedId)).sorted())
                #expect(await cache.events(sessionId: endedId(1)).count == 1)
            }
        }

        @Test func aPageAskedForWhileTheListingStartsOverIsFetchedFromTheNewCursor() async throws {
            try await world { w in
                // A cache whose machine writes wait while the hold is on: the first page stays half put in place.
                let cache = HookedCache()
                let r = try await w.client(cache: cache)
                _ = try await r.started()
                r.stop()
                _ = try await r.awaitState("offline") { $0.conn == .offline }
                // Nothing changed meanwhile: the first page again ends where it did, so the cursor does not move.
                let hold = Signal()
                let reached = Signal()
                w.onClose { hold.set() }
                cache.hookPutMachine { _ in
                    reached.set()
                    await hold.wait(30_000)
                }
                r.start()
                #expect(await reached.wait(15_000))
                #expect(r.current.conn == .online)
                let done = Signal()
                let page = Task {
                    defer { done.set() }
                    return try await r.loadMoreSessions()
                }
                // The request does not end with nothing fetched: it waits for the first page.
                await sleepMs(500)
                #expect(!done.isSet)
                #expect(r.current.sessions.count == 52)
                cache.hookPutMachine(nil)
                hold.set()
                #expect(await done.wait(15_000))
                #expect(try await page.value)
                let st = r.current
                #expect(st.endedIds == endedDown(120, 21))
                #expect(
                    st.sessionsNext == SessionPlace(endedMs: endedAt(21), sessionId: endedId(21)))
                #expect(!st.loadingSessions)
            }
        }

        @Test func aFirstPageThatFailsSaysSoAndIsTriedAgainByItself() async throws {
            try await world { w in
                let flaky = Flaky()
                flaky.down = true
                let r = try await w.client(http: flaky.http(for: w.server.url))
                r.start()
                // Connected, with no listing: it is not taken for an empty one, nor for one with nothing more to fetch.
                let failed = try await r.awaitState("the failed listing") {
                    $0.conn == .online && !$0.sessionsError.isEmpty
                }
                #expect(!failed.sessionsListed)
                #expect(!failed.loaded)
                #expect(failed.moreSessions)
                #expect(failed.sessionsNext == nil)
                flaky.down = false
                // No reconnect and no call from the screen: it comes by itself.
                let st = try await r.awaitState("the first page", ms: 15_000) {
                    $0.sessionsListed && $0.sessions.count == 52
                }
                #expect(st.sessionsError == "")
                #expect(st.sessionsNext == pageOneEnd)
                #expect(st.conn == .online)
            }
        }

        @Test func askingForAPageBeforeTheFirstOneCameFetchesTheFirst() async throws {
            try await world { w in
                let flaky = Flaky()
                flaky.down = true
                w.onClose { flaky.down = false }
                let r = try await w.client(http: flaky.http(for: w.server.url))
                r.start()
                _ = try await r.awaitState("the failed listing") {
                    $0.conn == .online && !$0.sessionsError.isEmpty
                }
                // Still failing: the retry says so and leaves nothing loading.
                await #expect(throws: (any Error).self) { try await r.loadMoreSessions() }
                #expect(!r.current.loadingSessions)
                #expect(!r.current.sessionsListed)
                flaky.down = false
                // The tap that tries again: the first page, and only it.
                #expect(try await r.loadMoreSessions())
                let st = r.current
                #expect(st.sessionsListed)
                #expect(st.sessionsNext == pageOneEnd)
                #expect(st.sessions.count == 52)
                // Every page left, asked for before the first one came, starts with the first.
                r.stop()
                flaky.down = true
                let again = try await w.client(http: flaky.http(for: w.server.url))
                again.start()
                _ = try await again.awaitState("the failed listing") {
                    $0.conn == .online && !$0.sessionsError.isEmpty
                }
                await #expect(throws: (any Error).self) { try await again.loadAllSessions() }
            }
        }

        @Test func ofTwoFirstPagesInFlightOnlyTheLaterAskedForIsPutInPlace() async throws {
            try await world { w in
                let r = try await w.client()
                _ = try await r.started()
                #expect(try await r.loadMoreSessions())
                // As when a stream was replaced while the lists of the one before were still on their way.
                // Which of the two calls reaches the engine first is not this test's to say: one of them is the later.
                async let a = r.refresh()
                async let b = r.refresh()
                #expect(await [a, b].sorted { !$0 && $1 } == [false, true])
                let st = r.current
                #expect(st.sessionsNext == pageOneEnd)
                #expect(st.endedIds == endedDown(120, 21))
                #expect(st.sessionsError == "")
                // The listing goes on from there.
                try await r.loadAllSessions()
                #expect(r.current.sessions.count == 122)
            }
        }

        @Test func noPageIsAskedForWithoutTheConnection() async throws {
            try await world { w in
                let r = try await w.client()
                _ = try await r.started()
                r.stop()
                _ = try await r.awaitState("offline") { $0.conn == .offline }
                // In the background nothing is fetched, though the server is there; a run of every page stops too.
                do {
                    try await r.loadMoreSessions()
                    Issue.record("expected no page without the connection")
                } catch let e as CommandError {
                    #expect(e.code == CommandError.unavailable)
                }
                do {
                    try await r.loadAllSessions()
                    Issue.record("expected no page without the connection")
                } catch let e as CommandError {
                    #expect(e.code == CommandError.unavailable)
                }
                #expect(r.current.sessions.count == 52)
                #expect(r.current.sessionsNext == pageOneEnd)
                // Back online the reason is gone and the listing goes on.
                r.start()
                _ = try await r.awaitState("online again", ms: 15_000) {
                    $0.conn == .online && $0.sessionsError.isEmpty
                }
                #expect(try await r.loadMoreSessions())
            }
        }
    }
}

@Suite struct SessionPlaceTests {
    @Test func aPlaceInTheListingIsByEndTimeThenId() {
        let place = SessionPlace(endedMs: 100, sessionId: "m")
        #expect(afterPlace(endedMs: 99, sessionId: "a", place: place))
        #expect(afterPlace(endedMs: 100, sessionId: "n", place: place))
        #expect(!afterPlace(endedMs: 100, sessionId: "m", place: place))
        #expect(!afterPlace(endedMs: 100, sessionId: "a", place: place))
        #expect(!afterPlace(endedMs: 101, sessionId: "z", place: place))
        // A session that has not ended is on the first page, never after a place.
        #expect(!afterPlace(endedMs: 0, sessionId: "z", place: place))
    }
}
