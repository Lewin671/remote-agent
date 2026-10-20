import Foundation

extension DeviceSecurity {
  public struct HeadLease: Sendable, Equatable {
    public var domain: Data
    public var head: Data
    public var issuedAt: UInt64
    public var notAfter: UInt64
    public var signature: Data
    public static func sign(_ state: State, signer: SigningKey, now: Int64, floor: UInt64 = 0) throws -> HeadLease {
      try check(state.core.member(state.core.authorityID)?.kind == Kind.client.rawValue
                && state.core.authorityID == deviceId(publicKey: signer.publicKey), .untrusted)
      let issued = max(UInt64(max(now, 0)), state.core.issuedAt, floor)
      try check(issued <= UInt64.max - lease)
      var result = HeadLease(domain: state.core.domain, head: try stateHash(state),
                             issuedAt: issued, notAfter: issued + lease, signature: Data())
      result.signature = signer.sign(try result.signBytes())
      return result
    }
    public func signBytes() throws -> Data {
      try nonzero(domain)
      try nonzero(head)
      try check(issuedAt > 0 && notAfter > issuedAt && notAfter - issuedAt <= lease)
      var w = Writer()
      w.raw(Data("ra3/lease\0".utf8))
      w.raw(domain)
      w.raw(head)
      w.u64(issuedAt)
      w.u64(notAfter)
      return w.data
    }
    public func bytes() throws -> Data {
      try check(signature.count == 64)
      var w = Writer()
      w.lp(try signBytes())
      w.lp(signature)
      return w.data
    }
    public static func parse(_ b: Data) throws -> HeadLease {
      try check(b.count <= 256)
      var r = Reader(data: b)
      let body = try r.lp(128)
      let sig = try r.lp(64)
      try r.done()
      var d = Reader(data: body)
      try check(d.raw(10) == Data("ra3/lease\0".utf8))
      let l = HeadLease(
        domain: try d.raw(32), head: try d.raw(32), issuedAt: try d.u64(), notAfter: try d.u64(),
        signature: sig)
      try d.done()
      try check(l.bytes() == b)
      return l
    }
    public func verify(_ state: State) throws {
      guard let m = state.core.member(state.core.authorityID) else { throw Failure.untrusted }
      try check(
        domain == state.core.domain && head == stateHash(state) && issuedAt >= state.core.issuedAt
          && verifyEd25519(publicKey: m.ed, message: signBytes(), signature: signature), .untrusted)
    }
    public func valid(_ state: State, now: UInt64) throws {
      try check(head == stateHash(state), .lease)
      try verify(state)
      try check(issuedAt <= now + clockSkewLimit, .clock)
      try check(now < notAfter, .lease)
    }
  }
}
