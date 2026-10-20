import Foundation
import RAProto

private let approvalContext = Data("remote-agent/v1/pair-approval\0".utf8)

public struct Enrollment: Sendable {
    public let seed: Data
    public let pairingId: String
    public let approval: Data
    public let signature: Data
    public init(seed: Data, pairingId: String, approval: Data, signature: Data) {
        self.seed = seed; self.pairingId = pairingId; self.approval = approval; self.signature = signature
    }
}

func verifyPairApproval(_ enrollment: Enrollment, root: Data, kind: PBDeviceKind, now: Int64) throws {
    try verifyPairApproval(
        enrollment, accountId: deviceId(publicKey: try Keys.derive(root: root).account.publicKey),
        deviceSeed: enrollment.seed, kind: kind, now: now)
}

func verifyPairApproval(
    _ enrollment: Enrollment, accountId: String, deviceSeed: Data, kind: PBDeviceKind, now: Int64
) throws {
    guard enrollment.approval.count <= 1024 else { throw PairError("配对批准无效") }
    let a = try PBPairApproval(serializedBytes: enrollment.approval)
    let pub = try SigningKey(seed: deviceSeed).publicKey
    guard a.accountID == accountId, a.pairingID == enrollment.pairingId,
          a.candidateDevicePublicKey == pub, a.candidateKind == kind, a.replacesDeviceID.isEmpty, a.nonce.count == 16, a.expiresMs > now,
          a.approverPublicKey.count == 32, deviceId(publicKey: a.approverPublicKey) == a.approverDeviceID,
          a.approverDeviceID != deviceId(publicKey: pub),
          verifyEd25519(publicKey: a.approverPublicKey, message: approvalContext + enrollment.approval, signature: enrollment.signature)
    else { throw PairError("配对批准与这台设备不匹配、已过期或签名无效") }
}

/// The caller supplies the verified current authority after SAS confirmation.
/// The returned bytes must be persisted unchanged before delivery/retry.
public func makePairApproval(
    accountID: String, pairingID: String, signer: SigningKey, candidate: Data,
    kind: PBDeviceKind, expires: Int64, replacesDeviceID: String = "", nonce: Data = randomBytes(16)
) throws -> (approval: Data, signature: Data) {
    try DeviceSecurity.check(DeviceSecurity.identifier(accountID) && DeviceSecurity.identifier(pairingID)
        && candidate.count == 32 && signer.publicKey != candidate && nonce.count == 16 && expires > 0
        && (kind == .client || kind == .machine)
        && (replacesDeviceID.isEmpty || (kind == .machine && DeviceSecurity.identifier(replacesDeviceID)
            && replacesDeviceID != deviceId(publicKey: candidate))))
    var a = PBPairApproval()
    a.accountID = accountID
    a.pairingID = pairingID
    a.approverDeviceID = deviceId(publicKey: signer.publicKey)
    a.approverPublicKey = signer.publicKey
    a.candidateDevicePublicKey = candidate
    a.candidateKind = kind
    a.expiresMs = expires
    a.nonce = nonce
    a.replacesDeviceID = replacesDeviceID
    let raw = try a.serializedData()
    return (raw, signer.sign(approvalContext + raw))
}
