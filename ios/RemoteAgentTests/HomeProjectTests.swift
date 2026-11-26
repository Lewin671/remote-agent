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

private func requestIdentityHashes(
    account: String,
    domain: Data,
    header: PBHeader,
    plaintext: Data
) -> (keyHash: Data, payloadHash: Data) {
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

private func extractDomainFromRecord(_ raw: Data) throws -> Data {
    // Format: "RA4REC\0" (7) + u32 version (4) + lpstr account + 32-byte domain + 32-byte genesis + ...
    let prefixLen = 7 + 4
    guard raw.count >= prefixLen + 4 else { throw TestError("record too short") }
    let acctLen = raw[prefixLen..<(prefixLen + 4)].reduce(0) { ($0 << 8) | Int($1) }
    let domainStart = prefixLen + 4 + acctLen
    guard raw.count >= domainStart + 32 else { throw TestError("record missing domain") }
    return Data(raw[domainStart..<(domainStart + 32)])
}

private struct TestError: Error, CustomStringConvertible {
    let description: String
    init(_ description: String) { self.description = description }
}

/// Real in-memory v4 security authority (one client + one enrolled machine) built through
/// `StandaloneSecurityHost`, allowing `FakeCoreExecutor` to serve genuine signed `PBEnvelope`s
/// over the production `RelayRpcEffect` byte pipe without spawning external processes.
private final class FakeRelayAuthority: @unchecked Sendable {
    static let nowMs: Int64 = 1_791_000_000_000
    static let server = "https://relay.example.com"

    let account: Account
    let machineID: String
    let domain: Data
    let securityStates: [PBSecurityState]
    let lease: Data
    let clientPubKey: Data
    let clientStoreSnapshot: ([SecurityStoreBucketKey: SecurityStoreStoredBucket], [SecurityStoreKeyId: SecurityStoreStoredKey])
    let clientHost: StandaloneSecurityHost
    let machineHost: StandaloneSecurityHost

    private init(
        account: Account,
        machineID: String,
        domain: Data,
        securityStates: [PBSecurityState],
        lease: Data,
        clientPubKey: Data,
        clientStoreSnapshot: ([SecurityStoreBucketKey: SecurityStoreStoredBucket], [SecurityStoreKeyId: SecurityStoreStoredKey]),
        clientHost: StandaloneSecurityHost,
        machineHost: StandaloneSecurityHost
    ) {
        self.account = account
        self.machineID = machineID
        self.domain = domain
        self.securityStates = securityStates
        self.lease = lease
        self.clientPubKey = clientPubKey
        self.clientStoreSnapshot = clientStoreSnapshot
        self.clientHost = clientHost
        self.machineHost = machineHost
    }

    private actor Cache {
        private var cached: FakeRelayAuthority?
        func getOrCreate() async throws -> FakeRelayAuthority {
            if let cached { return cached }
            let built = try await FakeRelayAuthority.build()
            cached = built
            return built
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
        actor RelayState {
            var states: [PBSecurityState] = []
            var lease = Data()
            var devicePubs: [String: Data] = [:]
            var pairingCommit = Data()
            var pairingState: PBPairingState = .open
            var joinerPublic = Data()
            var joinerKind: PBDeviceKind = .machine
            var joinerEdPub = Data()
            var joinerXPub = Data()
            var initiatorPublic = Data()
            var trust = Data()
            var approverPub = Data()

            func setJoiner(ephPub: Data, edPub: Data, xPub: Data) {
                joinerPublic = ephPub
                joinerKind = .machine
                joinerEdPub = edPub
                joinerXPub = xPub
                pairingState = .joined
            }

            func handle(_ eff: PBSecurityRpcEffect) -> PBSecurityRpcResultEvent {
                var res = PBSecurityRpcResultEvent()
                res.opID = eff.opID
                do {
                    switch eff.method {
                    case .registerDevice:
                        let req = try PBRegisterDeviceRequest(serializedBytes: eff.requestBytes)
                        let reg = try PBDeviceRegistration(serializedBytes: req.registration)
                        let devId = deviceId(publicKey: reg.devicePublicKey)
                        devicePubs[devId] = reg.devicePublicKey
                        if approverPub.isEmpty { approverPub = reg.devicePublicKey }
                        res.responseBytes = try PBRegisterDeviceResponse.with { $0.deviceID = devId }.serializedData()
                    case .getChallenge:
                        res.responseBytes = try PBGetChallengeResponse.with {
                            $0.challenge = Data(repeating: 0x42, count: 32)
                        }.serializedData()
                    case .login:
                        let req = try PBLoginRequest(serializedBytes: eff.requestBytes)
                        res.responseBytes = try PBLoginResponse.with {
                            $0.token = "tok-\(req.deviceID)"
                            $0.expiresMs = FakeRelayAuthority.nowMs + 86_400_000
                            $0.devicePublicKey = devicePubs[req.deviceID] ?? Data()
                        }.serializedData()
                    case .pinSecurityDomain:
                        let req = try PBPinSecurityDomainRequest(serializedBytes: eff.requestBytes)
                        states = [.with { $0.state = req.genesisState }]
                        res.responseBytes = try PBPinSecurityDomainResponse().serializedData()
                    case .putSecurityState:
                        let req = try PBPutSecurityStateRequest(serializedBytes: eff.requestBytes)
                        states.append(contentsOf: req.states)
                        if !req.lease.isEmpty { lease = req.lease }
                        let headEpoch = UInt64(states.count)
                        let headBytes = states.last?.state ?? Data()
                        res.responseBytes = try PBPutSecurityStateResponse.with {
                            $0.headEpoch = headEpoch
                            $0.head = headBytes
                        }.serializedData()
                    case .getSecurityStates:
                        let req = try PBGetSecurityStatesRequest(serializedBytes: eff.requestBytes)
                        let headEpoch = UInt64(states.count)
                        let start = Int(req.afterEpoch)
                        let slice = start < states.count ? Array(states[start...]) : []
                        res.responseBytes = try PBGetSecurityStatesResponse.with {
                            $0.states = slice
                            $0.headEpoch = headEpoch
                            $0.lease = lease
                        }.serializedData()
                    case .createPairing:
                        let req = try PBCreatePairingRequest(serializedBytes: eff.requestBytes)
                        pairingCommit = req.commit
                        pairingState = .open
                        res.responseBytes = try PBCreatePairingResponse.with {
                            $0.pairingID = "pair-1"
                            $0.code = "12345678"
                            $0.expiresMs = FakeRelayAuthority.nowMs + 600_000
                            $0.approverPublicKey = approverPub
                        }.serializedData()
                    case .getPairing:
                        res.responseBytes = try PBGetPairingResponse.with {
                            $0.state = pairingState
                            $0.commit = pairingCommit
                            $0.joinerPublic = joinerPublic
                            $0.joinerKind = joinerKind
                            $0.initiatorPublic = initiatorPublic
                            $0.devicePublicKey = joinerEdPub
                            $0.deviceHpkePublicKey = joinerXPub
                            $0.trust = trust
                        }.serializedData()
                    case .revealPairing:
                        let req = try PBRevealPairingRequest(serializedBytes: eff.requestBytes)
                        initiatorPublic = req.initiatorPublic
                        trust = req.trust
                        pairingState = .revealed
                        res.responseBytes = try PBRevealPairingResponse().serializedData()
                    case .approvePairing:
                        pairingState = .approved
                        res.responseBytes = try PBApprovePairingResponse().serializedData()
                    case .putDeviceMeta:
                        res.responseBytes = try PBPutDeviceMetaResponse.with { $0.applied = true }.serializedData()
                    default:
                        res.connectCode = 12
                        res.error = "unhandled setup rpc: \(eff.method)"
                    }
                } catch {
                    res.connectCode = 13
                    res.error = error.localizedDescription
                }
                return res
            }
        }

        let relay = RelayState()
        let clientStore = MemorySecurityStoreV4()
        let clientHost = try StandaloneSecurityHost(
            store: clientStore,
            allowPlainHttp: true,
            clock: { FakeRelayAuthority.nowMs },
            rpcOverride: { eff in await relay.handle(eff) }
        )

        _ = try await clientHost.client.prepareCreation(server: Self.server, storagePersisted: true)
        try await clientHost.client.confirmCreationCodeSaved()
        let created = try await clientHost.client.continueCreation()
        let acctId = created.status.accountID
        let clientId = created.status.deviceID
        guard !acctId.isEmpty, !clientId.isEmpty else {
            throw TestError("failed to create test authority account")
        }

        let machineEdPriv = Curve25519.Signing.PrivateKey()
        let machineEdPub = machineEdPriv.publicKey.rawRepresentation
        let machineXPriv = Curve25519.KeyAgreement.PrivateKey()
        let machineXPub = machineXPriv.publicKey.rawRepresentation
        let machineEphPub = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let machineId = deviceId(publicKey: machineEdPub)

        let pairing = try await clientHost.client.startPairing(webOrigin: Self.server)
        await relay.setJoiner(ephPub: machineEphPub, edPub: machineEdPub, xPub: machineXPub)
        _ = try await clientHost.client.pollPairing(pairingId: pairing.pairingID)
        _ = try await clientHost.client.approvePairing(pairingId: pairing.pairingID, sasConfirmed: true)

        let recKey = SecurityStoreBucketKey(bucket: Int32(PBSecurityStoreBucket.record.rawValue), key: "record")
        let clientBuckets = clientStore.readBuckets()
        let clientKeys = clientStore.readKeys()
        guard let clientRec = clientBuckets[recKey] else {
            throw TestError("client record missing after pairing")
        }
        let domain = try extractDomainFromRecord(clientRec.data)

        // Clone the authority's persisted record for the enrolled machine by swapping the
        // fixed-length (32-char hex) MemberID and installing the machine's Ed25519/X25519 keys.
        let clientIdBytes = Data(clientId.utf8)
        let machineIdBytes = Data(machineId.utf8)
        guard clientIdBytes.count == machineIdBytes.count,
              let idRange = clientRec.data.range(of: clientIdBytes)
        else {
            throw TestError("unable to locate member ID in persisted record")
        }
        var machineRecData = clientRec.data
        machineRecData.replaceSubrange(idRange, with: machineIdBytes)
        var machineBuckets = clientBuckets
        machineBuckets[recKey] = SecurityStoreStoredBucket(
            bucket: recKey.bucket,
            key: recKey.key,
            revision: clientRec.revision,
            data: machineRecData
        )
        let machineStore = MemorySecurityStoreV4()
        machineStore.writeBuckets(machineBuckets)
        let edKeyId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceEd.rawValue), slot: "")
        let xKeyId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceX.rawValue), slot: "")
        machineStore.writeKeys([
            edKeyId: SecurityStoreStoredKey(
                kind: edKeyId.kind, slot: "",
                privateKey: machineEdPriv.rawRepresentation, publicKey: machineEdPub
            ),
            xKeyId: SecurityStoreStoredKey(
                kind: xKeyId.kind, slot: "",
                privateKey: machineXPriv.rawRepresentation, publicKey: machineXPub
            ),
        ])
        let machineHost = try StandaloneSecurityHost(
            store: machineStore,
            allowPlainHttp: true,
            clock: { FakeRelayAuthority.nowMs },
            rpcOverride: { eff in await relay.handle(eff) }
        )
        _ = try await machineHost.client.bootstrap()

        let states = await relay.states
        let lease = await relay.lease
        let clientPub = await relay.devicePubs[clientId] ?? Data()
        return FakeRelayAuthority(
            account: Account(server: Self.server, deviceId: clientId, accountId: acctId),
            machineID: machineId,
            domain: domain,
            securityStates: states,
            lease: lease,
            clientPubKey: clientPub,
            clientStoreSnapshot: (clientBuckets, clientKeys),
            clientHost: clientHost,
            machineHost: machineHost
        )
    }

    func sealMachineMeta(_ meta: PBMachineMeta, seq: UInt64 = 1) async throws -> PBEnvelope {
        let plain = try meta.serializedData()
        let res = try await machineHost.securityOp {
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
        let keyRes = try await machineHost.securityOp {
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
        let metaRes = try await machineHost.securityOp {
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
        let res = try await clientHost.securityOp {
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
        let res = try await clientHost.securityOp {
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
        let res = try await machineHost.securityOp {
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
        let (keyHash, payloadHash) = requestIdentityHashes(
            account: account.accountId,
            domain: domain,
            header: header,
            plaintext: commandPlain
        )
        var bound = result
        bound.requestKeyHash = keyHash
        bound.payloadHash = payloadHash
        let plain = try bound.serializedData()
        let res = try await machineHost.securityOp {
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
    nonisolated let securityClient: SecurityClient
    nonisolated var optionalSecurityClient: SecurityClient? { securityClient }

    private final class HooksBox: @unchecked Sendable {
        let lock = NSLock()
        var hooks: Remote.CoreHooks?
        var closed = false
    }
    private nonisolated let hooksBox = HooksBox()
    private nonisolated let securityExecutor: SecurityExecutor
    private var inFlightRelayTasks: [UInt64: Task<Void, Never>] = [:]

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
        let store = authority.makeClientStore()
        self.securityStore = store
        let box = self.hooksBox
        self.securityExecutor = SecurityExecutor(
            store: store,
            defaultServer: authority.account.server,
            defaultDeviceId: authority.account.deviceId,
            emit: { ev in
                let hooks = box.lock.withLock { box.closed ? nil : box.hooks }
                await hooks?.onCoreEvent(ev)
            }
        )
        self.securityClient = SecurityClient(
            dispatchEvent: { ev in
                Task {
                    let hooks = box.lock.withLock { box.closed ? nil : box.hooks }
                    await hooks?.onCoreEvent(ev)
                }
            },
            setForegroundFn: { fg in
                Task {
                    let hooks = box.lock.withLock { box.closed ? nil : box.hooks }
                    await hooks?.onCoreEvent(PBCoreEvent.with {
                        $0.securityForeground = .with { $0.foreground = fg }
                    })
                }
            },
            isClosedFn: { box.lock.withLock { box.closed } }
        )
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

    func ensureSecurityBootstrapped() async throws {
        _ = try await securityClient.bootstrap()
    }

    nonisolated func handleCoreSecurityEffect(_ eff: PBCoreEffect) {
        switch eff.effect {
        case .securityKeyGenerate, .securityKeySign, .securityKeyAgree,
             .securityKeyEncapsulate, .securityKeyDiscard, .securityStorage:
            _ = securityExecutor.handleEffect(eff)
        case .securityRpc(let r):
            Task { await self.handleSecurityRpc(r) }
        case .relayRpc(let r):
            Task { await self.startRelayRpc(r) }
        case .relayRpcCancel(let c):
            Task { await self.cancelRelayRpc(c.opID) }
        default:
            break
        }
    }

    private func emitCoreEvent(_ ev: PBCoreEvent) async {
        let hooks = hooksBox.lock.withLock { hooksBox.closed ? nil : hooksBox.hooks }
        await hooks?.onCoreEvent(ev)
    }

    private func handleSecurityRpc(_ eff: PBSecurityRpcEffect) async {
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
        await emitCoreEvent(.with { $0.securityRpcResult = res })
    }

    private func startRelayRpc(_ eff: PBRelayRpcEffect) {
        let opID = eff.opID
        inFlightRelayTasks[opID]?.cancel()
        inFlightRelayTasks[opID] = Task { [weak self] in
            guard let self else { return }
            let result = await self.executeRelayRpc(eff)
            await self.finishRelayRpc(opID: opID, result: result)
        }
    }

    private func finishRelayRpc(opID: UInt64, result: PBRelayRpcResultEvent?) async {
        inFlightRelayTasks.removeValue(forKey: opID)
        guard let result, !Task.isCancelled else { return }
        await emitCoreEvent(.with { $0.relayRpcResult = result })
    }

    private func cancelRelayRpc(_ opID: UInt64) {
        inFlightRelayTasks.removeValue(forKey: opID)?.cancel()
    }

    private func executeRelayRpc(_ eff: PBRelayRpcEffect) async -> PBRelayRpcResultEvent? {
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
        for task in inFlightRelayTasks.values { task.cancel() }
        inFlightRelayTasks.removeAll()
        securityExecutor.close()
        securityClient.rejectAll()
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
    @Test func coreHostDrivesRealCoreAndPublishesObservableScreens() async throws {
        let screens = CoreScreens()
        let executor = try await FakeCoreExecutor.makeDefault()
        let navRecorder = NavRecorder()
        let trace = PipeTraceRecorder()
        let client = try CoreClient()
        let suiteName = "corehost-test-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)
        let key = scopeHmacKey(localKey: Demo.account.localKey)

        let host = CoreHost(
            client: client,
            executor: executor,
            screens: screens,
            defaults: ScopeDefaults(defaults),
            scopeHmacKey: key,
            draftStorageQueue: DraftStorageQueue(),
            clock: { FakeRelayAuthority.nowMs },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) },
            onNavigate: { eff in Task { await navRecorder.record(eff) } }
        )
        await host.setDebugEventObserver { trace.recordEvent($0) }
        await host.setDebugEffectObserver { trace.recordEffects($0) }
        await host.wireExecutorHooks()
        await host.startAsync()
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
        let client = try CoreClient()
        let suiteName = "corehost-smoke-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let host = CoreHost(
            client: client,
            executor: executor,
            screens: screens,
            defaults: ScopeDefaults(defaults),
            scopeHmacKey: scopeHmacKey(localKey: Demo.account.localKey),
            clock: { FakeRelayAuthority.nowMs },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) }
        )
        await host.wireExecutorHooks()

        // 1. Start -> online -> lists -> home rows
        await host.startAsync()
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
        let client = try CoreClient()
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

        let host = CoreHost(
            client: client,
            executor: executor,
            screens: screens,
            defaults: ScopeDefaults(defaults),
            scopeHmacKey: scopeHmacKey(localKey: Demo.account.localKey),
            clock: { FakeRelayAuthority.nowMs },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) }
        )
        await host.wireExecutorHooks()
        await host.startAsync()
        await waitUntil { screens.connection.state == .connected }

        await host.dispatch(PBCoreEvent.with {
            $0.runMachineCommand = .with {
                $0.opID = 77
                $0.machineID = executor.machineID
                $0.command = .with { $0.getSettings = PBGetSettingsRequest() }
                $0.skipConnectivityPreflight = true
            }
        })
        await waitUntil { started.withLock { $0 } }

        await host.dispatch(PBCoreEvent.with {
            $0.cancelMachineCommand = .with { $0.opID = 77 }
        })
        await waitUntil { cancelled.withLock { $0 } }
        #expect(cancelled.withLock { $0 })

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func runMachineCommandReturnsResultAndPropagatesCancellation() async throws {
        let executor = try await FakeCoreExecutor.makeDefault()
        let screens = CoreScreens()
        let client = try CoreClient()
        let suiteName = "corehost-run-cmd-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let host = CoreHost(
            client: client,
            executor: executor,
            screens: screens,
            defaults: ScopeDefaults(defaults),
            scopeHmacKey: scopeHmacKey(localKey: Demo.account.localKey),
            clock: { FakeRelayAuthority.nowMs },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) }
        )
        await host.wireExecutorHooks()
        await host.startAsync()
        await waitUntil { screens.connection.state == .connected }

        // 1. Happy path returns the daemon CommandResult through Core over the relay byte pipe.
        let okRes = try await host.runMachineCommand(
            machineID: executor.machineID,
            command: .with { $0.getSettings = PBGetSettingsRequest() },
            skipConnectivityPreflight: true
        )
        #expect(okRes.outcome == "executed")
        if case .getSettings = okRes.kind {
            // expected
        } else {
            Issue.record("expected getSettings command result")
        }

        // 2. Cancelling the caller task sends CancelMachineCommandAction and cancels the in-flight relay Forward RPC.
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
            try await host.runMachineCommand(
                machineID: executor.machineID,
                command: .with { $0.getSettings = PBGetSettingsRequest() },
                skipConnectivityPreflight: true
            )
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
