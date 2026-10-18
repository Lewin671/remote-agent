import Foundation

extension DeviceSecurity {
    static func object(_ value: Any) throws -> [String: Any] {
        guard let o = value as? [String: Any] else { throw Failure.invalid }
        return o
    }
    static func jsonHash(_ value: Any?) throws -> Data {
        guard let a = value as? [UInt8], a.count == 32 else { throw Failure.invalid }
        return Data(a)
    }
    static func jsonString(_ o: [String: Any], _ key: String) throws -> String {
        guard let s = o[key] as? String else { throw Failure.invalid }
        return s
    }
    static func jsonBytes(_ o: [String: Any], _ key: String) throws -> Data {
        let s = try jsonString(o, key)
        guard s.utf8.count <= maxRPC * 2, let b = Data(base64Encoded: s),
            b.base64EncodedString() == s
        else { throw Failure.invalid }
        return b
    }
    static func jsonUInt(_ o: [String: Any], _ key: String) throws -> UInt64 {
        guard let n = o[key] as? NSNumber, n.doubleValue >= 0, n.doubleValue <= Double(UInt64.max),
            n.doubleValue.rounded(.down) == n.doubleValue
        else { throw Failure.invalid }
        return n.uint64Value
    }
    public static func stateFromJSON(_ value: Any) throws -> State {
        let s = try object(value)
        let c = try object(s["Core"] as Any)
        let epoch = try jsonUInt(c, "Epoch")
        let ce = try jsonUInt(c, "ContentEpoch")
        try check(epoch > 0 && epoch <= 32768 && ce == epoch)
        guard let members = c["Members"] as? [[String: Any]], members.count <= 128,
            let grants = s["Grants"] as? [[String: Any]], grants.count <= 128
        else { throw Failure.invalid }
        let ms = try members.map { m in
            let kind = try jsonUInt(m, "Kind")
            try check(kind == 1 || kind == 2)
            return Member(
                id: try jsonString(m, "ID"), ed: try jsonHash(m["EdPublic"]),
                x: try jsonHash(m["XPublic"]),
                kind: UInt32(kind))
        }
        let removed =
            (c["Removed"] is NSNull || c["Removed"] == nil) ? [] : c["Removed"] as? [String]
        guard let removed else { throw Failure.invalid }
        let core = Core(
            account: try jsonString(c, "Account"), domain: try jsonHash(c["Domain"]),
            authorityID: try jsonString(c, "AuthorityID"),
            authorityEd: try jsonHash(c["AuthorityEd"]),
            authorityX: try jsonHash(c["AuthorityX"]), parent: try jsonHash(c["Parent"]),
            epoch: epoch,
            contentEpoch: ce, members: ms, removed: removed)
        let gs = try grants.map { g in
            Grant(
                recipient: try jsonString(g, "Recipient"), enc: try jsonHash(g["Enc"]),
                cipher: try jsonBytes(g, "Cipher"))
        }
        let result = State(core: core, grants: gs, signature: try jsonBytes(s, "Signature"))
        _ = try stateBytes(result)
        return result
    }
    public static func wireFromJSON(_ value: Any) throws -> Wire {
        let o = try object(value)
        let w = Wire(
            formed: Cipher(
                header: try jsonBytes(o, "Header"), nonce: try jsonBytes(o, "Nonce"),
                cipher: try jsonBytes(o, "Cipher"), origin: try jsonBytes(o, "Origin")),
            authorization: try jsonBytes(o, "Authorization"),
            authSignature: try jsonBytes(o, "AuthSignature"))
        _ = try wireBytes(w)
        return w
    }
    public struct PairContext: Sendable {
        public var id: String
        public var pin: Pin
        public var approver: String
        public var candidate: Member
        public var expected: Data
        public var nonce: Data
        public var expires: UInt64
        public var signature: Data
    }
    public static func pairContextFromJSON(_ value: Any) throws -> PairContext {
        let o = try object(value)
        let m = try object(o["Candidate"] as Any)
        let kind = try jsonUInt(m, "Kind")
        try check(kind == 1 || kind == 2)
        let p = Pin(
            account: try jsonString(o, "Account"), domain: try jsonHash(o["Domain"]),
            genesis: try jsonHash(o["Genesis"]), authorityID: try jsonString(o, "AuthorityID"),
            authorityEd: try jsonHash(o["AuthorityEd"]), authorityX: try jsonHash(o["AuthorityX"]))
        let c = PairContext(
            id: try jsonString(o, "ID"), pin: p, approver: try jsonString(o, "Approver"),
            candidate: Member(
                id: try jsonString(m, "ID"), ed: try jsonHash(m["EdPublic"]),
                x: try jsonHash(m["XPublic"]),
                kind: UInt32(kind)), expected: try jsonHash(o["Expected"]),
            nonce: try jsonHash(o["Nonce"]),
            expires: try jsonUInt(o, "Expires"), signature: try jsonBytes(o, "Signature"))
        _ = try pairBinding(c)
        return c
    }
    // Every SAS trust field is committed before the joiner's DH public key is
    // exposed. Context nonce/expiry/head/signature are authenticated separately;
    // they cannot provide a freely grindable post-DH input to the short SAS.
    public static func pairTrustDescriptor(_ p: Pin, approver: String) throws -> Data {
        try check(identifier(p.account) && identifier(p.authorityID) && identifier(approver))
        try nonzero(p.domain)
        try nonzero(p.genesis)
        try nonzero(p.authorityEd)
        try nonzero(p.authorityX)
        var w = Writer()
        w.raw(Data("ra2/pair-trust\0".utf8))
        w.str(p.account)
        w.raw(p.domain)
        w.raw(p.genesis)
        w.str(p.authorityID)
        w.raw(p.authorityEd)
        w.raw(p.authorityX)
        w.str(approver)
        return w.data
    }
    public static func pairDomainCommit(ephemeral: Data, trust: Data) throws -> Data {
        try check(ephemeral.count == 32)
        var w = Writer()
        w.lp(ephemeral)
        w.lp(trust)
        return digest("ra2/pair-commit\0", w.data)
    }
    public static func pairBinding(_ p: PairContext) throws -> Data {
        try check(
            opaque(p.id) && p.id.count >= 16 && identifier(p.approver) && p.expires > 0
                && identifier(p.candidate.id) && p.candidate.ed.count == 32
                && deviceId(publicKey: p.candidate.ed) == p.candidate.id
                && (p.candidate.kind == 1 || p.candidate.kind == 2))
        try nonzero(p.expected)
        try nonzero(p.nonce)
        try nonzero(p.candidate.x)
        _ = try pairTrustDescriptor(p.pin, approver: p.approver)
        var w = Writer()
        w.raw(Data("ra2/pair-context\0".utf8))
        w.str(p.id)
        w.str(p.pin.account)
        w.raw(p.pin.domain)
        w.raw(p.pin.genesis)
        w.str(p.pin.authorityID)
        w.raw(p.pin.authorityEd)
        w.raw(p.pin.authorityX)
        w.str(p.approver)
        w.str(p.candidate.id)
        w.raw(p.candidate.ed)
        w.raw(p.candidate.x)
        w.u32(p.candidate.kind)
        w.raw(p.expected)
        w.raw(p.nonce)
        w.u64(p.expires)
        return w.data
    }
    public static func verifyPairContext(_ c: PairContext, pin: Pin) throws {
        try check(
            c.pin == pin
                && verifyEd25519(
                    publicKey: pin.authorityEd, message: pairBinding(c), signature: c.signature),
            .untrusted)
    }
    public enum PairAction: String, Sendable { case approve, accept, cancel }
    public static func signPairContext(_ c: PairContext, signingKey: SigningKey, action: PairAction)
        throws -> Data
    {
        try check(
            deviceId(publicKey: signingKey.publicKey)
                == (action == .accept ? c.candidate.id : c.approver)
        )
        return signingKey.sign(Data("ra2/pair-\(action.rawValue)\0".utf8) + (try pairBinding(c)))
    }
}
