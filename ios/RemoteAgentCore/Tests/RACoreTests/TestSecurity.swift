import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf
import os

@testable import RACore

struct FixtureRecord: Sendable {
    let version: Int
    let lease: Data
    let account: String
    let domain: Data
    let genesis: Data
    let member: String
    let xPrivate: Data
    let states: [Data]
    let proofs: [Data?]
}

func testEdPublicKey(_ seed: Data) throws -> Data {
    try Curve25519.Signing.PrivateKey(rawRepresentation: seed).publicKey.rawRepresentation
}

func testEdSign(_ seed: Data, _ msg: Data) throws -> Data {
    try Curve25519.Signing.PrivateKey(rawRepresentation: seed).signature(for: msg)
}

func testXPublicKey(_ priv: Data) throws -> Data {
    try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: priv).publicKey.rawRepresentation
}

func testParseLeaseIssuedAt(_ lease: Data) -> Int64 {
    guard lease.count >= 86 else { return 0 }
    var v: UInt64 = 0
    for b in lease[78..<86] {
        v = (v << 8) | UInt64(b)
    }
    return Int64(bitPattern: v)
}

private func appendU32BE(_ out: inout Data, _ val: UInt32) {
    var v = val.bigEndian
    withUnsafeBytes(of: &v) { out.append(contentsOf: $0) }
}

private func appendU64BE(_ out: inout Data, _ val: UInt64) {
    var v = val.bigEndian
    withUnsafeBytes(of: &v) { out.append(contentsOf: $0) }
}

private func appendLP(_ out: inout Data, _ data: Data) {
    appendU32BE(&out, UInt32(data.count))
    out.append(data)
}

private func appendLPStr(_ out: inout Data, _ str: String) {
    appendLP(&out, Data(str.utf8))
}

func encodeV4RecordBytes(
    account: String,
    domain: Data,
    genesis: Data,
    memberId: String,
    member: Bool = true,
    server: String = "https://ra.example.com",
    accountPublicKey: Data = Data(),
    states: [Data],
    proofs: [Data?] = [],
    lease: Data = Data(),
    floorMs: Int64? = nil,
    contentKeys: [(Int64, Data)] = [],
    needsRecoveryRotation: Bool = false,
    recoveryObjectsPending: Bool = false
) throws -> Data {
    guard domain.count == keySize, genesis.count == keySize else {
        throw TestFailure("invalid domain or genesis size")
    }
    var out = Data("RA4REC\u{0}".utf8)
    appendU32BE(&out, 4)
    appendLPStr(&out, account)
    out.append(domain)
    out.append(genesis)
    appendLPStr(&out, memberId)
    out.append(member ? 1 : 0)
    appendLPStr(&out, server)
    appendLP(&out, accountPublicKey)
    appendU32BE(&out, UInt32(states.count))
    for i in states.indices {
        appendLP(&out, states[i])
        appendLP(&out, proofs.indices.contains(i) ? (proofs[i] ?? Data()) : Data())
    }
    appendLP(&out, lease)
    appendU64BE(&out, UInt64(bitPattern: floorMs ?? testParseLeaseIssuedAt(lease)))
    appendU32BE(&out, UInt32(contentKeys.count))
    for (ep, sec) in contentKeys {
        guard sec.count == keySize else { throw TestFailure("bad content key length") }
        appendU64BE(&out, UInt64(bitPattern: ep))
        out.append(sec)
    }
    out.append(needsRecoveryRotation ? 1 : 0)
    out.append(recoveryObjectsPending ? 1 : 0)
    return out
}

func encodeV4NotifyRecordBytes(
    account: String,
    domain: Data,
    genesis: Data,
    headState: Data,
    purposeKey: Data
) throws -> Data {
    guard domain.count == keySize, genesis.count == keySize, purposeKey.count == keySize else {
        throw TestFailure("invalid notify record params")
    }
    var out = Data("RA4NTF\u{0}".utf8)
    appendU32BE(&out, 4)
    appendLPStr(&out, account)
    out.append(domain)
    out.append(genesis)
    appendLP(&out, headState)
    out.append(purposeKey)
    return out
}

func deriveTestNotifyPurposeKey(
    contentSecret: Data,
    account: String,
    domain: Data,
    contentEpoch: UInt64
) -> Data {
    var info = Data("ra3/purpose-key\u{0}".utf8)
    appendLPStr(&info, account)
    info.append(domain)
    appendU64BE(&info, contentEpoch)
    appendU32BE(&info, 10)
    return hkdf(secret: contentSecret, salt: nil, info: info, size: keySize)
}

