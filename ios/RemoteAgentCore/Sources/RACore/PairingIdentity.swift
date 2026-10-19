import Foundation
import CryptoKit

/// Long-term candidate keys survive interruption; SAS ephemeral keys do not.
public struct PairingIdentity: Codable, Sendable, CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public let server: String
    public let pairingID: String
    public let seed: Data
    public let x: Data
    public init(server: String, pairingID: String, seed: Data, x: Data) throws {
        try DeviceSecurity.check(seed.count == 32 && x.count == 32, .invalid)
        self.server = server; self.pairingID = pairingID; self.seed = seed; self.x = x
    }
    public static func storageKey(server: String, pairingID: String) -> String {
        "pair-candidate-" + SHA256.hash(data: Data((server + "\0" + pairingID).utf8)).map { String(format: "%02x", $0) }.joined()
    }
    public var description: String { "PairingIdentity" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: [:]) }
}
