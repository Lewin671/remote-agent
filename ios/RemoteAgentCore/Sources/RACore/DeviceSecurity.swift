// Protocol 10 (state format 3): the phone as the device-security authority.
// A signed membership chain starts at the pinned genesis; each later state is
// signed by the parent's AuthorityID member's Ed25519 key, and a recovery
// transition is also authorized by the parent's recovery key. Content keys
// walk back through PrevSecret, stopping at a recovery boundary. Canonical
// bytes match Go's internal/deviceauth and the other clients.
import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf

public enum DeviceSecurity {
    public enum Failure: Error, Equatable {
        case invalid, untrusted, stale, conflict, pending, capacity, storage, unknown, unavailable,
             clock
    }
    public static let maxState = 64 << 10
    public static let maxCipher = 4 << 20
    public static let maxRPC = 1 << 20
    public static let maxMembers = 128
    public static let maxStates = 4096
    /// A pairing payload carries the whole chain; beyond this the phone must
    /// rebuild the account rather than hand over a chain no client accepts.
    public static let maxPairStates = 256
    public static let maxPairBytes = 800 << 10
    /// States more than five minutes ahead of this clock are refused.
    public static let clockSkewLimit: UInt64 = 5 * 60 * 1000
    /// The membership lease and the largest accepted one, in milliseconds.
    public static let lease: UInt64 = 72 * 60 * 60 * 1000
    public static let maxLease: UInt64 = 7 * 24 * 60 * 60 * 1000
    /// Counter values handed out before the next block is reserved durably.
    public static let counterBlock: UInt64 = 4096
    static let zero = Data(repeating: 0, count: 32)
    static func check(_ condition: Bool, _ error: Failure = .invalid) throws {
        if !condition { throw error }
    }
    /// A device or account ID: 32 lowercase hex characters.
    static func identifier(_ s: String) -> Bool {
        s.utf8.count == 32
            && s.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
    }
    /// The character set Go's validID accepts for session, request and device IDs.
    static func validID(_ s: String) -> Bool {
        (1...128).contains(s.utf8.count)
            && s.utf8.allSatisfy {
                (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0)
                    || $0 == 45 || $0 == 95 || $0 == 46 || $0 == 58
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
            guard let s = String(data: try lp(maxMembers), encoding: .utf8) else {
                throw Failure.invalid
            }
            return s
        }
        func done() throws { try check(at == data.count) }
    }

    public enum Kind: UInt32, Sendable {
        case machine = 1, client = 2
    }
    public struct Member: Sendable, Equatable {
        public var id: String
        public var ed: Data
        public var x: Data
        public var kind: UInt32
        public init(id: String, ed: Data, x: Data, kind: UInt32) {
            self.id = id
            self.ed = ed
            self.x = x
            self.kind = kind
        }
        func validate() throws {
            try check(
                identifier(id) && ed.count == 32 && deviceId(publicKey: ed) == id
                    && x.count == 32 && x != zero && (kind == Kind.machine.rawValue || kind == Kind.client.rawValue))
        }
    }
    public static func member(ed: Data, x: Data, kind: Kind) throws -> Member {
        let m = Member(id: deviceId(publicKey: ed), ed: ed, x: x, kind: kind.rawValue)
        try m.validate()
        return m
    }