func testRequestIdentity(
    account: String,
    domain: Data,
    header: PBHeader,
    plaintext: Data
) -> (key: Data, payload: Data) {
    var req = Data("ra4/request-key\u{0}".utf8)
    appendLPStr(&req, account)
    req.append(domain)
    appendLPStr(&req, header.sender)
    appendU32BE(&req, UInt32(header.kind.rawValue))
    appendLPStr(&req, header.machineID)
    appendLPStr(&req, header.sessionID)
    appendLPStr(&req, header.requestID)
    appendU64BE(&req, header.seq)
    let keyHash = sha256(req)

    var pay = Data("ra4/authorized-payload\u{0}".utf8)
    appendLP(&pay, keyHash)
    appendU32BE(&pay, UInt32(header.kind.rawValue))
    appendU64BE(&pay, UInt64(bitPattern: header.deadlineMs))
    appendLP(&pay, plaintext)
    let payloadHash = sha256(pay)
    return (keyHash, payloadHash)
}

/// Separate Go process owning a synthetic authority chain for integration tests.
final class TestSecurity: @unchecked Sendable {
    private static let registry = OSAllocatedUnfairLock<[String: TestSecurity]>(initialState: [:])
    private static let built = GoServer.binary.map {
        $0.deletingLastPathComponent().appendingPathComponent("securityfixture")
    }
    private let lock = NSLock()
    private let process: Process
    private let exited = DispatchSemaphore(value: 0)
    private let input: FileHandle
    private let output: FileHandle
    private var buffer = Data()
    private let serverUrl: String
    private let stateLock = NSLock()
    private var stores: [String: MemorySecurityStoreV4] = [:]
    var deviceSeeds: [String: Data] = [:]
    var xPrivates: [String: Data] = [:]
    var accountRoots: [String: Data] = [:]
    var domains: [String: Data] = [:]

    private init(serverUrl: String) throws {
        self.serverUrl = serverUrl
        let process = Process()
        process.executableURL = try Self.built.get()
        let input = Pipe()
        let output = Pipe()
        process.standardInput = input
        process.standardOutput = output
        process.standardError = FileHandle.standardError
        let exited = self.exited
        process.terminationHandler = { _ in exited.signal() }
        try process.run()
        self.process = process
        self.input = input.fileHandleForWriting
        self.output = output.fileHandleForReading
    }

    func call(_ op: String, _ fields: [String: Any] = [:]) throws -> [String: Any] {
        lock.lock()
        defer { lock.unlock() }
        var request = fields
        request["Op"] = op
        let data = try JSONSerialization.data(withJSONObject: request) + Data([10])
        try input.write(contentsOf: data)
        while !buffer.contains(10) {
            let next = output.availableData
            guard !next.isEmpty else { throw TestFailure("synthetic security fixture exited") }
            buffer.append(next)
        }
        let end = buffer.firstIndex(of: 10)!
        let line = buffer[..<end]
        buffer.removeSubrange(...end)
        guard let reply = try JSONSerialization.jsonObject(with: Data(line)) as? [String: Any]
        else { throw TestFailure("malformed synthetic fixture response") }
        if let error = reply["Error"] as? String {
            throw TestFailure("synthetic security fixture: " + error)
        }
        return reply
    }

    func bytes(_ reply: [String: Any], _ key: String) throws -> Data {
        guard let s = reply[key] as? String, let data = Data(base64Encoded: s) else {
            throw TestFailure("missing synthetic fixture bytes")
        }
        return data
    }

    static func parseRecord(_ reply: [String: Any]) throws -> FixtureRecord {
        guard let o = reply["Record"] as? [String: Any],
              let version = o["version"] as? Int, version == 3,
              let account = o["account"] as? String,
              let member = o["member"] as? String,
              let xString = o["x"] as? String, let x = Data(base64Encoded: xString),
              let domainString = o["domain"] as? String, let domain = Data(base64Encoded: domainString),
              let genesisString = o["genesis"] as? String, let genesis = Data(base64Encoded: genesisString),
              let leaseString = o["lease"] as? String, let lease = Data(base64Encoded: leaseString),
              let stateStrings = o["states"] as? [String],
              let proofEntries = o["proofs"] as? [Any],
              stateStrings.count == proofEntries.count
        else { throw TestFailure("malformed synthetic record") }
        let states = try stateStrings.map { raw -> Data in
            guard let b = Data(base64Encoded: raw) else { throw TestFailure("bad state") }
            return b
        }
        let proofs = try proofEntries.map { entry -> Data? in
            guard let s = entry as? String else { return nil }
            guard let b = Data(base64Encoded: s) else { throw TestFailure("bad proof") }
            return b
        }
        return FixtureRecord(
            version: version, lease: lease, account: account, domain: domain, genesis: genesis,
            member: member, xPrivate: x, states: states, proofs: proofs
        )
    }

