import Foundation
import RAProto

extension DeviceSecurity {
    /// The purpose key of one content epoch, which is what the notification
    /// path may hold: it opens pushes, never other content.
    public static func purposeKey(
        secret: Data, account: String, domain: Data, epoch: UInt64, purpose: Purpose
    ) throws -> Data {
        try SecCall.derivePurposeKey(
            secret: secret,
            accountId: account,
            domain: domain,
            contentEpoch: epoch,
            purpose: purpose.rawValue
        )
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
            guard record.member, let secret = record.secret else { throw Failure.untrusted }
            pin = record.pin
            state = record.state
            child = try purposeKey(
                secret: secret, account: pin.account, domain: pin.domain,
                epoch: state.core.contentEpoch, purpose: .notify)
        }

        private init(pin: Pin, state: State, child: Data) throws {
            try verifyPin(pin, state)
            try check(child.count == keySize)
            self.pin = pin
            self.state = state
            self.child = child
        }

        public func bytes() throws -> Data {
            var w = Writer()
            w.u32(3)
            w.str(pin.account)
            w.raw(pin.domain)
            w.raw(pin.genesis)
            w.lp(try stateBytes(state))
            w.raw(child)
            return w.data
        }

        public static func parse(_ data: Data) throws -> NotifyRecord {
            try check(data.count <= maxState + 512)
            var r = Reader(data: data)
            try check(r.u32() == 3)
            let pin = Pin(account: try r.str(), domain: try r.raw(32), genesis: try r.raw(32))
            let state = try parseState(r.lp(maxState))
            let key = try r.raw(32)
            try r.done()
            return try NotifyRecord(pin: pin, state: state, child: key)
        }

        public func open(_ env: PBEnvelope) throws -> PBNotifyContent {
            try SecCall.openNotifyEnvelope(
                pin: pin,
                states: [try stateBytes(state)],
                envelope: env,
                purposeKey: child
            )
        }
    }
}
