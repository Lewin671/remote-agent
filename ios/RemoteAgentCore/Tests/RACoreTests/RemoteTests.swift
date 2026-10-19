import Foundation
import RAProto
import Testing

@testable import RACore

/// The tests that run against the real Go server. One at a time: each starts
/// a server of its own and several wait on real time.
@Suite(.serialized, .enabled(if: GoServer.available, GoServer.missing))
struct ServerTests {}

/// A server, a fake daemon with one machine, and what a test opened and must close.
final class World: @unchecked Sendable {
    let server: TestServer
    let http = HTTP()
    let root = randomBytes(32)
    let phone: SyntheticPhone
    let daemon: FakeDaemon
    private let lock = NSLock()
    private var closers: [@Sendable () -> Void] = []

    private init() async throws {
        server = try await TestServer()
        do {
            phone = try await SyntheticPhone.create(
                root: root, server: server.url, http: http, setupToken: server.setupToken)
            daemon = try await FakeDaemon.join(phone: phone, http: http)
        } catch {
            server.close()
            throw error
        }
    }

    /// Runs a test in a fresh world, set up by `setUp`, and takes everything down afterwards.
    static func run(setUp: (World) async throws -> Void, _ body: (World) async throws -> Void)
        async throws
    {
        let w = try await World()
        do {
            try await setUp(w)
            try await body(w)
        } catch {
            await w.close()
            throw error
        }
        await w.close()
    }

    func onClose(_ f: @escaping @Sendable () -> Void) { lock.withLock { closers.append(f) } }

    private func close() async {
        for f in lock.withLock({ closers }) { f() }
        await daemon.disconnect()
        server.close()
    }

    /// A new client device of the account.
    func join() async throws -> Account {
        try await phone.enroll(seed: randomBytes(32), kind: .client).0
    }

    func remote(
        _ account: Account, http: HTTP? = nil, cache: (any CipherCache)? = nil,
        frameQueue: Int = 1024,
        historyIdleMs: Int64 = 60_000, historyIdleCount: Int = 8, streamOpenMs: Int64 = 15_000,
        linkGraceMs: Int64 = 10_000
    ) throws -> Remote {
        let r = try fixtureRemote(
            account: account, http: http ?? self.http, cache: cache, frameQueue: frameQueue,
            historyIdleMs: historyIdleMs,
            historyIdleCount: historyIdleCount, streamOpenMs: streamOpenMs,
            linkGraceMs: linkGraceMs,
            securityStorage: TestSecurity.find(server.url)?.store(account.deviceId))
        onClose { r.close() }
        return r
    }

    func client(
        cache: (any CipherCache)? = nil, http: HTTP? = nil, frameQueue: Int = 1024,
        historyIdleMs: Int64 = 60_000,
        historyIdleCount: Int = 8, linkGraceMs: Int64 = 10_000
    ) async throws -> Remote {
        try remote(
            try await join(), http: http, cache: cache, frameQueue: frameQueue,
            historyIdleMs: historyIdleMs,
            historyIdleCount: historyIdleCount, linkGraceMs: linkGraceMs)
    }
}

func session(_ cwd: String, _ title: String, status: PBSessionStatus = .idle) -> PBSession {
    var s = PBSession()
    s.cwd = cwd
    s.title = title
    s.status = status
    return s
}

func user(_ t: String) -> PBEvent {
    var e = PBEvent()
    e.userMessage.text = t
    return e
}

func agent(_ t: String) -> PBEvent {
    var e = PBEvent()
    e.agentMessage.text = t
    return e
}

private func interrupt(_ sid: String = "s1") -> PBCommand {
    var c = PBCommand()
    c.interrupt.sessionID = sid
    return c
}

/// What a test's listener heard from a terminal.
private final class Frames: TermListener, Sendable {
    let frames = Mailbox<PBTerminalFrame>()
    let closes = Mailbox<String>()
    func onFrame(_ frame: PBTerminalFrame) { frames.put(frame) }
    func onClose(_ reason: String) { closes.put(reason) }
}

extension ServerTests {
    /// The sync engine against the real Go server and a fake daemon.
    @Suite struct RemoteTests {
        private func world(_ body: (World) async throws -> Void) async throws {
            try await World.run(
                setUp: { w in
                    try await w.daemon.putMachine("fake-mac.local")
                    try await w.daemon.putSession("s1", session("/Users/me/Code/foo", "修复登录超时"))
                    try await w.daemon.upload("s1", user("你好"), agent("你好，有什么要做的？"), agent("第三条"))
                    try await w.daemon.connect()
                }, body)
        }

        @Test func storedPrefsRemainReadableAndEditableAfterTheirWriterIsRemoved() async throws {
            try await world { w in
                let writer = try await w.client()
                let reader = try await w.client()
                writer.start()
                _ = try await writer.awaitState("writer online") { $0.conn == .online }
                try await writer.changePrefs(.pin(path: "/stored"))
                try await w.phone.remove([writer.account.deviceId])
                try await reader.refreshTrustedDirectory()
                try await reader.readTrustedPrefs()
                #expect(reader.current.prefs?.pinned == ["/stored"])
                reader.start()
                _ = try await reader.awaitState("reader online") { $0.conn == .online }
                try await reader.changePrefs(.hide(path: "/next"))
                #expect(reader.current.prefs?.pinned == ["/stored"])
                _ = try await reader.awaitState("updated stored prefs") { $0.prefs?.hidden.contains("/next") == true }
            }
        }

        @Test func pollingRetriesDirectoryFailureEvenWhenAReadAlreadyInstalledTheHead() async throws {
            try await world { w in
                let victim = try await w.join()
                let fail = Signal()
                let attempts = Counter()
                let flaky = Intercept.http(for: w.server.url) { request in
                    if fail.isSet && request.url?.path.hasSuffix("/ListDevices") == true && attempts.increment() == 1 {
                        throw URLError(.networkConnectionLost)
                    }
                    return request
                }
                let r = try w.remote(try await w.join(), http: flaky)
                try await r.refreshTrustedDirectory()
                #expect(r.current.devices[victim.deviceId] != nil)
                try await w.phone.remove([victim.deviceId])
                let client = try await r.securitySession()
                _ = try await client.sync()
                fail.set()
                await r.startSecurityPolling(everyMs: 20)
                _ = try await r.awaitState("same-head directory retry", ms: 3000) { $0.devices[victim.deviceId] == nil }
                #expect(attempts.value >= 2)
            }
        }

        @Test func expiredPublicationStillAllowsTheStoredDirectoryToLoad() async throws {
            try await world { w in
                let account = try await w.join()
                let live = try w.remote(account)
                try await live.renameDevice(account.deviceId, name: "stored client name")
                let store = try #require(TestSecurity.find(w.server.url)?.store(account.deviceId))
                let record = try store.readSecurity()
                let expired = Int64(try #require(record.lease).notAfter)
                let reader = try Remote(account: account, http: w.http, clock: { expired },
                    securityStorage: TestSecurityStore(record))
                defer { reader.close() }
                try await reader.refreshTrustedDirectory()
                #expect(reader.current.devices[account.deviceId]?.meta?.name == "stored client name")
                await #expect(throws: DeviceSecurity.Failure.lease) {
                    try await reader.renameDevice(account.deviceId, name: "must not save")
                }
                try await reader.refreshTrustedDirectory()
                #expect(reader.current.devices[account.deviceId]?.meta?.name == "stored client name")
            }
        }

