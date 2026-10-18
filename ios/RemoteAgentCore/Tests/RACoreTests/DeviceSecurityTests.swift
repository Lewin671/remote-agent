import CryptoKit
import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct DeviceSecurityTests {
    typealias S = DeviceSecurity
    let v: [String: Any]
    init() throws { v = try loadJSON("internal/deviceauth/testdata/domain-v2.json") }
    func data(_ key: String) throws -> Data {
        guard let s = v[key] as? String, let b = Data(base64Encoded: s) else {
            throw S.Failure.invalid
        }
        return b
    }
    func pin() throws -> S.Pin {
        guard let p = v["Pin"] as? [String: Any] else { throw S.Failure.invalid }
        func b(_ key: String) throws -> Data {
            guard let a = p[key] as? [UInt8], a.count == 32 else { throw S.Failure.invalid }
            return Data(a)
        }
        return S.Pin(
            account: p["Account"] as! String, domain: try b("Domain"), genesis: try b("Genesis"),
            authorityID: p["AuthorityID"] as! String, authorityEd: try b("AuthorityEd"),
            authorityX: try b("AuthorityX"))
    }
    @Test func canonicalMembershipGrantAndKey() throws {
        let p = try pin()
        let genesis = try S.parseState(data("State"))
        let next = try S.parseState(data("NextState"))
        try S.verifyState(p, genesis)
        try S.advance(p, genesis, next)
        #expect(try S.stateBytes(next) == data("NextState"))
        let x = try Curve25519.KeyAgreement.PrivateKey(
            rawRepresentation: Data(hex: v.s("WriterX"))!)
        #expect(try S.openGrant(p, next, recipient: v.s("RecipientID"), x: x).hex == v.s("Secret"))
        #expect(throws: (any Error).self) {
            try S.openGrant(
                p, next, recipient: v.s("RecipientID"), x: Curve25519.KeyAgreement.PrivateKey())
        }
        #expect(throws: S.Failure.self) { try S.advance(p, next, genesis) }
        var wrong = next
        wrong.core.authorityEd[0] ^= 1
        #expect(throws: S.Failure.self) { try S.verifyState(p, wrong) }
    }
    @Test func exactCipherAndProjection() throws {
        let p = try pin()
        let state = try S.parseState(data("NextState"))
        let wire = try S.parseWire(data("Wire"))
        let secret = Data(hex: v.s("Secret"))!
        let seed = Data(hex: v.s("SessionSeed"))!
        let (header, plain) = try S.open(p, state, wire, secret: secret, sessionSeed: seed)
        #expect(plain == (try data("Plain")))
        #expect(
            try S.contentKey(secret: secret, sessionSeed: seed, header: header).hex == v.s("Key"))
        #expect(try S.wireBytes(wire) == data("Wire"))
        #expect(try S.headerBytes(header) == data("Header"))
        let env = try S.envelope(wire)
        #expect(try S.wireBytes(S.fromEnvelope(env)) == data("Wire"))
        var moved = env
        var ph = try PBHeader(serializedBytes: moved.header)
        ph.seq += 1
        moved.header = try ph.serializedData()
        #expect(throws: S.Failure.self) { try S.fromEnvelope(moved) }
        var bad = wire
        bad.formed.cipher[0] ^= 1
        #expect(throws: S.Failure.self) { try S.verifyWire(p, state, bad) }
        bad = wire
        bad.formed.origin[0] ^= 1
        #expect(throws: S.Failure.self) { try S.verifyWire(p, state, bad) }
    }
    @Test func truncationCounterAndUnexportedRoot() throws {
        let raw = try data("Header")
        for i in 0..<raw.count {
            #expect(throws: S.Failure.self) { try S.parseHeader(raw.prefix(i)) }
        }
        #expect(throws: S.Failure.self) { try S.parseHeader(raw + Data([0])) }
        let secret = Data(hex: v.s("Secret"))!
        let seed = Data(hex: v.s("SessionSeed"))!
        var h = try S.parseHeader(raw)
        let first = try S.contentKey(secret: secret, sessionSeed: seed, header: h)
        h.counter += 1
        #expect(try S.contentKey(secret: secret, sessionSeed: seed, header: h) != first)
        let formed = try S.form(
            secret: secret, sessionSeed: seed, header: h, plaintext: data("Plain"),
            signingKey: SigningKey(seed: Data(hex: v.s("WriterSeed"))!))
        #expect(formed.origin.count == 64)
        #expect(try S.commitment(formed).count == 32)
    }
}

