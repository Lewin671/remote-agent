// End-to-end encryption, matching internal/e2e in Go, web/src/crypto.ts and
// Android's core/E2e.kt: HKDF-SHA256 key derivation from the account root
// key, AES-256-GCM envelopes whose serialized header is the additional
// data, and Ed25519 signatures. internal/e2e/testdata/vectors.json keeps
// the implementations in step.
import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf

public let registerContext = "remote-agent/v1/register\u{0}"
public let loginContext = "remote-agent/v1/login\u{0}"

/// An Ed25519 key pair from a 32-byte seed.
///
/// CryptoKit salts its Ed25519 signatures, so two signatures of one message
/// differ; each still verifies under RFC 8032, which is all a peer checks.
public struct SigningKey: Sendable {
    private let priv: Curve25519.Signing.PrivateKey
    public let publicKey: Data

    public init(seed: Data) throws {
        guard seed.count == keySize else { throw CryptoError(L10nMessage.errorsIosCryptoSigningSeedBytes(count: keySize)) }
        priv = try Curve25519.Signing.PrivateKey(rawRepresentation: seed)
        publicKey = priv.publicKey.rawRepresentation
    }

    public func sign(_ message: Data) -> Data {
        // Signing with a valid key does not fail; CryptoKit only throws on allocation failure.
        (try? priv.signature(for: message)) ?? Data()
    }
}

public func verifyEd25519(publicKey: Data, message: Data, signature: Data) -> Bool {
    guard publicKey.count == 32, signature.count == 64,
          let key = try? Curve25519.Signing.PublicKey(rawRepresentation: publicKey) else { return false }
    return key.isValidSignature(signature, for: message)
}

public struct CryptoError: Error, CustomStringConvertible, Equatable {
    let localized: LocalizedMessage
    public var description: String { localized.resolve() }
    public init(_ description: String) { localized = LocalizedMessage(raw: description) }
    public init(_ description: LocalizedMessage) { localized = description }
    public static func == (lhs: CryptoError, rhs: CryptoError) -> Bool { lhs.description == rhs.description }
}

/// The account signing key derived from its root key.
public struct Keys: Sendable {
    public let account: SigningKey
    public static func derive(root: Data) throws -> Keys {
        guard root.count == keySize else { throw CryptoError(L10nMessage.errorsIosCryptoRootKeyBytes(count: keySize)) }
        return Keys(
            account: try SigningKey(seed: deriveKey(root: root, purpose: "account-sign")))
    }

    public static func deriveKey(root: Data, purpose: String) -> Data {
        hkdf(secret: root, salt: nil, info: Data("remote-agent/v1/\(purpose)".utf8), size: keySize)
    }
}

/// A device is named by the first 16 bytes of its public key's SHA-256.
public func deviceId(publicKey: Data) -> String { sha256(publicKey).prefix(16).hex }

public func registerMessage(_ registration: Data) -> Data { Data(registerContext.utf8) + registration }

public func loginMessage(_ challenge: Data) -> Data { Data(loginContext.utf8) + challenge }

/// A message for the person using the app, in their language.
public struct UserError: Error, LocalizedError, Equatable, CustomStringConvertible {
    let localized: LocalizedMessage
    public var message: String { localized.resolve() }
    public init(_ message: String) { localized = LocalizedMessage(raw: message) }
    public init(_ message: LocalizedMessage) { localized = message }
    public static func == (lhs: UserError, rhs: UserError) -> Bool { lhs.message == rhs.message }
    public var errorDescription: String? { message }
    public var description: String { message }
}

/// What a new device needs to join the account: "ra1.<root>.<server>".
public struct Secret: Equatable, Sendable, CustomStringConvertible {
    public let root: Data
    public let server: String

    public init(root: Data, server: String) {
        self.root = root
        self.server = server
    }

    /// The root key never appears in a description, so it cannot reach a log by accident.
    public var description: String { "Secret(server=\(server))" }

    public static func parse(_ text: String) throws -> Secret {
        let bad = UserError(L10nMessage.errorsThisIsNotARemoteAgentAccountKey())
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard t.hasPrefix("ra1.") else { throw bad }
        let parts = t.dropFirst(4).split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 2,
              let root = Data(base64URL: String(parts[0])),
              let serverBytes = Data(base64URL: String(parts[1])),
              let server = String(data: serverBytes, encoding: .utf8),
              root.count == keySize, !server.isEmpty else { throw bad }
        return Secret(root: root, server: try checkServer(server))
    }