    public struct Core: Sendable, Equatable {
        public var account: String
        public var domain: Data
        public var authorityID: String
        public var recoveryEd: Data
        public var parent: Data
        public var epoch: UInt64
        public var contentEpoch: UInt64
        public var issuedAt: UInt64
        public var notAfter: UInt64
        public var members: [Member]
        public var removed: [String]
        public func member(_ id: String) -> Member? { members.first { $0.id == id } }
        func validate() throws {
            try check(
                identifier(account) && identifier(authorityID) && recoveryEd.count == 32
                    && recoveryEd != zero && epoch > 0 && contentEpoch > 0 && contentEpoch <= epoch
                    && issuedAt > 0 && notAfter > issuedAt && notAfter - issuedAt <= maxLease
                    && (1...maxMembers).contains(members.count) && removed.count <= maxMembers)
            try nonzero(domain)
            try check(parent.count == 32)
            for (i, m) in members.enumerated() {
                try m.validate()
                if i > 0 { try check(members[i - 1].id < m.id) }
            }
            guard let a = member(authorityID), a.kind == Kind.client.rawValue else {
                throw Failure.invalid
            }
            for (i, id) in removed.enumerated() {
                try check(
                    identifier(id) && (i == 0 || removed[i - 1] < id) && member(id) == nil
                        && id != authorityID)
            }
            if epoch == 1 {
                try check(parent == zero && members.count == 1 && removed.isEmpty && contentEpoch == 1)
            } else {
                try check(parent != zero)
            }
        }
    }
    public struct Grant: Sendable, Equatable {
        public var recipient: String
        public var enc: Data
        public var cipher: Data
    }
    public struct State: Sendable, Equatable {
        public var core: Core
        public var grants: [Grant]
        public var prevSecret: Data
        public var signature: Data
    }

    public static func coreBytes(_ c: Core) throws -> Data {
        try c.validate()
        var w = Writer()
        w.u32(3)
        w.str(c.account)
        w.raw(c.domain)
        w.str(c.authorityID)
        w.raw(c.recoveryEd)
        w.raw(c.parent)
        w.u64(c.epoch)
        w.u64(c.contentEpoch)
        w.u64(c.issuedAt)
        w.u64(c.notAfter)
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
        try check(r.u32() == 3)
        var c = Core(
            account: try r.str(), domain: try r.raw(32), authorityID: try r.str(),
            recoveryEd: try r.raw(32), parent: try r.raw(32), epoch: try r.u64(),
            contentEpoch: try r.u64(), issuedAt: try r.u64(), notAfter: try r.u64(),
            members: [], removed: [])
        var n = try r.u32()
        try check(n <= maxMembers)
        for _ in 0..<n {
            c.members.append(
                Member(id: try r.str(), ed: try r.raw(32), x: try r.raw(32), kind: try r.u32()))
        }
        n = try r.u32()
        try check(n <= maxMembers)
        for _ in 0..<n { c.removed.append(try r.str()) }
        try r.done()
        try c.validate()
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
        let ch = try digest("ra3/core\0", coreBytes(s.core))
        let grants = try grantBytes(s)
        var p = Writer()
        p.lp(s.prevSecret)
        return digest("ra3/state\0", ch, grants, p.data)
    }
    static func signBytes(_ s: State) throws -> Data {
        let h = try stateHash(s)
        var w = Writer()
        w.raw(Data("ra3/state-sig\0".utf8))
        w.str(s.core.account)
        w.raw(s.core.domain)
        w.raw(s.core.parent)
        w.u64(s.core.epoch)
        w.u64(s.core.contentEpoch)
        w.u64(s.core.issuedAt)
        w.u64(s.core.notAfter)
        w.str(s.core.authorityID)
        w.raw(s.core.recoveryEd)
        w.raw(h)
        return w.data
    }
    public static func stateBytes(_ s: State) throws -> Data {
        let core = try coreBytes(s.core)
        let grants = try grantBytes(s)
        try check(s.prevSecret.count <= maxState && s.signature.count == 64)
        var w = Writer()
        w.lp(core)
        w.raw(grants)
        w.lp(s.prevSecret)
        w.lp(s.signature)
        try check(w.data.count <= maxState)
        return w.data
    }
    public static func parseState(_ b: Data) throws -> State {
        try check(b.count <= maxState)
        var r = Reader(data: b)
        let core = try parseCore(r.lp(maxState))
        let n = try r.u32()
        try check(n <= maxMembers)
        var grants: [Grant] = []
        for _ in 0..<n {
            grants.append(Grant(recipient: try r.str(), enc: try r.lp(32), cipher: try r.lp(48)))
        }
        let prev = try r.lp(maxState)
        let sig = try r.lp(64)
        try r.done()
        try check(sig.count == 64)
        let s = State(core: core, grants: grants, prevSecret: prev, signature: sig)
        _ = try stateBytes(s)
        return s
    }