extension DeviceSecurityTests {
    @Test func opensGoNonceBoundHPKERPC() throws {
        let p = try pin()
        let r = v["Reply"] as! [String: Any]
        let w = v["Waiter"] as! [String: Any]
        let capsule = S.Capsule(
            from: r["From"] as! String, to: r["To"] as! String,
            method: S.Method(rawValue: r["Method"] as! UInt32)!,
            enc: Data(base64Encoded: r["Enc"] as! String)!,
            cipher: Data(base64Encoded: r["Cipher"] as! String)!)
        let waiter = S.Waiter(
            pin: p, sender: w["Sender"] as! String,
            method: S.Method(rawValue: w["Method"] as! UInt32)!,
            nonce: Data(w["Nonce"] as! [UInt8]), requestHash: Data(w["RequestHash"] as! [UInt8]))
        let x = try Curve25519.KeyAgreement.PrivateKey(
            rawRepresentation: Data(hex: v.s("WriterX"))!)
        let reply = try S.reply(waiter, capsule, x: x)
        let state = try S.parseState(data("NextState"))
        let wire = try S.parseWire(data("Wire"))
        _ = try S.attach(wire.formed, jsonReply: reply, pin: p, state: state)
        #expect(
            try S.capsuleBytes(S.parseCapsule(S.capsuleBytes(capsule))) == S.capsuleBytes(capsule))
        var wrong = waiter
        wrong.nonce[0] ^= 1
        #expect(throws: S.Failure.self) { try S.reply(wrong, capsule, x: x) }
        let (request, _) = try S.request(
            pin: p, sender: v.s("RecipientID"),
            signingKey: SigningKey(seed: Data(hex: v.s("WriterSeed"))!), method: .authorize,
            jsonBody: S.authorizationRequest(wire.formed))
        #expect(request.cipher.count > 16)
    }
}

extension DeviceSecurityTests {
    @Test func verifiesFullGoCompletionAndRejectsWrongRevision() throws {
        let v = try loadJSON("internal/deviceauth/testdata/revocation-v2.json")
        let p = v["Pin"] as! [String: Any]
        func h(_ key: String) -> Data { Data(p[key] as! [UInt8]) }
        let pin = S.Pin(
            account: p["Account"] as! String, domain: h("Domain"), genesis: h("Genesis"),
            authorityID: p["AuthorityID"] as! String, authorityEd: h("AuthorityEd"),
            authorityX: h("AuthorityX"))
        let original = try JSONSerialization.data(withJSONObject: v["Original"]!)
        let complete = try JSONSerialization.data(withJSONObject: v["Completion"]!)
        let chain = try (v["Chain"] as! [String]).map { try S.parseState(Data(base64Encoded: $0)!) }
        try S.verifyCompletion(
            pin: pin, completionJSON: complete, originalJSON: original, chain: chain)
        var c = v["Completion"] as! [String: Any]
        var group = c["Group"] as! [String: Any]
        var receipts = group["Receipts"] as! [[String: Any]]
        receipts[0]["Revision"] = 1
        group["Receipts"] = receipts
        c["Group"] = group
        let bad = try JSONSerialization.data(withJSONObject: c)
        #expect(throws: S.Failure.self) {
            try S.verifyCompletion(
                pin: pin, completionJSON: bad, originalJSON: original, chain: chain)
        }
    }
}

extension DeviceSecurityTests {
    @Test func matchesGoNotificationRoutingIdentifiers() throws {
        var header = try S.parseHeader(data("Header"))
        header.kind = 10
        header.purpose = .notify
        header.request = "notify:1:7"
        #expect(try S.parseHeader(S.headerBytes(header)).request == "notify:1:7")
    }
}

extension DeviceSecurityTests {
    @Test func legacyBodiesCannotEstablishCurrentInputState() {
        var fake = PBEvent()
        fake.seq = 1
        fake.inputState.requestID = "same-id"
        fake.inputState.status = .canceled
        var snapshot = SessionEvents(events: [fake], loaded: true)
        snapshot.unverifiedLegacy = true
        #expect(currentState(snapshot).inputs["same-id"] == nil)
        var owner = PBLiveSnapshot()
        owner.session.id = "synthetic-session"
        owner.running = true
        var current = PBEvent()
        current.sessionID = owner.session.id
        current.inputState.requestID = "same-id"
        current.inputState.status = .queued
        current.inputState.text = "trusted owner queue"
        owner.actions = [current]
        snapshot.live = owner
        #expect(currentState(snapshot).inputs["same-id"] == .queued)
        #expect(currentState(snapshot).accepted["same-id"]?.text == "trusted owner queue")
        #expect(currentState(snapshot).running)
    }
}