    @discardableResult
    func seedMemberStore(
        _ rec: FixtureRecord,
        edSeed: Data,
        store existing: MemorySecurityStoreV4? = nil
    ) throws -> MemorySecurityStoreV4 {
        let edPub = try testEdPublicKey(edSeed)
        let xPub = try testXPublicKey(rec.xPrivate)
        let rootSeed = stateLock.withLock { () -> Data? in
            deviceSeeds[rec.member] = edSeed
            xPrivates[rec.member] = rec.xPrivate
            domains[rec.account] = rec.domain
            return accountRoots[rec.account]
        }
        let accPub = try rootSeed.map { try testEdPublicKey($0) } ?? Data()
        let recBytes = try encodeV4RecordBytes(
            account: rec.account,
            domain: rec.domain,
            genesis: rec.genesis,
            memberId: rec.member,
            member: true,
            server: serverUrl,
            accountPublicKey: accPub,
            states: rec.states,
            proofs: rec.proofs,
            lease: rec.lease
        )
        let target: MemorySecurityStoreV4 = stateLock.withLock {
            let s = existing ?? stores[rec.member] ?? MemorySecurityStoreV4()
            stores[rec.member] = s
            return s
        }
        try target.seedTestRecord(
            edPrivate: edSeed,
            edPublic: edPub,
            xPrivate: rec.xPrivate,
            xPublic: xPub,
            recordBytes: recBytes
        )
        return target
    }

    func enroll(seed: Data, kind: PBDeviceKind) throws -> (FixtureRecord, Data) {
        let reply = try call("enroll", ["Seed": seed.base64EncodedString(), "Kind": kind.rawValue])
        return try (Self.parseRecord(reply), bytes(reply, "State"))
    }

    func record(member: String) throws -> FixtureRecord {
        try Self.parseRecord(call("record", ["Member": member]))
    }

    func syncStoresForAll() {
        let snapshot = stateLock.withLock { (Array(stores), deviceSeeds) }
        for (memberId, store) in snapshot.0 {
            guard let seed = snapshot.1[memberId], let rec = try? record(member: memberId) else { continue }
            _ = try? seedMemberStore(rec, edSeed: seed, store: store)
        }
    }

    func remove(targets: [String]) throws -> Data {
        let reply = try call("remove", ["Targets": targets])
        return try bytes(reply, "State")
    }

    func seal(_ account: Account, _ header: PBHeader, _ plain: Data, seed: Data? = nil) throws -> PBEnvelope {
        let reply = try call(
            "seal",
            [
                "Member": account.deviceId,
                "Header": try header.serializedData().base64EncodedString(),
                "Plain": plain.base64EncodedString(),
                "SessionSeed": (seed ?? Data()).base64EncodedString(),
            ]
        )
        return try PBEnvelope(serializedBytes: bytes(reply, "Envelope"))
    }

    func seal<M: SwiftProtobuf.Message>(_ account: Account, _ header: PBHeader, _ message: M, seed: Data? = nil) throws -> PBEnvelope {
        try seal(account, header, try message.serializedData(), seed: seed)
    }

    func open(_ account: Account, _ envelope: PBEnvelope, seed: Data? = nil) throws -> (PBHeader, Data) {
        let reply = try call(
            "open",
            [
                "Member": account.deviceId,
                "Envelope": try envelope.serializedData().base64EncodedString(),
                "SessionSeed": (seed ?? Data()).base64EncodedString(),
            ]
        )
        return try (PBHeader(serializedBytes: bytes(reply, "Header")), bytes(reply, "Plain"))
    }

    func store(_ id: String) -> MemorySecurityStoreV4? {
        stateLock.withLock { stores[id] }
    }

    func domain(account: String) -> Data? {
        stateLock.withLock { domains[account] }
    }

    static func find(_ server: String) -> TestSecurity? { registry.withLock { $0[server] } }
    static func get(_ server: String) throws -> TestSecurity {
        guard let fixture = find(server) else { throw TestFailure("synthetic authority missing") }
        return fixture
    }

