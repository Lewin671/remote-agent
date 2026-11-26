#if DEBUG
import CryptoKit
import Foundation
import os
@testable import RACore
import RAProto
import SwiftProtobuf
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

private struct TestError: Error, CustomStringConvertible {
    let description: String
    init(_ description: String) { self.description = description }
}

private func withTimeout<T: Sendable>(
    seconds: Double,
    _ label: String,
    operation: @escaping @Sendable () async throws -> T
) async throws -> T {
    try await withThrowingTaskGroup(of: T.self) { group in
        group.addTask { try await operation() }
        group.addTask {
            try await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
            throw TestError("timed out after \(seconds)s: \(label)")
        }
        guard let first = try await group.next() else {
            throw TestError("empty task group: \(label)")
        }
        group.cancelAll()
        return first
    }
}

/// Minimal test-only Go `CoreClient` + `SecurityExecutor` node for driving real Core security
/// workflows and envelope ops directly in unit tests.
private final class TestCoreNode: @unchecked Sendable {
    private let core: CoreClient
    private let executor: SecurityExecutor
    private let clock: @Sendable () -> Int64
    private let queue = DispatchQueue(label: "ra.tests.core.node")
    private let lock = NSLock()
    private var closed = false
    private var nextID: UInt64 = 1
    private var timers: [String: (id: UInt64, task: Task<Void, Never>)] = [:]
    private var workflowWaiters: [UInt64: CheckedContinuation<PBSecurityWorkflowResultEffect, Error>] = [:]
    private var opWaiters: [UInt64: CheckedContinuation<PBSecurityOpResultEffect, Error>] = [:]

    private final class EmitBox: @unchecked Sendable {
        var send: (@Sendable (PBCoreEvent) -> Void)?
    }

    init(
        store: any SecurityStoreV4,
        clock: @escaping @Sendable () -> Int64,
        rpcOverride: SecurityExecutor.RpcOverride? = nil
    ) throws {
        self.core = try CoreClient()
        self.clock = clock
        let emitBox = EmitBox()
        self.executor = SecurityExecutor(
            store: store,
            allowPlainHttp: true,
            rpcOverride: rpcOverride,
            emit: { ev in emitBox.send?(ev) }
        )
        emitBox.send = { [weak self] ev in self?.enqueue(ev) }
        enqueue(.with { $0.entropyProvided = Self.buildEntropy(minIds: 16, minFloats: 16, minRandomBytes: 4096) })
        enqueue(.with {
            $0.securityConfig = .with {
                $0.inlineCrypto = true
                $0.foreground = true
            }
        })
    }

    private func isClosed() -> Bool { lock.withLock { closed } }

    func enqueue(_ ev: PBCoreEvent) {
        guard !isClosed() else { return }
        var stamped = ev
        if stamped.nowMs == 0 { stamped.nowMs = clock() }
        if stamped.monoMs == 0 { stamped.monoMs = stamped.nowMs }
        let finalEv = stamped
        queue.async { [weak self] in self?.step(finalEv) }
    }