    /// The server's origin; plain http only for local addresses.
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
    public static func == (lhs: OpenError, rhs: OpenError) -> Bool { lhs.description == rhs.description }
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

public enum Envelopes {
    public static let version = envelopeVersion
    static let tagSize = 16

    /// Seals plaintext; the header is serialized once and authenticated as
    /// sent. Pairing payloads are sent without compression.
    public static func seal(key: Data, header: PBHeader, plaintext: Data, nonce: Data = randomBytes(12)) throws -> PBEnvelope {
        guard key.count == keySize, nonce.count == 12 else { throw CryptoError(L10nMessage.errorsIosCryptoBadKeyNonceSize()) }
        guard header.kind == .pairing else { throw UnsupportedHistoryError() }
        var h = header
        if h.version == 0 { h.version = version }
        h.compression = .none
        let headerBytes: Data = try h.serializedData()
        let box = try AES.GCM.seal(
            plaintext, using: SymmetricKey(data: key), nonce: AES.GCM.Nonce(data: nonce),
            authenticating: headerBytes)
        var env = PBEnvelope()
        env.header = headerBytes
        env.nonce = nonce
        // The tag follows the ciphertext, as Go's cipher.AEAD lays it out.
        env.ciphertext = box.ciphertext + box.tag
        return env
    }

    public static func seal(key: Data, header: PBHeader, message: any SwiftProtobuf.Message) throws -> PBEnvelope {
        try seal(key: key, header: header, plaintext: try message.serializedData())
    }

    /// Opens an envelope, unpacks a packed payload, and returns the header exactly as sealed.
    public static func open(key: Data, envelope env: PBEnvelope?) throws -> (PBHeader, Data) {
        let bad = OpenError(L10nMessage.errorsIosCryptoEnvelopeDoesNotOpen())
        guard let env, env.nonce.count == 12, env.ciphertext.count >= tagSize, key.count == keySize else { throw bad }
        let split = env.ciphertext.count - tagSize
        let pt: Data
        do {
            let box = try AES.GCM.SealedBox(
                nonce: AES.GCM.Nonce(data: env.nonce), ciphertext: env.ciphertext.prefix(split),
                tag: env.ciphertext.suffix(tagSize))
            // The header authenticates as the bytes received, never as a re-encoding of them.
            pt = try AES.GCM.open(box, using: SymmetricKey(data: key), authenticating: env.header)
        } catch {
            throw bad
        }
        guard let h = try? PBHeader(serializedBytes: env.header) else { throw OpenError(L10nMessage.errorsIosCryptoMalformedHeader()) }
        guard h.version == version else { throw OpenError(L10nMessage.errorsIosCryptoUnsupportedEnvelopeVersion()) }
        guard h.kind == .pairing else { throw UnsupportedHistoryError() }
        switch h.compression {
        case .none: return (h, pt)
        case .deflate: return (h, try unpack(pt))
        // A compression this version does not know.
        default: throw bad
        }
    }

    public static func expect(_ h: PBHeader, _ want: Place) throws {
        if h.kind != want.kind
            || (!want.machineId.isEmpty && h.machineID != want.machineId)
            || (!want.sessionId.isEmpty && h.sessionID != want.sessionId)
            || (want.seq != 0 && h.seq != want.seq)
            || (!want.requestId.isEmpty && h.requestID != want.requestId) {
            throw OpenError(L10nMessage.errorsIosCryptoEnvelopeLocation())
        }
    }

    /// Opens, checks the place and parses.
    public static func openMessage<T: SwiftProtobuf.Message>(
        key: Data, envelope: PBEnvelope?, want: Place, as type: T.Type = T.self
    ) throws -> (PBHeader, T) {
        let (h, pt) = try open(key: key, envelope: envelope)
        try expect(h, want)
        guard let msg = try? T(serializedBytes: pt) else { throw OpenError(L10nMessage.errorsIosCryptoMalformedContent()) }
        return (h, msg)
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
