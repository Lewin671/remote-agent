import Foundation
import RAProto
import Testing

@testable import RACore

/// The Go envelope vector of protocol 10 (internal/e2e/testdata/v3-vectors.json).
@Suite struct V3VectorsTests {
    let v: [String: Any]
    init() throws { v = try loadJSON("internal/e2e/testdata/v3-vectors.json") }

    @Test func opensTheGoEnvelopeAndChecksItsOrigin() throws {
        let state = try DeviceSecurity.parseState(Data(base64Encoded: v.s("state"))!)
        let wire = DeviceSecurity.Wire(
            formed: DeviceSecurity.Cipher(
                header: Data(base64Encoded: v.s("header"))!,
                nonce: Data(base64Encoded: v.s("nonce"))!,
                cipher: Data(base64Encoded: v.s("cipher"))!,
                origin: Data(base64Encoded: v.s("origin"))!))
        let secret = Data(hex: v.s("content_secret"))!
        let (h, plain) = try DeviceSecurity.open(
            wire, state: state, head: nil, live: false, now: 0, secret: secret, sessionSeed: nil)
        #expect(plain == Data(base64Encoded: v.s("plain"))!)
        #expect(h.purpose == .command)
        let env = try DeviceSecurity.envelope(wire)
        #expect(
            try DeviceSecurity.wireBytes(DeviceSecurity.fromEnvelope(env))
                == DeviceSecurity.wireBytes(wire))
        #expect(try DeviceSecurity.stateBytes(state) == Data(base64Encoded: v.s("state"))!)
    }
}