    @discardableResult
    func runWorkflow(_ label: String = "", _ configure: @Sendable (inout PBSecurityWorkflowActionEvent) -> Void) async throws -> PBSecurityWorkflowResultEffect {
        let id: UInt64 = lock.withLock {
            let v = nextID
            nextID &+= 1
            return v
        }
        let res = try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { cont in
                let closedNow: Bool = lock.withLock {
                    if closed { return true }
                    workflowWaiters[id] = cont
                    return false
                }
                if closedNow {
                    cont.resume(throwing: TestError("TestCoreNode closed (\(label))"))
                    return
                }
                var action = PBSecurityWorkflowActionEvent()
                action.actionID = id
                configure(&action)
                enqueue(.with { $0.securityAction = action })
            }
        } onCancel: {
            let w = self.lock.withLock { self.workflowWaiters.removeValue(forKey: id) }
            w?.resume(throwing: CancellationError())
        }
        guard res.ok else {
            throw TestError("security workflow failed (\(label)): \(translateSecurityErrorMessage(res))")
        }
        return res
    }

    func securityOp(_ configure: @Sendable (inout PBSecurityOpRequestEvent) -> Void) async throws -> PBSecurityOpResultEffect {
        let id: UInt64 = lock.withLock {
            let v = nextID
            nextID &+= 1
            return v
        }
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { cont in
                let closedNow: Bool = lock.withLock {
                    if closed { return true }
                    opWaiters[id] = cont
                    return false
                }
                if closedNow {
                    cont.resume(throwing: TestError("TestCoreNode closed"))
                    return
                }
                var req = PBSecurityOpRequestEvent()
                req.opID = id
                configure(&req)
                enqueue(.with { $0.securityOpRequest = req })
            }
        } onCancel: {
            let w = self.lock.withLock { self.opWaiters.removeValue(forKey: id) }
            w?.resume(throwing: CancellationError())
        }
    }

    private func step(_ initial: PBCoreEvent) {
        var pending = [initial]
        var idx = 0
        while idx < pending.count && !isClosed() {
            let ev = pending[idx]
            idx += 1
            guard let batch = try? core.dispatch(ev) else { continue }
            for eff in batch.effects {
                if isClosed() { return }
                handle(eff, inline: &pending)
            }
        }
    }

    private func handle(_ eff: PBCoreEffect, inline: inout [PBCoreEvent]) {
        switch eff.effect {
        case .requestEntropy(let req):
            inline.append(.with {
                $0.nowMs = clock()
                $0.monoMs = clock()
                $0.entropyProvided = Self.buildEntropy(
                    minIds: max(Int(req.minIds), 16),
                    minFloats: max(Int(req.minFloats), 16),
                    minRandomBytes: req.minRandomBytes > 0 ? max(Int(req.minRandomBytes), 4096) : 0
                )
            })
        case .scheduleTimer(let st):
            let key = "\(st.kind.rawValue):\(st.key)"
            lock.withLock { timers.removeValue(forKey: key)?.task.cancel() }
            // Keep pairing poll ticks fast in memory tests while still driving real Core timers.
            let sleepMs = min(max(0, st.delayMs), 5)
            let task = Task { [weak self] in
                if sleepMs > 0 { try? await Task.sleep(nanoseconds: UInt64(sleepMs) * 1_000_000) }
                guard let self, !Task.isCancelled, !self.isClosed() else { return }
                let matches = self.lock.withLock { () -> Bool in
                    guard let cur = self.timers[key], cur.id == st.timerID else { return false }
                    self.timers.removeValue(forKey: key)
                    return true
                }
                guard matches else { return }
                self.enqueue(.with {
                    $0.timerFired = .with {
                        $0.timerID = st.timerID
                        $0.kind = st.kind
                        $0.key = st.key
                    }
                })
            }
            lock.withLock { timers[key] = (st.timerID, task) }
        case .cancelTimer(let ct):
            let key = "\(ct.kind.rawValue):\(ct.key)"
            let task = lock.withLock { () -> Task<Void, Never>? in
                guard let cur = timers[key], ct.timerID == 0 || cur.id == ct.timerID else { return nil }
                return timers.removeValue(forKey: key)?.task
            }
            task?.cancel()
        case .securityKeyGenerate, .securityKeySign, .securityKeyAgree,
             .securityKeyEncapsulate, .securityKeyDiscard, .securityStorage, .securityRpc:
            _ = executor.handleEffect(eff)
        case .securityWorkflowResult(let res):
            let waiter = lock.withLock { workflowWaiters.removeValue(forKey: res.actionID) }
            waiter?.resume(returning: res)
        case .securityOpResult(let res):
            let waiter = lock.withLock { opWaiters.removeValue(forKey: res.opID) }
            waiter?.resume(returning: res)
        default:
            break
        }
    }

    func close() {
        let (ts, ws, ops) = lock.withLock {
            () -> ([Task<Void, Never>], [CheckedContinuation<PBSecurityWorkflowResultEffect, Error>], [CheckedContinuation<PBSecurityOpResultEffect, Error>]) in
            guard !closed else { return ([], [], []) }
            closed = true
            let t = timers.values.map(\.task)
            let w = Array(workflowWaiters.values)
            let o = Array(opWaiters.values)
            timers.removeAll()
            workflowWaiters.removeAll()
            opWaiters.removeAll()
            return (t, w, o)
        }
        for t in ts { t.cancel() }
        for w in ws { w.resume(throwing: TestError("TestCoreNode closed")) }
        for o in ops { o.resume(throwing: TestError("TestCoreNode closed")) }
        executor.close()
        queue.sync { core.close() }
    }

    private static func buildEntropy(minIds: Int, minFloats: Int, minRandomBytes: Int) -> PBCoreEntropyProvided {
        .with {
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

/// Real in-memory v4 security authority loaded from the Go-generated `ios/testdata/home-project-v4.json`
/// fixture (produced and validated by `clientcore/security/ios_fixture_test.go`), allowing
/// `FakeCoreExecutor` to seal/open genuine signed `PBEnvelope`s via `CoreClient` + `SecurityExecutor`
/// with zero hand-rolled crypto formats, zero record-byte patching, and zero test-only Core flags.
private final class FakeRelayAuthority: @unchecked Sendable {
    static let nowMs: Int64 = 1_791_000_000_100
    static let server = "https://relay.example.com"

    private struct GoFixture: Decodable {
        let server: String
        let nowMs: Int64
        let accountId: String
        let clientId: String
        let machineId: String
        let clientEdSeed: Data
        let clientEdPub: Data
        let clientXPriv: Data
        let clientXPub: Data
        let machineEdSeed: Data
        let machineEdPub: Data
        let machineXPriv: Data
        let machineXPub: Data
        let states: [Data]
        let lease: Data
        let clientRecord: Data
        let machineRecord: Data
    }

    let account: Account
    let machineID: String
    let securityStates: [PBSecurityState]
    let lease: Data
    let clientPubKey: Data
    let clientStoreSnapshot: ([SecurityStoreBucketKey: SecurityStoreStoredBucket], [SecurityStoreKeyId: SecurityStoreStoredKey])
    let clientNode: TestCoreNode
    let machineNode: TestCoreNode

    private init(
        account: Account,
        machineID: String,
        securityStates: [PBSecurityState],
        lease: Data,
        clientPubKey: Data,
        clientStoreSnapshot: ([SecurityStoreBucketKey: SecurityStoreStoredBucket], [SecurityStoreKeyId: SecurityStoreStoredKey]),
        clientNode: TestCoreNode,
        machineNode: TestCoreNode
    ) {
        self.account = account
        self.machineID = machineID
        self.securityStates = securityStates
        self.lease = lease
        self.clientPubKey = clientPubKey
        self.clientStoreSnapshot = clientStoreSnapshot
        self.clientNode = clientNode
        self.machineNode = machineNode
    }

    private actor Cache {
        private var buildTask: Task<FakeRelayAuthority, Error>?

        func getOrCreate() async throws -> FakeRelayAuthority {
            if let existing = buildTask {
                return try await existing.value
            }
            let task = Task<FakeRelayAuthority, Error> {
                try await withTimeout(seconds: 30, "FakeRelayAuthority.build") {
                    try await FakeRelayAuthority.build()
                }
            }
            buildTask = task
            do {
                return try await task.value
            } catch {
                if buildTask == task { buildTask = nil }
                throw error
            }
        }
    }
    private static let cache = Cache()

    static func shared() async throws -> FakeRelayAuthority {
        try await cache.getOrCreate()
    }

    func makeClientStore() -> MemorySecurityStoreV4 {
        let store = MemorySecurityStoreV4()
        store.writeBuckets(clientStoreSnapshot.0)
        store.writeKeys(clientStoreSnapshot.1)
        store.saveLoginToken(
            SavedLogin(token: "tok-client", expiresMs: Self.nowMs + 86_400_000),
            server: Self.server,
            deviceId: account.deviceId
        )
        return store
    }

    private static func build() async throws -> FakeRelayAuthority {
        let fixtureURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appending(path: "testdata/home-project-v4.json")
        let raw = try Data(contentsOf: fixtureURL)
        let fix = try JSONDecoder().decode(GoFixture.self, from: raw)

        let clientStore = MemorySecurityStoreV4()
        try clientStore.seedTestRecord(
            edPrivate: fix.clientEdSeed,
            edPublic: fix.clientEdPub,
            xPrivate: fix.clientXPriv,
            xPublic: fix.clientXPub,
            recordBytes: fix.clientRecord
        )
        let clientNode = try TestCoreNode(store: clientStore, clock: { fix.nowMs })
        let clientBoot = try await clientNode.runWorkflow("client.bootstrap") { $0.bootstrap = PBSecurityActionEmpty() }
        guard clientBoot.status.deviceID == fix.clientId, clientBoot.status.accountID == fix.accountId else {
            throw TestError("client fixture bootstrap mismatch")
        }

        // Note: `machineNode` runs the client Core bootstrapped with a Machine member's v4 record,
        // relying on Core accepting that persisted record at bootstrap. If Core is later tightened
        // to require `self.kind == Client`, replace `machineNode` sealing calls with envelopes
        // generated by a Go fixture.
        let machineStore = MemorySecurityStoreV4()
        try machineStore.seedTestRecord(
            edPrivate: fix.machineEdSeed,
            edPublic: fix.machineEdPub,
            xPrivate: fix.machineXPriv,
            xPublic: fix.machineXPub,
            recordBytes: fix.machineRecord
        )
        let machineNode = try TestCoreNode(store: machineStore, clock: { fix.nowMs })
        let machineBoot = try await machineNode.runWorkflow("machine.bootstrap") { $0.bootstrap = PBSecurityActionEmpty() }
        guard machineBoot.status.deviceID == fix.machineId else {
            throw TestError("machine fixture bootstrap mismatch")
        }

        let states = fix.states.map { bytes in PBSecurityState.with { $0.state = bytes } }
        return FakeRelayAuthority(
            account: Account(server: fix.server, deviceId: fix.clientId, accountId: fix.accountId),
            machineID: fix.machineId,
            securityStates: states,
            lease: fix.lease,
            clientPubKey: fix.clientEdPub,
            clientStoreSnapshot: (clientStore.readBuckets(), clientStore.readKeys()),
            clientNode: clientNode,
            machineNode: machineNode
        )
    }

    func sealMachineMeta(_ meta: PBMachineMeta, seq: UInt64 = 1) async throws -> PBEnvelope {
        let plain = try meta.serializedData()
        let res = try await machineNode.securityOp {
            $0.sealEnvelope = .with {
                $0.purpose = UInt32(PBPayloadKind.machineMeta.rawValue)
                $0.machineID = machineID
                $0.seq = seq
                $0.plaintext = plain
            }
        }
        guard res.error.isEmpty else { throw TestError("sealMachineMeta: \(res.error)") }
        return res.sealed.envelope
    }

    func sealSession(sessionID: String, meta: PBSession, seq: UInt64 = 1) async throws -> (key: PBEnvelope, meta: PBEnvelope) {
        let seed = sha256(Data("session-seed:\(sessionID)".utf8))
        let keyRes = try await machineNode.securityOp {
            $0.sealEnvelope = .with {
                $0.purpose = UInt32(PBPayloadKind.sessionKey.rawValue)
                $0.machineID = machineID
                $0.sessionID = sessionID
                $0.seq = 1
                $0.plaintext = seed
            }
        }
        guard keyRes.error.isEmpty else { throw TestError("sealSessionKey: \(keyRes.error)") }
        let plain = try meta.serializedData()
        let metaRes = try await machineNode.securityOp {
            $0.sealEnvelope = .with {
                $0.purpose = UInt32(PBPayloadKind.sessionMeta.rawValue)
                $0.machineID = machineID
                $0.sessionID = sessionID
                $0.seq = seq
                $0.plaintext = plain
            }
        }
        guard metaRes.error.isEmpty else { throw TestError("sealSessionMeta: \(metaRes.error)") }
        return (keyRes.sealed.envelope, metaRes.sealed.envelope)
    }

    func sealPrefs(_ prefs: PBProjectPrefs, revision: UInt64) async throws -> PBEnvelope {
        let plain = try prefs.serializedData()
        let res = try await clientNode.securityOp {
            $0.sealEnvelope = .with {
                $0.purpose = UInt32(PBPayloadKind.accountPrefs.rawValue)
                $0.requestID = "project-prefs"
                $0.seq = revision
                $0.plaintext = plain
            }
        }
        guard res.error.isEmpty else { throw TestError("sealPrefs: \(res.error)") }
        return res.sealed.envelope
    }

    func openPrefs(_ envelope: PBEnvelope, revision: UInt64) async throws -> PBProjectPrefs {
        let res = try await clientNode.securityOp {
            $0.openEnvelope = .with {
                $0.envelope = envelope
                $0.expectedPurpose = UInt32(PBPayloadKind.accountPrefs.rawValue)
                $0.requestID = "project-prefs"
                $0.seq = revision
            }
        }
        guard res.error.isEmpty else { throw TestError("openPrefs: \(res.error)") }
        return try PBProjectPrefs(serializedBytes: res.opened.plaintext)
    }

    func openCommand(_ envelope: PBEnvelope) async throws -> (header: PBHeader, command: PBCommand, plain: Data) {
        let header = try PBHeader(serializedBytes: envelope.header)
        let res = try await machineNode.securityOp {
            $0.openEnvelope = .with {
                $0.envelope = envelope
                $0.expectedPurpose = UInt32(PBPayloadKind.command.rawValue)
                $0.machineID = machineID
                $0.sessionID = header.sessionID
                $0.requestID = header.requestID
            }
        }
        guard res.error.isEmpty else { throw TestError("openCommand: \(res.error)") }
        let cmd = try PBCommand(serializedBytes: res.opened.plaintext)
        return (header, cmd, res.opened.plaintext)
    }

    func sealCommandResult(
        for header: PBHeader,
        commandPlain: Data,
        result: PBCommandResult
    ) async throws -> PBEnvelope {
        // Compute authoritative requestKeyHash / payloadHash through Go Core's SealEnvelope op.
        let cmdSeal = try await clientNode.securityOp {
            $0.sealEnvelope = .with {
                $0.purpose = UInt32(PBPayloadKind.command.rawValue)
                $0.machineID = machineID
                $0.sessionID = header.sessionID
                $0.requestID = header.requestID
                $0.seq = header.seq
                $0.deadlineMs = header.deadlineMs
                $0.plaintext = commandPlain
            }
        }
        guard cmdSeal.error.isEmpty else { throw TestError("computeCommandHashes: \(cmdSeal.error)") }
        var bound = result
        bound.requestKeyHash = cmdSeal.sealed.requestKeyHash
        bound.payloadHash = cmdSeal.sealed.payloadHash
        let plain = try bound.serializedData()
        let res = try await machineNode.securityOp {
            $0.sealEnvelope = .with {
                $0.purpose = UInt32(PBPayloadKind.commandResult.rawValue)
                $0.machineID = machineID
                $0.sessionID = header.sessionID
                $0.requestID = header.requestID
                $0.plaintext = plain
            }
        }
        guard res.error.isEmpty else { throw TestError("sealCommandResult: \(res.error)") }
        return res.sealed.envelope
    }
}

private actor FakeCoreExecutor: CoreHostExecutor {
    nonisolated let authority: FakeRelayAuthority
    nonisolated let account: Account
    nonisolated let machineID: String
    nonisolated let securityStore: MemorySecurityStoreV4

    private final class HooksBox: @unchecked Sendable {
        let lock = NSLock()
        var hooks: Remote.CoreHooks?
        var closed = false
    }
    private nonisolated let hooksBox = HooksBox()

    var watchedRoutes: [PBWatchedSessionRoute] = []
    var openedLegs: [UInt64] = []
    var closedLegs: [UInt64] = []
    var activeLeg: UInt64?
    var streamCallbacks: [UInt64: Remote.StreamCallbacks] = [:]
    var fetchListsCount = 0
    var savedPrefs: [PBProjectPrefs] = []
    var createdRequests: [PBCreateSessionRequest] = []
    var autoReadyOnOpen = true
    var forwardHandler: (@Sendable (PBForwardRequest, PBCommand) async -> PBRelayRpcResultEvent?)?

    func setForwardHandler(_ handler: (@Sendable (PBForwardRequest, PBCommand) async -> PBRelayRpcResultEvent?)?) {
        forwardHandler = handler
    }

    var machines: [PBMachine]
    var sessions: [PBRemoteSession]
    var prefs = PBProjectPrefs.with { $0.schema = 1 }
    var prefsEnvelope: PBEnvelope
    var prefsRev: UInt64 = 1
    var nowMs: Int64 = FakeRelayAuthority.nowMs

    init(authority: FakeRelayAuthority, machines: [PBMachine], sessions: [PBRemoteSession], prefsEnvelope: PBEnvelope) {
        self.authority = authority
        self.account = authority.account
        self.machineID = authority.machineID
        self.securityStore = authority.makeClientStore()
        self.machines = machines
        self.sessions = sessions
        self.prefsEnvelope = prefsEnvelope
    }

    static func makeDefault() async throws -> FakeCoreExecutor {
        let authority = try await FakeRelayAuthority.shared()
        let machineMeta = try await authority.sealMachineMeta(.with {
            $0.hostname = "MacBook"
            $0.protocolVersion = peerProtocolVersion
            $0.agents = [.with { $0.agent = .codex }]
        })
        let machines: [PBMachine] = [
            .with {
                $0.id = authority.machineID
                $0.online = true
                $0.lastSeenMs = FakeRelayAuthority.nowMs
                $0.meta = machineMeta
            }
        ]
        let s1 = try await authority.sealSession(sessionID: "s1", meta: .with {
            $0.id = "s1"
            $0.cwd = "/Users/me/Code/foo"
            $0.title = "Inspect API"
            $0.agent = .codex
            $0.status = .idle
            $0.createdAt = .with { $0.seconds = 1_790_990_000 }
        })
        let s2 = try await authority.sealSession(sessionID: "s2", meta: .with {
            $0.id = "s2"
            $0.cwd = "/Users/me/Code/bar"
            $0.title = "Fix docs"
            $0.agent = .codex
            $0.status = .exited
            $0.endedAt = .with { $0.seconds = 1_790_998_000 }
        })
        let sessions: [PBRemoteSession] = [
            .with {
                $0.sessionID = "s1"
                $0.machineID = authority.machineID
                $0.updatedMs = 1_790_999_000_000
                $0.key = s1.key
                $0.meta = s1.meta
            },
            .with {
                $0.sessionID = "s2"
                $0.machineID = authority.machineID
                $0.updatedMs = 1_790_998_000_000
                $0.endedMs = 1_790_998_000_000
                $0.key = s2.key
                $0.meta = s2.meta
            }
        ]
        let prefsEnv = try await authority.sealPrefs(.with { $0.schema = 1 }, revision: 1)
        return FakeCoreExecutor(authority: authority, machines: machines, sessions: sessions, prefsEnvelope: prefsEnv)
    }

    func appendSession(sessionID: String, meta: PBSession, updatedMs: Int64) async throws {
        let sealed = try await authority.sealSession(sessionID: sessionID, meta: meta)
        sessions.append(.with {
            $0.sessionID = sessionID
            $0.machineID = machineID
            $0.updatedMs = updatedMs
            $0.key = sealed.key
            $0.meta = sealed.meta
        })
    }

    func installCoreHooks(_ hooks: Remote.CoreHooks) async {
        hooksBox.lock.withLock { hooksBox.hooks = hooks }
    }

    func handleSecurityRpc(_ eff: PBSecurityRpcEffect) async -> PBSecurityRpcResultEvent {
        var res = PBSecurityRpcResultEvent()
        res.opID = eff.opID
        do {
            switch eff.method {
            case .getSecurityStates:
                let req = try PBGetSecurityStatesRequest(serializedBytes: eff.requestBytes)
                let headEpoch = UInt64(authority.securityStates.count)
                let start = Int(req.afterEpoch)
                let slice = start < authority.securityStates.count ? Array(authority.securityStates[start...]) : []
                res.responseBytes = try PBGetSecurityStatesResponse.with {
                    $0.states = slice
                    $0.headEpoch = headEpoch
                    $0.lease = authority.lease
                }.serializedData()
            case .getChallenge:
                res.responseBytes = try PBGetChallengeResponse.with {
                    $0.challenge = Data(repeating: 0x42, count: 32)
                }.serializedData()
            case .login:
                let req = try PBLoginRequest(serializedBytes: eff.requestBytes)
                res.responseBytes = try PBLoginResponse.with {
                    $0.token = "tok-\(req.deviceID)"
                    $0.expiresMs = nowMs + 86_400_000
                    $0.devicePublicKey = authority.clientPubKey
                }.serializedData()
            case .putSecurityState:
                let headEpoch = UInt64(authority.securityStates.count)
                res.responseBytes = try PBPutSecurityStateResponse.with {
                    $0.headEpoch = headEpoch
                    $0.head = authority.securityStates.last?.state ?? Data()
                }.serializedData()
            case .putDeviceMeta:
                res.responseBytes = try PBPutDeviceMetaResponse.with { $0.applied = true }.serializedData()
            default:
                res.connectCode = 12
                res.error = "unhandled security rpc: \(eff.method)"
            }
        } catch {
            res.connectCode = 13
            res.error = error.localizedDescription
        }
        return res
    }

    func executeRelayRpc(_ eff: PBRelayRpcEffect) async -> PBRelayRpcResultEvent? {
        var res = PBRelayRpcResultEvent()
        res.opID = eff.opID
        res.relayEpoch = "epoch-1"
        do {
            switch eff.method {
            case .listMachines:
                res.responseBytes = try PBListMachinesResponse.with {
                    $0.machines = machines
                }.serializedData()

            case .listRemoteSessions:
                let req = try PBListRemoteSessionsRequest(serializedBytes: eff.requestBytes)
                if req.sessionIds.isEmpty && !req.hasAfter {
                    fetchListsCount += 1
                }
                let list: [PBRemoteSession]
                if !req.sessionIds.isEmpty {
                    let wanted = Set(req.sessionIds)
                    list = sessions.filter { wanted.contains($0.sessionID) }
                } else {
                    list = sessions
                }
                res.responseBytes = try PBListRemoteSessionsResponse.with {
                    $0.sessions = list
                }.serializedData()

            case .listDevices:
                res.responseBytes = try PBListDevicesResponse.with {
                    $0.currentDeviceID = account.deviceId
                    $0.devices = [
                        .with { $0.id = account.deviceId; $0.kind = .client; $0.createdMs = nowMs },
                        .with { $0.id = machineID; $0.kind = .machine; $0.createdMs = nowMs },
                    ]
                }.serializedData()

            case .getAccountPrefs:
                res.responseBytes = try PBGetAccountPrefsResponse.with {
                    $0.object = .with {
                        $0.objectID = "project-prefs"
                        $0.revision = prefsRev
                        $0.envelope = prefsEnvelope
                        $0.updatedMs = nowMs
                    }
                }.serializedData()

            case .putAccountPrefs:
                let req = try PBPutAccountPrefsRequest(serializedBytes: eff.requestBytes)
                let nextRev = req.expectedRevision + 1
                let opened = try await authority.openPrefs(req.envelope, revision: nextRev)
                savedPrefs.append(opened)
                prefs = opened
                prefsEnvelope = req.envelope
                prefsRev = nextRev
                res.responseBytes = try PBPutAccountPrefsResponse.with {
                    $0.applied = true
                    $0.object = .with {
                        $0.objectID = req.objectID
                        $0.revision = prefsRev
                        $0.envelope = prefsEnvelope
                        $0.updatedMs = nowMs
                    }
                }.serializedData()

            case .healthz:
                break

            case .getSecurityStates:
                let req = try PBGetSecurityStatesRequest(serializedBytes: eff.requestBytes)
                let headEpoch = UInt64(authority.securityStates.count)
                let start = Int(req.afterEpoch)
                let slice = start < authority.securityStates.count ? Array(authority.securityStates[start...]) : []
                res.responseBytes = try PBGetSecurityStatesResponse.with {
                    $0.states = slice
                    $0.headEpoch = headEpoch
                    $0.lease = authority.lease
                }.serializedData()

            case .getChallenge:
                res.responseBytes = try PBGetChallengeResponse.with {
                    $0.challenge = Data(repeating: 0x42, count: 32)
                }.serializedData()

            case .login:
                let req = try PBLoginRequest(serializedBytes: eff.requestBytes)
                res.responseBytes = try PBLoginResponse.with {
                    $0.token = "tok-\(req.deviceID)"
                    $0.expiresMs = nowMs + 86_400_000
                    $0.devicePublicKey = authority.clientPubKey
                }.serializedData()

            case .putDeviceMeta:
                res.responseBytes = try PBPutDeviceMetaResponse.with { $0.applied = true }.serializedData()

            case .forward:
                let req = try PBForwardRequest(serializedBytes: eff.requestBytes)
                let (header, cmd, plain) = try await authority.openCommand(req.command)
                if let custom = forwardHandler {
                    return await custom(req, cmd)
                }
                let cmdResult: PBCommandResult
                switch cmd.kind {
                case .listModels:
                    cmdResult = .with {
                        $0.outcome = "executed"
                        $0.reason = "ok"
                        $0.resultAvailable = true
                        $0.listModels = .with {
                            $0.defaultModel = "gpt-5"
                            $0.models = [
                                .with {
                                    $0.slug = "gpt-5"
                                    $0.displayName = "GPT-5"
                                    $0.efforts = ["low", "medium", "high"]
                                    $0.defaultEffort = "medium"
                                }
                            ]
                        }
                    }
                case .getSettings:
                    cmdResult = .with {
                        $0.outcome = "executed"
                        $0.reason = "ok"
                        $0.resultAvailable = true
                        $0.getSettings = .with { $0.settings = PBDaemonSettings() }
                    }
                case .listDirectories(let dirReq):
                    cmdResult = .with {
                        $0.outcome = "executed"
                        $0.reason = "ok"
                        $0.resultAvailable = true
                        $0.listDirectories = .with {
                            $0.path = dirReq.path.isEmpty ? "/Users/me" : dirReq.path
                            $0.dirs = ["Code"]
                        }
                    }
                case .createSession(let createReq):
                    createdRequests.append(createReq)
                    cmdResult = .with {
                        $0.outcome = "executed"
                        $0.reason = "ok"
                        $0.resultAvailable = true
                        $0.createSession = .with { $0.session.id = "sess-created" }
                    }
                default:
                    cmdResult = .with {
                        $0.outcome = "executed"
                        $0.reason = "ok"
                        $0.resultAvailable = true
                    }
                }
                let sealedResult = try await authority.sealCommandResult(
                    for: header,
                    commandPlain: plain,
                    result: cmdResult
                )
                res.responseBytes = try PBForwardResponse.with {
                    $0.result = sealedResult
                    $0.outcome = cmdResult.outcome
                    $0.reason = cmdResult.reason
                    $0.attemptID = req.attemptID
                }.serializedData()

            default:
                res.connectCode = 12
                res.diagnosticMessage = "unhandled relay rpc: \(eff.method)"
            }
        } catch {
            res.connectCode = 13
            res.diagnosticMessage = error.localizedDescription
        }
        return res
    }

    func openStream(legId: UInt64, helloFrame: Data, callbacks: Remote.StreamCallbacks) async {
        openedLegs.append(legId)
        activeLeg = legId
        streamCallbacks[legId] = callbacks
        if autoReadyOnOpen {
            let ready = PBServerFrame.with {
                $0.ready = .with { $0.serverTimeMs = nowMs }
            }
            if let raw = try? ready.serializedData() {
                await callbacks.onRawFrame(legId, raw)
            }
        }
    }

    func closeStream(legId: UInt64) async {
        closedLegs.append(legId)
        if activeLeg == legId {
            activeLeg = nil
        }
    }

    func dropActiveStream(errorCode: String = "unavailable", reason: String = "connection lost") async {
        guard let legId = activeLeg, let cb = streamCallbacks[legId] else { return }
        activeLeg = nil
        await cb.onClosed(PBCoreStreamClosedEvent.with {
            $0.legID = legId
            $0.errorCode = errorCode
            $0.error = reason
        })
    }

    func executeCacheRead(_ effect: PBCoreCacheReadEffect) async -> PBCoreEvent {
        var event = PBCoreEvent()
        switch effect.kind {
        case .bootstrap(let req):
            event.cacheBootstrapLoaded = .with { $0.opID = req.opID }
        case .events(let req):
            event.cacheEventsLoaded = .with {
                $0.opID = req.opID
                $0.sessionID = req.sessionID
            }
        case nil:
            break
        }
        return event
    }

    func executeCacheWrite(_ effect: PBCoreCacheWriteEffect) async {}

    func executeCacheDelete(_ effect: PBCoreCacheDeleteEffect, onSessionRemoved: (@Sendable (String) async -> Void)?) async {}

    func dispatchCommandAttempt(_ effect: PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent {
        Issue.record("unexpected legacy dispatchCommandAttempt on production relay pipe")
        return PBCoreCommandOutcomeEvent.with {
            $0.requestID = effect.requestID
            $0.attemptID = effect.attemptID
            $0.source = .local
            $0.notForwarded = true
            $0.transportCode = "unimplemented"
        }
    }

    func setWatchedSessions(_ routes: [PBWatchedSessionRoute]) async {
        watchedRoutes = routes
    }

    func renameDevice(_ id: String, name: String) async throws {}

    func close() async {
        hooksBox.lock.withLock {
            hooksBox.closed = true
            hooksBox.hooks = nil
        }
    }
}

private actor NavRecorder {
    var navigated: [PBCoreNavigateEffect] = []
    func record(_ eff: PBCoreNavigateEffect) { navigated.append(eff) }
}

private final class PipeTraceRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var entries: [String] = []

    func recordEvent(_ ev: PBCoreEvent) {
        if case .relayPipeConfig(let cfg) = ev.event {
            lock.withLock { entries.append("ev:relayPipeConfig(\(cfg.enabled))") }
        }
    }

    func recordEffects(_ effects: [PBCoreEffect]) {
        lock.withLock {
            for eff in effects {
                switch eff.effect {
                case .relayRpc(let r): entries.append("eff:relayRpc(\(r.method))")
                case .securityRpc(let r): entries.append("eff:securityRpc(\(r.method))")
                case .rpc: entries.append("eff:legacyRpc")
                case .ensureToken: entries.append("eff:legacyEnsureToken")
                case .discardToken: entries.append("eff:legacyDiscardToken")
                default: break
                }
            }
        }
    }

    func snapshot() -> [String] { lock.withLock { entries } }
}

@MainActor
@Suite struct HomeProjectTests {
    private func makeStartedHost(
        executor: FakeCoreExecutor,
        screens: CoreScreens,
        defaults: UserDefaults,
        draftStorageQueue: DraftStorageQueue = .shared,
        trace: PipeTraceRecorder? = nil,
        onNavigate: @escaping @MainActor @Sendable (PBCoreNavigateEffect) -> Void = { _ in }
    ) async throws -> CoreHost {
        let host = try CoreHost.singleCore(
            store: executor.securityStore,
            http: HTTP(),
            allowPlainHttp: true,
            screens: screens,
            defaults: defaults,
            draftStorageQueue: draftStorageQueue,
            clock: { FakeRelayAuthority.nowMs },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) },
            rpcOverride: { await executor.handleSecurityRpc($0) },
            relayRpcOverride: { await executor.executeRelayRpc($0) },
            onNavigate: onNavigate
        )
        if let trace {
            await host.setDebugEventObserver { trace.recordEvent($0) }
            await host.setDebugEffectObserver { trace.recordEffects($0) }
        }
        await host.clientInit(
            deviceDescription: "iOS Test",
            foreground: true,
            networkAvailable: true,
            networkSignature: "wifi"
        )
        await waitUntil { screens.appRoot.stage == .ready }
        await host.attachAccount(
            executor: executor,
            account: executor.account,
            defaults: ScopeDefaults(defaults),
            pendingStore: PendingStore.ephemeral().scoped(),
            sharedFileStore: SharedFileStore(root: SharedFileCenter.defaultRoot)
        )
        await host.startAsync()
        return host
    }

    @Test func coreHostDrivesRealCoreAndPublishesObservableScreens() async throws {
        let screens = CoreScreens()
        let executor = try await FakeCoreExecutor.makeDefault()
        let navRecorder = NavRecorder()
        let trace = PipeTraceRecorder()
        let suiteName = "corehost-test-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)
        let key = scopeHmacKey(localKey: executor.account.localKey)

        let host = try await makeStartedHost(
            executor: executor,
            screens: screens,
            defaults: defaults,
            draftStorageQueue: DraftStorageQueue(),
            trace: trace,
            onNavigate: { eff in Task { await navRecorder.record(eff) } }
        )
        await host.showHome()

        await waitUntil { screens.home.canStart && screens.home.running.count == 1 && screens.home.scopeMenu.count >= 2 }

        // Production pipe ordering: relayPipeConfig(true) precedes the first RPC-like effect,
        // and no legacy RpcEffect / EnsureTokenEffect / DiscardTokenEffect is ever emitted.
        let eventsAndEffects = trace.snapshot()
        let cfgIndex = try #require(eventsAndEffects.firstIndex(of: "ev:relayPipeConfig(true)"))
        let firstRpcIndex = try #require(eventsAndEffects.firstIndex(where: { $0.hasPrefix("eff:") }))
        #expect(cfgIndex < firstRpcIndex)
        #expect(eventsAndEffects.contains(where: { $0.hasPrefix("eff:relayRpc(") }))
        #expect(!eventsAndEffects.contains("eff:legacyRpc"))
        #expect(!eventsAndEffects.contains("eff:legacyEnsureToken"))
        #expect(!eventsAndEffects.contains("eff:legacyDiscardToken"))

        await host.dispatch(PBCoreEvent.with { $0.homeSetScope.scopeKey = "/Users/me/Code/foo" })
        let expectedToken = encodeScopeToken(scopeKey: "/Users/me/Code/foo", hmacKey: key)
        await waitUntil { defaults.string(forKey: AccountDefaults.key("homeScopeToken")) == expectedToken }
        #expect(screens.home.scopeKey == "/Users/me/Code/foo")

        await host.setManageActive(true)
        await host.dispatch(PBCoreEvent.with { $0.projectTogglePin.projectKey = "/Users/me/Code/foo" })
        await waitUntil {
            await executor.savedPrefs.contains(where: { saved in
                saved.pinned.contains(where: { $0.path == "/Users/me/Code/foo" })
            })
        }

        await host.showNewSession(projectKey: "/Users/me/Code/foo")
        await waitUntil { !screens.newSession.modelPanel.models.isEmpty }
        #expect(screens.newSession.modelPanel.models.first?.slug == "gpt-5")

        await host.setDraftText(scope: .newSession("/Users/me/Code/foo"), text: "Run checks")
        await waitUntil {
            screens.newSession.draft.text == "Run checks"
                && screens.newSession.draft.canSend
                && !screens.newSession.draft.restoring
        }
        await host.submitNewSession(
            projectKey: "/Users/me/Code/foo",
            autoModeSupported: false
        )
        await waitUntil { await executor.createdRequests.contains { $0.prompt == "Run checks" && $0.cwd == "/Users/me/Code/foo" } }
        await waitUntil { await navRecorder.navigated.contains { $0.route == .session && $0.sessionID == "sess-created" } }
        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func coreHostSmokeLifecycleConnectListsDisconnectAndReconnectCatchUp() async throws {
        let screens = CoreScreens()
        let executor = try await FakeCoreExecutor.makeDefault()
        let suiteName = "corehost-smoke-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let host = try await makeStartedHost(
            executor: executor,
            screens: screens,
            defaults: defaults
        )

        // 1. Start -> online -> lists -> home rows
        await host.showHome()
        await waitUntil {
            let fetches = await executor.fetchListsCount
            return screens.connection.state == .connected
                && screens.connection.connected
                && fetches >= 1
                && screens.home.running.contains { $0.sessionID == "s1" }
        }

        // 2. Disconnect (streamClosed) -> state reflects offline/reconnecting
        await executor.dropActiveStream(errorCode: "unavailable", reason: "server restarting")
        await waitUntil {
            screens.connection.state != .connected && !screens.connection.connected
        }

        // 3. Reconnect -> catch-up fetchLists delivers newly added session on reconnect
        let initialFetches = await executor.fetchListsCount
        try await executor.appendSession(
            sessionID: "s3",
            meta: .with {
                $0.id = "s3"
                $0.cwd = "/Users/me/Code/foo"
                $0.title = "Catch-up task"
                $0.agent = .claude
                $0.status = .idle
                $0.createdAt = .with { $0.seconds = 1_790_990_000 }
            },
            updatedMs: 1_791_000_001_000
        )
        await waitUntil {
            let legs = await executor.openedLegs.count
            let fetches = await executor.fetchListsCount
            return screens.connection.state == .connected
                && legs >= 2
                && fetches > initialFetches
                && screens.home.running.contains { $0.sessionID == "s3" }
        }

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func cancelDispatchEffectCancelsInFlightAttemptByRequestId() async throws {
        let executor = try await FakeCoreExecutor.makeDefault()
        let screens = CoreScreens()
        let suiteName = "corehost-cancel-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let started = OSAllocatedUnfairLock(initialState: false)
        let cancelled = OSAllocatedUnfairLock(initialState: false)
        await executor.setForwardHandler { _, _ in
            started.withLock { $0 = true }
            await withTaskCancellationHandler {
                try? await Task.sleep(for: .seconds(60))
            } onCancel: {
                cancelled.withLock { $0 = true }
            }
            return nil
        }

        let host = try await makeStartedHost(
            executor: executor,
            screens: screens,
            defaults: defaults
        )
        await waitUntil { screens.connection.state == .connected }

        await host.dispatch(PBCoreEvent.with {
            $0.pingMachine = .with {
                $0.opID = 77
                $0.machineID = executor.machineID
            }
        })
        await waitUntil { started.withLock { $0 } }

        await host.dispatch(PBCoreEvent.with {
            $0.cancelPingMachine = .with { $0.opID = 77 }
        })
        await waitUntil { cancelled.withLock { $0 } }
        #expect(cancelled.withLock { $0 })

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func pingMachineSucceedsAndPropagatesCancellation() async throws {
        let executor = try await FakeCoreExecutor.makeDefault()
        let screens = CoreScreens()
        let suiteName = "corehost-ping-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let host = try await makeStartedHost(
            executor: executor,
            screens: screens,
            defaults: defaults
        )
        await waitUntil { screens.connection.state == .connected }

        // 1. Happy path completes through Core over the relay byte pipe.
        try await host.pingMachine(machineID: executor.machineID)

        // 2. Cancelling the caller task sends CancelPingMachineAction and cancels the in-flight relay Forward RPC.
        let started = OSAllocatedUnfairLock(initialState: false)
        let cancelled = OSAllocatedUnfairLock(initialState: false)
        await executor.setForwardHandler { _, _ in
            started.withLock { $0 = true }
            await withTaskCancellationHandler {
                try? await Task.sleep(for: .seconds(60))
            } onCancel: {
                cancelled.withLock { $0 = true }
            }
            return nil
        }
        let task = Task {
            try await host.pingMachine(machineID: executor.machineID)
        }
        await waitUntil { started.withLock { $0 } }
        task.cancel()
        do {
            _ = try await task.value
            Issue.record("expected CancellationError")
        } catch is CancellationError {
            // expected
        }
        await waitUntil { cancelled.withLock { $0 } }
        #expect(cancelled.withLock { $0 })

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    private func waitUntil(_ cond: @escaping () async -> Bool) async {
        for _ in 0..<100 {
            if await cond() { return }
            try? await Task.sleep(for: .milliseconds(20))
        }
        Issue.record("condition not met before timeout")
    }

    @Test func goldenViewModelsRenderHomeProjectManageAndNewSessionCleanly() {
        let context = AccountContext(account: Demo.account, remote: Demo.remote())
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "home-golden-" + UUID().uuidString)!)
        let model = AppModel(settings: settings)
        let nav = Navigator()

        let home = ScreenGoldens.home("home_timeline", index: 0)
        #expect(!home.attention.isEmpty || home.topAttentionActionable)
        #expect(!home.running.isEmpty)
        #expect(!home.endedGroups.isEmpty)
        #expect(!home.headline.title.rendered().isEmpty)

        var allAtt = false
        var allRun = false
        var filter = ""
        render(
            HomeContent(
                home: home,
                allAttention: Binding(get: { allAtt }, set: { allAtt = $0 }),
                allRunning: Binding(get: { allRun }, set: { allRun = $0 }),
                filterText: Binding(get: { filter }, set: { filter = $0 })
            )
            .environment(context)
            .environment(model)
            .environment(nav)
        )

        let emptyHome = ScreenGoldens.home("home_empty_errors", index: 0)
        render(
            HomeContent(
                home: emptyHome,
                allAttention: .constant(false),
                allRunning: .constant(false),
                filterText: .constant("")
            )
            .environment(context)
            .environment(model)
            .environment(nav)
        )

        let manage = ScreenGoldens.manageProjects("project_manage_detail")
        #expect(!manage.listing.pinned.isEmpty || !manage.listing.others.isEmpty)
        render(
            ManageProjectsContent(manage: manage)
                .environment(context)
                .environment(model)
                .environment(nav)
        )

        let detail = ScreenGoldens.projectDetail("project_manage_detail")
        #expect(!detail.key.isEmpty)
        let ns = ScreenGoldens.newSession("new_session_lifecycle")
        #expect(!ns.projectKey.isEmpty)
        var showEnded = false
        var folded = false
        var text = ns.prompt
        render(
            ProjectDetailContent(
                detail: detail,
                newSession: ns,
                showEnded: Binding(get: { showEnded }, set: { showEnded = $0 }),
                scrolledAway: Binding(get: { folded }, set: { folded = $0 })
            ) {
                NewSessionContent(vm: ns, text: Binding(get: { text }, set: { text = $0 }))
            }
            .environment(context)
            .environment(model)
            .environment(nav)
        )

        render(
            NewTaskModelPanel(
                vm: ns,
                settled: ns.settled,
                blocked: nil,
                onRetryModels: {},
                onSelectModel: { _, _, _ in },
                onClose: {}
            )
            .environment(context)
            .environment(model)
            .environment(nav)
        )
    }

    @Test func concurrentDirPickersStayIsolatedByPickerKey() async throws {
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "ra-dirpicker-\(UUID())")!)
        let model = AppModel(settings: settings)
        let remote = Demo.remote()
        let initial = Demo.world(.everyday)
        model.setForeground(true)
        model.showDemo(account: Demo.account, remote: remote, world: initial)
        defer { model.closeSyncForTest() }
        let host = try #require(model.context?.coreHost)
        let core = try #require(model.context?.core)
        for _ in 0..<200 where core.home.headline.title.rendered().isEmpty {
            try? await Task.sleep(for: .milliseconds(20))
        }
        let t1 = await host.acquireDirPicker(key: "manage")
        let t2 = await host.acquireDirPicker(key: "schedule-form")
        await host.dispatch(PBCoreEvent.with {
            $0.projectOpenDirPicker = .with { $0.machineID = "mac"; $0.path = "/Users/me/Code"; $0.pickerKey = "manage" }
        })
        await host.dispatch(PBCoreEvent.with {
            $0.projectOpenDirPicker = .with { $0.machineID = "mac"; $0.path = "/Users/me/Docs"; $0.pickerKey = "schedule-form" }
        })
        for _ in 0..<200 where (core.dirPickers["manage"]?.currentPath ?? "").isEmpty || (core.dirPickers["schedule-form"]?.currentPath ?? "").isEmpty {
            try? await Task.sleep(for: .milliseconds(20))
        }
        #expect(core.dirPickers["manage"]?.pickerKey == "manage")
        #expect(core.dirPickers["schedule-form"]?.pickerKey == "schedule-form")
        #expect(core.dirPickers["manage"]?.currentPath != core.dirPickers["schedule-form"]?.currentPath)
        await host.dispatch(PBCoreEvent.with { $0.projectCloseDirPicker = .with { $0.pickerKey = "manage" } })
        await host.releaseDirPicker(t1)
        #expect(core.dirPickers["manage"] == nil)
        #expect(core.dirPickers["schedule-form"]?.currentPath == "/Users/me/Docs")
        await host.dispatch(PBCoreEvent.with { $0.projectCloseDirPicker = .with { $0.pickerKey = "schedule-form" } })
        await host.releaseDirPicker(t2)
    }

    @Test func scopeDefaultsStoresUsageCacheAsOpaqueBytes() {
        let suite = "ra-opaque-usage-test-\(UUID().uuidString)"
        let ud = UserDefaults(suiteName: suite)!
        defer { ud.removePersistentDomain(forName: suite) }
        let defaults = ScopeDefaults(ud, scope: ["https://example.com", "dev-1"])
        let raw7 = Data([0xff, 0x00, 0xca, 0xfe])
        let raw30 = Data([0xaa, 0xbb, 0xcc])
        defaults.saveUsageCache(machineID: "m1", preset: .sevenDays, raw: raw7)
        defaults.saveUsageCache(machineID: "m1", preset: .thirtyDays, raw: raw30)
        // Custom preset is never cached on disk.
        defaults.saveUsageCache(machineID: "m1", preset: .custom, raw: Data([1, 2, 3]))
        let loaded = defaults.loadUsageCache(machineID: "m1")
        #expect(loaded.count == 2)
        #expect(loaded.first(where: { $0.preset == .sevenDays })?.raw == raw7)
        #expect(loaded.first(where: { $0.preset == .thirtyDays })?.raw == raw30)
        defaults.deleteUsageCache(machineID: "m1")
        #expect(defaults.loadUsageCache(machineID: "m1").isEmpty)
    }

    private func render<V: View>(_ view: V) {
        let host = UIHostingController(rootView: view)
        let size = host.sizeThatFits(in: CGSize(width: 390, height: 1200))
        #expect(size.width <= 390.5)
        host.view.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
    }
}
#endif
