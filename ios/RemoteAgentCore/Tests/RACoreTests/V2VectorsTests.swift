import Foundation
import RAProto
import Testing

@testable import RACore

/// The Go vectors of the v2 protocol (internal/e2e/testdata/v2-vectors.json), checked in Swift.
@Suite struct V2VectorsTests {
    let v: [String: Any]

    init() throws { v = try loadJSON("internal/e2e/testdata/v2-vectors.json") }

    private func env(_ o: [String: Any]) -> PBEnvelope {
        var e = PBEnvelope()
        e.header = o.bytes("header")
        e.nonce = o.bytes("nonce")
        e.ciphertext = o.bytes("ciphertext")
        return e
    }

    @Test func derivesThePrefsAndDeviceMetaKeys() throws {
        let k = try Keys.derive(root: v.bytes("root"))
        #expect(k.prefs.hex == v.s("account_prefs"))
        #expect(k.deviceMeta.hex == v.s("device_meta"))
        #expect(k.notify.hex == v.s("notify"))
    }

    @Test func signsCommandsAsGoChecksThem() throws {
        let c = v.o("command_signature")
        let device = try SigningKey(seed: c.bytes("device_seed"))
        #expect(device.publicKey.hex == c.s("device_public_key"))
        #expect(deviceId(publicKey: device.publicKey) == c.s("device_id"))
        let e = env(c)
        #expect(signedEnvelopeBytes(e).hex == c.s("signed_message"))
        // Go's signature over those bytes verifies here.
        #expect(verifyEd25519(publicKey: device.publicKey, message: signedEnvelopeBytes(e), signature: c.bytes("signature")))
        // CryptoKit salts its signatures, so ours is not Go's byte for byte:
        // it is checked the way the daemon checks it, by verifying.
        let signed = signEnvelope(device: device, e)
        #expect(signed.signature.count == 64)
        #expect(verifyEd25519(publicKey: device.publicKey, message: signedEnvelopeBytes(signed), signature: signed.signature))
        // Any change to what was signed breaks it.
        var moved = signed
        moved.nonce = Data(count: 12)
        #expect(!verifyEd25519(publicKey: device.publicKey, message: signedEnvelopeBytes(moved), signature: signed.signature))
    }

    @Test func opensAnImage() throws {
        let i = v.o("image")
        let (h, p) = try Envelopes.openMessage(key: i.bytes("key"), envelope: env(i), want: Place(.image), as: PBImagePayload.self)
        #expect(h.machineID == "machine")
        #expect(h.sessionID == "session")
        #expect(try PBImagePayload(serializedBytes: i.bytes("plaintext")) == p)
        #expect(p.format == .png)
    }

    @Test func opensPrefsAndDeviceMeta() throws {
        let a = v.o("account_prefs_object")
        let (h, prefs) = try Envelopes.openMessage(
            key: a.bytes("key"), envelope: env(a), want: Place(.accountPrefs, requestId: "project-prefs"), as: PBProjectPrefs.self)
        #expect(try PBProjectPrefs(serializedBytes: a.bytes("plaintext")) == prefs)
        #expect(prefs.schema == 1)
        #expect(h.seq > 0)
        let d = v.o("device_meta_object")
        let (_, meta) = try Envelopes.openMessage(key: d.bytes("key"), envelope: env(d), want: Place(.deviceMeta), as: PBDeviceMeta.self)
        #expect(meta.platform == "android")
    }
}
