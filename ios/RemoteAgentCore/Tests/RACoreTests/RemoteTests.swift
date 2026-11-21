import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.enabled(if: GoServer.available, GoServer.missing), .timeLimit(.minutes(2)))
struct ServerTests {}

@Suite(.enabled(if: GoServer.available, GoServer.missing), .timeLimit(.minutes(2)))
struct SyncServerTests {}

/// Independent worlds overlap network waits, with at most four pairs of Go
/// processes starting at once. Each world owns its port, database and keys.
private actor WorldSlots {
    var available = 4
    var waiting: [CheckedContinuation<Void, Never>] = []

    func acquire() async {
        if available > 0 { available -= 1 }
        else { await withCheckedContinuation { waiting.append($0) } }
    }

    func release() {
        if waiting.isEmpty { available += 1 }
        else { waiting.removeFirst().resume() }
    }
}

/// A server, a fake daemon with one machine, and what a test opened and must close.
final class World: @unchecked Sendable {
    private static let slots = WorldSlots()
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
        await slots.acquire()
        do {
            let w = try await World()
            do {
                try await setUp(w)
                try await body(w)
            } catch {
                await w.close()
                throw error
            }
            await w.close()
        } catch {
            await slots.release()
            throw error
        }
        await slots.release()
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
        frameQueue: Int = 1024, frameQueueBytes: Int = 32 << 20, streamOpenMs: Int64 = 15_000
    ) async throws -> Remote {
        let r = try fixtureRemote(
            account: account, http: http ?? self.http, cache: cache, frameQueue: frameQueue,
            frameQueueBytes: frameQueueBytes, streamOpenMs: streamOpenMs,
            securityStorage: TestSecurity.find(server.url)?.store(account.deviceId))
        onClose { r.close() }
        return r
    }

    func client(
        cache: (any CipherCache)? = nil, http: HTTP? = nil, frameQueue: Int = 1024,
        frameQueueBytes: Int = 32 << 20
    ) async throws -> Remote {
        try await remote(
            try await join(), http: http, cache: cache, frameQueue: frameQueue,
            frameQueueBytes: frameQueueBytes)
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

/// What a test's listener heard from a terminal.
private final class Frames: TermListener, Sendable {
    let frames = Mailbox<PBTerminalFrame>()
    let closes = Mailbox<String>()
    func onFrame(_ frame: PBTerminalFrame) { frames.put(frame) }
    func onClose(_ reason: String) { closes.put(reason) }
}

private extension Remote {
    func hydrateExecutorState() async throws {
        _ = await ensureToken(opId: 1, forceLogin: false, timeoutMs: 15_000)
        _ = await syncSecuritySnapshot(opId: 2, networkGeneration: 0, touch: false)
        let fetched = await executeRpc(PBCoreRpcEffect.with {
            $0.opID = 3
            $0.fetchLists = PBCoreFetchListsRpc.with { $0.limit = 50 }
        }).listsFetched
        var s = RemoteState()
        s.conn = .online
        s.link = .online
        s.synced = true
        for m in fetched.machines {
            s.machines[m.machineID] = MachineView(
                id: m.machineID, online: m.online, lastSeenMs: m.lastSeenMs,
                meta: m.hasMeta ? m.meta : nil, metaRev: m.metaRev)
            s.currentMachines.insert(m.machineID)
        }
        for sess in fetched.sessions {
            s.sessions[sess.sessionID] = SessionView(
                id: sess.sessionID, machineId: sess.machineID,
                info: sess.hasMeta ? sess.meta : nil, metaRev: sess.metaRev,
                lastSeq: sess.lastSeq, updatedMs: sess.updatedMs, listedEndedMs: sess.listedEndedMs)
        }
        for dev in fetched.devices {
            s.devices[dev.deviceID] = DeviceView(
                id: dev.deviceID, kind: dev.kind, createdMs: dev.createdMs,
                meta: dev.hasMeta ? dev.meta : nil, metaRev: dev.metaRev, lastSeenMs: dev.lastSeenMs)
        }
        publishSyncState(s)
    }
}

extension SyncServerTests {
    /// Executor I/O, security gate, terminal streaming, pairing, and image/ping tests against Go test server.
    @Suite(.timeLimit(.minutes(2))) struct RemoteTests {
        private func world(_ body: (World) async throws -> Void) async throws {
            try await World.run(
                setUp: { w in
                    try await w.daemon.putMachine("fake-mac.local")
                    try await w.daemon.putSession("s1", session("/Users/me/Code/foo", "修复登录超时"))
                    try await w.daemon.upload("s1", user("你好"), agent("你好，有什么要做的？"), agent("第三条"))
                    try await w.daemon.connect()
                }, body)
        }

        @Test func streamsTheTerminal() async throws {
            try await world { w in
                let r = try await w.client()
                try await r.hydrateExecutorState()
                let heard = Frames()
                let conn = try await r.openTerminal("s1", listener: heard)
                let snap = try await heard.frames.take()
                #expect(snap.snapshot.cols == 120)
                #expect(try await heard.frames.take().output == Data(" world".utf8))
                var a = PBTerminalFrame()
                a.input = Data("中文\r".utf8)
                var b = PBTerminalFrame()
                b.input = Data("\u{1b}[A".utf8)
                conn.send(a)
                conn.send(b)
                #expect(try await w.daemon.termInput.take().input == Data("中文\r".utf8))
                #expect(try await w.daemon.termInput.take().input == Data("\u{1b}[A".utf8))
                conn.close()
            }
        }

        @Test func aTerminalFrameOutOfOrderEndsTheStream() async throws {
            try await world { w in
                let r = try await w.client()
                try await r.hydrateExecutorState()
                await w.daemon.setReplayTerminal(true)
                let heard = Frames()
                _ = try await r.openTerminal("s1", listener: heard)
                #expect(try await heard.frames.take().snapshot.cols == 120)
                #expect(try await heard.frames.take().output == Data(" world".utf8))
                #expect(try await heard.closes.take() == L10n.errorsTerminalFramesAreOutOfOrder())
            }
        }

        @Test func aTerminalThatCannotBeReachedSaysSo() async throws {
            try await world { w in
                let r = try await w.client()
                try await r.hydrateExecutorState()
                let heard = Frames()
                await #expect(throws: CommandError.self) {
                    try await r.openTerminal("nope", listener: heard)
                }
                w.server.stop()
                do {
                    _ = try await r.openTerminal("s1", listener: heard)
                    Issue.record("expected the terminal not to open")
                } catch {
                    #expect(errorText(error) == L10n.errorsCannotConnectToTerminal())
                }
                #expect(heard.closes.all.isEmpty)
            }
        }

        /// The server answers this client's pings, so a quiet stream stays up.
        @Test func aQuietSocketThatIsAnsweredStaysOpen() async throws {
            try await world { w in
                let acct = try await w.join()
                let api = try Api(server: w.server.url, http: w.http)
                var hello = PBPeerFrame()
                hello.hello.token = try await login(api: api, account: acct)
                let http = HTTP(), ready = Signal(), answered = Signal()
                let ticks = Counter()
                let parked = AsyncStream<Void>.makeStream()
                defer { parked.continuation.finish() }
                let socket = Socket(
                    http: http, url: api.socketURL("/v1/stream"),
                    hello: try hello.serializedData(), sleep: { ms in
                        #expect(ms == streamPingMs)
                        try #require(await ready.wait())
                        let task = try #require(await http.session.allTasks.first as? URLSessionWebSocketTask)
                        try await withCheckedThrowingContinuation { (done: CheckedContinuation<Void, Error>) in
                            task.sendPing { error in
                                if let error { done.resume(throwing: error) }
                                else { done.resume() }
                            }
                        }
                        if ticks.increment() == 11 {
                            answered.set()
                            for await _ in parked.stream { break }
                            throw CancellationError()
                        }
                    })
                defer { socket.close() }
                try #require(await socket.opened())
                let first = try PBServerFrame(serializedBytes: try await socket.receive())
                guard case .ready? = first.kind else {
                    Issue.record("expected Ready first")
                    return
                }
                let ended = Signal()
                let reader = Task {
                    while (try? await socket.receive()) != nil {}
                    _ = await socket.ended()
                    ended.set()
                }
                defer { reader.cancel() }
                ready.set()
                #expect(await answered.wait())
                #expect(!ended.isSet)
                socket.close()
                #expect(await ended.wait())
                #expect(await socket.ended().error == nil)
            }
        }

        @Test func pairsANewDevice() async throws {
            try await world { w in
                let r = try await w.client()
                try await r.hydrateExecutorState()
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
                let joined = try await w.remote(acct)
                try await joined.hydrateExecutorState()
                #expect(joined.current.sessions["s1"]?.info != nil)
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

        @Test func fetchesAnImageAndCachesOnlyItsEnvelope() async throws {
            try await world { w in
                let cache = MemoryCache()
                let r = try await w.client(cache: cache)
                try await r.hydrateExecutorState()
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

                let stored = try #require(cache.images["s1/00000000000000000000000000000001"])
                #expect(stored.range(of: png) == nil)
                #expect(try await r.image("s1", imageId: "00000000000000000000000000000001") == got)
                #expect(try await r.image("s1", imageId: "ffffffffffffffffffffffffffffffff") == .missing)
                var wrong = PBImagePayload()
                wrong.format = .jpeg
                wrong.data = png
                try await w.daemon.putImage("s1", "00000000000000000000000000000002", wrong)
                guard case .corrupt = try await r.image("s1", imageId: "00000000000000000000000000000002") else {
                    Issue.record("expected a corrupt image")
                    return
                }
            }
        }

        @Test func measuresTheWayToTheServerAndToAComputer() async throws {
            try await world { w in
                let r = try await w.client()
                try await r.hydrateExecutorState()
                let id = w.daemon.id
                #expect(try await r.pingServer() >= 0)
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

        @Test func installPublicationAndSyncEmitSecuritySnapshotHook() async throws {
            try await world { w in
                let r = try await w.client()
                try await r.hydrateExecutorState()
                let snapshots = Mailbox<PBCoreSecuritySnapshotEvent>()
                await r.installCoreHooks(Remote.CoreHooks(
                    onFetchSession: { _ in },
                    onRefresh: {},
                    onRefreshDevices: {},
                    onSavePrefs: { _ in },
                    onSecuritySnapshot: { snapshots.put($0) }
                ))
                let client = try await r.securitySession()
                let newSeed = randomBytes(32)
                let (enrolledAcct, enrolledRec) = try await w.phone.enroll(seed: newSeed, kind: .machine)
                let rawState = try DeviceSecurity.stateBytes(enrolledRec.state)
                var stateMsg = PBSecurityState()
                stateMsg.state = rawState
                var req = PBPutSecurityStateRequest()
                req.states = [stateMsg]
                if let lease = enrolledRec.lease {
                    req.lease = try lease.bytes()
                }
                try await client.installPublication(req)
                let snap = try await snapshots.take()
                #expect(snap.trustedMachineIds.contains(enrolledAcct.deviceId))

                // Sync outside syncSecuritySnapshot also notifies onSecuritySnapshot.
                let seed2 = randomBytes(32)
                let (acct2, _) = try await w.phone.enroll(seed: seed2, kind: .machine)
                _ = try await client.sync()
                let snap2 = try await snapshots.take()
                #expect(snap2.trustedMachineIds.contains(acct2.deviceId))
            }
        }
    }
}
