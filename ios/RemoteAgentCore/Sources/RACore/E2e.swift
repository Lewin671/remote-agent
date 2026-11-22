// Minimal E2E helpers aligned with internal/e2e and Android's core/E2e.kt:
// envelope cryptographic verification and signing run inside Go `clientcore`.
import Foundation
import RAProto
import SwiftProtobuf

public let loginContext = "remote-agent/v1/login\u{0}"

public struct CryptoError: Error, CustomStringConvertible, Equatable {
    let localized: LocalizedMessage
    public var description: String { localized.resolve() }
    public init(_ description: String) { localized = LocalizedMessage(raw: description) }
    public init(_ description: LocalizedMessage) { localized = description }
    public static func == (lhs: CryptoError, rhs: CryptoError) -> Bool { lhs.localized == rhs.localized }
}

/// Derives an account-scoped subkey without needing any raw account root key.
public func deriveScopeKey(key: Data, purpose: String) -> Data {
    hkdf(secret: key, salt: nil, info: Data("remote-agent/v1/\(purpose)".utf8), size: keySize)
}

/// A device is named by the first 16 bytes of its public key's SHA-256.
public func deviceId(publicKey: Data) -> String { sha256(publicKey).prefix(16).hex }

public func loginMessage(_ challenge: Data) -> Data { Data(loginContext.utf8) + challenge }

/// A message for the person using the app, in their language.
public struct UserError: Error, LocalizedError, Equatable, CustomStringConvertible {
    let localized: LocalizedMessage
    public var message: String { localized.resolve() }
    public init(_ message: String) { localized = LocalizedMessage(raw: message) }
    public init(_ message: LocalizedMessage) { localized = message }
    public static func == (lhs: UserError, rhs: UserError) -> Bool { lhs.localized == rhs.localized }
    public var errorDescription: String? { message }
    public var description: String { message }
}

/// Canonical server URL validation; plain HTTP only for local loopback addresses.
public enum Secret: Sendable {
    public static func checkServer(_ url: String) throws -> String {
        let invalid = UserError(L10nMessage.errorsIosInvalidServerAddress())
        let trimmed = url.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let u = URLComponents(string: trimmed), let scheme = u.scheme?.lowercased(),
              var host = u.host?.lowercased(), !host.isEmpty else { throw invalid }
        if u.user != nil || u.password != nil { throw UserError(L10nMessage.errorsTheServerAddressCannotIncludeAUsernameOrPassword()) }
        // URLComponents gives an IPv6 literal without its brackets.
        if host.hasPrefix("[") && host.hasSuffix("]") { host = String(host.dropFirst().dropLast()) }
        let local = ["localhost", "127.0.0.1", "::1"].contains(host)
        switch scheme {
        case "https": break
        case "http": if !local { throw UserError(L10nMessage.errorsTheServerMustUseHttps()) }
        default: throw invalid
        }
        if let port = u.port, !(1...65535).contains(port) { throw invalid }
        let shown = host.contains(":") ? "[\(host)]" : host
        return "\(scheme)://\(shown)" + (u.port.map { ":\($0)" } ?? "")
    }
}

extension Data {
    /// URL-safe base64, with or without padding.
    public init?(base64URL text: String) {
        var s = text.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        while s.hasSuffix("=") { s.removeLast() }
        guard s.count % 4 != 1 else { return nil }
        s += String(repeating: "=", count: (4 - s.count % 4) % 4)
        guard let d = Data(base64Encoded: s) else { return nil }
        self = d
    }

    public var base64URL: String {
        base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

public struct OpenError: Error, Equatable, CustomStringConvertible {
    let localized: LocalizedMessage
    public var description: String { localized.resolve() }
    public init(_ description: String) { localized = LocalizedMessage(raw: description) }
    public init(_ description: LocalizedMessage) { localized = description }
    public static func == (lhs: OpenError, rhs: OpenError) -> Bool { lhs.localized == rhs.localized }
}

/// Where an envelope must have been found; unset fields are not checked, except kind.
public struct Place: Sendable {
    public var kind: PBPayloadKind
    public var machineId = ""
    public var sessionId = ""
    public var seq: UInt64 = 0
    public var requestId = ""

    public init(_ kind: PBPayloadKind, machineId: String = "", sessionId: String = "", seq: UInt64 = 0, requestId: String = "") {
        self.kind = kind
        self.machineId = machineId
        self.sessionId = sessionId
        self.seq = seq
        self.requestId = requestId
    }
}

public func newRequestId() -> String { randomBytes(16).hex }

public var retiredHistoryError: String { L10n.errorsThisHistoryUsesAnOldEncryptionFormatAndCan() }
public struct UnsupportedHistoryError: Error, LocalizedError, Sendable {
    public init() {}
    public var errorDescription: String? { retiredHistoryError }
}
func rejectRetiredHistory(_ envelope: PBEnvelope) throws {
    if try PBHeader(serializedBytes: envelope.header).version == 1 { throw UnsupportedHistoryError() }
}
