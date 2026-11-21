import Foundation
import RAProto

/// Executes host-side private-key operations requested by Go `clientcore/security`.
public final class SecurityExecutor: @unchecked Sendable {
    private let signer: @Sendable () -> SigningKey?
    private let xPrivate: @Sendable () -> Data?

    public init(
        signer: @escaping @Sendable () -> SigningKey?,
        xPrivate: @escaping @Sendable () -> Data?
    ) {
        self.signer = signer
        self.xPrivate = xPrivate
    }

    public func keySign(_ effect: PBSecurityKeySignEffect) -> PBSecurityKeySignedEvent {
        var ev = PBSecurityKeySignedEvent()
        ev.opID = effect.opID
        guard let key = signer() else {
            ev.error = "security signer not configured"
            return ev
        }
        guard !effect.message.isEmpty else {
            ev.error = "empty message to sign"
            return ev
        }
        ev.signature = key.sign(effect.message)
        return ev
    }

    public func keyAgree(_ effect: PBSecurityKeyAgreeEffect) -> PBSecurityKeyAgreedEvent {
        var ev = PBSecurityKeyAgreedEvent()
        ev.opID = effect.opID
        guard let priv = xPrivate(), priv.count == keySize, effect.peerPublic.count == keySize else {
            ev.error = "security key agreement key unavailable"
            return ev
        }
        do {
            ev.shared = try SecCall.x25519Shared(priv, effect.peerPublic)
            return ev
        } catch {
            ev.error = "key agreement failed"
            return ev
        }
    }
}
