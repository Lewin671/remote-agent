import CryptoKit
import Foundation
import RAProto

extension DeviceSecurity {
    public static func purposeKey(
        secret: Data, account: String, domain: Data, epoch: UInt64, purpose: Purpose
    ) throws -> Data {
        try check(secret.count == 32 && identifier(account) && epoch > 0)
        try nonzero(domain)
        var w = Writer()
        w.raw(Data("ra2/purpose-key\0".utf8))
        w.str(account)
        w.raw(domain)
        w.u64(epoch)
        w.u32(purpose.rawValue)
        return hkdf(secret: secret, salt: nil, info: w.data, size: 32)
    }
    public struct NotifyRecord: Sendable, CustomStringConvertible, CustomDebugStringConvertible,
        CustomReflectable
    {
        public let pin: Pin
        public let state: State
        private let child: Data
        public var description: String {
            "DeviceSecurity.NotifyRecord(epoch=\(state.core.contentEpoch))"
        }
        public var debugDescription: String { description }
        public var customMirror: Mirror {
            Mirror(self, children: ["epoch": state.core.contentEpoch])
        }
        public init(record: Record) throws {
            pin = record.pin
            state = record.state
            child = try purposeKey(
                secret: record.secret(), account: pin.account, domain: pin.domain,
                epoch: state.core.contentEpoch, purpose: .notify)
        }
        private init(pin: Pin, state: State, child: Data) throws {
            try verifyState(pin, state)
            try check(child.count == 32)
            self.pin = pin
            self.state = state
            self.child = child
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
            w.raw(child)
            return w.data
        }
        public static func parse(_ data: Data) throws -> NotifyRecord {
            try check(data.count <= maxState + 512)
            var r = Reader(data: data)
            try check(r.u32() == 2)
            let pin = Pin(
                account: try r.str(), domain: try r.raw(32), genesis: try r.raw(32),
                authorityID: try r.str(), authorityEd: try r.raw(32), authorityX: try r.raw(32))
            let state = try parseState(r.lp(maxState))
            let key = try r.raw(32)
            try r.done()
            return try NotifyRecord(pin: pin, state: state, child: key)
        }
        public func open(_ env: PBEnvelope) throws -> PBNotifyContent {
            let wire = try fromEnvelope(env)
            let h = try verifyWire(pin, state, wire)
            try check(h.purpose == .notify, .untrusted)
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
            let key = hkdf(secret: child, salt: nil, info: w.data, size: 32)
            let c = wire.formed
            let box = try AES.GCM.SealedBox(
                nonce: AES.GCM.Nonce(data: c.nonce), ciphertext: c.cipher.dropLast(16),
                tag: c.cipher.suffix(16))
            let p = try unpackSecurity(
                h, AES.GCM.open(box, using: SymmetricKey(data: key), authenticating: c.header))
            let result = try PBNotifyContent(serializedBytes: p)
            try validateSecurityBody(h, result)
            return result
        }
    }
}