    static func initialize(_ account: Account, edSeed: Data, rootSeed: Data? = nil, recovery: Data? = nil) throws -> (TestSecurity, Data) {
        let fixture = try TestSecurity(serverUrl: account.server)
        let accountId: String
        if !account.accountId.isEmpty {
            accountId = account.accountId
        } else if let rootSeed {
            accountId = deviceId(publicKey: try testEdPublicKey(rootSeed))
        } else {
            accountId = account.deviceId
        }
        if let rootSeed {
            fixture.stateLock.withLock { fixture.accountRoots[accountId] = rootSeed }
        }
        let response = try fixture.call(
            "init",
            [
                "Account": accountId,
                "Seed": edSeed.base64EncodedString(),
                "Recovery": (recovery ?? Data()).base64EncodedString(),
            ]
        )
        let rec = try parseRecord(response)
        try fixture.seedMemberStore(rec, edSeed: edSeed)
        registry.withLock { $0[account.server] = fixture }
        return try (fixture, fixture.bytes(response, "Genesis"))
    }

    static func remove(_ server: String) {
        let removed = registry.withLock { $0.removeValue(forKey: server) }
        removed?.close()
    }

    private func close() {
        lock.lock()
        defer { lock.unlock() }
        try? input.close()
        if exited.wait(timeout: .now() + 5) == .timedOut, process.isRunning {
            kill(process.processIdentifier, SIGKILL)
            exited.wait()
        }
        try? output.close()
        stateLock.withLock {
            stores.removeAll()
            deviceSeeds.removeAll()
            xPrivates.removeAll()
            accountRoots.removeAll()
            domains.removeAll()
        }
    }
}

func fixtureRemote(
    account: Account,
    http: HTTP,
    cache: (any CipherCache)? = nil,
    log: @escaping @Sendable (String) -> Void = { _ in },
    clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
    sleep: @escaping Sleeper = realSleep,
    jitter: @escaping @Sendable () -> Double = { Double.random(in: 0..<1) },
    frameQueue: Int = 1024,
    frameQueueBytes: Int = 32 << 20,
    streamOpenMs: Int64 = 15_000,
    securityStore: (any SecurityStoreV4)? = nil
) throws -> Remote {
    let remote = try Remote(
        account: account,
        http: http,
        cache: cache,
        log: log,
        clock: clock,
        sleep: sleep,
        jitter: jitter,
        frameQueue: frameQueue,
        frameQueueBytes: frameQueueBytes,
        streamOpenMs: streamOpenMs,
        securityStore: securityStore
            ?? TestSecurity.find(account.server)?.store(account.deviceId)
            ?? MemorySecurityStoreV4(),
        allowPlainHttp: true
    )
    return remote
}

