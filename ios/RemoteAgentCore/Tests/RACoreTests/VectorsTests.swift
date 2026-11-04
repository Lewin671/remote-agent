import Foundation
import zlib
import RAProto
import Testing

@testable import RACore

/// The Go vectors, checked in Swift.
@Suite struct VectorsTests {
    let v: [String: Any]

    init() throws { v = try loadJSON("internal/e2e/testdata/vectors.json") }

    private func envelope(_ e: [String: Any]) -> PBEnvelope {
        var env = PBEnvelope()
        env.header = e.bytes("header")
        env.nonce = e.bytes("nonce")
        env.ciphertext = e.bytes("ciphertext")
        return env
    }

    @Test func derivesTheSameKeys() throws {
        let k = try Keys.derive(root: v.bytes("root"))
        #expect(k.account.publicKey.hex == v.s("account_public_key"))
        #expect(deviceId(publicKey: v.bytes("account_public_key")) == v.s("device_id"))
    }

    @Test func verifiesGoRegistrationSignaturesAndSignsOnesThatVerify() throws {
        let k = try Keys.derive(root: v.bytes("root"))
        let r = v.o("register_signature")
        let msg = registerMessage(r.bytes("message"))
        #expect(verifyEd25519(publicKey: k.account.publicKey, message: msg, signature: r.bytes("signature")))
        // CryptoKit's signatures are salted, so ours is compared by verifying it, not byte for byte.
        let ours = k.account.sign(msg)
        #expect(ours.count == 64)
        #expect(verifyEd25519(publicKey: k.account.publicKey, message: msg, signature: ours))
        #expect(!verifyEd25519(publicKey: k.account.publicKey, message: msg + Data([0]), signature: ours))
    }

    @Test func refusesAPayloadThatUnpacksTooLarge() throws {
        let big = Data(repeating: 0x61, count: 4096)
        let packed = try #require(pack(big))
        #expect(try unpack(packed) == big)
        #expect(throws: OpenError.self) { try unpack(packed, max: 4095) }
        #expect(throws: OpenError.self) { try unpack(packed.prefix(packed.count - 2)) }
        #expect(throws: OpenError.self) { try unpack(Data([0xff, 0xff, 0xff])) }
        // Random bytes do not pack smaller.
        #expect(pack(randomBytes(512)) == nil)
    }

    @Test func parsesTheSecret() throws {
        let sec = try Secret.parse(v.s("secret"))
        #expect(sec.root.hex == v.s("root"))
        #expect(sec.server == "https://ra.example.com")
        #expect(throws: UserError.self) { try Secret.parse("ra1.abc") }
        #expect(throws: UserError.self) { try Secret.parse("nope") }
        #expect(!String(describing: sec).contains(sec.root.base64URL))
    }

    @Test func acceptsOnlySafeServerAddresses() throws {
        #expect(try Secret.checkServer(" https://RA.example.com/path?q=1 ") == "https://ra.example.com")
        #expect(try Secret.checkServer("https://ra.example.com:8443") == "https://ra.example.com:8443")
        #expect(try Secret.checkServer("http://localhost:8080") == "http://localhost:8080")
        #expect(try Secret.checkServer("http://127.0.0.1:8080") == "http://127.0.0.1:8080")
        #expect(try Secret.checkServer("http://[::1]:8080") == "http://[::1]:8080")
        #expect(throws: UserError.self) { try Secret.checkServer("http://ra.example.com") }
        #expect(throws: UserError.self) { try Secret.checkServer("https://user:pw@ra.example.com") }
        #expect(throws: UserError.self) { try Secret.checkServer("ftp://ra.example.com") }
        #expect(throws: UserError.self) { try Secret.checkServer("ra.example.com") }
        #expect(throws: UserError.self) { try Secret.checkServer("") }
    }

    @Test func pairsTheSameWay() throws {
        let p = v.o("pairing")
        let initiator = try PairKey(privateKey: p.bytes("initiator_private"))
        let joiner = try PairKey(privateKey: p.bytes("joiner_private"))
        #expect(initiator.publicKey.hex == p.s("initiator_public"))
        #expect(joiner.publicKey.hex == p.s("joiner_public"))
        let tr = Pairing.transcript(
            pairingId: p.s("pairing_id"), initiatorPublic: initiator.publicKey, joinerPublic: joiner.publicKey,
            joinerKind: p["joiner_kind"] as! Int)
        #expect(tr.hex == p.s("transcript"))
        let a = try initiator.derive(otherPublic: joiner.publicKey, transcript: tr)
        let b = try joiner.derive(otherPublic: initiator.publicKey, transcript: tr)
        #expect(a.sas.hex == p.s("sas"))
        #expect(a.key.hex == p.s("key"))
        #expect(b.key.hex == p.s("key"))
        #expect(sasEmoji == p["emoji"] as! [String])
        #expect(Set(sasEmoji).count == 256)
    }

