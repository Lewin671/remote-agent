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

/// Test-only v4 envelope sealer/opener backed by `ios/testdata/home-project-v4.json`
/// (generated and validated by `clientcore/security/ios_fixture_test.go`), so `FakeCoreExecutor`
/// produces genuine signed `PBEnvelope`s for real `CoreHost` verification without any test-only
/// Core RPC bridge.
private final class FakeRelayAuthority: @unchecked Sendable {
    static let nowMs: Int64 = 1_791_000_000_100
    static let server = "https://relay.example.com"

    private struct GoFixture: Decodable {
        let server: String
        let nowMs: Int64
        let accountId: String
        let domain: Data
        let epoch: UInt64
        let contentEpoch: UInt64
        let stateHash: Data
        let contentSecret: Data
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
    private let domain: Data
    private let epoch: UInt64
    private let contentEpoch: UInt64
    private let stateHash: Data
    private let contentSecret: Data
    private let clientEdKey: Curve25519.Signing.PrivateKey
    private let machineEdKey: Curve25519.Signing.PrivateKey

    private init(
        account: Account,
        machineID: String,
        securityStates: [PBSecurityState],
        lease: Data,
        clientPubKey: Data,
        clientStoreSnapshot: ([SecurityStoreBucketKey: SecurityStoreStoredBucket], [SecurityStoreKeyId: SecurityStoreStoredKey]),
        domain: Data,
        epoch: UInt64,
        contentEpoch: UInt64,
        stateHash: Data,
        contentSecret: Data,
        clientEdKey: Curve25519.Signing.PrivateKey,
        machineEdKey: Curve25519.Signing.PrivateKey
    ) {
        self.account = account
        self.machineID = machineID
        self.securityStates = securityStates
        self.lease = lease
        self.clientPubKey = clientPubKey
        self.clientStoreSnapshot = clientStoreSnapshot
        self.domain = domain
        self.epoch = epoch
        self.contentEpoch = contentEpoch
        self.stateHash = stateHash
        self.contentSecret = contentSecret
        self.clientEdKey = clientEdKey
        self.machineEdKey = machineEdKey
    }

