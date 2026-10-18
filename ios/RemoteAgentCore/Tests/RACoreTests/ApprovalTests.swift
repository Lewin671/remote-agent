import Foundation
import RAProto
import Testing
@testable import RACore

@Test func goApprovalAndCandidateIdentitySas() throws {
    let v = try loadJSON("internal/e2e/testdata/pair-approval-vectors.json")
    let e = Enrollment(seed: v.bytes("candidate_seed"), pairingId: v.s("pairing_id"), approval: v.bytes("approval"), signature: v.bytes("signature"))
    try verifyPairApproval(e, root: v.bytes("root"), kind: .client, now: 0)
    #expect(throws: (any Error).self) { try verifyPairApproval(e, root: v.bytes("root"), kind: .machine, now: 0) }
    var bad = e.signature; bad[0] ^= 1
    #expect(throws: (any Error).self) { try verifyPairApproval(Enrollment(seed: e.seed, pairingId: e.pairingId, approval: e.approval, signature: bad), root: v.bytes("root"), kind: .client, now: 0) }
    let tr = Pairing.identityTranscript(pairingId: v.s("pairing_id"), initiatorPublic: v.bytes("initiator_public"), joinerPublic: v.bytes("joiner_public"), devicePublic: v.bytes("candidate_public"), joinerKind: 2)
    #expect(tr == v.bytes("transcript"))
    #expect(tr != Pairing.identityTranscript(pairingId: v.s("pairing_id"), initiatorPublic: v.bytes("initiator_public"), joinerPublic: v.bytes("joiner_public"), devicePublic: Data(count: 32), joinerKind: 2))
}
