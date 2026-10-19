import CryptoKit
import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct DeviceSecurityTests {
    typealias S = DeviceSecurity
    let v: [String: Any]
    init() throws { v = try loadJSON("internal/deviceauth/testdata/domain-v3.json") }
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
        // The old authority phone has no grant in the recovery state.
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
        bad.core.notAfter += 1
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
            counterEpoch: recovery.core.contentEpoch, secret: machineKey,
            contentKeys: [
                added.core.contentEpoch: try hex("content_secret_2"),
                genesis.core.contentEpoch: try hex("content_secret_1"),
            ],
            recovery: [recovery.core.epoch: try stmt()])
        #expect(try record.contentSecret(1) == (try hex("content_secret_1")))
        #expect(try record.contentSecret(2) == (try hex("content_secret_2")))
        #expect(try record.contentSecret(3) == machineKey)
        // The old authority phone was removed by the recovery. It keeps its
        // own grants from the states it belonged to, but has nothing at the
        // recovery: the empty PrevSecret is a boundary, not a retry.
        let oldPhone = genesis.core.authorityID
        let removed = try S.Record(
            pin: p, states: states, memberID: oldPhone, xPrivate: try hex("phone_x"),
            member: false, counterEpoch: 2, secret: try hex("content_secret_2"),
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
        future.core.notAfter = future.core.issuedAt + S.lease
        let raw = try S.stateBytes(future)
        let base = try S.Record(
            pin: p, states: [1: genesis], memberID: genesis.core.authorityID,
            xPrivate: try hex("phone_x"), member: true, counterEpoch: 1,
            secret: try hex("content_secret_1"))
        let client = DeviceSecurityClient(
            store: MemorySecurityStore(base), memberID: genesis.core.authorityID,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { 1_700_000_000_001 },
            fetchStates: { after in
                var res = PBGetSecurityStatesResponse()
                res.headEpoch = 2
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
        future.core.notAfter = future.core.issuedAt + S.lease
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
            request: randomHex(16), seq: 0, deadline: 1, compression: 0, counter: 1)
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
            request: phone, seq: 1, deadline: 0, compression: 0, counter: 1)
        _ = try S.scope(h, added)
        h.request = machine
        #expect(throws: S.Failure.self) { _ = try S.scope(h, added) }
    }

    @Test func countersAreReservedInBlocks() async throws {
        let p = try pin()
        let added = try state("added")
        let phone = added.core.authorityID
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        let secret = try hex("content_secret_2")
        let record = try S.Record(
            pin: p, states: [1: try state("genesis"), 2: added], memberID: phone,
            xPrivate: try hex("phone_x"), member: true, counterEpoch: added.core.contentEpoch,
            secret: secret)
        let store = MemorySecurityStore(record)
        let client = DeviceSecurityClient(
            store: store, memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt },
            fetchStates: { _ in throw S.Failure.storage })
        for expected in UInt64(1)...3 {
            let env = try await client.seal(
                purpose: .command, machine: machine, request: "0123456789abcdef",
                deadline: 1_700_000_200_000, plaintext: Data("go".utf8))
            let h = try S.parseHeader(env.securityHeader)
            #expect(h.epoch == added.core.epoch)
            #expect(h.counter == expected)
        }
        #expect(try store.readSecurity().blockEnd == S.counterBlock)
        // A restart skips the rest of the reserved block.
        let reopened = DeviceSecurityClient(
            store: store, memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt },
            fetchStates: { _ in throw S.Failure.storage })
        let env = try await reopened.seal(
            purpose: .command, machine: machine, request: "0123456789abcdef",
            deadline: 1_700_000_200_000, plaintext: Data("go".utf8))
        #expect(try S.parseHeader(env.securityHeader).counter == S.counterBlock + 1)
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
            request: "0123456789abcdef", seq: 0, deadline: added.core.notAfter,
            compression: 0, counter: 1)
        let formed = try S.form(
            secret: secret, sessionSeed: nil, header: h, plaintext: Data("no".utf8),
            signingKey: try SigningKey(seed: try hex("phone_seed")))
        let wire = S.Wire(formed: formed)
        _ = try S.verifyOrigin(wire, state: added, head: added, live: true, now: added.core.notAfter)
        #expect(throws: S.Failure.self) {
            _ = try S.verifyOrigin(
                wire, state: added, head: added, live: true, now: added.core.notAfter + 1)
        }
        // A stale content epoch is refused for control.
        h.contentEpoch = genesis.core.contentEpoch
        #expect(throws: S.Failure.self) {
            _ = try S.verifyOrigin(
                S.Wire(
                    formed: try S.form(
                        secret: secret, sessionSeed: nil, header: h, plaintext: Data("no".utf8),
                        signingKey: try SigningKey(seed: try hex("phone_seed")))),
                state: added, head: added, live: true, now: added.core.notAfter)
        }
        // Installing a state with a new content epoch resets the counter block.
        let base = try S.Record(
            pin: p, states: [1: genesis], memberID: phone, xPrivate: try hex("phone_x"),
            member: true, counter: 5, blockEnd: 100, counterEpoch: 1,
            secret: try hex("content_secret_1"))
        let store = MemorySecurityStore(base)
        let client = DeviceSecurityClient(
            store: store, memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt },
            fetchStates: { after in
                var res = PBGetSecurityStatesResponse()
                res.headEpoch = 2
                if after < 2 {
                    var value = PBSecurityState()
                    value.state = try S.stateBytes(added)
                    res.states = [value]
                }
                return res
            })
        let installed = try await client.sync()
        #expect(installed.headEpoch == 2)
        #expect(installed.counterEpoch == 2)
        #expect(installed.blockEnd == 0)
        let env = try await client.seal(
            purpose: .command, machine: machine, request: "0123456789abcdef",
            deadline: added.core.notAfter, plaintext: Data("go".utf8))
        let header = try S.parseHeader(env.securityHeader)
        #expect(header.epoch == 2 && header.contentEpoch == 2 && header.counter == 1)
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
                        session: "session", request: "", seq: 1, deadline: 0, compression: 0,
                        counter: 1)),
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
    init(_ record: DeviceSecurity.Record) { self.record = record }
    func readSecurity() throws -> DeviceSecurity.Record {
        lock.lock()
        defer { lock.unlock() }
        var r = record
        r.counter = r.blockEnd
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
        record = next
        return record
    }
}
