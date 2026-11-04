import Foundation
import RAProto
import Testing

@testable import RACore

extension ServerTests {
    @Test func aWarmCachePublishesTheNewestWindowBeforeBackfillingThePrefix() async throws {
        let count = DeviceSecurityClient.historyBatchSize + 3
        try await World.run(setUp: { w in
            try await w.daemon.putMachine("history-test-machine")
            try await w.daemon.putSession("s1", session("/work/example", "cached history"))
            try await w.daemon.uploadBatch("s1", (0..<count).map { agent("history \($0)") })
            try await w.daemon.connect()
        }) { w in
            let cache = MemoryCache()
            let first = try await w.client(cache: cache)
            first.start()
            _ = try await first.awaitEvents("s1", count)
            try await eventually("complete encrypted cache") { await cache.events(sessionId: "s1").count == count }
            first.stop()
            w.server.stop()
            let base = try #require(TestSecurity.find(w.server.url)?.store(first.account.deviceId))
            let storage = HistoryReadGate(base)
            defer { storage.release() }
            let again = try Remote(account: first.account, http: w.http, cache: cache, securityStorage: storage)
            w.onClose { again.close() }
            again.start()
            _ = try await again.awaitState("cached metadata") { $0.sessions["s1"]?.info != nil }
            storage.arm()
            let feed = again.events("s1")
            let tail = try await feed.await("first verified tail") {
                $0.events.count == DeviceSecurityClient.historyBatchSize
            }
            #expect(!tail.loaded && tail.error == nil && tail.historyPartial)
            #expect(tail.events.map(\.seq) == Array(4...UInt64(count)))
            #expect(tail.stateEvents.isEmpty && tail.verifiedEvents.isEmpty)
            storage.release()
            let whole = try await feed.await("complete cached history") { $0.events.count == count }
            #expect(whole.events.map(\.seq) == Array(1...UInt64(count)))
            #expect(whole.error == nil && whole.verifiedEvents.count == count)
        }
    }
}

/// Suspend the prefix read after the newest window has been published.
private final class HistoryReadGate: DeviceSecurity.Storage, @unchecked Sendable {
    private let base: any DeviceSecurity.Storage
    private let condition = NSCondition()
    private var armed = false
    private var reads = 0
    init(_ base: any DeviceSecurity.Storage) { self.base = base }
    func arm() { condition.lock(); armed = true; reads = 0; condition.unlock() }
    func release() { condition.lock(); armed = false; condition.broadcast(); condition.unlock() }
    func readSecurity() throws -> DeviceSecurity.Record {
        condition.lock()
        if armed {
            reads += 1
            while armed && reads >= 2 { condition.wait() }
        }
        condition.unlock()
        return try base.readSecurity()
    }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws { try base.initializeSecurity(record) }
    func writeSecurity(expectedHead: Data, _ record: DeviceSecurity.Record) throws -> DeviceSecurity.Record {
        try base.writeSecurity(expectedHead: expectedHead, record)
    }
}

/// Mirrors the other clients' newest-page/gap tests against a real relay.
extension ServerTests {
    private func newestWorld(_ body: (World) async throws -> Void) async throws {
        try await World.run(setUp: { w in
            try await w.daemon.putMachine("history-test-machine")
            try await w.daemon.putSession("s1", session("/work/example", "newest history"))
            for start in stride(from: 1, through: 1100, by: 100) {
                try await w.daemon.uploadBatch("s1", (start..<start+100).map { agent("Message \($0)") })
            }
            try await w.daemon.connect()
        }, body)
    }

