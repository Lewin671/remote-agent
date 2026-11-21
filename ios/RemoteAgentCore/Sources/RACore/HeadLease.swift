import Foundation

extension DeviceSecurity {
    public struct HeadLease: Sendable, Equatable {
        public var domain: Data
        public var head: Data
        public var issuedAt: UInt64
        public var notAfter: UInt64
        public var signature: Data

        public init(domain: Data, head: Data, issuedAt: UInt64, notAfter: UInt64, signature: Data) {
            self.domain = domain
            self.head = head
            self.issuedAt = issuedAt
            self.notAfter = notAfter
            self.signature = signature
        }

        public static func sign(_ state: State, signer: SigningKey, now: Int64, floor: UInt64 = 0) throws -> HeadLease {
            try check(
                state.core.member(state.core.authorityID)?.kind == Kind.client.rawValue
                    && state.core.authorityID == deviceId(publicKey: signer.publicKey),
                .untrusted
            )
            let issued = max(UInt64(max(now, 0)), state.core.issuedAt, floor)
            try check(issued <= UInt64.max - lease)
            let headRaw = try stateBytes(state)
            let (unsigned, signBytes) = try SecCall.newLeaseUnsigned(headStateBytes: headRaw, nowMs: issued)
            let sig = signer.sign(signBytes)
            return try SecCall.marshalLease(
                domain: unsigned.domain,
                headHash: unsigned.head,
                issuedAt: unsigned.issuedAt,
                notAfter: unsigned.notAfter,
                signature: sig
            ).1
        }

        public func bytes() throws -> Data {
            try SecCall.marshalLease(
                domain: domain,
                headHash: head,
                issuedAt: issuedAt,
                notAfter: notAfter,
                signature: signature
            ).0
        }

        public static func parse(_ b: Data) throws -> HeadLease {
            try SecCall.parseLease(b)
        }

        public func verify(_ state: State) throws {
            try SecCall.verifyLease(
                leaseBytes: try bytes(),
                headStateBytes: try stateBytes(state),
                nowMs: 0,
                checkWindow: false
            )
        }

        public func valid(_ state: State, now: UInt64) throws {
            do {
                try SecCall.verifyLease(
                    leaseBytes: try bytes(),
                    headStateBytes: try stateBytes(state),
                    nowMs: Int64(bitPattern: now),
                    checkWindow: true
                )
            } catch Failure.stale {
                throw Failure.lease
            }
        }
    }
}
