import Foundation
import RAProto

@testable import RACore

/// Synthetic identities enroll through actual pinned authority and server.
func approvedClient(authorizer: Account, http: HTTP, kind: PBDeviceKind = .client) async throws
    -> Account
{
    let security = try TestSecurity.get(authorizer.server)
    guard let store = security.store(authorizer.deviceId) else {
        throw TestFailure("synthetic approver store missing")
    }
    let old = try store.readSecurity()
    let descriptor = try DeviceSecurity.pairTrustDescriptor(old.pin, approver: authorizer.deviceId)
    let api = try Api(server: authorizer.server, http: http)
    let headers = Api.headers(token: try await login(api: api, account: authorizer))
    let a = try PairKey()
    let b = try PairKey()
    let seed = randomBytes(32)
    let pub = try SigningKey(seed: seed).publicKey
    var create = PBCreatePairingRequest()
    create.commit = try DeviceSecurity.pairDomainCommit(ephemeral: a.publicKey, trust: descriptor)
    let created = try await api.pairing.createPairing(request: create, headers: headers).value()
    let enrolled = try security.call(
        "enroll",
        [
            "Approver": authorizer.deviceId, "Pair": created.pairingID,
            "Seed": seed.base64EncodedString(), "Kind": kind.rawValue,
        ])
    let record = try DeviceSecurity.Record.parse(security.bytes(enrolled, "Record"))
    guard
        let candidate = record.state.core.members.first(where: { $0.id == deviceId(publicKey: pub) }
        )
    else { throw TestFailure("synthetic candidate missing") }
    let contextJSON = try JSONSerialization.data(withJSONObject: enrolled["PairContext"] as Any)
    let context = try DeviceSecurity.pairContextFromJSON(DeviceSecurity.safeJSONObject(contextJSON))
    var join = PBJoinPairingRequest()
    join.protocolVersion = protocolVersion
    join.code = created.code
    join.joinerPublic = b.publicKey
    join.joinerKind = kind
    join.devicePublicKey = pub
    join.deviceHpkePublicKey = candidate.x
    let joined = try await api.pairing.joinPairing(request: join).value()
    var reveal = PBRevealPairingRequest()
    reveal.pairingID = created.pairingID
    reveal.initiatorPublic = a.publicKey
    reveal.securityContext = contextJSON
    _ = try await api.pairing.revealPairing(request: reveal, headers: headers).value()
    let tr = Pairing.domainTranscript(
        pairingId: created.pairingID, initiatorPublic: a.publicKey, joinerPublic: b.publicKey,
        devicePublic: pub, hpkePublic: candidate.x, joinerKind: kind.rawValue, trust: descriptor)
    let shared = try a.derive(otherPublic: b.publicKey, transcript: tr)
    guard shared.key == (try b.derive(otherPublic: a.publicKey, transcript: tr)).key else {
        throw TestFailure("synthetic SAS mismatch")
    }
    var confirm = PBGetPairingRequest()
    confirm.pairingID = created.pairingID
    confirm.candidateToken = joined.candidateToken
    confirm.candidateSignature = try DeviceSecurity.signPairContext(
        context, signingKey: SigningKey(seed: seed), action: .accept)
    _ = try await api.pairing.getPairing(request: confirm).value()
    let (approval, sig) = try makePairApproval(
        account: authorizer, pairingId: created.pairingID, candidate: pub, kind: kind,
        expires: created.expiresMs)
    var payload = PBPairingPayload()
    payload.root = authorizer.root
    payload.server = authorizer.server
    payload.approval = approval
    payload.approvalSignature = sig
    payload.securityState = try security.bytes(enrolled, "State")
    var header = PBHeader()
    header.kind = .pairing
    header.requestID = created.pairingID
    header.sender = authorizer.deviceId
    var approve = PBApprovePairingRequest()
    approve.pairingID = created.pairingID
    approve.payload = try Envelopes.seal(key: shared.key, header: header, message: payload)
    approve.approval = approval
    approve.approvalSignature = sig
    _ = try await api.pairing.approvePairing(request: approve, headers: headers).value()
    let result = try await joinWithRoot(
        root: authorizer.root, server: authorizer.server, http: http, kind: kind,
        enrollment: Enrollment(
            seed: seed, pairingId: created.pairingID, approval: approval, signature: sig))
    security.install(record)
    var received = PBGetPairingRequest()
    received.pairingID = created.pairingID
    received.candidateToken = joined.candidateToken
    received.received = true
    _ = try await api.pairing.getPairing(request: received).value()
    return result
}
