import Foundation

/// Public registration material, stored separately from existing account and
/// security records. Binding to the genesis prevents reuse in another domain.
public struct AccountPublicProfile: Sendable, Equatable {
  public let pin: DeviceSecurity.Pin
  public let publicKey: Data
  public init(publicKey: Data, pin: DeviceSecurity.Pin) throws {
    try DeviceSecurity.check(
      publicKey.count == 32 && deviceId(publicKey: publicKey) == pin.account, .untrusted)
    try DeviceSecurity.nonzero(pin.domain)
    try DeviceSecurity.nonzero(pin.genesis)
    self.pin = pin
    self.publicKey = publicKey
  }
  public static func created(root: Data, genesis: DeviceSecurity.State) throws
    -> AccountPublicProfile
  {
    let pin = try DeviceSecurity.pin(for: genesis)
    try DeviceSecurity.verifyGenesis(pin, genesis)
    return try AccountPublicProfile(publicKey: Keys.derive(root: root).account.publicKey, pin: pin)
  }
  public var storageKey: String { "account-public-" + pin.account + "-" + pin.genesis.hex }
  public func bytes() throws -> Data {
    var w = DeviceSecurity.Writer()
    w.str(pin.account)
    w.raw(pin.domain)
    w.raw(pin.genesis)
    w.raw(publicKey)
    return w.data
  }
  public static func parse(_ bytes: Data) throws -> AccountPublicProfile {
    var r = DeviceSecurity.Reader(data: bytes)
    let account = try r.str()
    let domain = try r.raw(32)
    let genesis = try r.raw(32)
    let key = try r.raw(32)
    try r.done()
    return try AccountPublicProfile(
      publicKey: key, pin: .init(account: account, domain: domain, genesis: genesis))
  }
  /// For recovery/context-only retrieval: verify the full chain and the code's
  /// genesis prefix, and preserve any local pin and already accepted history.
  public static func recovery(
    publicKey: Data, states: [DeviceSecurity.State], proofs: [DeviceSecurity.RecoveryStatement?],
    genesisPrefix: Data, local: DeviceSecurity.Record? = nil
  ) throws -> AccountPublicProfile {
    guard let genesis = states.first else { throw DeviceSecurity.Failure.invalid }
    let pin = try DeviceSecurity.pin(for: genesis)
    try DeviceSecurity.check(
      genesisPrefix.count == 10 && pin.genesis.prefix(10) == genesisPrefix, .untrusted)
    try DeviceSecurity.verifyChain(pin: pin, states: states, proofs: proofs)
    if let local {
      try DeviceSecurity.check(local.pin == pin && states.count >= local.headEpoch, .conflict)
      for state in states where state.core.epoch <= local.headEpoch {
        guard let accepted = local.states[state.core.epoch] else {
          throw DeviceSecurity.Failure.conflict
        }
        try DeviceSecurity.check(
          DeviceSecurity.stateHash(state) == DeviceSecurity.stateHash(accepted), .conflict)
      }
    }
    return try AccountPublicProfile(publicKey: publicKey, pin: pin)
  }
}