    public struct Pin: Sendable, Equatable {
        public var account: String
        public var domain: Data
        public var genesis: Data
        public init(account: String, domain: Data, genesis: Data) {
            self.account = account
            self.domain = domain
            self.genesis = genesis
        }
    }
    public static func pin(for state: State) throws -> Pin {
        try check(state.core.epoch == 1)
        return Pin(
            account: state.core.account, domain: state.core.domain, genesis: try stateHash(state))
    }
    /// Anchors account, domain and the genesis hash. Later states get their
    /// authority from the chain, so no key is pinned here.
    public static func verifyPin(_ p: Pin, _ s: State) throws {
        try check(
            identifier(p.account) && p.domain.count == 32 && p.genesis.count == 32
                && p.genesis != zero)
        try check(s.core.account == p.account && s.core.domain == p.domain, .untrusted)
        if s.core.epoch == 1 {
            try check(try stateHash(s) == p.genesis, .untrusted)
        }
    }
    public static func verifyGenesis(_ p: Pin, _ s: State) throws {
        try verifyPin(p, s)
        try check(s.core.epoch == 1, .untrusted)
        guard let m = s.core.member(s.core.authorityID), m.kind == Kind.client.rawValue,
            s.core.members.count == 1, s.core.members[0].id == m.id
        else { throw Failure.untrusted }
        try check(
            verifyEd25519(publicKey: m.ed, message: signBytes(s), signature: s.signature),
            .untrusted)
    }

    static func sameMembers(_ a: [Member], _ b: [Member]) -> Bool { a == b }
    static func membershipDelta(_ parent: State, _ next: State) throws {
        var removed: [String] = []
        var added = 0
        var rotated = 0
        for m in parent.core.members {
            guard let n = next.core.member(m.id) else {
                removed.append(m.id)
                continue
            }
            try check(n.ed == m.ed && n.kind == m.kind)
            if n.x != m.x { rotated += 1 }
        }
        for m in next.core.members where parent.core.member(m.id) == nil {
            try check(!parent.core.removed.contains(m.id))
            added += 1
        }
        var expected = parent.core.removed
        expected.append(contentsOf: removed)
        expected.sort()
        try check(expected == next.core.removed)
        try check(
            !(added > 1 || rotated > 1 || (added > 0 && !removed.isEmpty)
                || (rotated > 0 && (added > 0 || !removed.isEmpty))
                || (added == 0 && removed.isEmpty && rotated == 0)))
    }
    static func recoveryDelta(_ parent: State, _ next: State) throws {
        var removed = parent.core.removed
        removed.append(parent.core.authorityID)
        removed.sort()
        try check(removed == next.core.removed)
        for m in parent.core.members where m.id != parent.core.authorityID {
            guard let n = next.core.member(m.id) else { throw Failure.invalid }
            try check(n == m)
        }
        for m in next.core.members where m.id != next.core.authorityID {
            try check(parent.core.member(m.id) != nil)
        }
    }
    /// Checks the signed transition from `parent` to `next`. A recovery
    /// transition also needs the statement signed by the parent's recovery key.
    public static func verifyTransition(
        parent: State, next: State, recovery: RecoveryStatement? = nil
    ) throws {
        try check(
            next.core.parent == stateHash(parent) && next.core.epoch == parent.core.epoch + 1
                && next.core.epoch != 0 && next.core.account == parent.core.account
                && next.core.domain == parent.core.domain, .stale)
        try check(next.core.issuedAt >= parent.core.issuedAt)
        guard let authority = next.core.member(next.core.authorityID),
            authority.kind == Kind.client.rawValue
        else { throw Failure.invalid }
        if let recovery {
            try recovery.verify(parent: parent, next: next)
            try check(next.core.contentEpoch == parent.core.contentEpoch + 1 && next.prevSecret.isEmpty)
            try check(next.core.member(parent.core.authorityID) == nil)
            try check(next.core.removed.contains(parent.core.authorityID))
            try check(
                verifyEd25519(
                    publicKey: authority.ed, message: signBytes(next), signature: next.signature),
                .untrusted)
            try recoveryDelta(parent, next)
        } else {
            try check(next.core.authorityID == parent.core.authorityID)
            guard let old = parent.core.member(parent.core.authorityID) else {
                throw Failure.untrusted
            }
            try check(
                verifyEd25519(publicKey: old.ed, message: signBytes(next), signature: next.signature),
                .untrusted)
            if next.core.contentEpoch == parent.core.contentEpoch {
                try check(
                    next.core.removed == parent.core.removed
                        && sameMembers(next.core.members, parent.core.members)
                        && next.prevSecret.isEmpty)
            } else if next.core.contentEpoch == parent.core.contentEpoch + 1 {
                try check(!next.prevSecret.isEmpty)
                try membershipDelta(parent, next)
            } else {
                throw Failure.invalid
            }
        }
    }

