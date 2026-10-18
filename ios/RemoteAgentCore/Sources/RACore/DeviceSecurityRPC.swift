import CryptoKit
import Foundation

extension DeviceSecurity {
    static func safeJSONObject(_ data: Data) throws -> Any {
        do {
            return try JSONSerialization.jsonObject(with: data, options: [.fragmentsAllowed])
        } catch {
            throw Failure.invalid
        }
    }
    public enum Method: UInt32, Sendable {
        case authorize = 1
        case catalog = 2
        case states = 3
        case historyKeys = 4
        case beginPair = 5
        case approvePair = 6
        case cancelPair = 7
        case delete = 8
        case getCompletion = 9
        case getFences = 10
        case ackFence = 11
        case completionStates = 12
    }
    public struct Capsule: Sendable {
        public var from: String
        public var to: String
        public var method: Method
        public var enc: Data
        public var cipher: Data
    }
    public struct Waiter: Sendable {
        public var pin: Pin
        public var sender: String
        public var method: Method
        public var nonce: Data
        public var requestHash: Data
    }
    public static func capsuleBytes(_ c: Capsule) throws -> Data {
        try check(
            identifier(c.from) && identifier(c.to) && c.enc.count == 32
                && (16...maxRPC).contains(c.cipher.count))
        var w = Writer()
        w.str(c.from)
        w.str(c.to)
        w.u32(c.method.rawValue)
        w.lp(c.enc)
        w.lp(c.cipher)
        return w.data
    }
    public static func parseCapsule(_ b: Data) throws -> Capsule {
        try check(b.count <= maxRPC + 512)
        var r = Reader(data: b)
        let from = try r.str()
        let to = try r.str()
        guard let method = Method(rawValue: try r.u32()) else { throw Failure.invalid }
        let c = Capsule(
            from: from, to: to, method: method, enc: try r.lp(32), cipher: try r.lp(maxRPC))
        try r.done()
        _ = try capsuleBytes(c)
        return c
    }
    static func rpcContext(_ p: Pin, from: String, to: String, method: Method, response: Bool)
        -> Data
    {
        var w = Writer()
        w.raw(Data("ra2/rpc\0".utf8))
        w.str(p.account)
        w.raw(p.domain)
        w.str(p.authorityID)
        w.str(from)
        w.str(to)
        w.u32(method.rawValue)
        w.u32(response ? 1 : 0)
        return w.data
    }
    public static func request(
        pin p: Pin, sender: String, signingKey: SigningKey, method: Method, jsonBody: Data
    ) throws -> (Capsule, Waiter) {
        try check(
            identifier(sender) && sender == deviceId(publicKey: signingKey.publicKey)
                && jsonBody.count <= maxRPC - 1024)
        _ = try safeJSONObject(jsonBody)
        let nonce = randomBytes(32)
        let ctx = rpcContext(p, from: sender, to: p.authorityID, method: method, response: false)
        var signed = Writer()
        signed.raw(Data("ra2/auth-request\0".utf8))
        signed.raw(ctx)
        signed.raw(nonce)
        signed.lp(jsonBody)
        let sig = signingKey.sign(signed.data)
        let requestHash = digest("ra2/request-digest\0", signed.data, sig)
        let json = try JSONSerialization.data(withJSONObject: [
            "Nonce": [UInt8](nonce), "Body": jsonBody.base64EncodedString(),
            "Signature": sig.base64EncodedString(),
        ])
        try check(json.count <= maxRPC - 16)
        let pub = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: p.authorityX)
        var hpke = try HPKE.Sender(
            recipientKey: pub, ciphersuite: suite, info: Data("ra2/rpc-info\0".utf8) + ctx)
        let cipher = try hpke.seal(json, authenticating: Data("ra2/rpc-aad\0".utf8) + ctx)
        return (
            Capsule(
                from: sender, to: p.authorityID, method: method, enc: hpke.encapsulatedKey,
                cipher: cipher),
            Waiter(pin: p, sender: sender, method: method, nonce: nonce, requestHash: requestHash)
        )
    }
    public static func reply(_ waiter: Waiter, _ c: Capsule, x: Curve25519.KeyAgreement.PrivateKey)
        throws -> Data
    {
        _ = try capsuleBytes(c)
        let p = waiter.pin
        try check(
            c.from == p.authorityID && c.to == waiter.sender && c.method == waiter.method,
            .untrusted)
        let ctx = rpcContext(p, from: c.from, to: c.to, method: c.method, response: true)
        var hpke = try HPKE.Recipient(
            privateKey: x, ciphersuite: suite, info: Data("ra2/rpc-info\0".utf8) + ctx,
            encapsulatedKey: c.enc)
        let plain = try hpke.open(c.cipher, authenticating: Data("ra2/rpc-aad\0".utf8) + ctx)
        try check(plain.count <= maxRPC)
        guard let r = try safeJSONObject(plain) as? [String: Any],
            let nonce = r["Nonce"] as? [UInt8],
            nonce.count == 32, let rh = r["RequestHash"] as? [UInt8], rh.count == 32,
            let error = r["Error"] as? String, error.utf8.count <= 32,
            let signature = r["Signature"] as? String, let sig = Data(base64Encoded: signature)
        else { throw Failure.invalid }
        var body = Data()
        if let text = r["Body"] as? String {
            guard let b = Data(base64Encoded: text), b.count <= maxRPC - 1024 else {
                throw Failure.invalid
            }
            body = b
        }
        try check(Data(nonce) == waiter.nonce && Data(rh) == waiter.requestHash, .untrusted)
        var signed = Writer()
        signed.raw(Data("ra2/auth-response\0".utf8))
        signed.raw(ctx)
        signed.raw(Data(nonce))
        signed.raw(Data(rh))
        signed.str(error)
        signed.lp(body)
        try check(
            verifyEd25519(publicKey: p.authorityEd, message: signed.data, signature: sig),
            .untrusted)
        switch error {
        case "": return body
        case "stale": throw Failure.stale
        case "conflict": throw Failure.conflict
        case "pending": throw Failure.pending
        case "capacity": throw Failure.capacity
        case "storage": throw Failure.storage
        default: throw Failure.untrusted
        }
    }
    public static func authorizationRequest(_ c: Cipher) throws -> Data {
        let h = try parseHeader(c.header)
        let commit = try commitment(c)
        let formed: [String: Any] = [
            "Header": c.header.base64EncodedString(), "Nonce": c.nonce.base64EncodedString(),
            "Cipher": c.cipher.base64EncodedString(), "Origin": c.origin.base64EncodedString(),
        ]
        return try JSONSerialization.data(withJSONObject: [
            "Header": c.header.base64EncodedString(), "Commit": [UInt8](commit),
            "Origin": c.origin.base64EncodedString(),
            "Cipher": h.purpose.mutable ? (formed as Any) : (NSNull() as Any),
        ])
    }
    public static func attach(_ c: Cipher, jsonReply: Data, pin: Pin, state: State) throws -> Wire {
        guard let r = try safeJSONObject(jsonReply) as? [String: String],
            let a = r["Authorization"],
            let ab = Data(base64Encoded: a), let s = r["Signature"],
            let sig = Data(base64Encoded: s)
        else { throw Failure.invalid }
        let w = Wire(formed: c, authorization: ab, authSignature: sig)
        _ = try verifyWire(pin, state, w)
        return w
    }
}
