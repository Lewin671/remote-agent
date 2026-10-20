import CryptoKit
import Foundation

/// Pure signing builders. Callers must serialize authority operations, persist
/// the exact result before publishing, and enforce the active/unlocked gate.
public struct Authority: Sendable {
  typealias S = DeviceSecurity
  typealias GrantSealer = @Sendable (S.Member, S.Core, Data, Data) throws -> S.Grant
  private let seal: GrantSealer
  private let nonce: @Sendable (Int) -> Data

  public init() {
    self.init(
      seal: { try S.sealGrant(recipient: $0, core: $1, coreHash: $2, secret: $3) },
      nonce: { randomBytes($0) })
  }
  init(seal: @escaping GrantSealer, nonce: @escaping @Sendable (Int) -> Data) {
    self.seal = seal
    self.nonce = nonce
  }
  public static func newSecret() -> Data { randomBytes(32) }
  public static func newRecovery() throws -> (seed: Data, publicKey: Data) {
    let seed = randomBytes(32)
    return (seed, try SigningKey(seed: seed).publicKey)
  }
  public static func newMember(signer: SigningKey, xPrivate: Data, kind: DeviceSecurity.Kind) throws
    -> DeviceSecurity.Member
  {
    try memberWithX(edPublic: signer.publicKey, xPrivate: xPrivate, kind: kind)
  }
  public static func memberWithX(edPublic: Data, xPrivate: Data, kind: DeviceSecurity.Kind) throws
    -> DeviceSecurity.Member
  {
    try S.member(
      ed: edPublic,
      x: Curve25519.KeyAgreement.PrivateKey(rawRepresentation: xPrivate).publicKey
        .rawRepresentation, kind: kind)
  }
  public static func needsRenewal(_ lease: DeviceSecurity.HeadLease?, now: Int64) -> Bool {
    guard let lease else { return true }
    let at = UInt64(max(now, 1))
    guard at >= lease.issuedAt else { return false }
    let remaining = at >= lease.notAfter ? 0 : lease.notAfter - at
    return remaining < 24 * 60 * 60 * 1000 && at - lease.issuedAt >= 12 * 60 * 60 * 1000
  }
  public func genesis(
    account: String, domain: Data, member: DeviceSecurity.Member,
    recoveryEd: Data, secret: Data, signer: SigningKey, now: Int64
  ) throws -> DeviceSecurity.State {
    try S.check(member.kind == S.Kind.client.rawValue && member.ed == signer.publicKey)
    let core = S.Core(
      account: account, domain: domain, authorityID: member.id, recoveryEd: recoveryEd,
      parent: S.zero, epoch: 1, contentEpoch: 1, issuedAt: UInt64(max(now, 1)),
      members: [member], removed: [])
    let state = try build(core, secret: secret, prev: Data(), signer: signer)
    try S.verifyGenesis(S.pin(for: state), state)
    return state
  }
  private func successor(_ parent: S.State, now: Int64, rotate: Bool) throws -> S.Core {
    try S.check(
      parent.core.epoch < UInt64.max && (!rotate || parent.core.contentEpoch < UInt64.max))
    var core = parent.core
    core.parent = try S.stateHash(parent)
    core.epoch += 1
    if rotate { core.contentEpoch += 1 }
    core.issuedAt = max(UInt64(max(now, 1)), core.issuedAt)
    return core
  }
  public func membership(
    parent: DeviceSecurity.State, members: [DeviceSecurity.Member], removed: [String],
    oldSecret: Data, newSecret: Data, signer: SigningKey, now: Int64
  ) throws -> DeviceSecurity.State {
    var core = try successor(parent, now: now, rotate: true)
    core.members = members.sorted { $0.id < $1.id }
    core.removed = removed.sorted()
    let prev = try S.sealPrevSecret(newSecret: newSecret, oldSecret: oldSecret, nonce: nonce(12))
    return try normal(parent, core: core, secret: newSecret, prev: prev, signer: signer)
  }
  public func pair(
    parent: DeviceSecurity.State, candidate: DeviceSecurity.Member, oldSecret: Data,
    newSecret: Data, signer: SigningKey, now: Int64
  ) throws -> DeviceSecurity.State {
    try S.check(
      parent.core.member(candidate.id) == nil && !parent.core.removed.contains(candidate.id))
    return try membership(
      parent: parent, members: parent.core.members + [candidate], removed: parent.core.removed,
      oldSecret: oldSecret, newSecret: newSecret, signer: signer, now: now)
  }
  public func remove(
    parent: DeviceSecurity.State, targets: [String], oldSecret: Data,
    newSecret: Data, signer: SigningKey, now: Int64
  ) throws -> DeviceSecurity.State {
    try S.check(
      !targets.isEmpty && !targets.contains(parent.core.authorityID)
        && targets.allSatisfy { parent.core.member($0) != nil })
    return try membership(
      parent: parent, members: parent.core.members.filter { !targets.contains($0.id) },
      removed: Array(Set(parent.core.removed + targets)), oldSecret: oldSecret,
      newSecret: newSecret, signer: signer, now: now)
  }
  public func rotateX(
    parent: DeviceSecurity.State, target: String, newXPublic: Data, oldSecret: Data,
    newSecret: Data, signer: SigningKey, now: Int64
  ) throws -> DeviceSecurity.State {
    guard let old = parent.core.member(target) else { throw S.Failure.invalid }
    try S.check(
      newXPublic.count == 32 && newXPublic != old.x && !parent.core.removed.contains(target))
    let members = parent.core.members.map { m in
      var m = m
      if m.id == target { m.x = newXPublic }
      return m
    }
    return try membership(
      parent: parent, members: members, removed: parent.core.removed,
      oldSecret: oldSecret, newSecret: newSecret, signer: signer, now: now)
  }
  /// Rotating the recovery credential vetoes pending recovery without a content epoch.
  public func veto(
    parent: DeviceSecurity.State, newRecoveryEd: Data, secret: Data,
    signer: SigningKey, now: Int64
  ) throws -> DeviceSecurity.State {
    var core = try successor(parent, now: now, rotate: false)
    core.recoveryEd = newRecoveryEd
    return try normal(parent, core: core, secret: secret, prev: Data(), signer: signer)
  }
  public func handoff(
    parent: DeviceSecurity.State, incoming: String, recoveryEd: Data,
    oldSecret: Data, newSecret: Data, signer: SigningKey, now: Int64
  ) throws -> DeviceSecurity.State {
    try S.check(
      incoming != parent.core.authorityID
        && parent.core.member(incoming)?.kind == S.Kind.client.rawValue
        && recoveryEd != parent.core.recoveryEd)
    var core = try successor(parent, now: now, rotate: true)
    core.authorityID = incoming
    core.recoveryEd = recoveryEd
    core.members.removeAll { $0.id == parent.core.authorityID }
    core.removed = (core.removed + [parent.core.authorityID]).sorted()
    return try normal(
      parent, core: core, secret: newSecret,
      prev: S.sealPrevSecret(newSecret: newSecret, oldSecret: oldSecret, nonce: nonce(12)),
      signer: signer)
  }
  public func recovery(
    parent: DeviceSecurity.State, newMember: DeviceSecurity.Member,
    members: [DeviceSecurity.Member],
    secret: Data, newSigner: SigningKey, oldRecoverySeed: Data, newRecoveryEd: Data,
    now: Int64
  ) throws -> (state: DeviceSecurity.State, statement: DeviceSecurity.RecoveryStatement) {
    try S.check(
      newMember.kind == S.Kind.client.rawValue && newMember.ed == newSigner.publicKey
        && !members.contains { $0.id == parent.core.authorityID })
    var core = try successor(parent, now: now, rotate: true)
    core.authorityID = newMember.id
    core.recoveryEd = newRecoveryEd
    core.members = members.sorted { $0.id < $1.id }
    core.removed = Array(Set(core.removed + [parent.core.authorityID])).sorted()
    let state = try build(core, secret: secret, prev: Data(), signer: newSigner)
    let statement = try S.RecoveryStatement(
      account: core.account, domain: core.domain, parent: core.parent,
      newAuthorityID: newMember.id, newRecoveryEd: newRecoveryEd, newMember: newMember,
      issuedAt: core.issuedAt, stateHash: S.stateHash(state), nonce: nonce(32), signature: Data()
    )
    .signed(recoverySeed: oldRecoverySeed)
    try S.verifyTransition(parent: parent, next: state, recovery: statement)
    return (state, statement)
  }
  private func normal(_ parent: S.State, core: S.Core, secret: Data, prev: Data, signer: SigningKey)
    throws -> S.State
  {
    let state = try build(core, secret: secret, prev: prev, signer: signer)
    try S.verifyTransition(parent: parent, next: state)
    return state
  }
  private func build(_ core: S.Core, secret: Data, prev: Data, signer: SigningKey) throws -> S.State
  {
    try S.check(secret.count == 32)
    let hash = try S.digest("ra3/core\0", S.coreBytes(core))
    let grants = try core.members.map { try seal($0, core, hash, secret) }
    var state = S.State(core: core, grants: grants, prevSecret: prev, signature: Data())
    state.signature = signer.sign(try S.signBytes(state))
    _ = try S.stateBytes(state)
    return state
  }
}