        @Test func archivedMachineLoadsAfterRestartFromRelayAndCache() async throws {
            try await world { w in
                let account = try await w.join()
                let cache = MemoryCache()
                let first = try w.remote(account, cache: cache)
                first.start()
                _ = try await first.awaitState("machine before removal") {
                    $0.machines[w.daemon.id]?.meta?.hostname == "fake-mac.local"
                }
                try await w.phone.remove([w.daemon.id])
                await first.refreshDevices()
                first.close()
                for store: (any CipherCache)? in [nil, cache] {
                    let restarted = try w.remote(account, cache: store)
                    restarted.start()
                    let state = try await restarted.awaitState("archived machine after restart") {
                        $0.machines[w.daemon.id]?.meta?.hostname == "fake-mac.local"
                            && $0.sessions["s1"] != nil
                    }
                    #expect(state.machines[w.daemon.id]?.online == false)
                    restarted.close()
                }
            }
        }

        @Test func aCheckpointBootstrapsWithoutTheHistoricalPrefix() async throws {
            try await world { w in
                var info=PBSession();info.id="checkpoint";info.title="current state";info.lastSeq=900
                var cp=PBLiveSnapshot();cp.session=info;cp.running=true
                info.currentCheckpoint=try cp.serializedData()
                try await w.daemon.putSession("checkpoint",info)
                let r=try await w.client();r.start()
                let state=try await r.awaitState("current checkpoint"){$0.sessions["checkpoint"]?.checkpointStatus=="ready"}
                #expect(state.sessions["checkpoint"]?.checkpoint?.session.lastSeq==900)
                #expect(state.sessions["checkpoint"]?.checkpoint?.running==true)
            }
        }
        @Test func aRemovedClientLearnsItAndStopsConnecting() async throws {
            try await world { w in
                let r = try await w.client()
                let other = try await w.client()
                r.start()
                other.start()
                _ = try await r.awaitState("online") {
                    $0.conn == .online && $0.devices[r.account.deviceId] != nil
                }
                _ = try await other.awaitState("online") {
                    $0.conn == .online && $0.devices[r.account.deviceId] != nil
                }
                try await w.phone.remove([r.account.deviceId])
                let st = try await r.awaitState("removed") { $0.removed }
                #expect(st.conn == .offline)
                // It stays put rather than reconnecting, also when asked to start.
                r.start()
                await sleepMs(1500)
                #expect(r.current.removed)
                #expect(r.current.conn == .offline)
                // Asked to try again, it is turned away again.
                r.retryLogin()
                _ = try await r.awaitState("trying") { !$0.removed }
                _ = try await r.awaitState("removed again") { $0.removed }
            }
        }

        @Test func aNewHeadRevokesMembersAndRetainsTheirSessionArchive() async throws {
            try await world { w in
                let security = try TestSecurity.get(w.server.url)
                let victim = try await w.join()
                // A prefs object the victim signed while it was a member.
                var h = PBHeader()
                h.kind = .accountPrefs
                h.requestID = projectPrefsID
                h.seq = 1
                var prefs = PBProjectPrefs()
                prefs.schema = 1
                let envelope = try security.seal(victim, h, prefs)
                let api = try Api(server: w.server.url, http: w.http)
                let token = try await login(api: api, account: victim)
                var put = PBPutAccountPrefsRequest()
                put.objectID = projectPrefsID
                put.expectedRevision = 0
                put.envelope = envelope
                _ = try await api.client.putAccountPrefs(
                    request: put, headers: Api.headers(token: token)
                ).value()

                let r = try await w.client()
                // Keep the stream and security polling stopped: this test must
                // learn the removal from the explicit head fetch alone. A push
                // refresh could otherwise supersede refreshDevices and make its
                // immediate assertions race the newer asynchronous refresh.
                let loaded = await r.refresh()
                #expect(loaded)
                #expect(r.current.devices[victim.deviceId] != nil)
                #expect(r.current.sessions["s1"] != nil)
                await r.loadPrefs()
                #expect(r.current.prefs?.revision == 1)

                // Without a removal push, the new head alone must drop the
                // member while retaining its sessions and readable prefs.
                try await w.phone.remove([victim.deviceId, w.daemon.id])
                await r.refreshDevices()
                #expect(r.current.devices[victim.deviceId] == nil)
                #expect(r.current.sessions["s1"] != nil)
                await r.loadPrefs()
                #expect(r.current.prefs?.revision == 1)
            }
        }

        @Test func activeHistorySurvivesExpiryAndTheSameFlowReloadsAfterReclamation() async throws {
            try await world { w in
                let r = try await w.client(cache: MemoryCache(), historyIdleMs: 100)
                r.start()
                let feed = r.events("s1")  // Followed before the session list exists.
                let follower = Task { for await _ in feed.updates() {} }
                _ = try await feed.await("3 events") { $0.events.count == 3 }
                let old = feed.value.events
                await sleepMs(350)
                #expect(feed.value.events.count == 3)
                follower.cancel()
                await follower.value
                try await eventually("the idle history to be reclaimed", ms: 5_000) {
                    feed.value.events.isEmpty
                }
                let restored = try await feed.await("the history again", ms: 5_000) {
                    $0.events.count == 3
                }
                #expect(restored.seqs == [1, 2, 3])
                // The snapshot handed out before did not change under its holder.
                #expect(old.count == 3)
                #expect(r.events("s1") === feed)
            }
        }

        @Test func idleBudgetDoesNotReclaimAnActiveConsumer() async throws {
            try await world { w in
                try await w.daemon.putSession("s2", session("/tmp", "two"))
                try await w.daemon.upload("s2", agent("two"))
                try await w.daemon.putSession("s3", session("/tmp", "three"))
                try await w.daemon.upload("s3", agent("three"))
                let r = try await w.client(cache: MemoryCache(), historyIdleCount: 1)
                r.start()
                let active = r.events("s1")
                let follower = Task { for await _ in active.updates() {} }
                defer { follower.cancel() }
                _ = try await active.await("3 events") { $0.events.count == 3 }
                let second = r.events("s2")
                _ = try await second.await("s2") { $0.events.count == 1 }
                let third = r.events("s3")
                _ = try await third.await("s3") { $0.events.count == 1 }
                try await eventually("s2 to be reclaimed", ms: 5_000) {
                    second.value.events.isEmpty
                }
                #expect(active.value.events.count == 3)
            }
        }