    @Test func theNetworkTailIsVisibleBeforeBackfillAndLiveDuplicatesMergeOnce() async throws {
        try await newestWorld { w in
            let requests = HistoryRequests()
            requests.holdPrefix = true
            defer { requests.release.set() }
            let cache = MemoryCache()
            let account = try await w.join()
            let remote = try w.remote(account, http: requests.http(w.server.url), cache: cache)
            remote.start()
            _ = try await remote.awaitState("session metadata") { $0.sessions["s1"]?.info != nil }
            let feed = remote.events("s1")
            let tail = try await feed.await("newest page") { $0.events.count == 500 }
            #expect(tail.events.map(\.seq) == Array(601...1100).map(UInt64.init))
            #expect(tail.historyPartial && !tail.loaded && tail.stateEvents.isEmpty)
            #expect(tail.verifiedEvents.isEmpty && currentState(tail).inputs.isEmpty)
            #expect(await requests.blocked.wait())
            #expect(requests.after == [600, 0])
            try await w.daemon.upload("s1", agent("Message 1101"))
            let live = try await feed.await("live suffix") { $0.events.last?.seq == 1101 }
            #expect(live.events.map(\.seq) == Array(601...1101).map(UInt64.init))
            #expect(live.historyPartial && live.stateEvents.isEmpty && !live.loaded)
            requests.release.set()
            let full = try await feed.await("backfill complete", ms: 20_000) { $0.loaded && $0.events.count == 1101 }
            #expect(full.events.map(\.seq) == Array(1...1101).map(UInt64.init))
            #expect(full.events == full.stateEvents && full.events == full.verifiedEvents)
            #expect(!full.historyPartial && full.fetchError == nil)
            #expect(await cache.events(sessionId: "s1").count == 1101)
            requests.reset()
            remote.reconnectNow()
            _ = try await remote.awaitState("reconnected") { $0.conn == .online }
            try await eventually("history read again after reconnecting") { requests.after == [1101] }
            let again = try await feed.await("history after reconnecting") { $0.loaded }
            #expect(again.events.map(\.seq) == Array(1...1101).map(UInt64.init))
        }
    }

    @Test func anUnreadableTailEventStillLetsThePrefixReachIt() async throws {
        try await newestWorld { w in
            let requests = HistoryRequests()
            requests.corrupt = 900
            let remote = try w.remote(await w.join(), http: requests.http(w.server.url))
            remote.start()
            _ = try await remote.awaitState("session metadata") { $0.sessions["s1"]?.info != nil }
            let feed = remote.events("s1")
            let stopped = try await feed.await("prefix reached the unreadable event", ms: 20_000) {
                $0.error != nil && $0.stateEvents.count == 899
            }
            #expect(stopped.events.map(\.seq) == Array(1...899).map(UInt64.init))
            #expect(!stopped.loaded && stopped.historyPartial)
            // The prefix is published before fetchEvents starts its final
            // request at the unreadable boundary; observe that request too.
            try await eventually("history request reaches the unreadable boundary") { requests.after == [600, 0, 500, 899] }
            #expect(requests.after == [600, 0, 500, 899])
        }
    }

    @Test func aLiveGapAfterACompleteHistoryKeepsTheWholeChat() async throws {
        try await newestWorld { w in
            let cache = MemoryCache()
            let first = try await w.client(cache: cache)
            first.start()
            _ = try await first.awaitEvents("s1", 1100)
            try await w.daemon.upload("s1", agent("Message 1101"), agent("Message 1102"))
            try await eventually("complete cache") { await cache.events(sessionId: "s1").count == 1102 }
            first.stop(); w.server.stop()
            let late = try #require(await cache.events(sessionId: "s1").first { $0.seq == 1102 })
            let older = HookedCache(cache)
            older.hookEvents { sid in await cache.events(sessionId: sid).filter { $0.seq <= 1100 } }
            let offline = try w.remote(first.account, cache: older)
            offline.start()
            _ = try await offline.awaitState("cached metadata") { $0.sessions["s1"]?.info != nil }
            let feed = offline.events("s1")
            _ = try await feed.await("cached history restored") { $0.stateEvents.count == 1100 }
            await offline.ingest("s1", [late], fillGaps: false, save: false)
            let gap = try await feed.await("a later event across a gap") { $0.historyPartial && $0.stateEvents.count == 1100 }
            #expect(gap.events.map(\.seq) == Array(1...1100).map(UInt64.init))
        }
    }

