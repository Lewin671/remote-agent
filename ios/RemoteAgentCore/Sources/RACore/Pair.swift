// Pairing crypto, delegated to Go clientcore corebind + SecCall.
import Foundation

private let badPairKey = UserError(L10nMessage.securityInvalidPairingKey())

/// One side's ephemeral X25519 key pair.
public struct PairKey: Sendable {
    private let priv: Data
    public let publicKey: Data

    public init(privateKey: Data = randomBytes(keySize)) throws {
        guard privateKey.count == keySize,
              let pub = try? SecCall.x25519PublicKey(privateKey)
        else { throw badPairKey }
        self.priv = privateKey
        self.publicKey = pub
    }

    /// The shared secrets with the other side; rejects keys that give an all-zero result.
    public func derive(otherPublic: Data, transcript: Data) throws -> PairSecrets {
        guard otherPublic.count == keySize,
              let shared = try? SecCall.x25519Shared(priv, otherPublic),
              shared.contains(where: { $0 != 0 })
        else { throw badPairKey }
        let (key, sas, emoji) = try SecCall.derivePairSecrets(shared: shared, transcript: transcript)
        return PairSecrets(sas: sas, key: key, emoji: emoji)
    }
}

public struct PairSecrets: Sendable {
    public let sas: Data
    public let key: Data
    public let emoji: [String]
}

public enum Pairing {
    public static func domainTranscript(
        pairingId: String, initiatorPublic: Data, joinerPublic: Data, devicePublic: Data,
        hpkePublic: Data, joinerKind: Int, trust: Data
    ) throws -> Data {
        try SecCall.pairDomainTranscript(
            pairingId: pairingId,
            initiatorPublic: initiatorPublic,
            joinerPublic: joinerPublic,
            deviceEdPublic: devicePublic,
            deviceXPublic: hpkePublic,
            joinerKind: joinerKind,
            trustDescriptor: trust
        )
    }

    public static func identityTranscript(
        pairingId: String, initiatorPublic: Data, joinerPublic: Data, devicePublic: Data,
        joinerKind: Int
    ) throws -> Data {
        try SecCall.pairIdentityTranscript(
            pairingId: pairingId,
            initiatorPublic: initiatorPublic,
            joinerPublic: joinerPublic,
            deviceEdPublic: devicePublic,
            joinerKind: joinerKind
        )
    }

    public static func transcript(
        pairingId: String, initiatorPublic: Data, joinerPublic: Data, joinerKind: Int
    ) throws -> Data {
        try SecCall.pairTranscript(
            pairingId: pairingId,
            initiatorPublic: initiatorPublic,
            joinerPublic: joinerPublic,
            joinerKind: joinerKind
        )
    }
}
