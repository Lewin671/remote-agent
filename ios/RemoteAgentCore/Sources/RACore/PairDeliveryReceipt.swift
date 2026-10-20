import Foundation
import RAProto

/// Independent private item; the existing RegistrationIntent format is unchanged.
/// Survives death between registration and the candidate's received acknowledgement.
public struct PairDeliveryReceipt: Codable, Sendable, CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public let intent: RegistrationIntent
    let candidateToken: String
    let pairingID: String
    public var description: String { "PairDeliveryReceipt" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: [:]) }
    init(intent: RegistrationIntent, candidateToken: String, pairingID: String) {
        self.intent = intent; self.candidateToken = candidateToken; self.pairingID = pairingID
    }
    public func resume(http: HTTP, persist: @Sendable (Account, DeviceSecurity.Record) async throws -> Void) async throws -> Account {
        let account = try await intent.resume(http: http, persist: persist)
        var request = PBGetPairingRequest(); request.pairingID = pairingID; request.candidateToken = candidateToken; request.received = true
        let result = try await Api(server: intent.targetServer, http: http).pairing.getPairing(request: request).value()
        try DeviceSecurity.check(result.state == .delivered, .untrusted)
        return account
    }
}