    public struct RecoveryStatement: Sendable, Equatable {
        public var account: String
        public var domain: Data
        public var parent: Data
        public var newAuthorityID: String
        public var newRecoveryEd: Data
        public var newMember: Member
        public var issuedAt: UInt64
        public var notAfter: UInt64
        public var stateHash: Data
        public var nonce: Data
        public var signature: Data
        func signBytes() throws -> Data {
            try check(
                identifier(account) && identifier(newAuthorityID) && newAuthorityID == newMember.id
                    && newMember.kind == Kind.client.rawValue && newRecoveryEd.count == 32
                    && newRecoveryEd != zero && issuedAt > 0 && notAfter > issuedAt
                    && notAfter - issuedAt <= maxLease && stateHash.count == 32
                    && nonce.count == 32 && nonce != zero && parent.count == 32 && parent != zero)
            try nonzero(domain)
            try newMember.validate()
            var w = Writer()
            w.raw(Data("ra3/recovery\0".utf8))
            w.str(account)
            w.raw(domain)
            w.raw(parent)
            w.str(newAuthorityID)
            w.raw(newRecoveryEd)
            w.str(newMember.id)
            w.raw(newMember.ed)
            w.raw(newMember.x)
            w.u32(newMember.kind)
            w.u64(issuedAt)
            w.u64(notAfter)
            w.raw(stateHash)
            w.raw(nonce)
            return w.data
        }
        public func bytes() throws -> Data {
            let body = try signBytes()
            try check(signature.count == 64)
            var w = Writer()
            w.lp(body)
            w.lp(signature)
            try check(w.data.count <= maxState)
            return w.data
        }
        public static func parse(_ b: Data) throws -> RecoveryStatement {
            try check(b.count <= maxState)
            var r = Reader(data: b)
            let body = try r.lp(maxState)
            let sig = try r.lp(64)
            try r.done()
            try check(sig.count == 64)
            let label = Data("ra3/recovery\0".utf8)
            guard body.count >= label.count, body.prefix(label.count) == label else {
                throw Failure.invalid
            }
            var q = Reader(data: body.subdata(in: label.count..<body.count))
            let s = RecoveryStatement(
                account: try q.str(), domain: try q.raw(32), parent: try q.raw(32),
                newAuthorityID: try q.str(), newRecoveryEd: try q.raw(32),
                newMember: Member(
                    id: try q.str(), ed: try q.raw(32), x: try q.raw(32), kind: try q.u32()),
                issuedAt: try q.u64(), notAfter: try q.u64(), stateHash: try q.raw(32),
                nonce: try q.raw(32), signature: sig)
            try q.done()
            // Re-encoding must reproduce the transmitted bytes exactly.
            try check(try s.signBytes() == body)
            return s
        }
        public func verify(parent: State, next: State) throws {
            guard let authority = next.core.member(next.core.authorityID) else {
                throw Failure.untrusted
            }
            try check(
                account == parent.core.account && domain == parent.core.domain
                    && self.parent == DeviceSecurity.stateHash(parent)
                    && newAuthorityID == next.core.authorityID
                    && newRecoveryEd == next.core.recoveryEd && newMember == authority
                    && issuedAt == next.core.issuedAt && notAfter == next.core.notAfter
                    && stateHash == DeviceSecurity.stateHash(next), .untrusted)
            try check(
                verifyEd25519(
                    publicKey: parent.core.recoveryEd, message: signBytes(), signature: signature),
                .untrusted)
        }
    }

