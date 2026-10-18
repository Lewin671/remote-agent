import Foundation
import RAProto
@testable import RACore

/// Synthetic fixture identities approve enrollment through the real server.
func approvedClient(authorizer: Account, http: HTTP, kind: PBDeviceKind = .client) async throws -> Account {
    let api = try Api(server: authorizer.server, http: http)
    let headers = Api.headers(token: try await login(api: api, account: authorizer))
    let a = try PairKey(), b = try PairKey()
    let seed = randomBytes(32), pub = try SigningKey(seed: seed).publicKey
    var create = PBCreatePairingRequest(); create.commit = Pairing.commit(initiatorPublic: a.publicKey)
    let created = try await api.pairing.createPairing(request: create, headers: headers).value()
    var join = PBJoinPairingRequest(); join.code = created.code; join.joinerPublic = b.publicKey
    join.joinerKind = kind; join.devicePublicKey = pub
    let joined = try await api.pairing.joinPairing(request: join).value()
    var reveal = PBRevealPairingRequest(); reveal.pairingID = created.pairingID; reveal.initiatorPublic = a.publicKey
    _ = try await api.pairing.revealPairing(request: reveal, headers: headers).value()
    let tr = Pairing.identityTranscript(pairingId: created.pairingID, initiatorPublic: a.publicKey, joinerPublic: b.publicKey, devicePublic: pub, joinerKind: kind.rawValue)
    let shared = try a.derive(otherPublic: b.publicKey, transcript: tr)
    guard shared.key == (try b.derive(otherPublic: a.publicKey, transcript: tr)).key else { throw PairError("test SAS disagrees") }
    let (approval, sig) = try makePairApproval(account: authorizer, pairingId: created.pairingID, candidate: pub, kind: kind, expires: created.expiresMs)
    var p = PBPairingPayload(); p.root = authorizer.root; p.server = authorizer.server
    p.approval = approval; p.approvalSignature = sig
    var h = PBHeader(); h.kind = .pairing; h.requestID = created.pairingID; h.sender = authorizer.deviceId
    var approve = PBApprovePairingRequest(); approve.pairingID = created.pairingID
    approve.payload = try Envelopes.seal(key: shared.key, header: h, message: p)
    approve.approval = approval; approve.approvalSignature = sig
    _ = try await api.pairing.approvePairing(request: approve, headers: headers).value()
    let result = try await joinWithRoot(root: authorizer.root, server: authorizer.server, http: http, kind: kind, enrollment: Enrollment(seed: seed, pairingId: created.pairingID, approval: approval, signature: sig))
    var got = PBGetPairingRequest(); got.pairingID = created.pairingID; got.candidateToken = joined.candidateToken; got.received = true
    _ = try await api.pairing.getPairing(request: got).value()
    return result
}
