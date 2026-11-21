import Foundation
import RAProto
import Testing

@testable import RACore

@Test(.timeLimit(.minutes(2))) func sharedMachineReplacementLineage() async throws {
    let v = try loadJSON("internal/e2e/testdata/machine-replacement-vectors.json")
    let states = try (v["states"] as! [String]).map {
        try DeviceSecurity.parseState(Data(base64Encoded: $0)!)
    }
    let pin = try DeviceSecurity.pin(for: states[0])
    let target = v.s("target")
    let old = v.s("old")
    let xPrivate = Data(base64Encoded: v.s("x"))!
    let secret = try DeviceSecurity.openGrant(pin, states.last!, recipient: target, xPrivate: xPrivate)
    let record = try DeviceSecurity.Record(
        pin: pin, states: Dictionary(uniqueKeysWithValues: states.map { ($0.core.epoch, $0) }),
        memberID: target, xPrivate: xPrivate, member: true, secret: secret)
    let security = AuthoritySecurity(
        store: MemorySecurityStore(record), memberID: target,
        signingKey: try SigningKey(seed: Data(repeating: 82, count: 32)),
        fetchStates: { _ in throw DeviceSecurity.Failure.untrusted })
    func proofList(_ key: String) -> [PBMachineReplacementProof] {
        (v[key] as! [[String: Any]]).map { p in
            var out = PBMachineReplacementProof()
            out.approval = Data(base64Encoded: p["approval"] as! String)!
            out.signature = Data(base64Encoded: p["signature"] as! String)!
            return out
        }
    }
    let orderedStates = Array(1...record.headEpoch).compactMap { record.states[$0] }
    let proofs = proofList("proofs")
    let skewOrigins = try DeviceSecurity.machineReplacementOrigins(
        account: pin.account, target: target, states: orderedStates, proofs: proofList("clock_skew_proofs"))
    #expect(skewOrigins.contains(old))
    #expect(throws: (any Error).self) {
        try DeviceSecurity.machineReplacementOrigins(
            account: pin.account, target: target, states: orderedStates, proofs: proofList("expired_proofs"))
    }
    let origins = try DeviceSecurity.machineReplacementOrigins(
        account: pin.account, target: target, states: orderedStates, proofs: proofs)
    #expect(origins.contains(old))
    let missing = try DeviceSecurity.machineReplacementOrigins(
        account: pin.account, target: target, states: orderedStates, proofs: [])
    #expect(!missing.contains(old))
    var bad = proofs
    bad[0].signature[0] ^= 1
    #expect(throws: (any Error).self) {
        try DeviceSecurity.machineReplacementOrigins(
            account: pin.account, target: target, states: orderedStates, proofs: bad)
    }
    #expect(throws: (any Error).self) {
        try DeviceSecurity.machineReplacementOrigins(
            account: pin.account, target: old, states: orderedStates, proofs: proofs)
    }
    await security.close()
}