/// Test-only Go `CoreClient` + `SecurityExecutor` event-loop driver for `RACoreTests`.
final class TestCoreDriver: @unchecked Sendable {
    private let core: CoreClient
    private let executor: SecurityExecutor
    private let client: SecurityClient
    private let clock: @Sendable () -> Int64
    private let sleep: Sleeper
    private let onStatusSnapshot: (@Sendable (PBSecurityWorkflowStatus) -> Void)?
    private let onTokenUpdated: (@Sendable (PBSecurityTokenUpdatedEffect) -> Void)?
    private let onDispatchCommand: (@Sendable (PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent)?
    private let onTerminalEffect: (@Sendable (PBCoreEffect) -> Void)?
    private let onSessionImageLoaded: (@Sendable (PBSessionImageLoadedEffect) -> Void)?
    private let queue = DispatchQueue(label: "ra.tests.core.driver")
    private let lock = NSLock()
    private var closed = false
    private var nextOpID: UInt64 = 1
    private var timers: [String: (id: UInt64, task: Task<Void, Never>)] = [:]
    private var pingWaiters: [UInt64: ContinuationWaiter<Void>] = [:]
    private var inFlightDispatches: [String: [String: Task<Void, Never>]] = [:]
    private var watchedRoutes: [PBWatchedSessionRoute] = []
    private var onWatchedSessions: (@Sendable ([PBWatchedSessionRoute]) -> Void)?

    init(
        executor: SecurityExecutor,
        client: SecurityClient,
        clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
        sleep: @escaping Sleeper = realSleep,
        onStatusSnapshot: (@Sendable (PBSecurityWorkflowStatus) -> Void)? = nil,
        onTokenUpdated: (@Sendable (PBSecurityTokenUpdatedEffect) -> Void)? = nil,
        onDispatchCommand: (@Sendable (PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent)? = nil,
        onTerminalEffect: (@Sendable (PBCoreEffect) -> Void)? = nil,
        onSessionImageLoaded: (@Sendable (PBSessionImageLoadedEffect) -> Void)? = nil,
        onWatchedSessions: (@Sendable ([PBWatchedSessionRoute]) -> Void)? = nil,
        initialConfig: PBSecurityConfigEvent? = nil,
        relayPipe: Bool = false
    ) throws {
        self.core = try CoreClient()
        self.executor = executor
        self.client = client
        self.clock = clock
        self.sleep = sleep
        self.onStatusSnapshot = onStatusSnapshot
        self.onTokenUpdated = onTokenUpdated
        self.onDispatchCommand = onDispatchCommand
        self.onTerminalEffect = onTerminalEffect
        self.onSessionImageLoaded = onSessionImageLoaded
        self.onWatchedSessions = onWatchedSessions
        enqueueEvent(PBCoreEvent.with {
            $0.entropyProvided = Self.buildEntropy(minIds: 16, minFloats: 16, minRandomBytes: 4096)
        })
        _ = relayPipe
        if let initialConfig {
            enqueueEvent(PBCoreEvent.with { $0.securityConfig = initialConfig })
        }
        if onDispatchCommand != nil {
            enqueueEvent(PBCoreEvent.with { $0.syncLifecycle.kind = .start })
        }
    }

    private func isClosed() -> Bool { lock.withLock { closed } }

    func enqueueEvent(_ ev: PBCoreEvent) {
        guard !isClosed() else { return }
        var stamped = ev
        if stamped.nowMs == 0 { stamped.nowMs = clock() }
        if stamped.monoMs == 0 { stamped.monoMs = stamped.nowMs }
        let finalEvent = stamped
        queue.async { [weak self] in
            self?.dispatchOnWorker(finalEvent)
        }
    }

    func viewHome() -> PBCoreHomeViewModel? {
        queue.sync {
            guard !isClosed() else { return nil }
            let screen = PBCoreScreen.with { $0.home = .with { $0.endedLimit = 50 } }
            return try? core.view(screen).home
        }
    }

    func pingMachine(machineID: String) async throws {
        try await awaitWaiter(
            isClosed: { self.isClosed() },
            closedError: { CommandError(CommandError.canceled, L10nMessage.errorsTerminalConnectionCanceled()) },
            register: { waiter in
                try lock.withLock {
                    guard !closed else { throw CommandError(CommandError.canceled, L10nMessage.errorsTerminalConnectionCanceled()) }
                    try Task.checkCancellation()
                    let id = nextOpID
                    nextOpID &+= 1
                    pingWaiters[id] = waiter
                    return id
                }
            },
            unregister: { [self] id in
                let removed = lock.withLock { pingWaiters.removeValue(forKey: id) }
                if removed != nil && !self.isClosed() {
                    self.enqueueEvent(PBCoreEvent.with {
                        $0.cancelPingMachine = .with { $0.opID = id }
                    })
                }
                return removed
            },
            dispatch: { id in
                enqueueEvent(PBCoreEvent.with {
                    $0.pingMachine = .with {
                        $0.opID = id
                        $0.machineID = machineID
                    }
                })
            }
        )
    }

    private func dispatchOnWorker(_ initial: PBCoreEvent) {
        var queueEvents = [initial]
        var idx = 0
        while idx < queueEvents.count && !isClosed() {
            let ev = queueEvents[idx]
            idx += 1
            guard let batch = try? core.dispatch(ev) else { continue }
            for eff in batch.effects {
                if isClosed() { return }
                handleEffect(eff, inlineQueue: &queueEvents)
            }
        }
    }

    private func handleEffect(_ eff: PBCoreEffect, inlineQueue: inout [PBCoreEvent]) {
        switch eff.effect {
        case .requestEntropy(let req):
            let ids = max(Int(req.minIds), 16)
            let floats = max(Int(req.minFloats), 16)
            let rbytes = req.minRandomBytes > 0 ? max(Int(req.minRandomBytes), 4096) : 0
            inlineQueue.append(PBCoreEvent.with {
                $0.nowMs = clock()
                $0.monoMs = clock()
                $0.entropyProvided = Self.buildEntropy(minIds: ids, minFloats: floats, minRandomBytes: rbytes)
            })
        case .scheduleTimer(let st):
            let tk = "\(st.kind.rawValue):\(st.key)"
            lock.withLock { timers.removeValue(forKey: tk)?.task.cancel() }
            let delayMs = max(0, st.delayMs)
            let sleep = self.sleep
            let task = Task { [weak self] in
                if delayMs > 0 { try? await sleep(delayMs) }
                guard let self, !Task.isCancelled, !self.isClosed() else { return }
                let matches = self.lock.withLock { () -> Bool in
                    guard let cur = self.timers[tk], cur.id == st.timerID else { return false }
                    self.timers.removeValue(forKey: tk)
                    return true
                }
                guard matches else { return }
                self.enqueueEvent(PBCoreEvent.with {
                    $0.timerFired = .with {
                        $0.timerID = st.timerID
                        $0.kind = st.kind
                        $0.key = st.key
                    }
                })
            }
            lock.withLock { timers[tk] = (st.timerID, task) }
        case .cancelTimer(let ct):
            let tk = "\(ct.kind.rawValue):\(ct.key)"
            let task = lock.withLock { () -> Task<Void, Never>? in
                guard let cur = timers[tk], ct.timerID == 0 || cur.id == ct.timerID else { return nil }
                return timers.removeValue(forKey: tk)?.task
            }
            task?.cancel()
        case .securityKeyGenerate, .securityKeySign, .securityKeyAgree,
             .securityKeyEncapsulate, .securityKeyDiscard, .securityStorage,
             .relayRpc, .relayRpcCancel:
            _ = executor.handleEffect(eff)
        case .securityWorkflowResult(let r):
            client.handleWorkflowResult(r)
            if r.hasStatus { onStatusSnapshot?(r.status) }
        case .securityStatusChanged(let s):
            if s.hasStatus {
                client.handleStatusChanged(s.status)
                onStatusSnapshot?(s.status)
            }
        case .securityTokenUpdated(let t):
            onTokenUpdated?(t)
        case .openStream(let s):
            inlineQueue.append(PBCoreEvent.with {
                $0.nowMs = clock()
                $0.monoMs = clock()
                $0.streamReady = .with {
                    $0.legID = s.legID
                    $0.serverTimeMs = clock()
                }
            })
        case .cacheRead(let r):
            var reply = PBCoreEvent()
            reply.nowMs = clock()
            reply.monoMs = clock()
            switch r.kind {
            case .bootstrap(let b)?:
                reply.cacheBootstrapLoaded = .with { $0.opID = b.opID }
            case .events(let e)?:
                reply.cacheEventsLoaded = .with { $0.opID = e.opID; $0.sessionID = e.sessionID }
            case nil:
                break
            }
            inlineQueue.append(reply)
        case .dispatchCommand(let cmdEff):
            guard let dispatchFn = onDispatchCommand else { break }
            let reqID = cmdEff.requestID
            let attID = cmdEff.attemptID
            let task = Task { [weak self] in
                guard let self else { return }
                let outcome = await dispatchFn(cmdEff)
                let stillTracked = self.lock.withLock { () -> Bool in
                    guard var byAttempt = self.inFlightDispatches[reqID], byAttempt.removeValue(forKey: attID) != nil else {
                        return false
                    }
                    if byAttempt.isEmpty {
                        self.inFlightDispatches.removeValue(forKey: reqID)
                    } else {
                        self.inFlightDispatches[reqID] = byAttempt
                    }
                    return !self.closed
                }
                if stillTracked && !Task.isCancelled {
                    self.enqueueEvent(PBCoreEvent.with { $0.commandOutcome = outcome })
                }
            }
            lock.withLock {
                if !closed {
                    inFlightDispatches[reqID, default: [:]][attID] = task
                } else {
                    task.cancel()
                }
            }
        case .cancelDispatch(let eff):
            let tasks = lock.withLock {
                eff.requestID.isEmpty ? [] : Array((inFlightDispatches.removeValue(forKey: eff.requestID) ?? [:]).values)
            }
            for task in tasks { task.cancel() }
        case .pingMachineResult(let res):
            let waiter = lock.withLock { pingWaiters.removeValue(forKey: res.opID) }
            if res.ok {
                waiter?.resolve(.success(()))
            } else {
                let code = res.errorCode.isEmpty ? "failed" : res.errorCode
                let msg = res.hasErrorText ? renderCoreText(res.errorText) : ""
                waiter?.resolve(.failure(CommandError(
                    code,
                    msg.isEmpty ? L10nMessage.errorsCommandFailed() : LocalizedMessage(raw: msg)
                )))
            }
        case .sessionImageLoaded(let res):
            onSessionImageLoaded?(res)
        case .terminalDirectSend, .terminalOpenSocket, .terminalCloseSocket, .terminalDeliverOutput, .terminalStateChanged:
            onTerminalEffect?(eff)
        default: break
        }
    }

    func close() {
        let (pendingTimers, pendingPings, pendingDispatches) = lock.withLock {
            () -> ([Task<Void, Never>], [ContinuationWaiter<Void>], [Task<Void, Never>]) in
            guard !closed else { return ([], [], []) }
            closed = true
            let ts = timers.values.map(\.task)
            let pings = Array(pingWaiters.values)
            let dispatches = inFlightDispatches.values.flatMap(\.values)
            timers.removeAll()
            pingWaiters.removeAll()
            inFlightDispatches.removeAll()
            return (ts, pings, dispatches)
        }
        for t in pendingTimers { t.cancel() }
        for t in pendingDispatches { t.cancel() }
        for waiter in pendingPings { waiter.resolve(.failure(CommandError(CommandError.canceled, L10nMessage.errorsTerminalConnectionCanceled()))) }
        queue.sync {
            core.close()
        }
    }

    private static func buildEntropy(minIds: Int, minFloats: Int, minRandomBytes: Int) -> PBCoreEntropyProvided {
        PBCoreEntropyProvided.with {
            $0.randomIds = (0..<minIds).map { _ in newRequestId() }
            $0.unitFloats = (0..<minFloats).map { _ in Double.random(in: 0..<1) }
            if minRandomBytes > 0 {
                var bytes = Data(count: minRandomBytes)
                _ = bytes.withUnsafeMutableBytes { buf in
                    guard let base = buf.baseAddress else { return errSecParam }
                    return SecRandomCopyBytes(kSecRandomDefault, minRandomBytes, base)
                }
                $0.randomBytes = bytes
            }
        }
    }
}

/// Attaches a `TestCoreDriver` to a headless `Remote` used in `RACoreTests` without a `CoreHost`.
func attachTestCoreDriver(
    to remote: Remote,
    clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
    sleep: @escaping Sleeper = realSleep
) async throws {
    let driver = try TestCoreDriver(
        executor: remote.securityExecutor,
        client: remote.securityClient,
        clock: clock,
        sleep: sleep,
        onStatusSnapshot: { [weak remote] st in
            Task { await remote?.applySecurityStatusSnapshot(st) }
        },
        onTokenUpdated: { [weak remote] eff in
            Task { await remote?.applySecurityTokenUpdated(eff) }
        },
        onDispatchCommand: { [weak remote] eff in
            guard let remote else {
                return PBCoreCommandOutcomeEvent.with {
                    $0.requestID = eff.requestID
                    $0.attemptID = eff.attemptID
                    $0.source = .local
                    $0.notForwarded = true
                    $0.transportCode = "canceled"
                }
            }
            return await remote.dispatchCommandAttempt(eff)
        },
        onTerminalEffect: { [weak remote] eff in
            guard let remote else { return }
            switch eff.effect {
            case .terminalDirectSend(let e)?: remote.handleTerminalDirectSend(e)
            case .terminalOpenSocket(let e)?: remote.handleTerminalOpenSocket(e)
            case .terminalCloseSocket(let e)?: remote.handleTerminalCloseSocket(e)
            case .terminalDeliverOutput(let e)?: remote.handleTerminalDeliverOutput(e)
            case .terminalStateChanged(let e)?: remote.handleTerminalStateChanged(e)
            default: break
            }
        },
        onSessionImageLoaded: { [weak remote] eff in
            remote?.handleSessionImageLoaded(eff)
        },
        relayPipe: true
    )
    TestCoreDriverRegistry.register(remote: remote, driver: driver)
    await remote.installCoreHooks(.init(
        onRefresh: {},
        onRefreshDevices: {},
        onCoreEvent: { ev in
            driver.enqueueEvent(ev)
        },
        onPingMachine: { machineId in
            try await driver.pingMachine(machineID: machineId)
        },
        onClose: {
            TestCoreDriverRegistry.unregister(remote: remote)
            driver.close()
        }
    ))
}

private enum TestCoreDriverRegistry {
    private static let lock = NSLock()
    private nonisolated(unsafe) static var drivers: [ObjectIdentifier: TestCoreDriver] = [:]

    static func register(remote: Remote, driver: TestCoreDriver) {
        lock.withLock { drivers[ObjectIdentifier(remote)] = driver }
    }

    static func unregister(remote: Remote) {
        lock.withLock { _ = drivers.removeValue(forKey: ObjectIdentifier(remote)) }
    }

    static func driver(for remote: Remote) -> TestCoreDriver? {
        lock.withLock { drivers[ObjectIdentifier(remote)] }
    }
}

extension Remote {
    func hydrateCoreSessions() async throws -> [String] {
        guard let driver = TestCoreDriverRegistry.driver(for: self) else {
            throw SecurityFailure.storage
        }
        driver.enqueueEvent(PBCoreEvent.with { $0.syncLifecycle.kind = .reconnectNow })
        let deadline = Date().addingTimeInterval(10)
        while Date() < deadline {
            if let home = driver.viewHome() {
                let ids = home.attention.map(\.sessionID)
                    + home.running.map(\.sessionID)
                    + home.endedGroups.flatMap { $0.items.map(\.sessionID) }
                if !ids.isEmpty {
                    return ids
                }
            }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
        if let home = driver.viewHome() {
            return home.attention.map(\.sessionID)
                + home.running.map(\.sessionID)
                + home.endedGroups.flatMap { $0.items.map(\.sessionID) }
        }
        return []
    }
}

/// Test-only standalone Go `CoreClient` + `SecurityExecutor` harness for `RACoreTests`.
final class TestSecurityNode: @unchecked Sendable {
    private final class Box: @unchecked Sendable {
        let lock = NSLock()
        var driver: TestCoreDriver?
        var closed = false
    }

    private let box = Box()
    private let executor: SecurityExecutor
    private let driver: TestCoreDriver
    nonisolated let client: SecurityClient

    init(
        store: any SecurityStoreV4,
        http: HTTP? = nil,
        allowPlainHttp: Bool = false,
        clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
        sleep: @escaping Sleeper = realSleep,
        relayRpcOverride: SecurityExecutor.RelayRpcOverride? = nil
    ) throws {
        let box = self.box
        let executor = SecurityExecutor(
            store: store,
            http: http,
            allowPlainHttp: allowPlainHttp,
            relayRpcOverride: relayRpcOverride,
            emit: { ev in
                let drv = box.lock.withLock { box.closed ? nil : box.driver }
                drv?.enqueueEvent(ev)
            }
        )
        let client = SecurityClient(
            dispatchEvent: { ev in
                let drv = box.lock.withLock { box.closed ? nil : box.driver }
                drv?.enqueueEvent(ev)
            },
            setForegroundFn: { fg in
                let drv = box.lock.withLock { box.closed ? nil : box.driver }
                drv?.enqueueEvent(PBCoreEvent.with {
                    $0.securityForeground = .with { $0.foreground = fg }
                })
            },
            isClosedFn: { box.lock.withLock { box.closed } }
        )
        let driver = try TestCoreDriver(
            executor: executor,
            client: client,
            clock: clock,
            sleep: sleep,
            initialConfig: .with {
                $0.inlineCrypto = true
                $0.foreground = true
            }
        )
        box.lock.withLock { box.driver = driver }
        self.executor = executor
        self.client = client
        self.driver = driver
    }

    func close() {
        box.lock.withLock {
            box.closed = true
            box.driver = nil
        }
        executor.close()
        client.rejectAll()
        driver.close()
    }
}

extension Remote {
    func testBootstrapSecurity() async throws {
        _ = try await securityClient.bootstrap()
    }

    @discardableResult
    func testSyncSecurity(touch: Bool = false) async throws -> PBSecurityWorkflowStatus {
        _ = try await securityClient.bootstrap()
        let res = try await securityClient.runAction {
            $0.syncNow = .with { $0.touch = touch }
        }
        return res.status
    }

    @discardableResult
    func testRenewLease(force: Bool = false) async throws -> PBSecurityWorkflowStatus {
        _ = try await securityClient.bootstrap()
        let res = try await securityClient.runAction {
            $0.renewLease = .with { $0.flag = force }
        }
        return res.status
    }
}

extension SecurityClient {
    func testDismissUpgradeNotice(store: any SecurityStoreV4) async throws -> PBSecurityWorkflowStatus {
        store.dismissLegacyRepairNotice()
        let res = try await runAction {
            $0.dismissUpgradeNotice = PBSecurityActionEmpty()
        }
        return res.status
    }
}
