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
            securityStore: TestSecurity.find(server.url)?.store(account.deviceId))
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
    @discardableResult
    func hydrateExecutorState() async throws -> [String: PBSession] {
        _ = await ensureToken(opId: 1, forceLogin: false, timeoutMs: 15_000)
        _ = try await syncSecurity()
        let fetched = await executeRpc(PBCoreRpcEffect.with {
            $0.opID = 3
            $0.fetchLists = PBCoreFetchListsRpc.with { $0.limit = 50 }
        }).listsFetched
        var sync = PBCoreSyncStateView()
        sync.connection = .connected
        sync.machinesListed = true
        sync.sessionsListed = true
        var openedSessions: [String: PBSession] = [:]
        for m in fetched.wireMachines {
            var sm = PBCoreSyncMachineView.with {
                $0.machineID = m.id
                $0.online = m.online
                $0.lastSeenMs = m.lastSeenMs
            }
            if m.hasMeta, let opened = try? await openSecureMessage(
                envelope: m.meta, want: Place(.machineMeta, machineId: m.id), as: PBMachineMeta.self) {
                sm.meta = opened.1
                sm.metaRev = opened.0.seq
                sm.protocolVersion = opened.1.protocolVersion
            }
            sync.machines.append(sm)
            sync.trustedMachineIds.append(m.id)
        }
        for rs in fetched.wireSessions {
            if rs.hasKey {
                _ = try? await openEnvelopeOp(
                    envelope: rs.key, expectedPurpose: .sessionKey,
                    machineId: rs.machineID, sessionId: rs.sessionID, history: true)
            }
            if rs.hasMeta, let opened = try? await openSecureMessage(
                envelope: rs.meta, want: Place(.sessionMeta, machineId: rs.machineID, sessionId: rs.sessionID), as: PBSession.self) {
                openedSessions[rs.sessionID] = opened.1
            }
            sync.sessions.append(.with {
                $0.sessionID = rs.sessionID
                $0.machineID = rs.machineID
                $0.lastSeq = rs.lastSeq
                $0.updatedMs = rs.updatedMs
                $0.listedEndedMs = rs.endedMs
            })
        }
        applyCoreSyncState(sync)
        return openedSessions
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
                let store = try #require(TestSecurity.find(w.server.url)?.store(acct.deviceId))
                var hello = PBPeerFrame()
                hello.hello.token = try await loginWithStore(api: api, deviceId: acct.deviceId, store: store).token
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
                let admin = try await w.remote(w.phone.account)
                try await admin.hydrateExecutorState()
                let started = try await admin.securityClient.startPairing(webOrigin: "https://app.example.com")
                let link = try #require(PairLink.parse(started.url))
                #expect(link.code == started.code)

                let joinStore = MemorySecurityStoreV4()
                let joinHost = try StandaloneSecurityHost(store: joinStore, http: w.http, allowPlainHttp: true)
                defer { joinHost.close() }
                async let joining = joinHost.client.startJoinPairing(rawLink: started.url)

                var polled = try await admin.securityClient.pollPairing(pairingId: started.pairingID)
                while !polled.joined {
                    try await realSleep(10)
                    polled = try await admin.securityClient.pollPairing(pairingId: started.pairingID)
                }
                let joinerInfo = try await joining
                #expect(polled.joinerKind == .client)
                #expect(polled.sasEmoji.count == 4)
                #expect(joinerInfo.sasEmoji == polled.sasEmoji)

                async let confirmJoin: PBSecurityWorkflowResultEffect = joinHost.client.confirmJoinSas(pairingId: joinerInfo.pairingID)
                async let approveAdmin = admin.securityClient.approvePairing(pairingId: started.pairingID, sasConfirmed: true)
                _ = try await (confirmJoin, approveAdmin)

                let status = try await joinHost.client.bootstrap()
                #expect(status.bootstrap == .ready)
                #expect(status.accountID == w.phone.account.accountId)
                let acct = Account(server: w.server.url, deviceId: status.deviceID, accountId: status.accountID)
                let joined = try Remote(account: acct, http: w.http, securityStore: joinStore, allowPlainHttp: true)
                defer { joined.close() }
                let openedSessions = try await joined.hydrateExecutorState()
                #expect(openedSessions["s1"] != nil)
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

        @Test func syncSecurityReflectsEnrolledAndRemovedDevices() async throws {
            try await world { w in
                let admin = try await w.remote(w.phone.account)
                try await admin.ensureSecurityBootstrapped()
                let newSeed = randomBytes(32)
                let (enrolledAcct, _) = try await w.phone.enroll(seed: newSeed, kind: .machine)
                let st1 = try await admin.syncSecurity()
                #expect(st1.isMember)
                let view1 = try #require(await admin.currentCoreSyncStateView())
                #expect(view1.trustedMachineIds.contains(enrolledAcct.deviceId))

                try await w.phone.remove([enrolledAcct.deviceId])
                _ = try await admin.syncSecurity()
                let view2 = try #require(await admin.currentCoreSyncStateView())
                #expect(!view2.trustedMachineIds.contains(enrolledAcct.deviceId) && view2.archiveMachineIds.contains(enrolledAcct.deviceId))
            }
        }
    }
}
