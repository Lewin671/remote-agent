import Foundation
import RAProto

/// Independent private item; the existing RegistrationIntent format is unchanged.
/// Survives death while installing the approved bootstrap or registering it.
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
        Task { await acknowledge(http: http) }
        return account
    }
    /// Delivery is advisory once the device can authenticate. In particular a
    /// missing/canceled/expired pairing cannot invalidate that device identity.
    func acknowledge(http: HTTP) async {
        var request = PBGetPairingRequest(); request.pairingID = pairingID; request.candidateToken = candidateToken; request.received = true
        _ = try? await Api(server: intent.targetServer, http: http).pairing.getPairing(request: request).value()
    }
}