        @Test func aReclaimedLoadingFlowCanReloadWithoutWaitingForItsOldCacheRead() async throws {
            try await world { w in
                let base = MemoryCache()
                let source = try await w.client(cache: base)
                source.start()
                _ = try await source.awaitEvents("s1", 3)
                source.stop()
                let gate = Signal()
                let blocked = Signal()
                let calls = Counter()
                let cache = HookedCache(base)
                cache.hookEvents { sid in
                    if calls.increment() == 2 {
                        blocked.set()
                        await gate.wait()
                        return Array(await base.events(sessionId: sid).prefix(1))
                    }
                    return await base.events(sessionId: sid)
                }
                let r = try await w.client(cache: cache, historyIdleMs: 100)
                r.start()
                _ = try await r.awaitState("cached session") { $0.sessions["s1"] != nil }
                let feed = r.events("s1")
                var follower = Task { for await _ in feed.updates() {} }
                defer {
                    gate.set()
                    follower.cancel()
                }
                // The eager events(sid) call completes normally. Later loads are
                // triggered solely by following this same feed.
                _ = try await feed.await("3 events", ms: 5_000) { $0.events.count == 3 }
                follower.cancel()
                await follower.value
                try await eventually("reclaimed", ms: 5_000) { feed.value.events.isEmpty }
                follower = Task { for await _ in feed.updates() {} }
                #expect(await blocked.wait(5_000))
                follower.cancel()
                await follower.value
                await sleepMs(350)  // Reclaim the unobserved, still-suspended buffer.
                follower = Task { for await _ in feed.updates() {} }
                _ = try await feed.await("3 events again", ms: 5_000) { $0.events.count == 3 }
                #expect(calls.value >= 3)
                gate.set()
                await sleepMs(150)
                #expect(feed.value.seqs == [1, 2, 3])
            }
        }

        @Test func syncsListsEventsAndPushes() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                let st = try await r.awaitState("session meta") {
                    $0.conn == .online && $0.sessions["s1"]?.info != nil
                }
                let machine = try #require(st.machines[w.daemon.id])
                #expect(machine.meta?.hostname == "fake-mac.local")
                #expect(machine.online)
                let s = try #require(st.sessions["s1"])
                #expect(s.info?.title == "修复登录超时")
                #expect(s.info?.cwd == "/Users/me/Code/foo")
                #expect(s.machineId == w.daemon.id)

