import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.timeLimit(.minutes(1))) struct SecurityExecutorTests {
    @Test func signAndKeyAgreeDelegateThroughCorebind() throws {
        let seed = Data((0..<32).map { UInt8($0 + 1) })
        let signingKey = try SigningKey(seed: seed)
        let xPriv = Data((0..<32).map { UInt8($0 + 33) })
        let peerXPriv = Data((0..<32).map { UInt8($0 + 65) })
        let peerXPub = try SecCall.x25519PublicKey(peerXPriv)

        let exec = SecurityExecutor(
            signer: { signingKey },
            xPrivate: { xPriv }
        )

        let msg = Data("sign-this-transcript".utf8)
        let signed = exec.keySign(.with { $0.opID = 11; $0.message = msg })
        #expect(signed.opID == 11 && signed.error.isEmpty)
        #expect(verifyEd25519(publicKey: signingKey.publicKey, message: msg, signature: signed.signature))

        let agreed = exec.keyAgree(.with { $0.opID = 22; $0.peerPublic = peerXPub })
        #expect(agreed.opID == 22 && agreed.error.isEmpty)
        #expect(agreed.shared == (try SecCall.x25519Shared(peerXPriv, SecCall.x25519PublicKey(xPriv))))
    }

    @Test func rejectsMissingKeysEmptyMessageAndInvalidPeerPublicKey() throws {
        let seed = Data(repeating: 7, count: 32)
        let signingKey = try SigningKey(seed: seed)
        let xPriv = Data(repeating: 9, count: 32)
        let exec = SecurityExecutor(signer: { signingKey }, xPrivate: { xPriv })

        let emptySign = exec.keySign(.with { $0.opID = 1; $0.message = Data() })
        #expect(emptySign.opID == 1 && !emptySign.error.isEmpty)

        let shortPeer = exec.keyAgree(.with { $0.opID = 2; $0.peerPublic = Data([1, 2, 3]) })
        #expect(shortPeer.opID == 2 && !shortPeer.error.isEmpty)

        let lowOrderPeer = exec.keyAgree(.with { $0.opID = 3; $0.peerPublic = Data(repeating: 0, count: 32) })
        #expect(lowOrderPeer.opID == 3 && !lowOrderPeer.error.isEmpty)

        let missing = SecurityExecutor(signer: { nil }, xPrivate: { nil })
        #expect(!missing.keySign(.with { $0.opID = 4; $0.message = Data("ok".utf8) }).error.isEmpty)
        #expect(!missing.keyAgree(.with { $0.opID = 5; $0.peerPublic = Data(repeating: 5, count: 32) }).error.isEmpty)
    }
}
