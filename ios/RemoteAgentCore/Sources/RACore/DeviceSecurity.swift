// Protocol 10 (state format 3): types, constants, cache, and delegation to clientcore SecCall.
import Foundation
import RAProto
import os

public enum DeviceSecurity {
    public enum Failure: Error, Equatable {
        case invalid, untrusted, stale, conflict, pending, capacity, storage, unknown, unavailable,
             lease, clock
    }

    public static let maxState = 64 << 10
    public static let maxCipher = 4 << 20
    public static let maxRPC = 1 << 20
    public static let maxMembers = 128
    public static let maxStates = 4096
    public static let maxPairStates = 256
    public static let maxPairBytes = 800 << 10
    public static let clockSkewLimit: UInt64 = 5 * 60 * 1000
    public static let lease: UInt64 = 72 * 60 * 60 * 1000
    public static let maxLease: UInt64 = lease
    static let zero = Data(repeating: 0, count: 32)

    static func check(_ condition: Bool, _ error: Failure = .invalid) throws {
        if !condition { throw error }
    }

    static func identifier(_ s: String) -> Bool {
        s.utf8.count == 32
            && s.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
    }

    static func nonzero(_ b: Data) throws { try check(b.count == 32 && b != zero) }

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

    public struct Member: Sendable, Equatable, Hashable {
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
                    && x.count == 32 && x != zero
                    && (kind == Kind.machine.rawValue || kind == Kind.client.rawValue))
        }
    }

    public static func member(ed: Data, x: Data, kind: Kind) throws -> Member {
        let m = Member(id: deviceId(publicKey: ed), ed: ed, x: x, kind: kind.rawValue)
        try m.validate()
        return m
    }

    public struct Core: Sendable, Equatable, Hashable {
        public var account: String
        public var domain: Data
        public var authorityID: String
        public var recoveryEd: Data
        public var parent: Data
        public var epoch: UInt64
        public var contentEpoch: UInt64
        public var issuedAt: UInt64
        public var members: [Member]
        public var removed: [String]
        public func member(_ id: String) -> Member? { members.first { $0.id == id } }
    }

    public struct Grant: Sendable, Equatable, Hashable {
        public var recipient: String
        public var enc: Data
        public var cipher: Data
    }

    public struct State: Sendable, Equatable, Hashable {
        public var core: Core
        public var grants: [Grant]
        public var prevSecret: Data
        public var signature: Data
    }

    private struct CachedState: Sendable {
        let raw: Data
        let hash: Data
    }

    private struct StateCache: Sendable {
        var entries: [State: CachedState] = [:]
        var order: [State] = []
    }

    private static let stateCache = OSAllocatedUnfairLock(initialState: StateCache())

    static func cacheState(_ s: State, raw: Data, hash: Data) {
        stateCache.withLock { cache in
            if cache.entries[s] == nil {
                cache.order.append(s)
                if cache.order.count > 512 {
                    let drop = cache.order.removeFirst()
                    cache.entries.removeValue(forKey: drop)
                }
            }
            cache.entries[s] = CachedState(raw: raw, hash: hash)
        }
    }

    private static func cachedState(_ s: State) -> CachedState? {
        stateCache.withLock { $0.entries[s] }
    }

    private static func encodeCoreFallback(_ c: Core) -> Data {
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
        w.u32(UInt32(c.members.count))
        for m in c.members {
            w.str(m.id); w.raw(m.ed); w.raw(m.x); w.u32(m.kind)
        }
        w.u32(UInt32(c.removed.count))
        for id in c.removed { w.str(id) }
        return w.data
    }

    private static func encodeGrantsFallback(_ s: State) -> Data {
        var w = Writer()
        w.u32(UInt32(s.grants.count))
        for g in s.grants {
            w.str(g.recipient); w.lp(g.enc); w.lp(g.cipher)
        }
        return w.data
    }

    private static func encodeStateFallback(_ s: State) throws -> Data {
        try check(s.signature.count == 64 && s.prevSecret.count <= maxState)
        var w = Writer()
        w.lp(encodeCoreFallback(s.core))
        w.raw(encodeGrantsFallback(s))
        w.lp(s.prevSecret)
        w.lp(s.signature)
        try check(w.data.count <= maxState)
        return w.data
    }

    public static func stateHash(_ s: State) throws -> Data {
        if let cached = cachedState(s) { return cached.hash }
        let ch = sha256(Data("ra3/core\0".utf8), encodeCoreFallback(s.core))
        var p = Writer()
        p.lp(s.prevSecret)
        return sha256(Data("ra3/state\0".utf8), ch, encodeGrantsFallback(s), p.data)
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
        w.str(s.core.authorityID)
        w.raw(s.core.recoveryEd)
        w.raw(h)
        return w.data
    }

    public static func stateBytes(_ s: State) throws -> Data {
        if let cached = cachedState(s) { return cached.raw }
        return try encodeStateFallback(s)
    }

    public static func parseState(_ b: Data) throws -> State {
        try SecCall.parseState(b).0
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
        return Pin(account: state.core.account, domain: state.core.domain, genesis: try stateHash(state))
    }

    public static func verifyPin(_ p: Pin, _ s: State) throws {
        try check(identifier(p.account) && p.domain.count == 32 && p.genesis.count == 32 && p.genesis != zero)
        try check(s.core.account == p.account && s.core.domain == p.domain, .untrusted)
        if s.core.epoch == 1 {
            try check(try stateHash(s) == p.genesis, .untrusted)
        }
    }

    public static func verifyGenesis(_ p: Pin, _ s: State) throws {
        _ = try SecCall.verifyChain(pin: p, states: [try stateBytes(s)], recoveryProofs: [nil])
    }

    public static func verifyTransition(
        parent: State, next: State, recovery: RecoveryStatement? = nil
    ) throws {
        try SecCall.verifyTransition(
            parentBytes: try stateBytes(parent),
            nextBytes: try stateBytes(next),
            recoveryBytes: try recovery.map { try $0.bytes() }
        )
    }

    public struct RecoveryStatement: Sendable, Equatable {
        public var account: String
        public var domain: Data
        public var parent: Data
        public var newAuthorityID: String
        public var newRecoveryEd: Data
        public var newMember: Member
        public var issuedAt: UInt64
        public var stateHash: Data
        public var nonce: Data
        public var signature: Data

        public func bytes() throws -> Data {
            try SecCall.marshalRecoveryStatement(self).0
        }

        public static func parse(_ b: Data) throws -> RecoveryStatement {
            try SecCall.parseRecoveryStatement(b)
        }

        public func verify(parent: State, next: State) throws {
            try SecCall.verifyRecoveryStatement(
                statementBytes: try bytes(),
                parentBytes: try DeviceSecurity.stateBytes(parent),
                nextBytes: try DeviceSecurity.stateBytes(next)
            )
        }
    }

    public static func openGrant(
        _ p: Pin, _ s: State, recipient: String, xPrivate: Data
    ) throws -> Data {
        try verifyPin(p, s)
        try check(xPrivate.count == keySize, .untrusted)
        let raw = try stateBytes(s)
        let enc = try SecCall.grantChallenge(pin: p, stateBytes: raw, memberId: recipient)
        let dh: Data
        do {
            dh = try SecCall.x25519Shared(xPrivate, enc)
        } catch {
            throw Failure.untrusted
        }
        return try SecCall.openGrantWithDH(pin: p, stateBytes: raw, memberId: recipient, dh: dh)
    }

    public static func openPrevSecret(newSecret: Data, blob: Data) throws -> Data {
        try SecCall.openPrevSecret(newSecret: newSecret, blob: blob)
    }

    public static func verifyChain(
        pin: Pin, states: [State], proofs: [RecoveryStatement?], nowMs: UInt64? = nil
    ) throws {
        let now = nowMs ?? UInt64(Date().timeIntervalSince1970 * 1000)
        _ = try SecCall.verifyChain(
            pin: pin,
            states: try states.map { try stateBytes($0) },
            recoveryProofs: try proofs.map { try $0?.bytes() },
            nowMs: Int64(bitPattern: now)
        )
    }

    public static func machineReplacementOrigins(
        account: String,
        target: String,
        states: [State],
        proofs: [PBMachineReplacementProof]
    ) throws -> Set<String> {
        try SecCall.machineReplacementOrigins(
            account: account,
            targetMachineId: target,
            states: try states.map { try stateBytes($0) },
            proofs: proofs
        )
    }

    // MARK: Pairing trust

    public static func pairTrustDescriptor(_ p: Pin, approver: String) throws -> Data {
        try SecCall.pairTrustDescriptor(pin: p, approver: approver)
    }

    public static func parsePairTrustDescriptor(_ b: Data) throws -> (Pin, String) {
        try SecCall.parsePairTrustDescriptor(b)
    }

    public static func pairDomainCommit(ephemeral: Data, trust: Data) throws -> Data {
        try SecCall.pairDomainCommit(initiatorPublic: ephemeral, trustDescriptor: trust)
    }

    // MARK: Envelope purposes (kept for Core security op dispatch & tests)

    public enum Purpose: UInt32, Sendable {
        case event = 1, sessionMeta = 2, sessionKey = 3, machineMeta = 4, command = 5, result = 6,
             terminalOutput = 7, terminalInput = 8, notify = 10, image = 11, prefs = 12,
             deviceMeta = 13, terminalResize = 14, terminalHistory = 15

        public var mutable: Bool {
            [.sessionMeta, .machineMeta, .deviceMeta, .prefs].contains(self)
        }

        public var control: Bool {
            [.command, .terminalInput, .terminalResize, .terminalHistory].contains(self)
        }

        public var sessionKey: Bool {
            [.event, .image, .terminalOutput, .terminalInput, .terminalResize, .terminalHistory].contains(self)
        }

        public var kind: UInt32 { [.terminalResize, .terminalHistory].contains(self) ? 8 : rawValue }
    }
}