    private actor Cache {
        private var buildTask: Task<FakeRelayAuthority, Error>?

        func getOrCreate() async throws -> FakeRelayAuthority {
            if let existing = buildTask {
                return try await existing.value
            }
            let task = Task<FakeRelayAuthority, Error> {
                try await withTimeout(seconds: 30, "FakeRelayAuthority.build") {
                    try FakeRelayAuthority.build()
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

    private static func build() throws -> FakeRelayAuthority {
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

        let clientEdKey = try Curve25519.Signing.PrivateKey(rawRepresentation: fix.clientEdSeed)
        let machineEdKey = try Curve25519.Signing.PrivateKey(rawRepresentation: fix.machineEdSeed)
        let states = fix.states.map { bytes in PBSecurityState.with { $0.state = bytes } }
        return FakeRelayAuthority(
            account: Account(server: fix.server, deviceId: fix.clientId, accountId: fix.accountId),
            machineID: fix.machineId,
            securityStates: states,
            lease: fix.lease,
            clientPubKey: fix.clientEdPub,
            clientStoreSnapshot: (clientStore.readBuckets(), clientStore.readKeys()),
            domain: fix.domain,
            epoch: fix.epoch,
            contentEpoch: fix.contentEpoch,
            stateHash: fix.stateHash,
            contentSecret: fix.contentSecret,
            clientEdKey: clientEdKey,
            machineEdKey: machineEdKey
        )
    }

    private static func appendU32(_ out: inout Data, _ v: UInt32) {
        var be = v.bigEndian
        withUnsafeBytes(of: &be) { out.append(contentsOf: $0) }
    }

    private static func appendU64(_ out: inout Data, _ v: UInt64) {
        var be = v.bigEndian
        withUnsafeBytes(of: &be) { out.append(contentsOf: $0) }
    }

    private static func appendLP(_ out: inout Data, _ d: Data) {
        appendU32(&out, UInt32(d.count))
        out.append(d)
    }

    private static func appendStr(_ out: inout Data, _ s: String) {
        appendLP(&out, Data(s.utf8))
    }

    private func deriveKey(
        purpose: UInt32,
        sender: String,
        machine: String,
        session: String,
        request: String,
        seq: UInt64
    ) -> SymmetricKey {
        var purposeInfo = Data("ra3/purpose-key\u{0}".utf8)
        Self.appendStr(&purposeInfo, account.accountId)
        purposeInfo.append(domain)
        Self.appendU64(&purposeInfo, contentEpoch)
        Self.appendU32(&purposeInfo, purpose)
        let purposeKey = HKDF<SHA256>.deriveKey(
            inputKeyMaterial: SymmetricKey(data: contentSecret),
            salt: Data(),
            info: purposeInfo,
            outputByteCount: 32
        )

        var contentInfo = Data("ra3/content-key\u{0}".utf8)
        Self.appendStr(&contentInfo, account.accountId)
        contentInfo.append(domain)
        Self.appendU64(&contentInfo, epoch)
        contentInfo.append(stateHash)
        Self.appendU64(&contentInfo, contentEpoch)
        Self.appendU32(&contentInfo, purpose)
        Self.appendStr(&contentInfo, sender)
        Self.appendStr(&contentInfo, machine)
        Self.appendStr(&contentInfo, session)
        Self.appendStr(&contentInfo, request)
        Self.appendU64(&contentInfo, seq)
        return HKDF<SHA256>.deriveKey(
            inputKeyMaterial: purposeKey,
            salt: Data(),
            info: contentInfo,
            outputByteCount: 32
        )
    }

    private func sealEnvelope(
        sender: String,
        signingKey: Curve25519.Signing.PrivateKey,
        kind: PBPayloadKind,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: UInt64 = 0,
        deadlineMs: UInt64 = 0,
        plain: Data
    ) throws -> PBEnvelope {
        let purpose = UInt32(kind.rawValue)
        var headerBytes = Data()
        Self.appendU32(&headerBytes, 3)
        Self.appendStr(&headerBytes, account.accountId)
        headerBytes.append(domain)
        Self.appendU64(&headerBytes, epoch)
        headerBytes.append(stateHash)
        Self.appendU64(&headerBytes, contentEpoch)
        Self.appendStr(&headerBytes, sender)
        Self.appendU32(&headerBytes, purpose)
        Self.appendU32(&headerBytes, purpose)
        Self.appendStr(&headerBytes, machine)
        Self.appendStr(&headerBytes, session)
        Self.appendStr(&headerBytes, request)
        Self.appendU64(&headerBytes, seq)
        Self.appendU64(&headerBytes, deadlineMs)
        Self.appendU32(&headerBytes, 0)

        let aeadKey = deriveKey(
            purpose: purpose,
            sender: sender,
            machine: machine,
            session: session,
            request: request,
            seq: seq
        )
        let nonce = AES.GCM.Nonce()
        let nonceData = Data(nonce)
        let sealed = try AES.GCM.seal(plain, using: aeadKey, nonce: nonce, authenticating: headerBytes)
        let cipher = sealed.ciphertext + sealed.tag

        var commitIn = Data("ra3/ciphertext\u{0}".utf8)
        Self.appendLP(&commitIn, headerBytes)
        Self.appendLP(&commitIn, nonceData)
        Self.appendLP(&commitIn, cipher)
        let commit = Data(SHA256.hash(data: commitIn))
        let hh = Data(SHA256.hash(data: headerBytes))

        var originMsg = Data("ra3/cipher-origin\u{0}".utf8)
        Self.appendStr(&originMsg, account.accountId)
        originMsg.append(domain)
        originMsg.append(stateHash)
        Self.appendStr(&originMsg, sender)
        originMsg.append(hh)
        originMsg.append(commit)
        let sig = try signingKey.signature(for: originMsg)

        let projection = try PBHeader.with {
            $0.version = 3
            $0.kind = kind
            $0.machineID = machine
            $0.sessionID = session
            $0.requestID = request
            $0.seq = seq
            $0.deadlineMs = Int64(bitPattern: deadlineMs)
            $0.sender = sender
        }.serializedData()

        return PBEnvelope.with {
            $0.header = projection
            $0.nonce = nonceData
            $0.ciphertext = cipher
            $0.signature = sig
            $0.securityHeader = headerBytes
        }
    }

    private func openClientEnvelope(_ envelope: PBEnvelope, expectedKind: PBPayloadKind) throws -> (PBHeader, Data) {
        let header = try PBHeader(serializedBytes: envelope.header)
        guard header.kind == expectedKind else {
            throw TestError("unexpected envelope kind \(header.kind), want \(expectedKind)")
        }
        guard envelope.ciphertext.count >= 16 else {
            throw TestError("truncated ciphertext")
        }
        var commitIn = Data("ra3/ciphertext\u{0}".utf8)
        Self.appendLP(&commitIn, envelope.securityHeader)
        Self.appendLP(&commitIn, envelope.nonce)
        Self.appendLP(&commitIn, envelope.ciphertext)
        let commit = Data(SHA256.hash(data: commitIn))
        let hh = Data(SHA256.hash(data: envelope.securityHeader))
        var originMsg = Data("ra3/cipher-origin\u{0}".utf8)
        Self.appendStr(&originMsg, account.accountId)
        originMsg.append(domain)
        originMsg.append(stateHash)
        Self.appendStr(&originMsg, header.sender)
        originMsg.append(hh)
        originMsg.append(commit)
        guard clientEdKey.publicKey.isValidSignature(envelope.signature, for: originMsg) else {
            throw TestError("invalid client signature")
        }

        let aeadKey = deriveKey(
            purpose: UInt32(expectedKind.rawValue),
            sender: header.sender,
            machine: header.machineID,
            session: header.sessionID,
            request: header.requestID,
            seq: header.seq
        )
        let nonce = try AES.GCM.Nonce(data: envelope.nonce)
        let box = try AES.GCM.SealedBox(
            nonce: nonce,
            ciphertext: envelope.ciphertext.dropLast(16),
            tag: envelope.ciphertext.suffix(16)
        )
        let plain = try AES.GCM.open(box, using: aeadKey, authenticating: envelope.securityHeader)
        return (header, plain)
    }

    func sealMachineMeta(_ meta: PBMachineMeta, seq: UInt64 = 1) async throws -> PBEnvelope {
        try sealEnvelope(
            sender: machineID,
            signingKey: machineEdKey,
            kind: .machineMeta,
            machine: machineID,
            seq: seq,
            plain: try meta.serializedData()
        )
    }

    func sealSession(sessionID: String, meta: PBSession, seq: UInt64 = 1) async throws -> (key: PBEnvelope, meta: PBEnvelope) {
        let seed = sha256(Data("session-seed:\(sessionID)".utf8))
        let keyEnv = try sealEnvelope(
            sender: machineID,
            signingKey: machineEdKey,
            kind: .sessionKey,
            machine: machineID,
            session: sessionID,
            seq: 1,
            plain: seed
        )
        let metaEnv = try sealEnvelope(
            sender: machineID,
            signingKey: machineEdKey,
            kind: .sessionMeta,
            machine: machineID,
            session: sessionID,
            seq: seq,
            plain: try meta.serializedData()
        )
        return (keyEnv, metaEnv)
    }

    func sealPrefs(_ prefs: PBProjectPrefs, revision: UInt64) async throws -> PBEnvelope {
        try sealEnvelope(
            sender: account.deviceId,
            signingKey: clientEdKey,
            kind: .accountPrefs,
            request: "project-prefs",
            seq: revision,
            plain: try prefs.serializedData()
        )
    }

    func openPrefs(_ envelope: PBEnvelope, revision: UInt64) async throws -> PBProjectPrefs {
        let (header, plain) = try openClientEnvelope(envelope, expectedKind: .accountPrefs)
        guard header.requestID == "project-prefs", header.seq == revision else {
            throw TestError("prefs header mismatch: req=\(header.requestID) seq=\(header.seq)")
        }
        return try PBProjectPrefs(serializedBytes: plain)
    }

    func openCommand(_ envelope: PBEnvelope) async throws -> (header: PBHeader, command: PBCommand, plain: Data) {
        let (header, plain) = try openClientEnvelope(envelope, expectedKind: .command)
        let cmd = try PBCommand(serializedBytes: plain)
        return (header, cmd, plain)
    }

    func sealCommandResult(
        for header: PBHeader,
        commandPlain: Data,
        result: PBCommandResult
    ) async throws -> PBEnvelope {
        var reqKeyIn = Data("ra4/request-key\u{0}".utf8)
        Self.appendStr(&reqKeyIn, account.accountId)
        reqKeyIn.append(domain)
        Self.appendStr(&reqKeyIn, header.sender)
        Self.appendU32(&reqKeyIn, UInt32(PBPayloadKind.command.rawValue))
        Self.appendStr(&reqKeyIn, header.machineID)
        Self.appendStr(&reqKeyIn, header.sessionID)
        Self.appendStr(&reqKeyIn, header.requestID)
        Self.appendU64(&reqKeyIn, header.seq)
        let requestKeyHash = Data(SHA256.hash(data: reqKeyIn))

        var payloadIn = Data("ra4/authorized-payload\u{0}".utf8)
        Self.appendLP(&payloadIn, requestKeyHash)
        Self.appendU32(&payloadIn, UInt32(PBPayloadKind.command.rawValue))
        Self.appendU64(&payloadIn, UInt64(bitPattern: header.deadlineMs))
        Self.appendLP(&payloadIn, commandPlain)
        let payloadHash = Data(SHA256.hash(data: payloadIn))

        var bound = result
        bound.requestKeyHash = requestKeyHash
        bound.payloadHash = payloadHash
        return try sealEnvelope(
            sender: machineID,
            signingKey: machineEdKey,
            kind: .commandResult,
            machine: machineID,
            session: header.sessionID,
            request: header.requestID,
            plain: try bound.serializedData()
        )
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

            case .putSecurityState:
                let headEpoch = UInt64(authority.securityStates.count)
                res.responseBytes = try PBPutSecurityStateResponse.with {
                    $0.headEpoch = headEpoch
                    $0.head = authority.securityStates.last?.state ?? Data()
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

    func waitFor(
        timeoutSeconds: Double = 5,
        label: String = "FakeCoreExecutor state",
        _ predicate: @escaping @Sendable (isolated FakeCoreExecutor) -> Bool
    ) async throws {
        try await eventuallyAsync(label, timeoutSeconds: timeoutSeconds) {
            await self.checkPredicate(predicate)
        }
    }

    private func checkPredicate(_ predicate: @Sendable (isolated FakeCoreExecutor) -> Bool) -> Bool {
        predicate(self)
    }

    func close() async {
        hooksBox.lock.withLock {
            hooksBox.closed = true
            hooksBox.hooks = nil
        }
    }
}

private actor NavRecorder {
    var navigated: [PBCoreNavigateEffect] = []

    func record(_ eff: PBCoreNavigateEffect) {
        navigated.append(eff)
    }

    func waitFor(
        timeoutSeconds: Double = 5,
        label: String = "navigation",
        _ predicate: @escaping @Sendable ([PBCoreNavigateEffect]) -> Bool
    ) async throws {
        try await eventuallyAsync(label, timeoutSeconds: timeoutSeconds) {
            await predicate(self.navigated)
        }
    }
}

private func parkUntilCancelled(onCancel: @escaping @Sendable () -> Void) async {
    let state = OSAllocatedUnfairLock<(cancelled: Bool, cont: CheckedContinuation<Void, Never>?)>(
        initialState: (false, nil)
    )
    await withTaskCancellationHandler {
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            let alreadyCancelled = state.withLock { s -> Bool in
                if s.cancelled || Task.isCancelled { return true }
                s.cont = cont
                return false
            }
            if alreadyCancelled { cont.resume() }
        }
    } onCancel: {
        onCancel()
        let cont = state.withLock { s -> CheckedContinuation<Void, Never>? in
            s.cancelled = true
            let c = s.cont
            s.cont = nil
            return c
        }
        cont?.resume()
    }
}

@MainActor
private func waitForObservation(
    label: String,
    timeoutSeconds: Double = 5,
    _ cond: @escaping @MainActor () -> Bool
) async throws {
    try await eventuallyOrThrow(label, timeoutSeconds: timeoutSeconds, cond)
}

private final class PipeTraceRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var entries: [String] = []

    func recordEvent(_ ev: PBCoreEvent) {
        _ = ev
    }

    func recordEffects(_ effects: [PBCoreEffect]) {
        lock.withLock {
            for eff in effects {
                if case .relayRpc(let r) = eff.effect {
                    entries.append("eff:relayRpc(\(r.method))")
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
        sharedFileQueue: SharedFileStorageQueue = SharedFileStorageQueue(),
        draftStorageQueue: DraftStorageQueue = DraftStorageQueue(),
        trace: PipeTraceRecorder? = nil,
        onNavigate: @escaping @MainActor @Sendable (PBCoreNavigateEffect) -> Void = { _ in }
    ) async throws -> (host: CoreHost, sharedFileRoot: URL) {
        let sharedFileRoot = FileManager.default.temporaryDirectory
            .appending(path: "ra-home-sf-\(UUID().uuidString)", directoryHint: .isDirectory)
        let host = try CoreHost.singleCore(
            store: executor.securityStore,
            http: HTTP(),
            allowPlainHttp: true,
            screens: screens,
            defaults: defaults,
            sharedFileStore: SharedFileStore(root: sharedFileRoot),
            sharedFileQueue: sharedFileQueue,
            draftStorageQueue: draftStorageQueue,
            clock: { FakeRelayAuthority.nowMs },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) },
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
        try await waitForObservation(label: "appRoot ready") { screens.appRoot.stage == .ready }
        await host.attachAccount(
            executor: executor,
            account: executor.account,
            defaults: ScopeDefaults(defaults),
            pendingStore: PendingStore.ephemeral().scoped(),
            sharedFileStore: SharedFileStore(root: sharedFileRoot)
        )
        await host.startAsync()
        return (host, sharedFileRoot)
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

        let (host, sharedFileRoot) = try await makeStartedHost(
            executor: executor,
            screens: screens,
            defaults: defaults,
            draftStorageQueue: DraftStorageQueue(),
            trace: trace,
            onNavigate: { eff in Task { await navRecorder.record(eff) } }
        )
        defer { try? FileManager.default.removeItem(at: sharedFileRoot) }
        await host.showHome()

        try await waitForObservation(label: "home populated") {
            screens.home.canStart && screens.home.running.count == 1 && screens.home.scopeMenu.count >= 2
        }

        // Production pipe is unconditional: all unary relay traffic flows over RelayRpcEffect.
        let eventsAndEffects = trace.snapshot()
        #expect(eventsAndEffects.contains(where: { $0.hasPrefix("eff:relayRpc(") }))
        #expect(!eventsAndEffects.contains("eff:legacyEnsureToken"))
        #expect(!eventsAndEffects.contains("eff:legacyDiscardToken"))

        await host.dispatch(PBCoreEvent.with { $0.homeSetScope.scopeKey = "/Users/me/Code/foo" })
        let expectedToken = encodeScopeToken(scopeKey: "/Users/me/Code/foo", hmacKey: key)
        try await waitForObservation(label: "homeScopeToken saved") {
            screens.home.scopeKey == "/Users/me/Code/foo"
                && defaults.string(forKey: AccountDefaults.key("homeScopeToken")) == expectedToken
        }
        #expect(screens.home.scopeKey == "/Users/me/Code/foo")

        await host.setManageActive(true)
        await host.dispatch(PBCoreEvent.with { $0.projectTogglePin.projectKey = "/Users/me/Code/foo" })
        try await executor.waitFor(label: "savedPrefs pinned") { exec in
            exec.savedPrefs.contains(where: { saved in
                saved.pinned.contains(where: { $0.path == "/Users/me/Code/foo" })
            })
        }

        await host.showNewSession(projectKey: "/Users/me/Code/foo")
        try await waitForObservation(label: "newSession models loaded") { !screens.newSession.modelPanel.models.isEmpty }
        #expect(screens.newSession.modelPanel.models.first?.slug == "gpt-5")

        await host.setDraftText(scope: .newSession("/Users/me/Code/foo"), text: "Run checks")
        try await waitForObservation(label: "draft ready to send") {
            screens.newSession.draft.text == "Run checks"
                && screens.newSession.draft.canSend
                && !screens.newSession.draft.restoring
        }
        await host.submitNewSession(
            projectKey: "/Users/me/Code/foo",
            autoModeSupported: false
        )
        try await executor.waitFor(label: "createSession request") { exec in
            exec.createdRequests.contains { $0.prompt == "Run checks" && $0.cwd == "/Users/me/Code/foo" }
        }
        try await navRecorder.waitFor(label: "navigate to sess-created") { nav in
            nav.contains { $0.route == .session && $0.sessionID == "sess-created" }
        }
        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func coreHostSmokeLifecycleConnectListsDisconnectAndReconnectCatchUp() async throws {
        let screens = CoreScreens()
        let executor = try await FakeCoreExecutor.makeDefault()
        let suiteName = "corehost-smoke-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let (host, sharedFileRoot) = try await makeStartedHost(
            executor: executor,
            screens: screens,
            defaults: defaults
        )
        defer { try? FileManager.default.removeItem(at: sharedFileRoot) }

        // 1. Start -> online -> lists -> home rows
        await host.showHome()
        try await waitForObservation(label: "connected and s1 listed") {
            screens.connection.state == .connected
                && screens.connection.connected
                && screens.home.running.contains { $0.sessionID == "s1" }
        }
        #expect(await executor.fetchListsCount >= 1)

        // 2. Disconnect (streamClosed) -> state reflects offline/reconnecting
        await executor.dropActiveStream(errorCode: "unavailable", reason: "server restarting")
        try await waitForObservation(label: "disconnected") {
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
        try await waitForObservation(label: "reconnected and s3 caught up") {
            screens.connection.state == .connected
                && screens.home.running.contains { $0.sessionID == "s3" }
        }
        #expect(await executor.openedLegs.count >= 2)
        #expect(await executor.fetchListsCount > initialFetches)

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func cancelDispatchEffectCancelsInFlightAttemptByRequestId() async throws {
        let executor = try await FakeCoreExecutor.makeDefault()
        let screens = CoreScreens()
        let suiteName = "corehost-cancel-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let startedFlag = OSAllocatedUnfairLock(initialState: false)
        let cancelledFlag = OSAllocatedUnfairLock(initialState: false)
        await executor.setForwardHandler { _, _ in
            startedFlag.withLock { $0 = true }
            await parkUntilCancelled {
                cancelledFlag.withLock { $0 = true }
            }
            return nil
        }

        let (host, sharedFileRoot) = try await makeStartedHost(
            executor: executor,
            screens: screens,
            defaults: defaults
        )
        defer { try? FileManager.default.removeItem(at: sharedFileRoot) }
        try await waitForObservation(label: "connected for cancel test") { screens.connection.state == .connected }

        await host.dispatch(PBCoreEvent.with {
            $0.pingMachine = .with {
                $0.opID = 77
                $0.machineID = executor.machineID
            }
        })
        try await eventuallyAsync("forward attempt started") { startedFlag.withLock { $0 } }

        await host.dispatch(PBCoreEvent.with {
            $0.cancelPingMachine = .with { $0.opID = 77 }
        })
        try await eventuallyAsync("forward attempt cancelled") { cancelledFlag.withLock { $0 } }
        #expect(cancelledFlag.withLock { $0 })

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func pingMachineSucceedsAndPropagatesCancellation() async throws {
        let executor = try await FakeCoreExecutor.makeDefault()
        let screens = CoreScreens()
        let suiteName = "corehost-ping-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let (host, sharedFileRoot) = try await makeStartedHost(
            executor: executor,
            screens: screens,
            defaults: defaults
        )
        defer { try? FileManager.default.removeItem(at: sharedFileRoot) }
        try await waitForObservation(label: "connected for ping test") { screens.connection.state == .connected }

        // 1. Happy path completes through Core over the relay byte pipe.
        try await host.pingMachine(machineID: executor.machineID)

        // 2. Cancelling the caller task sends CancelPingMachineAction and cancels the in-flight relay Forward RPC.
        let startedFlag = OSAllocatedUnfairLock(initialState: false)
        let cancelledFlag = OSAllocatedUnfairLock(initialState: false)
        await executor.setForwardHandler { _, _ in
            startedFlag.withLock { $0 = true }
            await parkUntilCancelled {
                cancelledFlag.withLock { $0 = true }
            }
            return nil
        }
        let task = Task {
            try await host.pingMachine(machineID: executor.machineID)
        }
        try await eventuallyAsync("ping forward started") { startedFlag.withLock { $0 } }
        task.cancel()
        do {
            _ = try await task.value
            Issue.record("expected CancellationError")
        } catch is CancellationError {
            // expected
        }
        try await eventuallyAsync("ping forward cancelled") { cancelledFlag.withLock { $0 } }
        #expect(cancelledFlag.withLock { $0 })

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
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
        try await waitForObservation(label: "demo home hydrated") { !core.home.headline.title.rendered().isEmpty }
        let t1 = await host.acquireDirPicker(key: "manage")
        let t2 = await host.acquireDirPicker(key: "schedule-form")
        await host.dispatch(PBCoreEvent.with {
            $0.projectOpenDirPicker = .with { $0.machineID = "mac"; $0.path = "/Users/me/Code"; $0.pickerKey = "manage" }
        })
        await host.dispatch(PBCoreEvent.with {
            $0.projectOpenDirPicker = .with { $0.machineID = "mac"; $0.path = "/Users/me/Docs"; $0.pickerKey = "schedule-form" }
        })
        try await waitForObservation(label: "concurrent dirPickers hydrated") {
            !(core.dirPickers["manage"]?.currentPath ?? "").isEmpty
                && !(core.dirPickers["schedule-form"]?.currentPath ?? "").isEmpty
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