    @Test func aSparseCiphertextCacheKeepsItsReadableTailAndOnlyItsPrefixStateWhenOffline() async throws {
        try await newestWorld { w in
            let cache = MemoryCache()
            let first = try await w.client(cache: cache)
            first.start()
            _ = try await first.awaitEvents("s1", 1100)
            try await eventually("complete cache") { await cache.events(sessionId: "s1").count == 1100 }
            first.stop(); w.server.stop()
            let sparse = HookedCache(cache)
            sparse.hookEvents { sid in await cache.events(sessionId: sid).filter { $0.seq <= 20 || $0.seq >= 601 } }
            let offline = try w.remote(first.account, cache: sparse)
            offline.start()
            _ = try await offline.awaitState("cached metadata") { $0.sessions["s1"]?.info != nil }
            let feed = offline.events("s1")
            let restored = try await feed.await("sparse cache restored") {
                $0.stateEvents.count == 20 && $0.events.first?.seq == 601 && $0.events.last?.seq == 1100
            }
            #expect(restored.historyPartial && !restored.loaded)
            #expect(restored.stateEvents.map(\.seq) == Array(1...20).map(UInt64.init))
            #expect(restored.events.map(\.seq) == Array(601...1100).map(UInt64.init))
            #expect(restored.verifiedEvents.count == 20)
            _ = try await feed.await("automatic backfill failed offline") { $0.fetchError != nil }
        }
    }

    @Test(arguments: [UInt64(650), 1100])
    func anInteriorOrLastGapCannotCompleteHistoryAndRetryFillsIt(missing: UInt64) async throws {
        try await newestWorld { w in
            let requests = HistoryRequests()
            requests.missing = missing
            let remote = try w.remote(await w.join(), http: requests.http(w.server.url))
            remote.start()
            let feed = remote.events("s1")
            let gap = try await feed.await("gap reported", ms: 20_000) { $0.fetchError != nil }
            #expect(!gap.loaded && gap.historyPartial && gap.error == nil)
            #expect(gap.stateEvents.count == Int(missing - 1))
            #expect(!gap.stateEvents.contains { $0.seq == missing })
            requests.missing = 0
            remote.retryEvents("s1")
            let full = try await feed.await("gap repaired", ms: 20_000) { $0.loaded }
            #expect(full.events.map(\.seq) == Array(1...1100).map(UInt64.init))
            #expect(full.events == full.stateEvents && !full.historyPartial)
            #expect(full.fetchError == nil)
        }
    }

    /// newestWorld's whole history in a disk cache, its client stopped; the relay stays up.
    private func warmCache(_ w: World) async throws -> (MemoryCache, Account) {
        let cache = MemoryCache()
        let first = try await w.client(cache: cache)
        first.start()
        _ = try await first.awaitEvents("s1", 1100)
        try await eventually("complete cache") { await cache.events(sessionId: "s1").count == 1100 }
        first.stop()
        return (cache, first.account)
    }

    private func reopen(_ w: World, _ account: Account, _ requests: HistoryRequests, cache: any CipherCache, last: UInt64) async throws -> Remote {
        let again = try w.remote(account, http: requests.http(w.server.url), cache: cache)
        again.start()
        _ = try await again.awaitState("listing from the relay") {
            $0.conn == .online && $0.loaded && $0.sessions["s1"]?.lastSeq == last
        }
        return again
    }

    @Test func eventsNewerThanTheCacheArePublishedBeforeTheCachedPrefixIsRead() async throws {
        try await newestWorld { w in
            let (cache, account) = try await warmCache(w)
            try await w.daemon.upload("s1", agent("Message 1101"), agent("Message 1102"), agent("Message 1103"))
            let gated = HookedCache(cache)
            let entered = Signal(), release = Signal()
            gated.hookEventPage { _, _ in
                entered.set()
                _ = await release.wait(20_000)
            }
            defer { release.set() }
            let requests = HistoryRequests()
            let again = try await reopen(w, account, requests, cache: gated, last: 1103)
            let feed = again.events("s1")
            let newer = try await feed.await("newer events after the cached window") { $0.events.last?.seq == 1103 }
            // The cached prefix has not been read, let alone verified.
            #expect(await entered.wait())
            #expect(newer.events.map(\.seq) == Array(973...1103).map(UInt64.init))
            #expect(newer.stateEvents.isEmpty && newer.verifiedEvents.isEmpty)
            #expect(newer.historyPartial && !newer.loaded && newer.error == nil)
            #expect(requests.after == [1100])
            release.set()
            let whole = try await feed.await("whole history", ms: 20_000) { $0.loaded }
            #expect(whole.events.map(\.seq) == Array(1...1103).map(UInt64.init))
            #expect(whole.events == whole.stateEvents && whole.events == whole.verifiedEvents)
            #expect(!whole.historyPartial && whole.error == nil && whole.fetchError == nil)
            // Nothing the cache held came from the relay.
            #expect(requests.served == [1101, 1102, 1103])
            #expect(await cache.events(sessionId: "s1").count == 1103)
        }
    }