    static let suite = HPKE.Ciphersuite(
        kem: .Curve25519_HKDF_SHA256, kdf: .HKDF_SHA256, aead: .AES_GCM_256)
    static func grantContext(_ c: Core, _ ch: Data, _ id: String) -> Data {
        var w = Writer()
        w.str(c.account)
        w.raw(c.domain)
        w.u64(c.contentEpoch)
        w.raw(ch)
        w.str(id)
        return w.data
    }
    /// Opens the content key granted to `recipient` in `s`. `pin` only anchors
    /// the account and domain for later states; the grant is signed with the
    /// state, so the caller must have verified the chain.
    public static func openGrant(
        _ p: Pin, _ s: State, recipient: String, x: Curve25519.KeyAgreement.PrivateKey
    ) throws -> Data {
        try verifyPin(p, s)
        guard let g = s.grants.first(where: { $0.recipient == recipient }),
            let m = s.core.member(recipient)
        else { throw Failure.untrusted }
        try check(m.x == x.publicKey.rawRepresentation, .untrusted)
        let ch = try digest("ra3/core\0", coreBytes(s.core))
        let ctx = grantContext(s.core, ch, recipient)
        var r = try HPKE.Recipient(
            privateKey: x, ciphersuite: suite,
            info: Data("ra3/grant-info\0".utf8) + ctx, encapsulatedKey: g.enc)
        let key = try r.open(g.cipher, authenticating: Data("ra3/grant-aad\0".utf8) + ctx)
        try check(key.count == 32, .untrusted)
        return key
    }
    public static func sealPrevSecret(newSecret: Data, oldSecret: Data) throws -> Data {
        try check(newSecret.count == 32 && oldSecret.count == 32)
        let nonce = randomBytes(12)
        let box = try AES.GCM.seal(
            oldSecret, using: SymmetricKey(data: newSecret), nonce: AES.GCM.Nonce(data: nonce),
            authenticating: Data("ra3/prev-secret\0".utf8))
        return nonce + box.ciphertext + box.tag
    }
    public static func openPrevSecret(newSecret: Data, blob: Data) throws -> Data {
        try check(newSecret.count == 32 && blob.count == 60)
        let box = try AES.GCM.SealedBox(
            nonce: AES.GCM.Nonce(data: blob.prefix(12)), ciphertext: blob.dropFirst(12).dropLast(16),
            tag: blob.suffix(16))
        let out = try AES.GCM.open(
            box, using: SymmetricKey(data: newSecret),
            authenticating: Data("ra3/prev-secret\0".utf8))
        try check(out.count == 32)
        return out
    }

    /// Checks a whole chain from the pinned genesis. A recovery transition
    /// needs its proof; every other entry must have none.
    public static func verifyChain(
        pin: Pin, states: [State], proofs: [RecoveryStatement?]
    ) throws {
        try check(!states.isEmpty && states.count == proofs.count && states.count <= maxStates)
        try verifyGenesis(pin, states[0])
        try check(proofs[0] == nil)
        for i in 1..<states.count {
            try verifyTransition(parent: states[i - 1], next: states[i], recovery: proofs[i])
        }
    }

