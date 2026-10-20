import Connect
import CryptoKit
import Foundation
import RAProto

/// Completion keys only. The recovery credential displayed to the user is absent.
struct AuthorityMaterial: Codable, Sendable {
    let server: String
    let root: Data
    let seed: Data
    let x: Data
    let publicKey: Data
    let chain: Data
    func bytes() throws -> Data { try JSONEncoder().encode(self) }
    static func parse(_ data: Data) throws -> Self { try JSONDecoder().decode(Self.self, from: data) }
}