    @Test func aCacheWithAHoleBehindANewerRelayDownloadsEachMissingEventOnce() async throws {
        try await newestWorld { w in
            let (cache, account) = try await warmCache(w)
            try await w.daemon.upload("s1", agent("Message 1101"), agent("Message 1102"), agent("Message 1103"))
            let sparse = HookedCache(cache)
            sparse.hookEvents { sid in await cache.events(sessionId: sid).filter { $0.seq <= 20 || $0.seq >= 601 } }
            let requests = HistoryRequests()
            let again = try await reopen(w, account, requests, cache: sparse, last: 1103)
            let whole = try await again.events("s1").await("hole filled", ms: 20_000) { $0.loaded }
            #expect(whole.events.map(\.seq) == Array(1...1103).map(UInt64.init))
            #expect(whole.events == whole.stateEvents && whole.error == nil)
            #expect(requests.after.first == 1100)
            let served = requests.served
            #expect(served.count == Set(served).count)
            #expect(served.sorted() == (Array(21...600) + [1101, 1102, 1103]).map(UInt64.init))
        }
    }

    @Test func aBadCachedPrefixIsDroppedWhileTheNewerReadIsStillInFlight() async throws {
        try await newestWorld { w in
            let (cache, account) = try await warmCache(w)
            try await w.daemon.upload("s1", agent("Message 1101"))
            let good = try #require(await cache.events(sessionId: "s1").first { $0.seq == 5 })
            cache.damage(sessionId: "s1", seq: 5) { $0.envelope.ciphertext = Data($0.envelope.ciphertext.reversed()) }
            let requests = HistoryRequests()
            requests.holdAfter = 1100
            defer { requests.release.set() }
            let again = try await reopen(w, account, requests, cache: cache, last: 1101)
            let feed = again.events("s1")
            #expect(await requests.blocked.wait())
            let full = try await feed.await("history read again from the relay", ms: 20_000) { $0.loaded }
            #expect(full.events.map(\.seq) == Array(1...1101).map(UInt64.init))
            #expect(full.events == full.stateEvents && full.error == nil && full.fetchError == nil)
            // The held read was the discarded history's: answered now, it changes nothing.
            requests.release.set()
            try await eventually("held read answered") { requests.served.filter { $0 == 1101 }.count == 2 }
            try await eventually("cache filled again") { await cache.events(sessionId: "s1").count == 1101 }
            let after = feed.value
            #expect(after.loaded && after.events.map(\.seq) == Array(1...1101).map(UInt64.init))
            let again5 = try #require(await cache.events(sessionId: "s1").first { $0.seq == 5 })
            #expect(again5.envelope == good.envelope)
        }
    }

    @Test func anUnreadableNewerEventFromTheRelayDoesNotDiscardTheCache() async throws {
        try await newestWorld { w in
            let (cache, account) = try await warmCache(w)
            try await w.daemon.upload("s1", agent("Message 1101"), agent("Message 1102"), agent("Message 1103"))
            let gated = HookedCache(cache)
            let release = Signal()
            gated.hookEventPage { _, _ in _ = await release.wait(20_000) }
            defer { release.set() }
            let requests = HistoryRequests()
            requests.corrupt = 1102
            let again = try await reopen(w, account, requests, cache: gated, last: 1103)
            let feed = again.events("s1")
            let failed = try await feed.await("the relay's event did not open") { $0.error != nil }
            #expect(failed.stateEvents.isEmpty && failed.events.last?.seq == 1101)
            release.set()
            let stopped = try await feed.await("prefix restored up to it", ms: 20_000) { $0.stateEvents.count == 1101 }
            #expect(stopped.events.map(\.seq) == Array(1...1101).map(UInt64.init))
            #expect(stopped.error != nil && !stopped.loaded)
            // Kept, with the relay's readable event added.
            #expect(await cache.events(sessionId: "s1").map(\.seq) == Array(1...1101).map(UInt64.init))
        }
    }