    // MARK: Pairing trust

    public static func pairTrustDescriptor(_ p: Pin, approver: String) throws -> Data {
        try check(
            identifier(p.account) && identifier(approver) && p.genesis.count == 32
                && p.genesis != zero)
        try nonzero(p.domain)
        var w = Writer()
        w.raw(Data("ra3/pair-trust\0".utf8))
        w.str(p.account)
        w.raw(p.domain)
        w.raw(p.genesis)
        w.str(approver)
        return w.data
    }
    public static func parsePairTrustDescriptor(_ b: Data) throws -> (Pin, String) {
        let label = Data("ra3/pair-trust\0".utf8)
        guard b.count > label.count, b.prefix(label.count) == label else {
            throw Failure.invalid
        }
        var r = Reader(data: b.subdata(in: label.count..<b.count))
        let p = Pin(account: try r.str(), domain: try r.raw(32), genesis: try r.raw(32))
        let approver = try r.str()
        try r.done()
        try check(
            identifier(p.account) && identifier(approver) && p.genesis != zero, .invalid)
        try nonzero(p.domain)
        return (p, approver)
    }
    public static func pairDomainCommit(ephemeral: Data, trust: Data) throws -> Data {
        try check(ephemeral.count == 32)
        var w = Writer()
        w.lp(ephemeral)
        w.lp(trust)
        return digest("ra3/pair-commit\0", w.data)
    }