    /// The joiner's kind comes from the server, which may send any int32.
    /// The reviewed Go labels for the pairing domain transcript/commit.
    @Test func bindsThePairingDomainWithTheProtocol10Labels() throws {
        let (a, b) = (Data(repeating: 1, count: 32), Data(repeating: 2, count: 32))
        let device = Data(repeating: 3, count: 32)
        let hpke = Data(repeating: 4, count: 32)
        let trust = Data(repeating: 5, count: 17)
        let transcript = Pairing.domainTranscript(
            pairingId: "pair-test", initiatorPublic: a, joinerPublic: b, devicePublic: device,
            hpkePublic: hpke, joinerKind: 2, trust: trust)
        #expect(transcript.hex == "07413d1f09c11d5ed6b66a63047a31697b33fa87d333f51407abf757f3509072")
        #expect(
            try DeviceSecurity.pairDomainCommit(ephemeral: a, trust: trust).hex
                == "a6306644a67899cfe48699987335847e062ec8614959435e68bb0222c72e619b")
    }

    @Test func aJoinerKindThatIsNoKindStopsThePairingAndNeverTraps() throws {
        for raw in [-1, 0, 3, Int(Int32.min), Int(Int32.max)] {
            var sent = PBGetPairingResponse()
            sent.joinerKind = .UNRECOGNIZED(raw)
            // As it arrives: the number survives the wire.
            let got = try PBGetPairingResponse(serializedBytes: sent.serializedData() as Data)
            #expect(got.joinerKind.rawValue == raw)
            #expect(throws: PairError.self) { try joinerKind(got) }
        }
        for kind in [PBDeviceKind.machine, .client] {
            var st = PBGetPairingResponse()
            st.joinerKind = kind
            #expect(try joinerKind(st) == kind)
        }
        // The transcript writes the number's 32 bits whatever it is, as Go, Kotlin and the page do.
        let id = "p"
        let (a, b) = (Data(repeating: 1, count: 32), Data(repeating: 2, count: 32))
        var want = Data("remote-agent/v1/pair\u{0}".utf8)
        for p in [Data(id.utf8), a, b] { want += Data([0, 0, 0, UInt8(p.count)]) + p }
        want += Data([0xff, 0xff, 0xff, 0xff])
        #expect(Pairing.transcript(pairingId: id, initiatorPublic: a, joinerPublic: b, joinerKind: -1) == sha256(want))
    }

    @Test func rejectsAllZeroSharedSecrets() throws {
        // A low-order point gives an all-zero shared secret.
        #expect(throws: UserError.self) {
            try PairKey().derive(otherPublic: Data(count: 32), transcript: Data(count: 32))
        }
        #expect(throws: UserError.self) {
            try PairKey().derive(otherPublic: Data(count: 31), transcript: Data(count: 32))
        }
    }
}

/// The vectors Kotlin made, which Go and TypeScript also check.
@Suite struct KotlinVectorsTests {
    let v: [String: Any]

    init() throws { v = try loadJSON("internal/e2e/testdata/kotlin-vectors.json") }

    @Test func agreesWithKotlin() throws {
        let k = try Keys.derive(root: v.bytes("root"))
        #expect(k.account.publicKey.hex == v.s("account_public_key"))

        let reg = v.o("register_signature")
        #expect(verifyEd25519(
            publicKey: k.account.publicKey, message: registerMessage(reg.bytes("message")),
            signature: reg.bytes("signature")))

        let login = v.o("login_signature")
        let device = try SigningKey(seed: login.bytes("device_seed"))
        #expect(device.publicKey.hex == login.s("device_public_key"))
        #expect(deviceId(publicKey: device.publicKey) == login.s("device_id"))
        #expect(verifyEd25519(
            publicKey: device.publicKey, message: loginMessage(login.bytes("challenge")),
            signature: login.bytes("signature")))
    }
}

// Builds compressed fixtures for the current bounded decoder.
/// `plaintext` as raw DEFLATE (RFC 1951, no zlib or gzip wrapper), or nil when that is not smaller.
func pack(_ plaintext: Data) -> Data? {
    guard !plaintext.isEmpty else { return nil }
    var z = z_stream()
    // Negative window bits: no zlib header or checksum.
    guard deflateInit2_(&z, Z_DEFAULT_COMPRESSION, Z_DEFLATED, -MAX_WBITS, 8, Z_DEFAULT_STRATEGY,
                        ZLIB_VERSION, Int32(MemoryLayout<z_stream>.size)) == Z_OK else { return nil }
    defer { deflateEnd(&z) }
    // Only a result smaller than the input is of use: a buffer of that size is enough to tell.
    var out = [UInt8](repeating: 0, count: plaintext.count)
    let (status, produced): (Int32, Int) = plaintext.withUnsafeBytes { src in
        out.withUnsafeMutableBytes { dst in
            z.next_in = UnsafeMutablePointer(mutating: src.bindMemory(to: Bytef.self).baseAddress)
            z.avail_in = uInt(src.count)
            z.next_out = dst.bindMemory(to: Bytef.self).baseAddress
            z.avail_out = uInt(dst.count)
            let s = deflate(&z, Z_FINISH)
            return (s, dst.count - Int(z.avail_out))
        }
    }
    guard status == Z_STREAM_END, produced < plaintext.count else { return nil }
    return Data(out[0..<produced])
}
