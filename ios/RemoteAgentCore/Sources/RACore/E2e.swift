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
        guard seed.count == keySize else { throw CryptoError("signing seed must be \(keySize) bytes") }
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
    public let description: String
    public init(_ description: String) { self.description = description }
}

/// The keys derived from an account's root key, one per purpose.
public struct Keys: Sendable {
    public let account: SigningKey
    public let wrap: Data
    public let meta: Data
    public let command: Data
    public let notify: Data
    /// Seals account preference objects (ProjectPrefs).
    public let prefs: Data
    /// Seals client devices' metadata (DeviceMeta).
    public let deviceMeta: Data

    public static func derive(root: Data) throws -> Keys {
        guard root.count == keySize else { throw CryptoError("root key must be \(keySize) bytes") }
        return Keys(
            account: try SigningKey(seed: deriveKey(root: root, purpose: "account-sign")),
            wrap: deriveKey(root: root, purpose: "wrap"),
            meta: deriveKey(root: root, purpose: "machine-meta"),
            command: deriveKey(root: root, purpose: "command"),
            notify: deriveKey(root: root, purpose: "notify"),
            prefs: deriveKey(root: root, purpose: "account-prefs"),
            deviceMeta: deriveKey(root: root, purpose: "device-meta"))
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
    public let message: String
    public init(_ message: String) { self.message = message }
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
        let bad = UserError("这不是 remote-agent 的账户密钥")
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
        let invalid = UserError("server 地址无效")
        let trimmed = url.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let u = URLComponents(string: trimmed), let scheme = u.scheme?.lowercased(),
              var host = u.host?.lowercased(), !host.isEmpty else { throw invalid }
        if u.user != nil || u.password != nil { throw UserError("server 地址不能包含用户名或密码") }
        // URLComponents gives an IPv6 literal without its brackets.
        if host.hasPrefix("[") && host.hasSuffix("]") { host = String(host.dropFirst().dropLast()) }
        let local = ["localhost", "127.0.0.1", "::1"].contains(host)
        switch scheme {
        case "https": break
        case "http": if !local { throw UserError("server 必须使用 https") }
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
    public let description: String
    public init(_ description: String) { self.description = description }
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

/// Whether payloads of a kind are packed before sealing. Session keys are
/// random, images are already compressed, and pairing payloads are read
/// before the devices share a protocol check.
private func packs(_ kind: PBPayloadKind) -> Bool {
    switch kind {
    case .sessionKey, .image, .pairing: return false
    default: return true
    }
}

public enum Envelopes {
    public static let version = envelopeVersion
    static let tagSize = 16

    /// Seals plaintext; the header is serialized once and authenticated as
    /// sent. A payload that packs smaller is sealed packed, which the header
    /// says (Header.compression); the caller's value of it is not used.
    public static func seal(key: Data, header: PBHeader, plaintext: Data, nonce: Data = randomBytes(12)) throws -> PBEnvelope {
        guard key.count == keySize, nonce.count == 12 else { throw CryptoError("bad key or nonce size") }
        let packed = packs(header.kind) && plaintext.count >= packMin ? pack(plaintext) : nil
        var h = header
        if h.version == 0 { h.version = version }
        h.compression = packed != nil ? .deflate : .none
        let headerBytes: Data = try h.serializedData()
        let box = try AES.GCM.seal(
            packed ?? plaintext, using: SymmetricKey(data: key), nonce: AES.GCM.Nonce(data: nonce),
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
        let bad = OpenError("envelope does not open")
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
        guard let h = try? PBHeader(serializedBytes: env.header) else { throw OpenError("malformed header") }
        guard h.version == version else { throw OpenError("unsupported envelope version") }
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
            throw OpenError("envelope is not where it belongs")
        }
    }

    /// Opens, checks the place and parses.
    public static func openMessage<T: SwiftProtobuf.Message>(
        key: Data, envelope: PBEnvelope?, want: Place, as type: T.Type = T.self
    ) throws -> (PBHeader, T) {
        let (h, pt) = try open(key: key, envelope: envelope)
        try expect(h, want)
        guard let msg = try? T(serializedBytes: pt) else { throw OpenError("malformed content") }
        return (h, msg)
    }
}

private let envelopeSignatureContext = "remote-agent/v1/envelope-signature\u{0}"

/// What a device signs for an envelope (Envelope.signature): a context,
/// then the header, nonce and ciphertext as transmitted, each with its
/// length as a big-endian uint32. Same as SignedEnvelopeBytes in Go.
public func signedEnvelopeBytes(_ env: PBEnvelope) -> Data {
    var out = Data(envelopeSignatureContext.utf8)
    for part in [env.header, env.nonce, env.ciphertext] {
        out += u32be(part.count)
        out += part
    }
    return out
}

/// The envelope with the device's signature, so the daemon can tell which device sent it.
public func signEnvelope(device: SigningKey, _ env: PBEnvelope) -> PBEnvelope {
    var out = env
    out.signature = device.sign(signedEnvelopeBytes(env))
    return out
}

public func newRequestId() -> String { randomBytes(16).hex }