    // MARK: Envelopes

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
                && h.contentEpoch > 0 && h.contentEpoch <= h.epoch
                && h.counter > 0 && h.compression <= 1 && h.kind == h.purpose.kind
                && (h.machine.isEmpty || identifier(h.machine))
                && (h.session.isEmpty || validID(h.session))
                && (h.request.isEmpty || validID(h.request)))
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
        w.u32(3)
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
        try check(r.u32() == 3)
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
    /// Ownership from the state the sender claims. DeviceMeta is self-only.
    static func scope(_ h: Header, _ s: State) throws -> Member {
        try validate(h)
        try check(
            h.account == s.core.account && h.domain == s.core.domain && h.epoch == s.core.epoch
                && h.contentEpoch == s.core.contentEpoch && h.state == stateHash(s), .stale)
        guard let writer = s.core.member(h.sender) else { throw Failure.untrusted }
        if !h.machine.isEmpty {
            try check(
                s.core.member(h.machine)?.kind == Kind.machine.rawValue, .untrusted)
        }
        switch h.purpose {
        case .deviceMeta:
            try check(h.sender == h.request, .untrusted)
            try check(s.core.member(h.request)?.kind == Kind.client.rawValue, .untrusted)
        case .prefs, .command, .terminalInput, .terminalResize, .terminalHistory:
            try check(writer.kind == Kind.client.rawValue, .untrusted)
        default:
            try check(
                writer.kind == Kind.machine.rawValue && writer.id == h.machine, .untrusted)
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
        return digest("ra3/ciphertext\0", w.data)
    }
    static func originBytes(_ h: Header, _ hh: Data, _ commit: Data) -> Data {
        var w = Writer()
        w.raw(Data("ra3/cipher-origin\0".utf8))
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
        w.raw(Data("ra3/content-key\0".utf8))
        w.str(h.account)
        w.raw(h.domain)
        w.u64(h.epoch)
        w.raw(h.state)
        w.u64(h.contentEpoch)
        w.u32(h.purpose.rawValue)
        w.str(h.sender)
        w.str(h.machine)
        w.str(h.session)
        w.str(h.request)
        w.u64(h.seq)
        w.u64(h.counter)
        var purposeInfo = Writer()
        purposeInfo.raw(Data("ra3/purpose-key\0".utf8))
        purposeInfo.str(h.account)
        purposeInfo.raw(h.domain)
        purposeInfo.u64(h.contentEpoch)
        purposeInfo.u32(h.purpose.rawValue)
        let child = hkdf(secret: secret, salt: nil, info: purposeInfo.data, size: 32)
        return hkdf(secret: child, salt: salt, info: w.data, size: 32)
    }
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
    /// Checks the ciphertext's origin against `s`. For live traffic the sender
    /// must still be in `head`, and control needs head's lease and content
    /// epoch. Stored history is checked against its own state only.
    public static func verifyOrigin(
        _ w: Wire, state s: State, head: State?, live: Bool, now: UInt64
    ) throws -> Header {
        let c = w.formed
        let h = try parseHeader(c.header)
        _ = try scope(h, s)
        if live {
            guard let head else { throw Failure.stale }
            try check(head.core.member(h.sender) != nil, .untrusted)
            if h.purpose.control {
                try check(head.core.notAfter >= now && head.core.contentEpoch == h.contentEpoch, .stale)
            }
        }
        let commit = try commitment(c)
        guard let m = s.core.member(h.sender) else { throw Failure.untrusted }
        let hh = sha256(c.header)
        try check(
            verifyEd25519(
                publicKey: m.ed, message: originBytes(h, hh, commit), signature: c.origin),
            .untrusted)
        return h
    }
    public static func open(
        _ w: Wire, state s: State, head: State?, live: Bool, now: UInt64, secret: Data,
        sessionSeed: Data?
    ) throws -> (Header, Data) {
        let h = try verifyOrigin(w, state: s, head: head, live: live, now: now)
        let key = try contentKey(secret: secret, sessionSeed: sessionSeed, header: h)
        let c = w.formed
        let box = try AES.GCM.SealedBox(
            nonce: AES.GCM.Nonce(data: c.nonce), ciphertext: c.cipher.dropLast(16),
            tag: c.cipher.suffix(16))
        return (h, try AES.GCM.open(box, using: SymmetricKey(data: key), authenticating: c.header))
    }
    public static func wireBytes(_ w: Wire) throws -> Data {
        _ = try commitment(w.formed)
        try check(w.formed.origin.count == 64)
        var e = Writer()
        e.lp(w.formed.header)
        e.lp(w.formed.nonce)
        e.lp(w.formed.cipher)
        e.lp(w.formed.origin)
        return e.data
    }
    public static func parseWire(_ b: Data) throws -> Wire {
        try check(b.count <= maxCipher + 4096)
        var r = Reader(data: b)
        let w = Wire(
            formed: Cipher(
                header: try r.lp(1024), nonce: try r.lp(12), cipher: try r.lp(maxCipher),
                origin: try r.lp(64)))
        try r.done()
        _ = try wireBytes(w)
        return w
    }
    public static func envelope(_ w: Wire) throws -> PBEnvelope {
        let h = try parseHeader(w.formed.header)
        _ = try wireBytes(w)
        var ph = PBHeader()
        ph.version = 3
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
        return e
    }
    public static func fromEnvelope(_ e: PBEnvelope) throws -> Wire {
        try check(e.header.count <= 1024 && e.unknownFields.data.isEmpty)
        let w = Wire(
            formed: Cipher(
                header: e.securityHeader, nonce: e.nonce, cipher: e.ciphertext, origin: e.signature))
        let h = try parseHeader(w.formed.header)
        let ph = try PBHeader(serializedBytes: e.header)
        try check(
            ph.version == 3 && ph.kind.rawValue == h.kind && ph.machineID == h.machine
                && ph.sessionID == h.session && ph.requestID == h.request && ph.seq == h.seq
                && UInt64(bitPattern: ph.deadlineMs) == h.deadline && ph.sender == h.sender
                && ph.compression.rawValue == h.compression && ph.unknownFields.data.isEmpty,
            .untrusted)
        _ = try wireBytes(w)
        return w
    }
}
