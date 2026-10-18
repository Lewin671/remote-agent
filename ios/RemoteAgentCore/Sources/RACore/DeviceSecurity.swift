// Current trust comes from a SAS-pinned authority, never from the shared
// account root or a relay-provided registration. Canonical bytes match Go/Web.
import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf

public enum DeviceSecurity {
    public enum Failure: Error, Equatable {
        case invalid, untrusted, stale, conflict, pending, capacity, storage
    }
    public static let maxState = 64 << 10
    public static let maxCipher = 4 << 20
    public static let maxRPC = 1 << 20
    static let zero = Data(repeating: 0, count: 32)
    static func check(_ condition: Bool, _ error: Failure = .invalid) throws {
        if !condition { throw error }
    }
    static func identifier(_ s: String) -> Bool {
        s.utf8.count == 32
            && s.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
    }
    static func opaque(_ s: String) -> Bool {
        (1...128).contains(s.utf8.count)
            && s.utf8.allSatisfy {
                (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0)
                    || $0 == 45
                    || $0 == 95 || $0 == 58
            }
    }
    static func nonzero(_ b: Data) throws { try check(b.count == 32 && b != zero) }
    static func digest(_ label: String, _ parts: Data...) -> Data {
        var h = SHA256()
        h.update(data: Data(label.utf8))
        for p in parts { h.update(data: p) }
        return Data(h.finalize())
    }
    struct Writer {
        var data = Data()
        mutating func raw(_ b: Data) { data.append(b) }
        mutating func u32(_ n: UInt32) {
            var v = n.bigEndian
            withUnsafeBytes(of: &v) { data.append(contentsOf: $0) }
        }
        mutating func u64(_ n: UInt64) {
            var v = n.bigEndian
            withUnsafeBytes(of: &v) { data.append(contentsOf: $0) }
        }
        mutating func lp(_ b: Data) {
            u32(UInt32(b.count))
            raw(b)
        }
        mutating func str(_ s: String) { lp(Data(s.utf8)) }
    }
    struct Reader {
        let data: Data
        var at = 0
        mutating func raw(_ n: Int) throws -> Data {
            try check(n >= 0 && n <= data.count - at)
            let b = data.subdata(in: at..<(at + n))
            at += n
            return b
        }
        mutating func u32() throws -> UInt32 {
            try raw(4).reduce(UInt32(0)) { ($0 << 8) | UInt32($1) }
        }
        mutating func u64() throws -> UInt64 {
            try raw(8).reduce(UInt64(0)) { ($0 << 8) | UInt64($1) }
        }
        mutating func lp(_ max: Int) throws -> Data {
            let n = try u32()
            try check(n <= max)
            return try raw(Int(n))
        }
        mutating func str() throws -> String {
            guard let s = String(data: try lp(128), encoding: .utf8) else { throw Failure.invalid }
            return s
        }
        func done() throws { try check(at == data.count) }
    }
    public struct Member: Sendable, Equatable {
        public var id: String
        public var ed: Data
        public var x: Data
        public var kind: UInt32
    }
    public struct Core: Sendable {
        public var account: String
        public var domain: Data
        public var authorityID: String
        public var authorityEd: Data
        public var authorityX: Data
        public var parent: Data
        public var epoch: UInt64
        public var contentEpoch: UInt64
        public var members: [Member]
        public var removed: [String]
    }
    public struct Grant: Sendable {
        public var recipient: String
        public var enc: Data
        public var cipher: Data
    }
    public struct State: Sendable {
        public var core: Core
        public var grants: [Grant]
        public var signature: Data
    }
    public struct Pin: Sendable, Equatable {
        public var account: String
        public var domain: Data
        public var genesis: Data
        public var authorityID: String
        public var authorityEd: Data
        public var authorityX: Data
        public init(
            account: String, domain: Data, genesis: Data, authorityID: String, authorityEd: Data,
            authorityX: Data
        ) {
            self.account = account
            self.domain = domain
            self.genesis = genesis
            self.authorityID = authorityID
            self.authorityEd = authorityEd
            self.authorityX = authorityX
        }
    }
    static func validate(_ c: Core) throws {
        try check(
            identifier(c.account) && identifier(c.authorityID) && c.epoch > 0
                && c.contentEpoch == c.epoch
                && (1...128).contains(c.members.count) && c.removed.count <= 128)
        try nonzero(c.domain)
        try nonzero(c.authorityEd)
        try nonzero(c.authorityX)
        try check(c.parent.count == 32)
        for (i, m) in c.members.enumerated() {
            try check(
                identifier(m.id) && m.ed.count == 32 && deviceId(publicKey: m.ed) == m.id
                    && (m.kind == 1 || m.kind == 2))
            try nonzero(m.x)
            if i > 0 { try check(c.members[i - 1].id < m.id) }
        }
        guard let a = c.members.first(where: { $0.id == c.authorityID }) else {
            throw Failure.invalid
        }
        try check(a.kind == 1 && a.ed != c.authorityEd && a.x != c.authorityX)
        for (i, id) in c.removed.enumerated() {
            try check(
                identifier(id) && (i == 0 || c.removed[i - 1] < id)
                    && !c.members.contains(where: { $0.id == id }))
        }
        if c.epoch == 1 {
            try check(c.parent == zero && c.members.count == 1 && c.removed.isEmpty)
        } else {
            try nonzero(c.parent)
        }
    }
    public static func coreBytes(_ c: Core) throws -> Data {
        try validate(c)
        var w = Writer()
        w.u32(2)
        w.str(c.account)
        w.raw(c.domain)
        w.str(c.authorityID)
        w.raw(c.authorityEd)
        w.raw(c.authorityX)
        w.raw(c.parent)
        w.u64(c.epoch)
        w.u64(c.contentEpoch)
        w.u32(UInt32(c.members.count))
        for m in c.members {
            w.str(m.id)
            w.raw(m.ed)
            w.raw(m.x)
            w.u32(m.kind)
        }
        w.u32(UInt32(c.removed.count))
        for id in c.removed { w.str(id) }
        return w.data
    }
    public static func parseCore(_ b: Data) throws -> Core {
        try check(b.count <= maxState)
        var r = Reader(data: b)
        try check(r.u32() == 2)
        var c = Core(
            account: try r.str(), domain: try r.raw(32), authorityID: try r.str(),
            authorityEd: try r.raw(32), authorityX: try r.raw(32), parent: try r.raw(32),
            epoch: try r.u64(), contentEpoch: try r.u64(), members: [], removed: [])
        var n = try r.u32()
        try check(n <= 128)
        for _ in 0..<n {
            c.members.append(
                Member(id: try r.str(), ed: try r.raw(32), x: try r.raw(32), kind: try r.u32()))
        }
        n = try r.u32()
        try check(n <= 128)
        for _ in 0..<n { c.removed.append(try r.str()) }
        try r.done()
        try validate(c)
        return c
    }
    static func grantBytes(_ s: State) throws -> Data {
        try check(s.grants.count == s.core.members.count)
        var w = Writer()
        w.u32(UInt32(s.grants.count))
        for (i, g) in s.grants.enumerated() {
            try check(g.recipient == s.core.members[i].id && g.cipher.count == 48)
            try nonzero(g.enc)
            w.str(g.recipient)
            w.lp(g.enc)
            w.lp(g.cipher)
        }
        return w.data
    }
    public static func stateHash(_ s: State) throws -> Data {
        let ch = try digest("ra2/core\0", coreBytes(s.core))
        let gh = try digest("ra2/grants\0", grantBytes(s))
        return digest("ra2/state\0", ch, gh)
    }
    public static func stateBytes(_ s: State) throws -> Data {
        try check(s.signature.count == 64)
        var w = Writer()
        w.lp(try coreBytes(s.core))
        w.raw(try grantBytes(s))
        w.lp(s.signature)
        try check(w.data.count <= maxState)
        return w.data
    }
    public static func parseState(_ b: Data) throws -> State {
        try check(b.count <= maxState)
        var r = Reader(data: b)
        let core = try parseCore(r.lp(maxState))
        let n = try r.u32()
        try check(n <= 128)
        var grants: [Grant] = []
        for _ in 0..<n {
            grants.append(Grant(recipient: try r.str(), enc: try r.lp(32), cipher: try r.lp(48)))
        }
        let s = State(core: core, grants: grants, signature: try r.lp(64))
        try r.done()
        _ = try stateBytes(s)
        return s
    }
    public static func verifyState(_ p: Pin, _ s: State) throws {
        let c = s.core
        try check(
            c.account == p.account && c.domain == p.domain && c.authorityID == p.authorityID
                && c.authorityEd == p.authorityEd && c.authorityX == p.authorityX, .untrusted)
        try nonzero(p.genesis)
        let hash = try stateHash(s)
        if c.epoch == 1 { try check(hash == p.genesis, .untrusted) }
        var w = Writer()
        w.raw(Data("ra2/state-sig\0".utf8))
        w.str(p.account)
        w.raw(p.domain)
        w.u64(c.epoch)
        w.raw(hash)
        try check(
            verifyEd25519(publicKey: p.authorityEd, message: w.data, signature: s.signature),
            .untrusted)
    }
    public static func advance(_ p: Pin, _ old: State, _ next: State) throws {
        try verifyState(p, old)
        try verifyState(p, next)
        try check(
            old.core.epoch != .max && next.core.epoch == old.core.epoch + 1
                && next.core.parent == stateHash(old), .stale)
        var removed: [String] = []
        var added = 0
        for m in old.core.members {
            if let n = next.core.members.first(where: { $0.id == m.id }) {
                try check(n == m)
            } else {
                removed.append(m.id)
            }
        }
        for m in next.core.members where !old.core.members.contains(where: { $0.id == m.id }) {
            added += 1
        }
        try check(
            removed == next.core.removed && added <= 1 && !(added > 0 && !removed.isEmpty)
                && (added > 0 || !removed.isEmpty))
    }
    static let suite = HPKE.Ciphersuite(
        kem: .Curve25519_HKDF_SHA256, kdf: .HKDF_SHA256, aead: .AES_GCM_256)
    public static func openGrant(
        _ p: Pin, _ s: State, recipient: String, x: Curve25519.KeyAgreement.PrivateKey
    ) throws -> Data {
        try verifyState(p, s)
        guard let g = s.grants.first(where: { $0.recipient == recipient }),
            let m = s.core.members.first(where: { $0.id == recipient })
        else { throw Failure.untrusted }
        try check(m.x == x.publicKey.rawRepresentation, .untrusted)
        var w = Writer()
        w.str(p.account)
        w.raw(p.domain)
        w.u64(s.core.contentEpoch)
        w.raw(try digest("ra2/core\0", coreBytes(s.core)))
        w.str(recipient)
        var r = try HPKE.Recipient(
            privateKey: x, ciphersuite: suite, info: Data("ra2/grant-info\0".utf8) + w.data,
            encapsulatedKey: g.enc)
        let key = try r.open(g.cipher, authenticating: Data("ra2/grant-aad\0".utf8) + w.data)
        try check(key.count == 32, .untrusted)
        return key
    }
    public enum Purpose: UInt32, Sendable {
        case event = 1, sessionMeta = 2, sessionKey = 3, machineMeta = 4, command = 5, result = 6,
            terminalOutput = 7, terminalInput = 8, notify = 10, image = 11, prefs = 12, deviceMeta =
            13,
            terminalResize = 14, terminalHistory = 15
        public var mutable: Bool {
            [.sessionMeta, .machineMeta, .deviceMeta, .prefs].contains(self)
        }
        public var control: Bool {
            [.command, .terminalInput, .terminalResize, .terminalHistory].contains(self)
        }
        var sessionKey: Bool {
            [.event, .image, .terminalOutput, .terminalInput, .terminalResize, .terminalHistory]
                .contains(
                    self)
        }
        var kind: UInt32 { [.terminalResize, .terminalHistory].contains(self) ? 8 : rawValue }
    }
    public struct Header: Sendable {
        public var account: String
        public var domain: Data
        public var epoch: UInt64
        public var state: Data
        public var contentEpoch: UInt64
        public var sender: String
        public var kind: UInt32
        public var purpose: Purpose
        public var machine: String
        public var session: String
        public var request: String
        public var seq: UInt64
        public var deadline: UInt64
        public var compression: UInt32
        public var counter: UInt64
    }
    static func validate(_ h: Header) throws {
        try check(
            identifier(h.account) && identifier(h.sender) && h.epoch > 0
                && h.contentEpoch == h.epoch
                && h.counter > 0 && h.compression <= 1 && h.kind == h.purpose.kind
                && (h.machine.isEmpty || identifier(h.machine))
                && (h.session.isEmpty || opaque(h.session))
                && (h.request.isEmpty || opaque(h.request)))
        try nonzero(h.domain)
        try nonzero(h.state)
        switch h.purpose {
        case .prefs:
            try check(
                h.request == "project-prefs" && h.machine.isEmpty && h.session.isEmpty && h.seq > 0)
        case .deviceMeta:
            try check(identifier(h.request) && h.machine.isEmpty && h.session.isEmpty && h.seq > 0)
        case .machineMeta:
            try check(!h.machine.isEmpty && h.session.isEmpty && h.request.isEmpty && h.seq > 0)
        case .sessionMeta, .sessionKey, .event:
            try check(!h.machine.isEmpty && !h.session.isEmpty && h.request.isEmpty && h.seq > 0)
        case .command, .result: try check(!h.machine.isEmpty && h.request.count >= 16)
        case .terminalInput, .terminalResize, .terminalHistory, .terminalOutput:
            try check(
                !h.machine.isEmpty && !h.session.isEmpty && h.request.count >= 16 && h.seq > 0)
        case .image, .notify:
            try check(!h.machine.isEmpty && !h.session.isEmpty && !h.request.isEmpty)
        }
        if h.purpose.control { try check(h.deadline > 0) }
    }
    public static func headerBytes(_ h: Header) throws -> Data {
        try validate(h)
        var w = Writer()
        w.u32(2)
        w.str(h.account)
        w.raw(h.domain)
        w.u64(h.epoch)
        w.raw(h.state)
        w.u64(h.contentEpoch)
        w.str(h.sender)
        w.u32(h.kind)
        w.u32(h.purpose.rawValue)
        w.str(h.machine)
        w.str(h.session)
        w.str(h.request)
        w.u64(h.seq)
        w.u64(h.deadline)
        w.u32(h.compression)
        w.u64(h.counter)
        return w.data
    }
    public static func parseHeader(_ b: Data) throws -> Header {
        try check(b.count <= 1024)
        var r = Reader(data: b)
        try check(r.u32() == 2)
        let account = try r.str()
        let domain = try r.raw(32)
        let epoch = try r.u64()
        let state = try r.raw(32)
        let contentEpoch = try r.u64()
        let sender = try r.str()
        let kind = try r.u32()
        guard let purpose = Purpose(rawValue: try r.u32()) else { throw Failure.invalid }
        let h = Header(
            account: account, domain: domain, epoch: epoch, state: state,
            contentEpoch: contentEpoch,
            sender: sender, kind: kind, purpose: purpose, machine: try r.str(),
            session: try r.str(),
            request: try r.str(), seq: try r.u64(), deadline: try r.u64(), compression: try r.u32(),
            counter: try r.u64())
        try r.done()
        try validate(h)
        return h
    }
    static func scope(_ h: Header, _ s: State) throws -> Member {
        try validate(h)
        try check(
            h.account == s.core.account && h.domain == s.core.domain && h.epoch == s.core.epoch
                && h.contentEpoch == s.core.contentEpoch && h.state == stateHash(s), .stale)
        guard let writer = s.core.members.first(where: { $0.id == h.sender }) else {
            throw Failure.untrusted
        }
        if !h.machine.isEmpty {
            try check(
                s.core.members.contains(where: { $0.id == h.machine && $0.kind == 1 }), .untrusted)
        }
        if h.purpose == .deviceMeta {
            try check(
                s.core.members.contains(where: { $0.id == h.request && $0.kind == 2 }), .untrusted)
        } else if h.purpose != .prefs && !h.purpose.control {
            try check(writer.kind == 1 && writer.id == h.machine, .untrusted)
        }
        return writer
    }
    public struct Cipher: Sendable {
        public var header: Data
        public var nonce: Data
        public var cipher: Data
        public var origin: Data
    }
    public struct Wire: Sendable {
        public var formed: Cipher
        public var authorization: Data
        public var authSignature: Data
    }
    public static func commitment(_ c: Cipher) throws -> Data {
        try check(
            c.header.count <= 1024 && c.nonce.count == 12
                && (16...maxCipher).contains(c.cipher.count))
        _ = try parseHeader(c.header)
        var w = Writer()
        w.lp(c.header)
        w.lp(c.nonce)
        w.lp(c.cipher)
        return digest("ra2/ciphertext\0", w.data)
    }
    static func originBytes(_ h: Header, _ hh: Data, _ commit: Data) -> Data {
        var w = Writer()
        w.raw(Data("ra2/cipher-origin\0".utf8))
        w.str(h.account)
        w.raw(h.domain)
        w.raw(h.state)
        w.str(h.sender)
        w.raw(hh)
        w.raw(commit)
        return w.data
    }
    public static func contentKey(secret: Data, sessionSeed: Data?, header h: Header) throws -> Data
    {
        try check(secret.count == 32)
        try validate(h)
        var salt: Data?
        if h.purpose.sessionKey {
            try check(sessionSeed?.count == 32)
            salt = sessionSeed
        }
        var w = Writer()
        w.raw(Data("ra2/content-key\0".utf8))
        w.str(h.account)
        w.raw(h.domain)
        w.u64(h.contentEpoch)
        w.u32(h.purpose.rawValue)
        w.str(h.sender)
        w.str(h.machine)
        w.str(h.session)
        w.str(h.request)
        w.u64(h.seq)
        w.u64(h.counter)
        var purposeInfo = Writer()
        purposeInfo.raw(Data("ra2/purpose-key\0".utf8))
        purposeInfo.str(h.account)
        purposeInfo.raw(h.domain)
        purposeInfo.u64(h.contentEpoch)
        purposeInfo.u32(h.purpose.rawValue)
        let child = hkdf(secret: secret, salt: nil, info: purposeInfo.data, size: 32)
        return hkdf(secret: child, salt: salt, info: w.data, size: 32)
    }
    // The caller's vault MUST reserve header.counter durably before this call.
    public static func form(
        secret: Data, sessionSeed: Data?, header h: Header, plaintext: Data, signingKey: SigningKey
    ) throws -> Cipher {
        try check(
            plaintext.count <= maxCipher - 16
                && deviceId(publicKey: signingKey.publicKey) == h.sender)
        let hb = try headerBytes(h)
        let key = try contentKey(secret: secret, sessionSeed: sessionSeed, header: h)
        let nonce = randomBytes(12)
        let box = try AES.GCM.seal(
            plaintext, using: SymmetricKey(data: key), nonce: AES.GCM.Nonce(data: nonce),
            authenticating: hb)
        var c = Cipher(header: hb, nonce: nonce, cipher: box.ciphertext + box.tag, origin: Data())
        c.origin = signingKey.sign(originBytes(h, sha256(hb), try commitment(c)))
        return c
    }
    public struct WriteAuth: Sendable {
        public var header: Header
        public var writerKind: UInt32
        public var headerHash: Data
        public var commit: Data
        public var nonce: Data
    }
    public static func parseAuthorization(_ b: Data) throws -> WriteAuth {
        try check(b.count <= 1152)
        var r = Reader(data: b)
        let a = WriteAuth(
            header: try parseHeader(r.lp(1024)), writerKind: try r.u32(), headerHash: try r.raw(32),
            commit: try r.raw(32), nonce: try r.raw(32))
        try r.done()
        try check(a.writerKind == 1 || a.writerKind == 2)
        try check(a.headerHash == sha256(headerBytes(a.header)))
        try nonzero(a.commit)
        try nonzero(a.nonce)
        return a
    }
    public static func verifyWire(_ p: Pin, _ s: State, _ w: Wire) throws -> Header {
        try verifyState(p, s)
        let h = try parseHeader(w.formed.header)
        let m = try scope(h, s)
        let hh = sha256(w.formed.header)
        let commit = try commitment(w.formed)
        try check(
            verifyEd25519(
                publicKey: m.ed, message: originBytes(h, hh, commit), signature: w.formed.origin),
            .untrusted)
        let a = try parseAuthorization(w.authorization)
        try check(
            headerBytes(a.header) == w.formed.header && a.writerKind == m.kind && a.headerHash == hh
                && a.commit == commit, .untrusted)
        try check(
            verifyEd25519(
                publicKey: p.authorityEd, message: Data("ra2/write-auth\0".utf8) + w.authorization,
                signature: w.authSignature), .untrusted)
        return h
    }
    // Body decoding also enforces purpose/direction before any control dispatch.
    public static func open(_ p: Pin, _ s: State, _ w: Wire, secret: Data, sessionSeed: Data?)
        throws
        -> (Header, Data)
    {
        let h = try verifyWire(p, s, w)
        let key = try contentKey(secret: secret, sessionSeed: sessionSeed, header: h)
        let c = w.formed
        let box = try AES.GCM.SealedBox(
            nonce: AES.GCM.Nonce(data: c.nonce), ciphertext: c.cipher.dropLast(16),
            tag: c.cipher.suffix(16))
        return (h, try AES.GCM.open(box, using: SymmetricKey(data: key), authenticating: c.header))
    }
    public static func wireBytes(_ w: Wire) throws -> Data {
        _ = try commitment(w.formed)
        _ = try parseAuthorization(w.authorization)
        try check(w.formed.origin.count == 64 && w.authSignature.count == 64)
        var e = Writer()
        e.lp(w.formed.header)
        e.lp(w.formed.nonce)
        e.lp(w.formed.cipher)
        e.lp(w.formed.origin)
        e.lp(w.authorization)
        e.lp(w.authSignature)
        return e.data
    }
    public static func parseWire(_ b: Data) throws -> Wire {
        try check(b.count <= maxCipher + 4096)
        var r = Reader(data: b)
        let w = Wire(
            formed: Cipher(
                header: try r.lp(1024), nonce: try r.lp(12), cipher: try r.lp(maxCipher),
                origin: try r.lp(64)), authorization: try r.lp(1152), authSignature: try r.lp(64))
        try r.done()
        _ = try wireBytes(w)
        return w
    }
    public static func envelope(_ w: Wire) throws -> PBEnvelope {
        let h = try parseHeader(w.formed.header)
        _ = try wireBytes(w)
        var ph = PBHeader()
        ph.version = 2
        ph.kind = PBPayloadKind(rawValue: Int(h.kind))!
        ph.machineID = h.machine
        ph.sessionID = h.session
        ph.requestID = h.request
        ph.seq = h.seq
        ph.deadlineMs = Int64(bitPattern: h.deadline)
        ph.sender = h.sender
        ph.compression = PBCompression(rawValue: Int(h.compression))!
        var e = PBEnvelope()
        e.header = try ph.serializedData()
        e.nonce = w.formed.nonce
        e.ciphertext = w.formed.cipher
        e.signature = w.formed.origin
        e.securityHeader = w.formed.header
        e.authorization = w.authorization
        e.authorizationSignature = w.authSignature
        return e
    }
    public static func fromEnvelope(_ e: PBEnvelope) throws -> Wire {
        try check(e.header.count <= 1024 && e.unknownFields.data.isEmpty)
        let w = Wire(
            formed: Cipher(
                header: e.securityHeader, nonce: e.nonce, cipher: e.ciphertext, origin: e.signature),
            authorization: e.authorization, authSignature: e.authorizationSignature)
        let h = try parseHeader(w.formed.header)
        let ph = try PBHeader(serializedBytes: e.header)
        try check(
            ph.version == 2 && ph.kind.rawValue == h.kind && ph.machineID == h.machine
                && ph.sessionID == h.session && ph.requestID == h.request && ph.seq == h.seq
                && UInt64(bitPattern: ph.deadlineMs) == h.deadline && ph.sender == h.sender
                && ph.compression.rawValue == h.compression && ph.unknownFields.data.isEmpty,
            .untrusted)
        _ = try wireBytes(w)
        return w
    }
}
