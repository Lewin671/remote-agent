import CryptoKit
import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct DeviceSecurityTests {
    typealias S = DeviceSecurity
    let v: [String: Any]
    init() throws { v = try loadJSON("internal/deviceauth/testdata/domain-v3.json") }
    @Test func compressedProtocol10Envelopes() throws {
        let head = try state("added")
        let fixtures = v["compressed_envelopes"] as! [[String: String]]
        #expect(fixtures.count == 3)
        for fixture in fixtures {
            let wire = try S.parseWire(Data(base64Encoded: fixture["wire"]!)!)
            let (header, packed) = try S.open(
                wire, state: head, head: head, live: false, now: head.core.issuedAt,
                secret: hex("content_secret_2"),
                sessionSeed: Data(base64Encoded: fixture["seed"]!)!)
            #expect(header.compression == 1)
            #expect(try unpackSecurity(header, packed) == Data(base64Encoded: fixture["plain"]!)!)
        }
    }

    func hex(_ key: String) throws -> Data {
        guard let s = v[key] as? String, let b = Data(hex: s) else { throw S.Failure.invalid }
        return b
    }
    func state(_ key: String) throws -> S.State {
        guard let o = v[key] as? [String: Any], let raw = Data(base64Encoded: o.s("state")) else {
            throw S.Failure.invalid
        }
        let s = try S.parseState(raw)
        let h = try S.stateHash(s)
        #expect(h.hex == o.s("hash"))
        return s
    }
    func stmt() throws -> S.RecoveryStatement {
        guard let raw = Data(base64Encoded: v.s("recovery_statement")) else {
            throw S.Failure.invalid
        }
        return try S.RecoveryStatement.parse(raw)
    }
    func pin() throws -> S.Pin { try S.pin(for: try state("genesis")) }
    func phoneX() throws -> Curve25519.KeyAgreement.PrivateKey {
        try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: try hex("phone_x"))
    }
    func machineX() throws -> Curve25519.KeyAgreement.PrivateKey {
        try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: try hex("machine_x"))
    }

    private func signedLease(_ state:S.State) throws -> S.HeadLease {
        var l=S.HeadLease(domain:state.core.domain,head:try S.stateHash(state),issuedAt:state.core.issuedAt,notAfter:state.core.issuedAt+S.lease,signature:Data())
        l.signature=try SigningKey(seed:hex("phone_seed")).sign(l.signBytes());return l
    }
    @Test func committedLeaseAndKeyCacheSurviveStaleSameHeadWrites() throws {
        let genesis=try state("genesis"), head=try state("added")
        let secret=try S.openGrant(pin(),head,recipient:genesis.core.authorityID,x:phoneX())
        var old=try S.Record(pin:pin(),states:[1:genesis,2:head],memberID:genesis.core.authorityID,xPrivate:hex("phone_x"),member:true,secret:secret)
        let first=try S.HeadLease.parse(Data(base64Encoded:v.s("lease"))!)
        let renewed=try S.HeadLease.parse(Data(base64Encoded:v.s("renewed_lease"))!)
        let bootstrap = try S.Record(pin:pin(),states:[1:genesis],memberID:genesis.core.authorityID,xPrivate:hex("phone_x"),member:true,secret:S.openGrant(pin(),genesis,recipient:genesis.core.authorityID,x:phoneX()))
        #expect(try old.includesInstallation(bootstrap))
        #expect(try !bootstrap.includesInstallation(old))
        old.lease=renewed;old.floor=renewed.notAfter;old.contentKeys[1]=Data(repeating:42,count:32)
        var stale=old;stale.lease=first;stale.floor=1;stale.contentKeys=[:]
        #expect(throws:S.Failure.self) { try stale.mergingCommitted(old) }
        stale.lease=nil
        let merged=try stale.mergingCommitted(old)
        #expect(merged.lease==renewed && merged.floor==renewed.notAfter && merged.contentKeys[1]==old.contentKeys[1])
    }
    @Test func verifiesTheGoChainAndGrants() throws {
        let p = try pin()
        let genesis = try state("genesis")
        let added = try state("added")
        let renewal = try state("renewal")
        let recovery = try state("recovery")
        try S.verifyChain(pin: p, states: [genesis, added, renewal, recovery], proofs: [nil, nil, nil, try stmt()])
        #expect(try S.stateBytes(added) == {
            let o = v["added"] as! [String: Any]
            return Data(base64Encoded: o.s("state"))!
        }())
        let phone = try S.openGrant(p, added, recipient: genesis.core.authorityID, x: phoneX())
        #expect(phone == (try hex("content_secret_2")))
        let machine = try S.openGrant(p, added, recipient: (try state("added")).core.members[1].id, x: machineX())
        #expect(machine == (try hex("content_secret_2")))
        // The old authority client has no grant in the recovery state.
        #expect(throws: S.Failure.self) {
            _ = try S.openGrant(p, recovery, recipient: genesis.core.authorityID, x: phoneX())
        }
        #expect(recovery.prevSecret.isEmpty)
        #expect(recovery.core.contentEpoch == renewal.core.contentEpoch + 1)
    }

    @Test func rejectsATamperedStateAndPrevSecret() throws {
        let p = try pin()
        let genesis = try state("genesis")
        let added = try state("added")
        let renewal = try state("renewal")
        let stmt = try stmt()
        var bad = added
        bad.prevSecret[0] ^= 1
        #expect(throws: S.Failure.self) { try S.verifyTransition(parent: genesis, next: bad) }
        bad = renewal
        bad.core.issuedAt += 1
        #expect(throws: S.Failure.self) { try S.verifyTransition(parent: added, next: bad) }
        bad = renewal
        bad.signature[0] ^= 1
        #expect(throws: S.Failure.self) { try S.verifyTransition(parent: added, next: bad) }
        let recovery = try state("recovery")
        #expect(throws: S.Failure.self) {
            try S.verifyTransition(parent: renewal, next: recovery, recovery: nil)
        }
        var replay = recovery
        replay.core.removed = []
        #expect(throws: S.Failure.self) {
            try S.verifyTransition(parent: renewal, next: replay, recovery: stmt)
        }
        #expect(throws: S.Failure.self) {
            try S.verifyChain(pin: p, states: [try state("genesis"), recovery], proofs: [nil, nil])
        }
    }

    @Test func walksPrevSecretAndStopsAtTheRecoveryBoundary() throws {
        let p = try pin()
        let genesis = try state("genesis")
        let added = try state("added")
        let renewal = try state("renewal")
        let recovery = try state("recovery")
        let machine = added.core.members[1]
        let machineKey = try S.openGrant(p, recovery, recipient: machine.id, x: machineX())
        let states: [UInt64: S.State] = [
            genesis.core.epoch: genesis, added.core.epoch: added,
            renewal.core.epoch: renewal, recovery.core.epoch: recovery,
        ]
        var record = try S.Record(
            pin: p, states: states, memberID: machine.id,
            xPrivate: try hex("machine_x"), member: true,
            secret: machineKey,
            contentKeys: [
                added.core.contentEpoch: try hex("content_secret_2"),
                genesis.core.contentEpoch: try hex("content_secret_1"),
            ],
            recovery: [recovery.core.epoch: try stmt()])
        #expect(try record.contentSecret(1) == (try hex("content_secret_1")))
        #expect(try record.contentSecret(2) == (try hex("content_secret_2")))
        #expect(try record.contentSecret(3) == machineKey)
        // The old authority client was removed by the recovery. It keeps its
        // own grants from the states it belonged to, but has nothing at the
        // recovery: the empty PrevSecret is a boundary, not a retry.
        let oldPhone = genesis.core.authorityID
        let removed = try S.Record(
            pin: p, states: states, memberID: oldPhone, xPrivate: try hex("phone_x"),
            member: false, secret: try hex("content_secret_2"),
            recovery: [recovery.core.epoch: try stmt()])
        #expect(try removed.contentSecret(1) == (try hex("content_secret_1")))
        #expect(try removed.contentSecret(2) == (try hex("content_secret_2")))
        #expect(throws: S.Failure.unavailable) { try removed.contentSecret(3) }
    }

    @Test func rejectsAStateFromTheFutureBeforeInstallingIt() async throws {
        let p = try pin()
        let genesis = try state("genesis")
        var future = try state("added")
        future.core.issuedAt = 9_999_999_999_999

        let raw = try S.stateBytes(future)
        let base = try S.Record(
            pin: p, states: [1: genesis], memberID: genesis.core.authorityID,
            xPrivate: try hex("phone_x"), member: true,
            secret: try hex("content_secret_1"))
        let client = DeviceSecurityClient(
            store: MemorySecurityStore(base), memberID: genesis.core.authorityID,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { 1_700_000_000_001 },
            fetchStates: { after in
                var res = PBGetSecurityStatesResponse()
                res.headEpoch = 2
                res.lease = Data()
                if after < 2 {
                    var value = PBSecurityState()
                    value.state = raw
                    res.states = [value]
                }
                return res
            })
        await #expect(throws: S.Failure.clock) { try await client.sync() }
    }

    @Test func verifyChainRefusesFutureStates() throws {
        let genesis = try state("genesis")
        var future = genesis
        future.core.issuedAt = UInt64(Date().timeIntervalSince1970 * 1000) + 10 * 60_000

        future.signature = try SigningKey(seed: try hex("phone_seed")).sign(
            S.signBytes(future))
        let p = S.Pin(
            account: genesis.core.account, domain: genesis.core.domain,
            genesis: try S.stateHash(future))
        #expect(throws: S.Failure.clock) {
            try S.verifyChain(pin: p, states: [future], proofs: [nil])
        }
    }

    @Test func liveControlAlsoChecksTheHeadsIssuedAt() throws {
        let added = try state("added")
        let secret = try hex("content_secret_2")
        let signer = try SigningKey(seed: try hex("phone_seed"))
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        let phone = deviceId(publicKey: signer.publicKey)
        let header = S.Header(
            account: added.core.account, domain: added.core.domain, epoch: added.core.epoch,
            state: try S.stateHash(added), contentEpoch: added.core.contentEpoch,
            sender: phone,
            kind: S.Purpose.command.kind, purpose: .command, machine: machine, session: "",
            request: randomHex(16), seq: 0, deadline: 1, compression: 0)
        let wire = S.Wire(
            formed: try S.form(
                secret: secret, sessionSeed: nil, header: header, plaintext: Data([1]),
                signingKey: signer))
        #expect(throws: S.Failure.clock) {
            _ = try S.open(
                wire, state: added, head: added, live: true, now: 1_600_000_000_000,
                secret: secret, sessionSeed: nil)
        }
        _ = try S.open(
            wire, state: added, head: added, live: true, now: added.core.issuedAt,
            secret: secret, sessionSeed: nil)
    }

    @Test func deviceMetaIsSelfOnly() throws {
        let added = try state("added")
        let phone = added.core.authorityID
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        var h = S.Header(
            account: added.core.account, domain: added.core.domain, epoch: added.core.epoch,
            state: try S.stateHash(added), contentEpoch: added.core.contentEpoch, sender: phone,
            kind: S.Purpose.deviceMeta.kind, purpose: .deviceMeta, machine: "", session: "",
            request: phone, seq: 1, deadline: 0, compression: 0)
        _ = try S.scope(h, added)
        h.request = machine
        #expect(throws: S.Failure.self) { _ = try S.scope(h, added) }
    }

    @Test(arguments: [S.Purpose.event, .sessionMeta, .sessionKey, .machineMeta, .command,
        .result, .terminalOutput, .terminalInput, .notify, .image, .prefs, .deviceMeta,
        .terminalResize, .terminalHistory])
    func everyNewPublicationNeedsALiveLease(purpose: S.Purpose) async throws {
        let genesis = try state("genesis"), head = try state("added")
        let first = try S.HeadLease.parse(Data(base64Encoded: v["lease"] as! String)!)
        let renewedRaw = Data(base64Encoded: v["renewed_lease"] as! String)!
        let clientRole = purpose.control || purpose == .prefs || purpose == .deviceMeta
        let member = head.core.members.first { $0.kind == (clientRole ? 2 : 1) }!
        let machine = head.core.members.first { $0.kind == 1 }!.id
        let key = try SigningKey(seed: hex(clientRole ? "phone_seed" : "machine_seed"))
        let record = try S.Record(pin: pin(), states: [1: genesis, 2: head], memberID: member.id,
            xPrivate: hex(clientRole ? "phone_x" : "machine_x"), member: true,
            secret: hex("content_secret_2"), lease: first)
        let store = MemorySecurityStore(record)
        let clock = PublicationClock(first.issuedAt)
        let sdk = DeviceSecurityClient(store: store, memberID: member.id, signingKey: key,
            clock: { clock.now() }, fetchStates: { _ in
                var res = PBGetSecurityStatesResponse(); res.headEpoch = 2; res.lease = renewedRaw
                return res
            })
        let standalone = purpose == .prefs || purpose == .deviceMeta
        let noSession = standalone || [.machineMeta, .command, .result].contains(purpose)
        let noRequest = [S.Purpose.event, .sessionMeta, .sessionKey, .machineMeta].contains(purpose)
        let request = purpose == .prefs ? "project-prefs" : purpose == .deviceMeta ? member.id : noRequest ? "" : "lease-request-123456"
        let plain = Data([1,2,3]), seed = Data(repeating:7,count:32)
        func seal() async throws -> PBEnvelope {
            try await sdk.seal(purpose: purpose, machine: standalone ? "" : machine,
                session: noSession ? "" : "lease-session", request: request, seq: 1,
                deadline: purpose.control ? first.notAfter+60000 : 0, plaintext: plain, sessionSeed: seed)
        }
        let stored = try await seal()
        clock.set(first.notAfter)
        await #expect(throws: S.Failure.lease) { try await seal() }
        if !purpose.control { #expect(try await sdk.openHistory(stored, sessionSeed: seed).1 == plain) }
        let renewed = try await sdk.sync()
        #expect(renewed.states.count == 2)
        _ = try await seal()
    }

    @Test(arguments: [false, true])
    func failedPullPersistsObservedExpiryAndRefreshesSealingCache(nearSavedFloor: Bool) async throws {
        let genesis = try state("genesis"), head = try state("added")
        let lease = try S.HeadLease.parse(Data(base64Encoded: v.s("lease"))!)
        let key = try SigningKey(seed: hex("phone_seed"))
        let phone = head.core.authorityID
        let floor = nearSavedFloor ? lease.notAfter - 1 : lease.issuedAt
        let record = try S.Record(pin: pin(), states: [1: genesis, 2: head], memberID: phone,
            xPrivate: hex("phone_x"), member: true, floor: floor, floorSaved: floor,
            secret: hex("content_secret_2"), lease: lease)
        let store = MemorySecurityStore(record)
        let clock = PublicationClock(floor)
        let sdk = DeviceSecurityClient(store: store, memberID: phone, signingKey: key,
            clock: { clock.now() }, fetchStates: { _ in
                #expect(try store.readSecurity().floor == lease.notAfter)
                throw S.Failure.unavailable
            })
        _ = try await sdk.identity() // Cache the pre-expiry record.
        clock.set(lease.notAfter)
        await #expect(throws: S.Failure.unavailable) { try await sdk.sync() }
        #expect(try store.readSecurity().floor == lease.notAfter)
        clock.set(lease.issuedAt)
        let reopened = DeviceSecurityClient(store: store, memberID: phone, signingKey: key,
            clock: { clock.now() }, fetchStates: { _ in throw S.Failure.unavailable })
        for client in [sdk, reopened] {
            await #expect(throws: S.Failure.lease) {
                try await client.seal(purpose: .prefs, request: "project-prefs", seq: 1,
                    plaintext: Data([1]))
            }
            await client.close()
        }
    }

    @Test func expiredLeaseExplainsWhyAChangeCannotBeSaved() {
        let text = errorText(S.Failure.lease)
        #expect(text.contains("无法保存"))
        #expect(text.contains("续期"))
        #expect(text.contains("历史"))
    }

    @Test func sealingUsesIndependentRandomNonces() async throws {
        let p = try pin()
        let added = try state("added")
        let phone = added.core.authorityID
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        let secret = try hex("content_secret_2")
        let record = try S.Record(
            pin: p, states: [1: try state("genesis"), 2: added], memberID: phone,
            xPrivate: try hex("phone_x"), member: true,
            secret: secret)
        let store = MemorySecurityStore(record)
        let client = DeviceSecurityClient(
            store: store, memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt },
            fetchStates: { _ in throw S.Failure.storage })
        var nonces: Set<Data> = []
        for _ in 1...3 {
            let env = try await client.seal(
                purpose: .command, machine: machine, request: "0123456789abcdef",
                deadline: 1_700_000_200_000, plaintext: Data("go".utf8))
            let h = try S.parseHeader(env.securityHeader)
            #expect(h.epoch == added.core.epoch)
            #expect(nonces.insert(env.nonce).inserted)
        }
        // Reopening also uses a fresh random nonce.
        let reopened = DeviceSecurityClient(
            store: store, memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt },
            fetchStates: { _ in throw S.Failure.storage })
        let env = try await reopened.seal(
            purpose: .command, machine: machine, request: "0123456789abcdef",
            deadline: 1_700_000_200_000, plaintext: Data("go".utf8))
        #expect(nonces.insert(env.nonce).inserted)
    }

    @Test func controlNeedsTheHeadLeaseAndInstallsANewContentEpoch() async throws {
        let p = try pin()
        let genesis = try state("genesis")
        let added = try state("added")
        let phone = added.core.authorityID
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        let secret = try hex("content_secret_2")
        var h = S.Header(
            account: added.core.account, domain: added.core.domain, epoch: added.core.epoch,
            state: try S.stateHash(added), contentEpoch: added.core.contentEpoch, sender: phone,
            kind: S.Purpose.command.kind, purpose: .command, machine: machine, session: "",
            request: "0123456789abcdef", seq: 0, deadline: (added.core.issuedAt + S.lease),
            compression: 0)
        let formed = try S.form(
            secret: secret, sessionSeed: nil, header: h, plaintext: Data("no".utf8),
            signingKey: try SigningKey(seed: try hex("phone_seed")))
        let wire = S.Wire(formed: formed)
        _ = try S.verifyOrigin(wire, state: added, head: added, live: true, now: (added.core.issuedAt + S.lease))
        #expect(throws: S.Failure.self) {
            try signedLease(added).valid(added,now: added.core.issuedAt+S.lease+1)
        }
        // A stale content epoch is refused for control.
        h.contentEpoch = genesis.core.contentEpoch
        #expect(throws: S.Failure.self) {
            _ = try S.verifyOrigin(
                S.Wire(
                    formed: try S.form(
                        secret: secret, sessionSeed: nil, header: h, plaintext: Data("no".utf8),
                        signingKey: try SigningKey(seed: try hex("phone_seed")))),
                state: added, head: added, live: true, now: (added.core.issuedAt + S.lease))
        }
        // Installing a state changes the content key.
        let base = try S.Record(
            pin: p, states: [1: genesis], memberID: phone, xPrivate: try hex("phone_x"),
            member: true,
            secret: try hex("content_secret_1"))
        let store = MemorySecurityStore(base)
        let leaseRaw = try signedLease(added).bytes()
        let client = DeviceSecurityClient(
            store: store, memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt },
            fetchStates: { after in
                var res = PBGetSecurityStatesResponse()
                res.headEpoch = 2
                res.lease = leaseRaw
                if after < 2 {
                    var value = PBSecurityState()
                    value.state = try S.stateBytes(added)
                    res.states = [value]
                }
                return res
            })
        let installed = try await client.sync()
        #expect(installed.headEpoch == 2)
        let env = try await client.seal(
            purpose: .command, machine: machine, request: "0123456789abcdef",
            deadline: (added.core.issuedAt + S.lease), plaintext: Data("go".utf8))
        let header = try S.parseHeader(env.securityHeader)
        #expect(header.epoch == 2 && header.contentEpoch == 2)
    }

    @Test func readsAStoredStateOnlyAsHistory() throws {
        let added = try state("added")
        let p = try pin()
        let genesis = try state("genesis")
        let wire = try S.Wire(
            formed: S.Cipher(
                header: try S.headerBytes(
                    S.Header(
                        account: added.core.account, domain: added.core.domain,
                        epoch: added.core.epoch, state: try S.stateHash(added),
                        contentEpoch: added.core.contentEpoch,
                        sender: added.core.authorityID, kind: S.Purpose.event.kind,
                        purpose: .event,
                        machine: added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id,
                        session: "session", request: "", seq: 1, deadline: 0, compression: 0)),
                nonce: randomBytes(12), cipher: randomBytes(16), origin: randomBytes(64)))
        #expect(throws: S.Failure.self) {
            _ = try S.verifyOrigin(wire, state: added, head: genesis, live: true, now: 0)
        }
        #expect(throws: S.Failure.self) {
            _ = try S.verifyOrigin(wire, state: added, head: nil, live: false, now: 0)
        }
        #expect(p.account == added.core.account)
    }
}

