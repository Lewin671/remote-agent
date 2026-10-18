import CryptoKit
import Foundation
import Testing

@testable import RACore

/// The reviewed protocol-10 vectors: a recovery boundary and an eight-state
/// chain with a fork, a tampered replay, accumulated tombstone and a
/// `RecoveryEd` rotation.
@Suite struct ProtocolVectorsTests {
    typealias S = DeviceSecurity
    func state(_ value: Any) throws -> S.State {
        guard let o = value as? [String: Any], let raw = Data(base64Encoded: o.s("state")) else {
            throw S.Failure.invalid
        }
        let s = try S.parseState(raw)
        #expect(try S.stateHash(s).hex == o.s("hash"))
        return s
    }
    func hex(_ o: [String: Any], _ key: String) throws -> Data {
        guard let s = o[key] as? String, let b = Data(hex: s) else { throw S.Failure.invalid }
        return b
    }

    @Test func recoveryVectorKeepsTheOldHistoryOnlyOnRetainedDevices() throws {
        let v = try loadJSON("internal/deviceauth/testdata/recovery-v3.json")
        let genesis = try state(v["genesis"] as Any)
        let parent = try state(v["parent"] as Any)
        let recovery = try state(v["recovery"] as Any)
        guard let stmtRaw = Data(base64Encoded: v.s("recovery_statement")) else {
            throw S.Failure.invalid
        }
        let stmt = try S.RecoveryStatement.parse(stmtRaw)
        try S.verifyTransition(parent: parent, next: recovery, recovery: stmt)
        #expect(recovery.prevSecret.isEmpty)
        let pin = try S.pin(for: genesis)
        let machineX = try Curve25519.KeyAgreement.PrivateKey(
            rawRepresentation: try hex(v, "machine_x"))
        guard let machine = recovery.core.members.first(where: { $0.kind == S.Kind.machine.rawValue })
        else { throw S.Failure.invalid }
        _ = try S.openGrant(pin, recovery, recipient: machine.id, x: machineX)
        #expect(recovery.core.member(parent.core.authorityID) == nil)
        let newSeed = try hex(v, "new_phone_seed")
        let newX = try Curve25519.KeyAgreement.PrivateKey(
            rawRepresentation: try hex(v, "new_phone_x"))
        let newID = deviceId(publicKey: try SigningKey(seed: newSeed).publicKey)
        let secret = try S.openGrant(pin, recovery, recipient: newID, x: newX)
        let record = try S.Record(
            pin: pin,
            states: [
                genesis.core.epoch: genesis, parent.core.epoch: parent,
                recovery.core.epoch: recovery,
            ],
            memberID: newID, xPrivate: newX.rawRepresentation, member: true,
            counterEpoch: recovery.core.contentEpoch, secret: secret,
            recovery: [recovery.core.epoch: stmt])
        #expect(try record.contentSecret(recovery.core.contentEpoch) == secret)
        #expect(throws: S.Failure.unavailable) {
            try record.contentSecret(parent.core.contentEpoch)
        }
    }

    @Test func chainVectorVerifiesForkTombstoneAndRecoveryRotation() throws {
        let v = try loadJSON("internal/deviceauth/testdata/chain-v3.json")
        guard let values = v["states"] as? [Any], values.count == 8 else {
            throw S.Failure.invalid
        }
        let states = try values.map(state)
        let pin = try S.pin(for: states[0])
        try S.verifyChain(pin: pin, states: states, proofs: Array(repeating: nil, count: 8))
        let fork = try state(v["fork"] as Any)
        try S.verifyTransition(parent: states[0], next: fork)
        #expect(try S.stateHash(fork) != S.stateHash(states[1]))
        let bad = try state(v["bad_replay"] as Any)
        #expect(throws: S.Failure.self) { try S.verifyTransition(parent: states[0], next: bad) }
        #expect(states[7].core.removed.count == 1)
        #expect(states[7].core.recoveryEd != states[6].core.recoveryEd)
        // Re-adding a tombstoned ID is rejected even before signatures.
        var readd = states[6]
        let machine = states[4].core.members.first { $0.kind == S.Kind.machine.rawValue }!
        readd.core.members.append(machine)
        readd.core.members.sort { $0.id < $1.id }
        #expect(throws: S.Failure.self) { try S.membershipDelta(states[5], readd) }
    }
}
