// Pairing crypto, matching internal/e2e/pair.go, web/src/pair.ts and
// Android's core/Pair.kt: X25519 keys, the initiator's commitment, the
// transcript, and the four-emoji SAS.
import CryptoKit
import Foundation

private let pairCommitContext = "remote-agent/v1/pair-commit\u{0}"
private let pairTranscriptContext = "remote-agent/v1/pair\u{0}"

private let badPairKey = UserError("配对密钥无效")

/// One side's ephemeral X25519 key pair.
public struct PairKey: Sendable {
    private let priv: Curve25519.KeyAgreement.PrivateKey
    public let publicKey: Data

    public init(privateKey: Data = randomBytes(32)) throws {
        guard privateKey.count == 32 else { throw badPairKey }
        priv = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: privateKey)
        publicKey = priv.publicKey.rawRepresentation
    }

    /// The shared secrets with the other side; rejects keys that give an all-zero result.
    public func derive(otherPublic: Data, transcript: Data) throws -> PairSecrets {
        guard otherPublic.count == 32,
            let other = try? Curve25519.KeyAgreement.PublicKey(rawRepresentation: otherPublic),
            let secret = try? priv.sharedSecretFromKeyAgreement(with: other)
        else { throw badPairKey }
        let shared = secret.withUnsafeBytes { Data($0) }
        // A low-order point gives zero whatever our key is: the secret would be no secret.
        guard shared.contains(where: { $0 != 0 }) else { throw badPairKey }
        return PairSecrets(
            sas: hkdf(
                secret: shared, salt: transcript, info: Data("remote-agent/v1/pair-sas".utf8),
                size: 4),
            key: hkdf(
                secret: shared, salt: transcript, info: Data("remote-agent/v1/pair-key".utf8),
                size: keySize
            ))
    }
}

public struct PairSecrets: Sendable {
    public let sas: Data
    public let key: Data
    public var emoji: [String] { sas.map { sasEmoji[Int($0)] } }
}

public enum Pairing {
    public static func domainTranscript(
        pairingId: String, initiatorPublic: Data, joinerPublic: Data, devicePublic: Data,
        hpkePublic: Data, joinerKind: Int, trust: Data
    ) -> Data {
        let inner = identityTranscript(
            pairingId: pairingId, initiatorPublic: initiatorPublic, joinerPublic: joinerPublic,
            devicePublic: devicePublic, joinerKind: joinerKind)
        return sha256(
            Data("ra3/pair-domain\0".utf8) + u32be(inner.count) + inner + u32be(hpkePublic.count)
                + hpkePublic + u32be(trust.count) + trust)
    }
    public static func identityTranscript(
        pairingId: String, initiatorPublic: Data, joinerPublic: Data, devicePublic: Data,
        joinerKind: Int
    ) -> Data {
        var n = UInt32(devicePublic.count).bigEndian
        return sha256(
            Data("remote-agent/v1/pair-identity\0".utf8)
                + transcript(
                    pairingId: pairingId, initiatorPublic: initiatorPublic,
                    joinerPublic: joinerPublic,
                    joinerKind: joinerKind) + withUnsafeBytes(of: &n) { Data($0) } + devicePublic)
    }

    public static func commit(initiatorPublic: Data) -> Data {
        sha256(Data(pairCommitContext.utf8), initiatorPublic)
    }

    public static func transcript(
        pairingId: String, initiatorPublic: Data, joinerPublic: Data, joinerKind: Int
    ) -> Data {
        var t = Data(pairTranscriptContext.utf8)
        for p in [Data(pairingId.utf8), initiatorPublic, joinerPublic] {
            t += u32be(p.count)
            t += p
        }
        t += u32be(joinerKind)
        return sha256(t)
    }
}

/// The 256 emoji a SAS byte maps to; the same table, in the same order, as every other client.
public let sasEmoji: [String] = [
    "🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼", "🐨", "🐯", "🦁", "🐮", "🐷", "🐸", "🐵", "🐔",
    "🐧", "🐦", "🐤", "🦆", "🦅", "🦉", "🦇", "🐺", "🐗", "🐴", "🦄", "🐝", "🐛", "🦋", "🐌", "🐞",
    "🐜", "🦗", "🕷", "🦂", "🐢", "🐍", "🦎", "🦖", "🦕", "🐙", "🦑", "🦐", "🦞", "🦀", "🐡", "🐠",
    "🐟", "🐬", "🐳", "🐋", "🦈", "🐊", "🐅", "🐆", "🦓", "🦍", "🦧", "🐘", "🦛", "🦏", "🐪", "🐫",
    "🦒", "🦘", "🐃", "🐂", "🐄", "🐎", "🐖", "🐏", "🐑", "🦙", "🐐", "🦌", "🐕", "🐩", "🦮", "🐈",
    "🐓", "🦃", "🦚", "🦜", "🦢", "🦩", "🕊", "🐇", "🦝", "🦨", "🦡", "🦦", "🦥", "🐁", "🐀", "🐿",
    "🦔", "🌵", "🎄", "🌲", "🌳", "🌴", "🌱", "🌿", "☘", "🍀", "🎍", "🎋", "🍃", "🍂", "🍁", "🍄",
    "🐚", "🌾", "💐", "🌷", "🌹", "🥀", "🌺", "🌸", "🌼", "🌻", "🌞", "🌝", "🌛", "🌜", "🌚", "🌕",
    "🌖", "🌗", "🌘", "🌑", "🌒", "🌓", "🌔", "🌙", "🌎", "🌍", "🌏", "🪐", "💫", "⭐", "🌟", "✨",
    "⚡", "☄", "💥", "🔥", "🌪", "🌈", "☀", "🌤", "⛅", "🌥", "☁", "🌦", "🌧", "⛈", "🌩", "🌨",
    "❄", "☃", "⛄", "🌬", "💨", "💧", "💦", "☔", "☂", "🌊", "🍏", "🍎", "🍐", "🍊", "🍋", "🍌",
    "🍉", "🍇", "🍓", "🫐", "🍈", "🍒", "🍑", "🥭", "🍍", "🥥", "🥝", "🍅", "🍆", "🥑", "🥦", "🥬",
    "🥒", "🌶", "🫑", "🌽", "🥕", "🧄", "🧅", "🥔", "🍠", "🥐", "🥯", "🍞", "🥖", "🥨", "🧀", "🥚",
    "🍳", "🧈", "🥞", "🧇", "🥓", "🥩", "🍗", "🍖", "🌭", "🍔", "🍟", "🍕", "🥪", "🥙", "🧆", "🌮",
    "🌯", "🥗", "🥘", "🥫", "🍝", "🍜", "🍲", "🍛", "🍣", "🍱", "🥟", "🦪", "🍤", "🍙", "🍚", "🍘",
    "🍥", "🥠", "🥮", "🍢", "🍡", "🍧", "🍨", "🍦", "🥧", "🧁", "🍰", "🎂", "🍮", "🍭", "🍬", "🍫",
]