    @Test func shortAndEmptySessionsEachNeedOnlyOneRequest() async throws {
        try await World.run(setUp: { w in
            try await w.daemon.putMachine("history-test-machine")
            for sid in ["short", "empty"] { try await w.daemon.putSession(sid, session("/work/example", sid)) }
            try await w.daemon.upload("short", agent("one"), agent("two"))
        }) { w in
            let requests = HistoryRequests()
            let remote = try w.remote(await w.join(), http: requests.http(w.server.url))
            remote.start()
            _ = try await remote.awaitState("initial Ready and session listing") {
                $0.conn == .online && $0.loaded && $0.sessions["short"]?.info != nil && $0.sessions["empty"]?.info != nil
            }
            let short = try await remote.events("short").await("short complete") { $0.loaded }
            let empty = try await remote.events("empty").await("empty complete") { $0.loaded }
            #expect(short.events.count == 2 && !short.historyPartial)
            #expect(empty.events.isEmpty && !empty.historyPartial)
            #expect(requests.after == [0, 0])
        }
    }
}

@Suite struct PartialHistoryStateTests {
    @Test func aTranscriptSuffixCannotSettleAnInputAcrossAGap() {
        var tail = PBEvent()
        tail.seq = 601; tail.sessionID = "session"
        tail.inputState.requestID = "request"; tail.inputState.status = .submitted
        var snapshot = SessionEvents(events: [tail], stateEvents: [], historyPartial: true)
        #expect(currentState(snapshot).inputs.isEmpty)
        snapshot.stateEvents = PagedList([tail])
        #expect(currentState(snapshot).inputs["request"] == .submitted)
        snapshot.unverifiedLegacy = true
        snapshot.verifiedEvents = []
        #expect(currentState(snapshot).inputs.isEmpty)
    }
}

private final class HistoryRequests: @unchecked Sendable {
    private let lock = NSLock()
    private var values: [UInt64] = []
    private var seqs: [UInt64] = []
    private var hold = false
    private var holdAt: UInt64?
    private var omitted: UInt64 = 0
    private var damaged: UInt64 = 0
    let blocked = Signal(), release = Signal()
    var after: [UInt64] { lock.withLock { values } }
    /// Every seq the relay sent back, in the order sent, duplicates included.
    var served: [UInt64] { lock.withLock { seqs } }
    func reset() { lock.withLock { values = []; seqs = [] } }
    /// Holds the request for what comes after this seq until `release`.
    var holdAfter: UInt64? { get { lock.withLock { holdAt } } set { lock.withLock { holdAt = newValue } } }
    var corrupt: UInt64 { get { lock.withLock { damaged } } set { lock.withLock { damaged = newValue } } }
    var holdPrefix: Bool { get { lock.withLock { hold } } set { lock.withLock { hold = newValue } } }
    var missing: UInt64 { get { lock.withLock { omitted } } set { lock.withLock { omitted = newValue } } }
    func http(_ origin: String) -> HTTP {
        Intercept.http(for: origin, transform: { [self] request, data, response in
            guard request.url?.path.hasSuffix("/GetEvents") == true, response.statusCode == 200 else { return (data, response) }
            var page = try PBGetEventsResponse(serializedBytes: data)
            guard missing != 0 || corrupt != 0 else {
                lock.withLock { seqs += page.events.map(\.seq) }
                return (data, response)
            }
            page.events.removeAll { $0.seq == missing }
            for i in page.events.indices where page.events[i].seq == corrupt {
                page.events[i].envelope.ciphertext[page.events[i].envelope.ciphertext.startIndex] ^= 1
            }
            lock.withLock { seqs += page.events.map(\.seq) }
            let headers = response.allHeaderFields.reduce(into: [String:String]()) { out, item in
                let key = String(describing: item.key)
                if !["content-encoding", "content-length"].contains(key.lowercased()) { out[key] = String(describing: item.value) }
            }
            return (try page.serializedData(), HTTPURLResponse(url: response.url!, statusCode: response.statusCode, httpVersion: nil, headerFields: headers)!)
        }) { [self] request in
            if request.url?.path.hasSuffix("/GetEvents") == true {
                let ask = try PBGetEventsRequest(serializedBytes: request.httpBody ?? Data())
                let pause = lock.withLock {
                    values.append(ask.afterSeq)
                    return (hold && ask.afterSeq == 0) || holdAt == ask.afterSeq
                }
                if pause {
                    blocked.set()
                    guard await release.wait(20_000) else { throw TestFailure("prefix gate timed out") }
                }
            }
            return request
        }
    }
}