/// A storage that keeps the whole record, for counter and chain tests.
final class MemorySecurityStore: DeviceSecurity.Storage, @unchecked Sendable {
    private let lock = NSLock()
    private var record: DeviceSecurity.Record
    init(_ record: DeviceSecurity.Record) {
        var r=record
        if r.lease == nil, let signer=try? SigningKey(seed:Data(repeating:0x11,count:32)), deviceId(publicKey:signer.publicKey)==r.state.core.authorityID {
            var l=DeviceSecurity.HeadLease(domain:r.state.core.domain,head:try! DeviceSecurity.stateHash(r.state),issuedAt:r.state.core.issuedAt,notAfter:r.state.core.issuedAt+DeviceSecurity.lease,signature:Data())
            l.signature=try! signer.sign(l.signBytes());r.lease=l
        }
        self.record=r
    }
    func readSecurity() throws -> DeviceSecurity.Record {
        lock.lock()
        defer { lock.unlock() }
        var r = record
        return r
    }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws {
        throw DeviceSecurity.Failure.conflict
    }
    func writeSecurity(expectedHead: Data, _ next: DeviceSecurity.Record) throws
        -> DeviceSecurity.Record
    {
        lock.lock()
        defer { lock.unlock() }
        guard try DeviceSecurity.stateHash(record.state) == expectedHead else {
            throw DeviceSecurity.Failure.stale
        }
        record = try next.mergingCommitted(record)
        return record
    }
}

private final class PublicationClock: @unchecked Sendable {
    private let lock = NSLock()
    private var value: UInt64
    init(_ value: UInt64) { self.value = value }
    func now() -> UInt64 { lock.withLock { value } }
    func set(_ value: UInt64) { lock.withLock { self.value = value } }
}