                let ev = try await r.awaitEvents("s1", 3)
                #expect(ev.seqs == [1, 2, 3])
                #expect(ev.events[0].userMessage.text == "你好")
                // A pushed event arrives in order, once.
                try await w.daemon.upload("s1", agent("第四条"))
                let ev4 = try await r.awaitEvents("s1", 4)
                #expect(ev4.seqs == [1, 2, 3, 4])
                // The longer history continues the shorter one: its pages are shared.
                #expect(ev4.events.extends(ev.events))
                // A newer meta revision replaces the older one.
                try await w.daemon.putSession("s1", session("/Users/me/Code/foo", "新的标题"))
                _ = try await r.awaitState("new title") { $0.sessions["s1"]?.info?.title == "新的标题" }
            }
        }

        @Test func forwardsCommandsAndReportsOffline() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                let id = w.daemon.id
                _ = try await r.awaitState("online machine") {
                    $0.conn == .online && $0.machines[id]?.online == true
                        && $0.sessions["s1"]?.info != nil
                }
                var send = PBCommand()
                send.send.sessionID = "s1"
                send.send.text = "跑一下测试"
                send.send.requestID = "q1"
                let res = try await r.command("s1", send)
                #expect(res.send.queuePosition == 1)
                let (h, cmd) = try #require(await w.daemon.commands.poll(5_000))
                #expect(h.sessionID == "s1")
                #expect(cmd.send.text == "跑一下测试")
                #expect(cmd.protocolVersion == protocolVersion)
                #expect(h.sender == r.account.deviceId)
                #expect(h.deadlineMs > Int64(Date().timeIntervalSince1970 * 1000))

                // The daemon's own error comes back as it is.
                var compact = PBCommand()
                compact.compact.sessionID = "s1"
                do {
                    try await r.command("s1", compact)
                    Issue.record("expected an error")
                } catch let e as CommandError {
                    #expect(e.code == "unimplemented")
                    #expect(e.fromDaemon)
                }

                await w.daemon.disconnect()
                _ = try await r.awaitState("offline machine") { $0.machines[id]?.online == false }
                do {
                    try await r.command("s1", interrupt())
                    Issue.record("expected unavailable")
                } catch let e as CommandError {
                    // Sealing is local now: the envelope was handed to the relay,
                    // so an offline machine leaves the outcome unknown.
                    #expect(e.code == CommandError.unknownOutcome)
                }
            }
        }

        private func refused(_ w: World, protocol version: UInt32) async throws -> CommandError {
            let r = try await w.client()
            r.start()
            let id = w.daemon.id
            _ = try await r.awaitState("online machine") {
                $0.conn == .online && $0.machines[id]?.online == true
                    && $0.sessions["s1"]?.info != nil
            }
            try await w.daemon.putMachine("fake-mac.local", protocol: version)
            _ = try await r.awaitState("machine protocol") {
                $0.machines[id]?.meta?.protocolVersion == version
            }
            do {
                try await r.command("s1", interrupt())
            } catch let e as CommandError {
                // Nothing was sent.
                #expect(await w.daemon.commands.poll(300) == nil)
                #expect(!e.fromDaemon)
                return e
            }
            throw TestFailure("expected a version error")
        }

        @Test func sendsNothingToAMachineOfAnOlderProtocol() async throws {
            try await world { w in
                let e = try await refused(w, protocol: protocolVersion - 1)
                #expect(e.code == "failed_precondition")
                #expect(e.message == "fake-mac 上的 remote-agent 版本较旧，请在电脑上更新后再试")
            }
        }

        @Test func sendsNothingToAMachineOfANewerProtocol() async throws {
            try await world { w in
                let e = try await refused(w, protocol: protocolVersion + 1)
                #expect(e.code == "failed_precondition")
                #expect(e.message == "这个 App 版本较旧，请更新后再试")
            }
        }

        @Test func streamsTheTerminal() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                let id = w.daemon.id
                _ = try await r.awaitState("session") {
                    $0.conn == .online && $0.sessions["s1"] != nil
                        && $0.machines[id]?.online == true
                }
                let heard = Frames()
                let conn = try await r.openTerminal("s1", listener: heard)
                let snap = try #require(await heard.frames.poll(5_000))
                #expect(snap.snapshot.cols == 120)
                #expect(try #require(await heard.frames.poll(5_000)).output == Data(" world".utf8))
                var a = PBTerminalFrame()
                a.input = Data("中文\r".utf8)
                var b = PBTerminalFrame()
                b.input = Data("\u{1b}[A".utf8)
                conn.send(a)
                conn.send(b)
                #expect(
                    try #require(await w.daemon.termInput.poll(5_000)).input == Data("中文\r".utf8))
                #expect(
                    try #require(await w.daemon.termInput.poll(5_000)).input
                        == Data("\u{1b}[A".utf8))
                conn.close()
                // Closed by this side: the listener is not told.
                #expect(await heard.closes.poll(300) == nil)
            }
        }

        @Test func aTerminalFrameOutOfOrderEndsTheStream() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                let id = w.daemon.id
                _ = try await r.awaitState("session") {
                    $0.conn == .online && $0.sessions["s1"] != nil
                        && $0.machines[id]?.online == true
                }
                await w.daemon.setReplayTerminal(true)
                let heard = Frames()
                _ = try await r.openTerminal("s1", listener: heard)
                #expect(try #require(await heard.frames.poll(5_000)).snapshot.cols == 120)
                #expect(try #require(await heard.frames.poll(5_000)).output == Data(" world".utf8))
                // The same number again: refused, and the stream is over, said once.
                #expect(await heard.closes.poll(5_000) == "终端帧顺序不对")
                #expect(await heard.frames.poll(300) == nil)
                #expect(await heard.closes.poll(300) == nil)
            }
        }

        @Test func aTerminalThatCannotBeReachedSaysSo() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                _ = try await r.awaitState("session") {
                    $0.conn == .online && $0.sessions["s1"] != nil
                }
                let heard = Frames()
                // No such session: nothing is opened.
                await #expect(throws: CommandError.self) {
                    try await r.openTerminal("nope", listener: heard)
                }
                w.server.stop()
                do {
                    _ = try await r.openTerminal("s1", listener: heard)
                    Issue.record("expected the terminal not to open")
                } catch {
                    #expect(errorText(error) == "连不上终端")
                }
                #expect(await heard.closes.poll(300) == nil)
            }
        }

        /// The server answers this client's pings, so a quiet stream stays up.
        @Test func aQuietSocketThatIsAnsweredStaysOpen() async throws {
            try await world { w in
                let acct = try await w.join()
                let api = try Api(server: w.server.url, http: w.http)
                var hello = PBPeerFrame()
                hello.hello.token = try await login(api: api, account: acct)
                let socket = Socket(
                    http: w.http, url: api.socketURL("/v1/stream"),
                    hello: try hello.serializedData(), pingMs: 100, sleep: realSleep)
                #expect(await socket.opened())
                let first = try PBServerFrame(serializedBytes: try await socket.receive())
                guard case .ready? = first.kind else {
                    Issue.record("expected Ready first")
                    return
                }
                // Ten pings later it is still there: each was answered before the next.
                let ended = Signal()
                Task {
                    // Read on, as the engine does: URLSession only notices an answer while something is being read.
                    while (try? await socket.receive()) != nil {}
                    _ = await socket.ended()
                    ended.set()
                }
                await sleepMs(1_000)
                #expect(!ended.isSet)
                socket.close()
                #expect(await ended.wait(5_000))
            }
        }

        @Test func pairsANewDevice() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                _ = try await r.awaitState("online") { $0.conn == .online }
                let pairing = try await PhonePairing.start(w.phone)
                let link = try #require(
                    PairLink.parse(pairing.link.url(web: "https://app.example.com")))
                #expect(link.code == pairing.code)
                let joinerEmoji = Mailbox<[String]>()
                let http = w.http
                async let joining = joinByPairing(
                    link: link, http: http, onEmoji: { joinerEmoji.put($0) }, stopped: { false },
                    persist: { acct, record in try TestSecurity.get(acct.server).install(record) },
                    persistRegistration: { _ in }, persistIdentity: { _ in }, confirm: { _ in true })
                let (emoji, st) = try await pairing.reveal()
                #expect(st.joinerKind == .client)
                try await pairing.approve()
                let acct = try await joining
                #expect(emoji.count == 4)
                #expect(joinerEmoji.all.suffix(1) == [emoji])
                #expect(acct.root.isEmpty)
                #expect(acct.server == w.server.url)
                #expect(acct.accountId == w.phone.pin.account)
                // The device that joined is the account's: it logs in and lists like any other.
                let joined = try w.remote(acct)
                joined.start()
                _ = try await joined.awaitState("the new device's lists") {
                    $0.conn == .online && $0.sessions["s1"]?.info != nil
                }
            }
        }

        @Test func comingBackReusesTheLogin() async throws {
            try await world { w in
                let logins = Counter()
                let counting = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/Login") == true { logins.increment() }
                    return request
                }
                let r = try w.remote(try await w.join(), http: counting)
                r.start()
                _ = try await r.awaitState("online") { $0.conn == .online }
                #expect(logins.value == 1)
                // The app went to the background and came back: no second login.
                r.stop()
                _ = try await r.awaitState("offline") { $0.conn == .offline }
                r.start()
                _ = try await r.awaitState("online again") { $0.conn == .online }
                #expect(logins.value == 1)
            }
        }

        /// A client whose logins are counted, and the logins it was told to keep, in order.
        private func counted(_ w: World, _ account: Account) throws -> (
            Remote, Counter, Mailbox<SavedLogin?>
        ) {
            let logins = Counter()
            let counting = Intercept.http(for: w.server.url) { request in
                if request.url?.path.hasSuffix("/Login") == true { logins.increment() }
                return request
            }
            return (try w.remote(account, http: counting), logins, Mailbox<SavedLogin?>())
        }

        @Test func aLoginKeptFromTheLastRunOpensTheStreamWithoutLoggingIn() async throws {
            try await world { w in
                let acct = try await w.join()
                let (first, logins, kept) = try counted(w, acct)
                first.keepLogin(nil) { kept.put($0) }
                first.start()
                _ = try await first.awaitState("online") { $0.conn == .online }
                let saved = try #require(await kept.poll(2000) ?? nil)
                #expect(
                    !saved.token.isEmpty
                        && saved.expiresMs > Int64(Date().timeIntervalSince1970 * 1000) + 86_400_000
                )
                #expect(logins.value == 1)
                first.close()

                // The app was started again: the same device, the login it kept.
                let (second, again, keptAgain) = try counted(w, acct)
                second.keepLogin(saved) { keptAgain.put($0) }
                second.start()
                let id = acct.deviceId
                _ = try await second.awaitState("online with the kept login") {
                    $0.conn == .online && $0.sessions["s1"]?.info != nil && $0.devices[id] != nil
                }
                #expect(again.value == 0)
                // Commands and lists go out under it too.
                try await second.renameDevice(acct.deviceId, name: "kept")
                #expect(again.value == 0)
                #expect(keptAgain.all.isEmpty)
            }
        }

        @Test func aKeptLoginTheServerRefusesIsDroppedAndReplaced() async throws {
            try await world { w in
                let acct = try await w.join()
                let (r, logins, kept) = try counted(w, acct)
                // A token the server never gave, as one it revoked or lost with its data: it lasts long by its own word.
                let stale = SavedLogin(
                    token: "not-a-token-of-this-server",
                    expiresMs: Int64(Date().timeIntervalSince1970 * 1000) + 30 * 86_400_000)
                r.keepLogin(stale) { kept.put($0) }
                r.start()
                // Refused with 1008: dropped first, then a login of the device's own.
                let dropped = await kept.poll(5000)
                #expect(dropped == .some(nil))
                r.reconnectNow()
                let fresh = try #require(await kept.poll(5000) ?? nil)
                #expect(fresh.token != stale.token)
                _ = try await r.awaitState("online after logging in") { $0.conn == .online }
                #expect(logins.value == 1)
            }
        }

        @Test func aKeptLoginAboutToRunOutIsNotUsed() async throws {
            try await world { w in
                let acct = try await w.join()
                let (first, _, kept) = try counted(w, acct)
                first.keepLogin(nil) { kept.put($0) }
                first.start()
                _ = try await first.awaitState("online") { $0.conn == .online }
                let saved = try #require(await kept.poll(2000) ?? nil)
                first.close()

                let (second, logins, keptAgain) = try counted(w, acct)
                let soon = SavedLogin(
                    token: saved.token,
                    expiresMs: Int64(Date().timeIntervalSince1970 * 1000) + 3_600_000)
                second.keepLogin(soon) { keptAgain.put($0) }
                second.start()
                _ = try await second.awaitState("online") { $0.conn == .online }
                #expect(logins.value == 1)
                let renewed = try #require(await keptAgain.poll(2000) ?? nil)
                #expect(renewed.expiresMs > soon.expiresMs)
            }
        }

        @Test func aDeviceTheServerNoLongerKnowsKeepsNoLogin() async throws {
            try await world { w in
                let acct = try await w.join()
                let (r, _, kept) = try counted(w, acct)
                r.keepLogin(nil) { kept.put($0) }
                r.start()
                _ = try await r.awaitState("online") { $0.conn == .online }
                #expect(await kept.poll(2000) != nil)
                // Another client removes it; its stream is closed and its login refused from then on.
                let other = try await w.client()
                other.start()
                _ = try await other.awaitState("online") { $0.conn == .online }
                try await w.phone.remove([acct.deviceId])
                _ = try await r.awaitState("removed", ms: 20_000) { $0.removed }
                #expect(kept.all.last == .some(nil))
            }
        }

        @Test func aKeptLoginDescribesItselfWithoutTheToken() {
            let login = SavedLogin(token: "bearer-secret-value", expiresMs: 1_800_000_000_000)
            var dumped = ""
            dump(login, to: &dumped)
            for text in ["\(login)", String(describing: login), String(reflecting: login), dumped] {
                #expect(!text.contains("bearer-secret-value"))
            }
        }

        @Test func aStreamWithoutReadyIsReplacedEvenThoughTheUpgradeSucceeded() async throws {
            try await world { w in
                let acct = try await w.join()
                let hole = try StreamBlackhole()
                w.onClose { hole.close() }
                // Only the stream goes to the fixture; auth and list RPCs use the real server.
                let real = w.server.url
                let routed = Intercept.http(for: hole.url) { $0.moved(to: real) }
                let r = try w.remote(
                    Account(
                        server: hole.url, root: acct.root, deviceSeed: acct.deviceSeed,
                        deviceId: acct.deviceId),
                    http: routed, streamOpenMs: 300)
                r.start()
                _ = try await r.awaitState("missing Ready timeout") {
                    $0.conn == .offline && $0.error.contains("超时")
                }
                r.reconnectNow()
                _ = try await r.awaitState("replacement ready") { $0.conn == .online }
                #expect(hole.attempts.value == 2)
            }
        }

        /// How many logins a client makes when its first stream is closed with `code` and the next one works.
        private func loginsWhenTheFirstStreamIsClosed(_ w: World, with code: Int) async throws
            -> Int
        {
            let acct = try await w.join()
            let hole = try StreamBlackhole(firstClose: code)
            w.onClose { hole.close() }
            let logins = Counter()
            let real = w.server.url
            let routed = Intercept.http(for: hole.url) { request in
                if request.url?.path.hasSuffix("/Login") == true { logins.increment() }
                return request.moved(to: real)
            }
            let r = try w.remote(
                Account(
                    server: hole.url, root: acct.root, deviceSeed: acct.deviceSeed,
                    deviceId: acct.deviceId), http: routed)
            r.start()
            let lost = try await r.awaitState("the closed stream") { $0.conn == .offline }
            #expect(lost.error == "与 server 的连接断开")
            r.reconnectNow()
            _ = try await r.awaitState("the next stream") { $0.conn == .online }
            #expect(hole.attempts.value == 2)
            return logins.value
        }

        @Test func aHelloRefusedWith1008DropsTheTokenAndLogsInAgain() async throws {
            try await world { w in
                let logins = try await loginsWhenTheFirstStreamIsClosed(w, with: policyViolation)
                #expect(logins == 2)
            }
        }

        @Test func aStreamClosedForAnotherReasonKeepsTheToken() async throws {
            try await world { w in
                let logins = try await loginsWhenTheFirstStreamIsClosed(w, with: 1011)
                #expect(logins == 1)
            }
        }

        @Test func aStreamThatOverflowsIsReplacedAndNothingIsLost() async throws {
            try await world { w in
                let acct = try await w.join()
                let proxy = try CountingProxy(to: w.server.port)
                w.onClose { proxy.close() }
                // Hold consumption until later pushes fill the one-frame queue.
                let cache = HookedCache()
                let hold = Signal(), blocked = Signal(), release = Signal()
                w.onClose { release.set() }
                cache.hookPutEvents { sid, events in
                    if hold.isSet && !release.isSet {
                        blocked.set()
                        await release.wait()
                    }
                    await cache.inner.putEvents(sessionId: sid, events: events)
                }
                let r = try w.remote(
                    Account(
                        server: proxy.url, root: acct.root, deviceSeed: acct.deviceSeed,
                        deviceId: acct.deviceId),
                    cache: cache, frameQueue: 1)
                r.start()
                _ = try await r.awaitEvents("s1", 3)
                try await eventually("initial events to be cached") {
                    await cache.inner.events(sessionId: "s1").count == 3
                }
                hold.set()
                try await w.daemon.upload("s1", agent("快速的第 0 条"))
                #expect(await blocked.wait(10_000))
                for i in 1..<30 { try await w.daemon.upload("s1", agent("快速的第 \(i) 条")) }
                release.set()
                try await eventually("overflow to replace the stream") { proxy.streams.value >= 2 }
                let ev = try await r.awaitEvents("s1", 33, ms: 40_000)
                #expect(ev.seqs == Array(1...33))
                // The queue did overflow: the stream was dropped and made again, and history filled the gap.
                #expect(proxy.streams.value >= 2)
                try await eventually("every event to be cached") {
                    await cache.inner.events(sessionId: "s1").count == 33
                }
            }
        }

        @Test func repeatedNetworkCallbacksDuringLoginLeaveOnlyOneCurrentStream() async throws {
            try await world { w in
                let acct = try await w.join()
                let proxy = try CountingProxy(to: w.server.port)
                let gate = Signal()
                let entered = Signal()
                let challenges = Counter()
                w.onClose {
                    gate.set()
                    proxy.close()
                }
                let blocked = Intercept.http(for: proxy.url) { request in
                    if request.url?.path.hasSuffix("/GetChallenge") == true {
                        challenges.increment()
                        entered.set()
                        while !gate.isSet { try await Task.sleep(nanoseconds: 10_000_000) }
                    }
                    return request
                }
                let r = try w.remote(
                    Account(
                        server: proxy.url, root: acct.root, deviceSeed: acct.deviceSeed,
                        deviceId: acct.deviceId),
                    http: blocked)
                r.start()
                #expect(await entered.wait(5_000))
                for _ in 0..<4 { r.reconnectNow() }
                await sleepMs(200)
                gate.set()
                _ = try await r.awaitState("one online stream") { $0.conn == .online && $0.loaded }
                await sleepMs(200)
                #expect(proxy.streams.value == 1)
                #expect(challenges.value >= 1)
            }
        }

        @Test func aLoginThatDoesNotReturnTimesOutAndCanRecover() async throws {
            try await world { w in
                let acct = try await w.join()
                let unblocked = Signal()
                let attempts = Counter()
                w.onClose { unblocked.set() }
                let slow = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/GetChallenge") == true {
                        attempts.increment()
                        while !unblocked.isSet { try await Task.sleep(nanoseconds: 10_000_000) }
                    }
                    return request
                }
                let r = try w.remote(acct, http: slow, streamOpenMs: 300)
                r.start()
                let off = try await r.awaitState("login timeout") { $0.conn == .offline }
                #expect(off.error == "连接超时")
                #expect(!r.current.removed)
                unblocked.set()
                r.reconnectNow()
                _ = try await r.awaitState("login recovery") { $0.conn == .online }
                #expect(attempts.value >= 2)
            }
        }

        @Test func networkChangeReplacesAnOnlineStreamAndCatchesUpExactlyOnce() async throws {
            try await world { w in
                let acct = try await w.join()
                let proxy = try CountingProxy(to: w.server.port)
                w.onClose { proxy.close() }
                let r = try w.remote(
                    Account(
                        server: proxy.url, root: acct.root, deviceSeed: acct.deviceSeed,
                        deviceId: acct.deviceId))
                r.start()
                _ = try await r.awaitEvents("s1", 3)
                r.reconnectNow()
                _ = try await r.awaitState("network replacement") { $0.conn == .connecting }
                try await w.daemon.upload("s1", agent("during replacement"))
                let events = try await r.awaitEvents("s1", 4)
                #expect(events.seqs == [1, 2, 3, 4])
                _ = try await r.awaitState("online again") { $0.conn == .online }
                #expect(proxy.streams.value == 2)
            }
        }

        @Test func reconnectsWithoutLosingOrRepeatingEvents() async throws {
            try await world { w in
                let cache = MemoryCache()
                let r = try await w.client(cache: cache)
                r.start()
                _ = try await r.awaitEvents("s1", 3)
                w.server.stop()
                _ = try await r.awaitState("offline") { $0.conn != .online }
                try await w.server.start()
                try await w.daemon.login()
                try await w.daemon.connect()
                try await w.daemon.upload("s1", agent("断线期间"), agent("又一条"))
                _ = try await r.awaitState("online", ms: 40_000) { $0.conn == .online }
                let ev = try await r.awaitEvents("s1", 5)
                #expect(ev.seqs == [1, 2, 3, 4, 5])

                // A new run shows the cached history before it is online.
                r.stop()
                w.server.stop()
                let again = try w.remote(r.account, cache: cache)
                again.start()
                _ = try await again.awaitState("cached session") { $0.sessions["s1"]?.info != nil }
                let cached = try await again.awaitEvents("s1", 5)
                #expect(cached.events.count == 5)
                #expect(again.current.machines[w.daemon.id]?.online == false)
            }
        }

        @Test func aSlowCacheWriteDoesNotRollBackNewerMetadata() async throws {
            try await world { w in
                let gate = Signal()
                let blocked = Signal()
                let cache = HookedCache()
                cache.hookPutEvents { _, _ in
                    blocked.set()
                    await gate.wait()
                }
                w.onClose { gate.set() }
                let r = try await w.client(cache: cache)
                r.start()
                _ = try await r.awaitState("session") {
                    $0.conn == .online && $0.sessions["s1"]?.info != nil
                }
                // Loading the history writes it to the cache, which hangs here.
                _ = r.events("s1")
                #expect(await blocked.wait(10_000))
                // Meanwhile newer metadata arrives through the stream.
                try await w.daemon.putSession("s1", session("/Users/me/Code/foo", "新的标题"))
                _ = try await r.awaitState("new title") { $0.sessions["s1"]?.info?.title == "新的标题" }
                gate.set()
                _ = try await r.awaitEvents("s1", 3)
                await sleepMs(200)
                #expect(r.current.sessions["s1"]?.info?.title == "新的标题")
            }
        }

        @Test func aSlowCacheReadDoesNotOverwriteTheListsOfALaterConnection() async throws {
            try await world { w in
                let base = MemoryCache()
                let source = try await w.client(cache: base)
                source.start()
                _ = try await source.awaitState("lists") {
                    $0.conn == .online && $0.sessions["s1"]?.info != nil
                }
                source.stop()
                let gate = Signal()
                let blocked = Signal()
                let cache = HookedCache(base)
                cache.hookMachines {
                    blocked.set()
                    await gate.wait()
                }
                w.onClose { gate.set() }
                let r = try await w.client(cache: cache)
                r.start()
                #expect(await blocked.wait(10_000))
                // The app went to the background and came back, and the network changed, while the cache was being read.
                r.stop()
                r.start()
                r.setNetwork(false)
                r.setNetwork(true)
                await sleepMs(1_000)
                #expect(r.current.conn != .online)
                gate.set()
                let mid = w.daemon.id
                _ = try await r.awaitState("the machine online") {
                    $0.conn == .online && $0.machines[mid]?.online == true
                }
                await sleepMs(200)
                #expect(r.current.machines[mid]?.online == true)
            }
        }

        @Test func aFrozenAttemptResendsTheSameRequest() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                let id = w.daemon.id
                _ = try await r.awaitState("online machine") {
                    $0.conn == .online && $0.machines[id]?.online == true
                }
                let a = Attempt()
                let first = try a.next {
                    try r.prepare(machineId: id, sessionId: "s1", interrupt())
                }
                try await r.forward(first)
                // Pretend the answer was lost: the retry carries the same request ID and deadline.
                a.settle(CommandError(CommandError.unknownOutcome, "lost"))
                _ = try await w.join()  // Membership advances; retry must reauthorize the same intent.
                try await r.forward(
                    try a.next { try r.prepare(machineId: id, sessionId: "s1", interrupt()) })
                let (h1, _) = try #require(await w.daemon.commands.poll(5_000))
                let (h2, _) = try #require(await w.daemon.commands.poll(5_000))
                #expect(h1.requestID == h2.requestID)
                #expect(h1.deadlineMs == h2.deadlineMs)
            }
        }

        @Test func anUnknownOutcomeStaysUnknownWhenTheMachineThenGoesOffline() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                let daemon = w.daemon
                _ = try await r.awaitState("online machine") {
                    $0.conn == .online && $0.machines[daemon.id]?.online == true
                }
                // The daemon takes the command and drops off before answering: it may
                // have run. The retry then finds the machine offline, which says
                // nothing about the first try.
                await daemon.setAnswer { _, c in
                    await daemon.disconnect()
                    return FakeDaemon.defaultResult(c)
                }
                do {
                    try await r.forward(
                        try r.prepare(machineId: daemon.id, sessionId: "s1", interrupt()))
                    Issue.record("expected an error")
                } catch let e as CommandError {
                    #expect(e.code == CommandError.unknownOutcome)
                }
            }
        }

        @Test func aFailedLoginWhileRetryingAnUnknownCommandKeepsItUnknown() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                _ = try await r.awaitState("online") { $0.conn == .online }
                let p = try r.prepare(machineId: w.daemon.id, sessionId: "s1", interrupt())
                // The login is refused from here on: the retry is told to log in
                // again, and that fails. The command it retries may have run.
                try await w.phone.remove([r.account.deviceId])
                do {
                    try await r.forward(p, retryOfUnknown: true)
                    Issue.record("expected an error")
                } catch let e as CommandError {
                    #expect(e.code == CommandError.unknownOutcome)
                }
            }
        }

        @Test func aResultThatDoesNotOpenLeavesTheOutcomeUnknown() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                let id = w.daemon.id
                _ = try await r.awaitState("online machine") {
                    $0.conn == .online && $0.machines[id]?.online == true
                }
                await w.daemon.setGarbleResults(true)
                let a = Attempt()
                let first = try a.next {
                    try r.prepare(machineId: id, sessionId: "s1", interrupt())
                }
                var shown: Error?
                do {
                    try await r.forward(first)
                } catch {
                    shown = a.settle(error)
                }
                // The daemon did get it; its answer could not be read. Retry the same.
                #expect((shown as? CommandError)?.unknown == true)
                #expect(a.frozen)
                #expect(
                    try a.next { try r.prepare(machineId: id, sessionId: "s1", interrupt()) }
                        === first)
            }
        }

        @Test func aServerOutOfReachIsNamedOnlyAfterAMoment() async throws {
            try await world { w in
                let r = try await w.client(linkGraceMs: 300)
                r.start()
                let on = try await r.awaitState("session") {
                    $0.link == .online && $0.sessions["s1"]?.info != nil
                }
                #expect(!on.unlinked)
                w.server.stop()
                // Just lost: it is being remade, and what was known still shows.
                let lost = try await r.awaitState("lost") { $0.conn != .online }
                #expect(lost.link == .waiting)
                #expect(!lost.unlinked)
                #expect(lost.offlineSinceMs > 0)
                let out = try await r.awaitState("out of reach") { $0.link == .noServer }
                #expect(out.unlinked)
                try await w.server.start()
                r.reconnectNow()
                _ = try await r.awaitState("back") { $0.link == .online && !$0.unlinked }
            }
        }

        @Test func aDeviceWithoutANetworkSaysSoAtOnce() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                _ = try await r.awaitState("online") { $0.link == .online }
                // The server is still there: the system's word alone ends the connection.
                w.server.stop()
                r.setNetwork(false)
                let off = try await r.awaitState("no network") { $0.link == .noNetwork }
                #expect(off.unlinked)
                try await w.server.start()
                r.setNetwork(true)
                _ = try await r.awaitState("back", ms: 20_000) { $0.link == .online }
            }
        }

        @Test func aHistoryThatFailsToLoadSaysSoOnTheSession() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                _ = try await r.awaitState("session") {
                    $0.conn == .online && $0.sessions["s1"]?.info != nil
                }
                w.server.stop()
                // Which comes first is a race the client does not decide: the read failing, which is
                // said on the session, or the stream dropping, after which a failed read is not said
                // apart (architecture 「断线时显示什么」) and the next connection reads again.
                let events = r.events("s1")
                try await eventually("the read to fail or the stream to drop", ms: 20_000) {
                    events.value.fetchError != nil || r.current.conn != .online
                }
                if let said = events.value.fetchError { #expect(said.hasPrefix("没能读取历史")) }
                // Back again, a retry loads it and clears the error.
                try await w.server.start()
                r.retryEvents("s1")
                let ok = try await events.await("the history", ms: 20_000) {
                    $0.events.count == 3 && $0.fetchError == nil
                }
                #expect(ok.events.count == 3)
            }
        }

        @Test func aCachedEventThatDoesNotOpenIsDroppedAndFetchedAgain() async throws {
            try await world { w in
                let cache = MemoryCache()
                let source = try await w.client(cache: cache)
                source.start()
                _ = try await source.awaitEvents("s1", 3)
                try await eventually("the events cached") {
                    await cache.events(sessionId: "s1").count == 3
                }
                source.close()
                let good = try #require(await cache.events(sessionId: "s1").first { $0.seq == 2 })
                cache.damage(sessionId: "s1", seq: 2) {
                    $0.envelope.ciphertext = Data($0.envelope.ciphertext.reversed())
                }

                let r = try await w.client(cache: cache)
                r.start()
                let ev = try await r.events("s1").await("the whole history") {
                    $0.loaded && $0.events.count == 3
                }
                #expect(ev.seqs == [1, 2, 3])
                // Nothing is missing, so nothing says so; and the next opening does not meet the bad copy again.
                #expect(ev.error == nil)
                try await eventually("the cache filled again") {
                    await cache.events(sessionId: "s1").count == 3
                }
                let again = try #require(await cache.events(sessionId: "s1").first { $0.seq == 2 })
                #expect(again.envelope == good.envelope)
            }
        }

        @Test func stoppingDuringLoginLeavesTheStreamClosed() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                r.stop()
                await sleepMs(3000)
                #expect(r.current.conn != .online)
            }
        }

        @Test func aReaderThatFallsBehindCatchesUpFromHistory() async throws {
            try await world { w in
                let r = try await w.client(frameQueue: 1)
                r.start()
                _ = try await r.awaitEvents("s1", 3)
                for i in 0..<40 { try await w.daemon.upload("s1", agent("快速的第 \(i) 条")) }
                let ev = try await r.awaitEvents("s1", 43, ms: 40_000)
                #expect(ev.seqs == Array(1...43))
            }
        }

        @Test func fetchesAnImageAndCachesOnlyItsEnvelope() async throws {
            try await world { w in
                let cache = MemoryCache()
                let r = try await w.client(cache: cache)
                r.start()
                _ = try await r.awaitState("session") {
                    $0.conn == .online && $0.sessions["s1"]?.info != nil
                }
                let png = Data([0x89, 0x50, 0x4E, 0x47, 13, 10, 26, 10, 1, 2, 3])
                var payload = PBImagePayload()
                payload.format = .png
                payload.width = 1
                payload.height = 1
                payload.data = png
                try await w.daemon.putImage("s1", "00000000000000000000000000000001", payload)
                let got = try await r.image("s1", imageId: "00000000000000000000000000000001")
                guard case .loaded(let loaded) = got else {
                    Issue.record("expected the image, got \(got)")
                    return
                }
                #expect(loaded.data == png)
                let legacyID = "00000000000000000000000000000003"
                let legacyKey = try #require(await w.daemon.sessionKeys["s1"])
                var legacyHeader = PBHeader()
                legacyHeader.kind = .image
                legacyHeader.machineID = w.daemon.id
                legacyHeader.sessionID = "s1"
                legacyHeader.requestID = legacyID
                let legacy = try Envelopes.seal(
                    key: legacyKey, header: legacyHeader, message: payload)
                await cache.putImage(key: "s1/" + legacyID, envelope: try legacy.serializedData())
                #expect(try await r.image("s1", imageId: legacyID) == .legacy(payload))
                // What the cache holds is the sealed envelope, not the picture.
                let stored = try #require(cache.images["s1/00000000000000000000000000000001"])
                #expect(stored.range(of: png) == nil)
                // The second time it comes from the cache, and is opened and checked again.
                #expect(try await r.image("s1", imageId: "00000000000000000000000000000001") == got)
                #expect(
                    try await r.image("s1", imageId: "ffffffffffffffffffffffffffffffff") == .missing
                )
                // A format that does not match its bytes is refused.
                var wrong = PBImagePayload()
                wrong.format = .jpeg
                wrong.data = png
                try await w.daemon.putImage("s1", "00000000000000000000000000000002", wrong)
                guard
                    case .corrupt = try await r.image(
                        "s1", imageId: "00000000000000000000000000000002")
                else {
                    Issue.record("expected a corrupt image")
                    return
                }
            }
        }

        @Test func concurrentPreferenceCASReappliesBothOperations() async throws {
            try await world { w in
                let gate = PreferenceWriterBarrier()
                let racing = Intercept.http(for:w.server.url) { request in
                    if request.url?.path.hasSuffix("/PutAccountPrefs") == true { await gate.arrive() }
                    return request
                }
                let a=try w.remote(try await w.join(),http:racing)
                let b=try w.remote(try await w.join(),http:racing)
                a.start();b.start()
                _=try await a.awaitState("a online"){$0.conn == .online}
                _=try await b.awaitState("b online"){$0.conn == .online}
                async let first:Void = a.changePrefs(.pin(path:"/synthetic/a"))
                async let second:Void = b.changePrefs(.hide(path:"/synthetic/b"))
                _=try await(first,second)
                let state=try await a.awaitState("both CAS operations") {
                    $0.prefs?.pinned.contains("/synthetic/a") == true && $0.prefs?.hidden.contains("/synthetic/b") == true
                }
                #expect(state.prefs?.revision == 2)
            }
        }

        @Test func twoClientsChangingProjectsKeepBothChanges() async throws {
            try await world { w in
                let a = try await w.client()
                let b = try await w.client()
                a.start()
                b.start()
                _ = try await a.awaitState("a prefs") { $0.conn == .online && $0.prefs != nil }
                _ = try await b.awaitState("b prefs") { $0.conn == .online && $0.prefs != nil }
                try await a.changePrefs(.pin(path: "/Users/me/Code/foo"))
                // b has not seen a's write yet when it writes: the server refuses, b redoes its change on a's.
                try await b.changePrefs(.hide(path: "/Users/me/Code/billing"))
                let sa = try await a.awaitState("a sees both") {
                    $0.prefs?.hidden.contains("/Users/me/Code/billing") == true
                }
                #expect(sa.prefs?.pinned == ["/Users/me/Code/foo"])
                #expect(sa.prefs?.revision == 2)
            }
        }

        @Test func preferencesThatCouldNotBeReadAreReadAgainWhileTheConnectionHolds() async throws {
            try await world { w in
                let reads = Counter()
                let flaky = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/ListDevices") == true,
                        reads.increment() == 1
                    {
                        throw URLError(.networkConnectionLost)
                    }
                    return request
                }
                let r = try w.remote(try await w.join(), http: flaky)
                r.start()
                _ = try await r.awaitState("online") { $0.conn == .online && $0.synced }
                try await eventually("the first read") { reads.value >= 1 }
                // The stream stayed up throughout: nothing but the read itself brings them.
                let st = try await r.awaitState("preferences") { $0.prefs != nil }
                #expect(st.conn == .online)
                #expect(prefsBlocked(st) == nil)
                #expect(reads.value >= 2)
            }
        }

        @Test func aClientsNameReachesTheOtherClients() async throws {
            try await world { w in
                let a = try await w.client()
                let b = try await w.client()
                a.start()
                b.start()
                let id = a.account.deviceId
                _ = try await a.awaitState("a devices") {
                    $0.conn == .online && $0.devices[id] != nil
                }
                _ = try await b.awaitState("b sees a") { $0.devices[id] != nil }
                try await a.describeSelf(platform: "ios", description: "iOS 26 · iPhone")
                try await a.renameDevice(id, name: "我的手机")
                let sb = try await b.awaitState("b sees the name") {
                    $0.devices[id]?.meta?.name == "我的手机"
                }
                // Naming it kept what it said of itself.
                #expect(sb.devices[id]?.meta?.platform == "ios")
                #expect(sb.devices[id]?.kind == .client)
            }
        }

        @Test func aDeviceListInFlightDoesNotUndoWhatWasPushedMeanwhile() async throws {
            try await world { w in
                let hold = Signal()
                let intercepted = Counter()
                let held = Signal()
                let gate = Signal()
                w.onClose { gate.set() }
                let slow = Intercept.http(
                    for: w.server.url,
                    answered: { request in
                        guard hold.isSet, request.url?.path.hasSuffix("/ListDevices") == true,
                            intercepted.increment() == 1
                        else { return }
                        held.set()
                        await gate.wait()
                    }
                ) { $0 }
                let a = try w.remote(try await w.join(), http: slow)
                let other = try await w.client()
                let leaving = try await w.join()
                a.start()
                other.start()
                _ = try await a.awaitState("a devices") {
                    $0.conn == .online && $0.devices[leaving.deviceId] != nil
                }
                _ = try await other.awaitState("other online") { $0.conn == .online }
                // The server has answered with the list as it is now; the answer is still on its way.
                hold.set()
                let reading = Task { await a.refreshDevices() }
                #expect(await held.wait(10_000))
                try await w.phone.remove([leaving.deviceId])
                let joined = try await w.join()
                _ = try await a.awaitState("both pushes") {
                    $0.devices[leaving.deviceId] == nil && $0.devices[joined.deviceId] != nil
                }
                gate.set()
                await reading.value
                #expect(a.current.devices[leaving.deviceId] == nil)
                #expect(a.current.devices[joined.deviceId] != nil)
                #expect(a.current.devices[a.account.deviceId] != nil)
            }
        }

        @Test func measuresTheWayToTheServerAndToAComputer() async throws {
            try await world { w in
                let r = try await w.client()
                r.start()
                let id = w.daemon.id
                _ = try await r.awaitState("online machine") {
                    $0.conn == .online && $0.machines[id]?.online == true
                }
                #expect(try await r.pingServer() >= 0)
                // The fake daemon has no settings: its refusal is still an answer that made the round trip.
                do {
                    _ = try await r.pingMachine(id)
                    Issue.record("the fake daemon answers settings with an error")
                } catch let e as CommandError {
                    #expect(e.fromDaemon)
                }
                w.server.stop()
                await #expect(throws: (any Error).self) { try await r.pingServer(timeoutMs: 2_000) }
            }
        }
    }
}

private actor PreferenceWriterBarrier {
    private var count=0
    private var waiting:CheckedContinuation<Void,Never>?
    func arrive() async {
        count += 1
        if count == 1 { await withCheckedContinuation {waiting=$0} }
        else if count == 2 {waiting?.resume();waiting=nil}
    }
}
