import CryptoKit
import Foundation

extension DeviceSecurity {
    /// The local security state: the pinned anchor, the signed chain, this
    /// device's exchange key, the content-key cache, the
    /// time floor. Stored under the account's unlocked-device protection.
    public struct Record: Sendable, CustomStringConvertible, CustomDebugStringConvertible,
        CustomReflectable
    {
        public let pin: Pin
        public var states: [UInt64: State]
        public var headEpoch: UInt64
        public let memberID: String
        private let xPrivate: Data
        public var member: Bool
        public var floor: UInt64
        public var floorSaved: UInt64
        public var secret: Data?
        public var contentKeys: [UInt64: Data]
        public var lease: HeadLease?
        public var recovery: [UInt64: RecoveryStatement]
        public var state: State { states[headEpoch]! }
        public var description: String { "DeviceSecurity.Record(epoch=\(headEpoch))" }
        public var debugDescription: String { description }
        public var customMirror: Mirror {
            Mirror(self, children: ["epoch": headEpoch, "memberID": memberID])
        }
        public init(
            pin: Pin, states: [UInt64: State], memberID: String, xPrivate: Data, member: Bool,
            floor: UInt64 = 0, floorSaved: UInt64 = 0, secret: Data? = nil,
            contentKeys: [UInt64: Data] = [:], recovery: [UInt64: RecoveryStatement] = [:], lease: HeadLease? = nil
        ) throws {
            try check(
                xPrivate.count == 32 && identifier(memberID) && !states.isEmpty, .invalid)
            guard let head = states.values.max(by: { $0.core.epoch < $1.core.epoch }),
                states.count == Int(head.core.epoch)
            else { throw Failure.invalid }
            for ep in 1...head.core.epoch {
                try check(states[ep]?.core.epoch == ep, .storage)
            }
            try verifyGenesis(pin, states[1]!)
            if head.core.epoch > 1 {
                for ep in 2...head.core.epoch {
                    try verifyTransition(
                        parent: states[ep - 1]!, next: states[ep]!, recovery: recovery[ep])
                }
            }
            try check((head.core.member(memberID) != nil) == member, .storage)
            if member {
                guard let secret, secret.count == 32 else { throw Failure.storage }
                let x = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: xPrivate)
                let opened = try openGrant(pin, head, recipient: memberID, x: x)
                try check(opened == secret, .storage)
            } else if let secret {
                // A removed device keeps the last content key it held so its
                // own history stays readable; it cannot open the new head.
                try check(secret.count == 32, .storage)
            }
            for (_, key) in contentKeys { try check(key.count == 32, .storage) }
            self.pin = pin
            self.states = states
            self.headEpoch = head.core.epoch
            self.memberID = memberID
            self.xPrivate = xPrivate
            self.member = member
            self.floor = floor
            self.floorSaved = floorSaved
            self.secret = secret
            self.contentKeys = contentKeys
            self.recovery = recovery
            self.lease = lease
        }
        public func includesInstallation(_ older: Record) throws -> Bool {
            guard pin == older.pin, memberID == older.memberID, headEpoch >= older.headEpoch,
                let previous = states[older.headEpoch]
            else { return false }
            return try stateHash(previous) == stateHash(older.state)
                && (headEpoch > older.headEpoch || xPrivate == older.xPrivate)
        }
        /// Called under the storage transaction lock using the record read there.
        public func mergingCommitted(_ old: Record) throws -> Record {
            try check(pin == old.pin && memberID == old.memberID && xPrivate == old.xPrivate, .stale)
            guard headEpoch >= old.headEpoch, let previous = states[old.headEpoch] else { throw Failure.stale }
            try check(stateHash(previous) == stateHash(old.state), .stale)
            var next = self
            next.floor = max(floor, old.floor); next.floorSaved = max(floorSaved, old.floorSaved)
            for (epoch,key) in old.contentKeys {
                if let existing = next.contentKeys[epoch] { try check(existing == key, .conflict) }
                next.contentKeys[epoch] = key
            }
            let nextHash = try stateHash(state)
            // A head change invalidates its predecessor's independent lease.
            // Never carry that certificate into a same-head CAS, where validating
            // it against the new authority would prevent takeover renewal.
            if next.lease?.head != nextHash { next.lease = nil }
            if let proposed = next.lease { try proposed.verify(state) }
            if let committed = old.lease, committed.head == nextHash {
                try committed.verify(state)
                if let proposed = next.lease {
                    try check(proposed.issuedAt >= committed.issuedAt && proposed.notAfter >= committed.notAfter
                        && (proposed.issuedAt != committed.issuedAt || proposed.notAfter == committed.notAfter), .stale)
                } else { next.lease = committed }
            }
            return next
        }
        public var xKey: Curve25519.KeyAgreement.PrivateKey {
            get throws { try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: xPrivate) }
        }
        /// The content key of `epoch`, from the cache or by walking PrevSecret
        /// back from the head. Reaching an empty PrevSecret is a recovery
        /// boundary: older content is unavailable to this device.
        public func contentSecret(_ epoch: UInt64) throws -> Data {
            if let k = contentKeys[epoch] { return k }
            // A retained or removed device can still open its own grants from
            // the states it belonged to, even after a later content epoch
            // replaced the key and the head no longer names it.
            if let own = states.values.first(where: {
                $0.core.contentEpoch == epoch && $0.core.member(memberID) != nil
            }), let key = try? openGrant(pin, own, recipient: memberID, x: xKey) {
                return key
            }
            let head = state
            guard head.core.contentEpoch >= epoch else { throw Failure.stale }
            var key: Data? = member ? secret : contentKeys[head.core.contentEpoch]
            var ep = head.core.epoch
            while ep >= 1 {
                guard let s = states[ep] else { throw Failure.storage }
                if s.core.contentEpoch == epoch {
                    guard let key else { throw Failure.unavailable }
                    return key
                }
                guard let prev = states[ep - 1] else { throw Failure.storage }
                if prev.core.contentEpoch != s.core.contentEpoch {
                    if s.prevSecret.isEmpty || key == nil { throw Failure.unavailable }
                    guard let current = key else { throw Failure.unavailable }
                    key = try openPrevSecret(newSecret: current, blob: s.prevSecret)
                }
                ep -= 1
            }
            throw Failure.unavailable
        }
        public func bytes() throws -> Data {
            var w = Writer()
            w.u32(3)
            w.str(pin.account)
            w.raw(pin.domain)
            w.raw(pin.genesis)
            w.str(memberID)
            w.raw(xPrivate)
            w.u32(member ? 1 : 0)
            w.u64(floor)
            w.u64(floorSaved)
            w.lp(secret ?? Data())
            let ordered = states.values.sorted { $0.core.epoch < $1.core.epoch }
            w.u32(UInt32(ordered.count))
            for s in ordered {
                w.u64(s.core.epoch)
                w.lp(try stateBytes(s))
            }
            let proofs = recovery.sorted { $0.key < $1.key }
            w.u32(UInt32(proofs.count))
            for (ep, proof) in proofs {
                w.u64(ep)
                w.lp(try proof.bytes())
            }
            let keys = contentKeys.sorted { $0.key < $1.key }
            w.u32(UInt32(keys.count))
            for (ep, key) in keys {
                w.u64(ep)
                w.raw(key)
            }
            w.lp(try lease?.bytes() ?? Data())
            return w.data
        }
        public static func parse(_ b: Data) throws -> Record {
            try check(b.count <= maxState * maxStates + 4096, .capacity)
            var r = Reader(data: b)
            try check(r.u32() == 3)
            let pin = Pin(account: try r.str(), domain: try r.raw(32), genesis: try r.raw(32))
            let memberID = try r.str()
            let x = try r.raw(32)
            let member = try r.u32() != 0
            let floor = try r.u64()
            let floorSaved = try r.u64()
            let secretRaw = try r.lp(maxState)
            let secret = secretRaw.isEmpty ? nil : secretRaw
            var states: [UInt64: State] = [:]
            var n = try r.u32()
            try check(n <= UInt32(maxStates), .capacity)
            for _ in 0..<n {
                let ep = try r.u64()
                let s = try parseState(r.lp(maxState))
                try check(s.core.epoch == ep && states[ep] == nil, .invalid)
                states[ep] = s
            }
            var recovery: [UInt64: RecoveryStatement] = [:]
            n = try r.u32()
            try check(n <= UInt32(maxStates), .capacity)
            for _ in 0..<n {
                let ep = try r.u64()
                try check(recovery[ep] == nil, .invalid)
                recovery[ep] = try RecoveryStatement.parse(r.lp(maxState))
            }
            var keys: [UInt64: Data] = [:]
            n = try r.u32()
            try check(n <= UInt32(maxStates), .capacity)
            for _ in 0..<n {
                let ep = try r.u64()
                let key = try r.raw(32)
                try check(keys[ep] == nil, .invalid)
                keys[ep] = key
            }
            let leaseRaw = try r.lp(256)
            let lease = leaseRaw.isEmpty ? nil : try HeadLease.parse(leaseRaw)
            try r.done()
            return try Record(
                pin: pin, states: states, memberID: memberID, xPrivate: x, member: member,
                floor: floor, floorSaved: floorSaved, secret: secret, contentKeys: keys,
                recovery: recovery, lease: lease)
        }
    }
    /// Each method is one platform-atomic transaction shared by all foreground
    /// callers. Missing or corrupt state throws; initialize is never an upsert.
    public protocol Storage: Sendable {
        func readSecurity() throws -> Record
        func initializeSecurity(_ record: Record) throws
        /// Writes the whole record if the stored head is still `expectedHead`.
        func writeSecurity(expectedHead: Data, _ record: Record) throws -> Record
    }
}
