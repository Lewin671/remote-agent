import Foundation
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
        #expect(k.wrap.hex == v.s("wrap"))
        #expect(k.meta.hex == v.s("meta"))
        #expect(k.command.hex == v.s("command"))
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

    @Test func opensAGoEnvelope() throws {
        let e = v.o("envelope")
        let key = e.bytes("key")
        let env = envelope(e)
        let (h, pt) = try Envelopes.open(key: key, envelope: env)
        #expect(String(decoding: pt, as: UTF8.self) == e.s("plaintext"))
        try Envelopes.expect(h, Place(.event, machineId: "machine", sessionId: "session", seq: 3))
        #expect(throws: OpenError.self) { try Envelopes.expect(h, Place(.event, seq: 4)) }
        #expect(throws: OpenError.self) { try Envelopes.expect(h, Place(.sessionMeta, seq: 3)) }
        var moved = env
        moved.header[moved.header.count - 1] ^= 1
        #expect(throws: OpenError.self) { try Envelopes.open(key: key, envelope: moved) }
        var cut = env
        cut.ciphertext = cut.ciphertext.prefix(15)
        #expect(throws: OpenError.self) { try Envelopes.open(key: key, envelope: cut) }
        #expect(throws: OpenError.self) { try Envelopes.open(key: key, envelope: nil) }
    }

    @Test func opensAPackedGoEnvelope() throws {
        // Sealed packed by Go (Header.compression): its plaintext went through DEFLATE first.
        let e = v.o("packed")
        let env = envelope(e)
        let (h, pt) = try Envelopes.open(key: e.bytes("key"), envelope: env)
        #expect(String(decoding: pt, as: UTF8.self) == e.s("plaintext"))
        #expect(h.compression == .deflate)
        #expect(env.ciphertext.count < pt.count)
        try Envelopes.expect(h, Place(.event, machineId: "machine", sessionId: "session", seq: 4))
    }

    @Test func sealsWhatItOpens() throws {
        let key = Data(repeating: 7, count: 32)
        var h = PBHeader()
        h.kind = .event
        h.machineID = "m"
        h.sessionID = "s"
        // The largest uint64 but 15: seq must not pass through a signed number.
        h.seq = UInt64.max - 15
        let short = Data("短 ✓".utf8)
        let env = try Envelopes.seal(key: key, header: h, plaintext: short)
        let (oh, opt) = try Envelopes.open(key: key, envelope: env)
        #expect(opt == short)
        #expect(oh.seq == UInt64.max - 15)
        #expect(oh.compression == .none)
        #expect(oh.version == envelopeVersion)

        let long = Data(String(repeating: "压缩载荷 ✓ ", count: 60).utf8)
        let packed = try Envelopes.seal(key: key, header: h, plaintext: long)
        let (ph, ppt) = try Envelopes.open(key: key, envelope: packed)
        #expect(ppt == long)
        #expect(ph.compression == .deflate)
        #expect(packed.ciphertext.count < long.count)

        // Kinds that are never packed stay as they are, however well they would pack.
        h.kind = .image
        let image = try Envelopes.seal(key: key, header: h, plaintext: long)
        #expect(try Envelopes.open(key: key, envelope: image).0.compression == .none)

        #expect(throws: OpenError.self) { try Envelopes.open(key: Data(repeating: 8, count: 32), envelope: env) }
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
        #expect(k.wrap.hex == v.s("wrap"))
        #expect(k.meta.hex == v.s("meta"))
        #expect(k.command.hex == v.s("command"))
        #expect(k.notify.hex == v.s("notify"))

        for name in ["envelope", "packed"] {
            let e = v.o(name)
            var env = PBEnvelope()
            env.header = e.bytes("header")
            env.nonce = e.bytes("nonce")
            env.ciphertext = e.bytes("ciphertext")
            let (h, pt) = try Envelopes.open(key: e.bytes("key"), envelope: env)
            #expect(String(decoding: pt, as: UTF8.self) == e.s("plaintext"))
            if let seq = e["seq"] as? String { #expect(String(h.seq) == seq) }
            // Sealing the same content under the same nonce gives Kotlin's bytes: same header encoding, same tag layout.
            if name == "envelope" {
                var again = h
                again.compression = .none
                let ours = try Envelopes.seal(key: e.bytes("key"), header: again, plaintext: pt, nonce: e.bytes("nonce"))
                #expect(ours.header.hex == e.s("header"))
                #expect(ours.ciphertext.hex == e.s("ciphertext"))
            }
        }

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
