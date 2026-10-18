import CryptoKit
import Foundation

extension DeviceSecurity {
    /// Stored only with the account's unlocked-device protection. No receiver
    /// private key or complete content secret is delegated to a locked worker.
    public struct Record: Sendable, CustomStringConvertible, CustomDebugStringConvertible,
        CustomReflectable
    {
        public let pin: Pin
        public let state: State
        public let memberID: String
        private let xPrivate: Data
        public var counter: UInt64
        public var description: String { "DeviceSecurity.Record(epoch=\(state.core.epoch))" }
        public var debugDescription: String { description }
        public var customMirror: Mirror {
            Mirror(self, children: ["epoch": state.core.epoch, "memberID": memberID])
        }
        public init(pin: Pin, state: State, memberID: String, xPrivate: Data, counter: UInt64 = 0)
            throws
        {
            try verifyState(pin, state)
            try check(xPrivate.count == 32 && identifier(memberID))
            let x = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: xPrivate)
            _ = try openGrant(pin, state, recipient: memberID, x: x)
            self.pin = pin
            self.state = state
            self.memberID = memberID
            self.xPrivate = xPrivate
            self.counter = counter
        }
        public func secret() throws -> Data {
            try openGrant(
                pin, state, recipient: memberID,
                x: Curve25519.KeyAgreement.PrivateKey(rawRepresentation: xPrivate))
        }
        public func openReply(_ waiter: Waiter, _ capsule: Capsule) throws -> Data {
            try check(waiter.pin == pin && waiter.sender == memberID, .untrusted)
            return try reply(
                waiter, capsule, x: Curve25519.KeyAgreement.PrivateKey(rawRepresentation: xPrivate))
        }
        public func advanced(_ next: State) throws -> Record {
            try advance(pin, state, next)
            return try Record(pin: pin, state: next, memberID: memberID, xPrivate: xPrivate)
        }
        public func bytes() throws -> Data {
            var w = Writer()
            w.u32(2)
            w.str(pin.account)
            w.raw(pin.domain)
            w.raw(pin.genesis)
            w.str(pin.authorityID)
            w.raw(pin.authorityEd)
            w.raw(pin.authorityX)
            w.lp(try stateBytes(state))
            w.str(memberID)
            w.raw(xPrivate)
            w.u64(counter)
            return w.data
        }
        public static func parse(_ b: Data) throws -> Record {
            try check(b.count <= maxState + 512)
            var r = Reader(data: b)
            try check(r.u32() == 2)
            let pin = Pin(
                account: try r.str(), domain: try r.raw(32), genesis: try r.raw(32),
                authorityID: try r.str(), authorityEd: try r.raw(32), authorityX: try r.raw(32))
            let state = try parseState(r.lp(maxState))
            let id = try r.str()
            let x = try r.raw(32)
            let counter = try r.u64()
            try r.done()
            return try Record(pin: pin, state: state, memberID: id, xPrivate: x, counter: counter)
        }
    }
    public struct Historical: Sendable, CustomStringConvertible {
        public let state: State
        public let secret: Data
        public var description: String { "DeviceSecurity.Historical(epoch=\(state.core.epoch))" }
        public init(state: State, secret: Data) throws {
            try check(secret.count == 32 && state.core.epoch > 0 && state.core.epoch <= 32768)
            self.state = state
            self.secret = secret
        }
        public func bytes() throws -> Data {
            var w = Writer()
            w.lp(try stateBytes(state))
            w.raw(secret)
            return w.data
        }
        public static func parse(_ data: Data) throws -> Historical {
            try check(data.count <= maxState + 36)
            var r = Reader(data: data)
            let state = try parseState(r.lp(maxState))
            let key = try r.raw(32)
            try r.done()
            return try Historical(state: state, secret: key)
        }
    }
    /// Each method is a platform-atomic transaction shared by all foreground
    /// callers. Missing/corrupt state throws; initialize is never an upsert.
    public protocol Storage: Sendable {
        func readSecurity() throws -> Record
        func initializeSecurity(_ record: Record) throws
        func reserveSecurity(expectedState: Data) throws -> Record
        func advanceSecurity(expectedState: Data, next: State) throws -> Record
        func readSecurityHistory(epoch: UInt64) throws -> Historical?
        func cacheSecurityHistory(_ history: Historical) throws
        func readRemovalOperations() throws -> Data?
        func writeRemovalOperations(_ data: Data) throws
    }
}
