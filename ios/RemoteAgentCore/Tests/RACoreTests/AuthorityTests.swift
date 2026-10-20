import CryptoKit
import Foundation
import RAProto
import Testing

@testable import RACore

// Only test keys implement this generator; production always uses CryptoKit's
// Curve25519 generator. HPKE itself and grant-context serialization are shared.
private struct FixedHPKEPublic: HPKEDiffieHellmanPublicKey {
  typealias EphemeralPrivateKey = FixedHPKEPrivate
  let key: Curve25519.KeyAgreement.PublicKey
  init<D: ContiguousBytes>(_ bytes: D, kem: HPKE.KEM) throws {
    key = try .init(bytes, kem: kem)
  }
  func hpkeRepresentation(kem: HPKE.KEM) throws -> Data { try key.hpkeRepresentation(kem: kem) }
}
private struct FixedHPKEPrivate: HPKEDiffieHellmanPrivateKeyGeneration {
  let key: Curve25519.KeyAgreement.PrivateKey
  init() { key = try! .init(rawRepresentation: Data(repeating: 99, count: 32)) }
  var publicKey: FixedHPKEPublic {
    try! .init(key.publicKey.rawRepresentation, kem: .Curve25519_HKDF_SHA256)
  }
  func sharedSecretFromKeyAgreement(with publicKeyShare: FixedHPKEPublic) throws -> SharedSecret {
    try key.sharedSecretFromKeyAgreement(with: publicKeyShare.key)
  }
}
private struct AuthorityEntry: Codable {
  var Name: String
  var State: Data
  var Proof: Data?
  var Lease: Data
}
private struct AuthorityVector: Codable {
  var Entries: [AuthorityEntry]
  var Approval: Data
  var ApprovalSignature: Data
  var RecoveryCode: String
}
@Suite struct AuthorityTests {
  typealias S = DeviceSecurity
  let now: Int64 = 1_700_000_000_000
  func fill(_ b: UInt8, _ n: Int = 32) -> Data { Data(repeating: b, count: n) }
  func key(_ b: UInt8) throws -> SigningKey { try SigningKey(seed: fill(b)) }
  func member(_ b: UInt8, _ kind: S.Kind = .client) throws -> S.Member {
    try Authority.newMember(signer: key(b), xPrivate: fill(b + 10), kind: kind)
  }
  func authority() -> Authority {
    Authority(
      seal: { member, core, hash, secret in
        try S.sealGrant(
          recipient: member, core: core, coreHash: hash, secret: secret,
          key: FixedHPKEPublic(member.x, kem: .Curve25519_HKDF_SHA256))
      }, nonce: { Data(repeating: 88, count: $0) })
  }
  private func generate(_ a: Authority) throws -> (AuthorityVector, [S.State]) {
    let admin = try member(1)
    let incoming = try member(2)
    let machine = try member(3, .machine)
    let recovered = try member(4)
    let recovery = try key(21).publicKey
    let next = try key(22).publicKey
    let final = try key(23).publicKey
    let account = deviceId(publicKey: try key(30).publicKey)
    let g = try a.genesis(
      account: account, domain: fill(50), member: admin, recoveryEd: recovery,
      secret: fill(40), signer: key(1), now: now)
    let p = try a.pair(
      parent: g, candidate: incoming, oldSecret: fill(40), newSecret: fill(41), signer: key(1),
      now: now - 10)
    let m = try a.membership(
      parent: p, members: [machine] + p.core.members.reversed(), removed: [],
      oldSecret: fill(41), newSecret: fill(42), signer: key(1), now: now + 1)
    let x = try a.rotateX(
      parent: m, target: machine.id, newXPublic: PairKey(privateKey: fill(70)).publicKey,
      oldSecret: fill(42), newSecret: fill(43), signer: key(1), now: now + 2)
    let rm = try a.remove(
      parent: x, targets: [machine.id, machine.id], oldSecret: fill(43),
      newSecret: fill(44), signer: key(1), now: now + 3)
    let v = try a.veto(
      parent: rm, newRecoveryEd: next, secret: fill(44), signer: key(1), now: now + 4)
    let h = try a.handoff(
      parent: v, incoming: incoming.id, recoveryEd: final,
      oldSecret: fill(44), newSecret: fill(45), signer: key(1), now: now + 5)
    let r = try a.recovery(
      parent: h, newMember: recovered, members: [recovered], secret: fill(46),
      newSigner: key(4), oldRecoverySeed: fill(23), newRecoveryEd: recovery, now: now + 6)
    let states = [g, p, m, x, rm, v, h, r.state]
    let names = [
      "genesis", "pair", "membership", "rotateX", "remove", "veto", "handoff", "recovery",
    ]
    var entries: [AuthorityEntry] = []
    for (i, state) in states.enumerated() {
      let lease = try S.HeadLease.sign(
        state, signer: key(i == 7 ? 4 : i == 6 ? 2 : 1),
        now: now - 100, floor: UInt64(now + 100))
      entries.append(
        AuthorityEntry(
          Name: names[i], State: try S.stateBytes(state),
          Proof: i == 7 ? try r.statement.bytes() : nil, Lease: try lease.bytes()))
    }
    let approval = try makePairApproval(
      accountID: account, pairingID: "0123456789abcdef0123456789abcdef",
      signer: key(1), candidate: machine.ed, kind: .machine, expires: now + 300_000,
      replacesDeviceID: recovered.id, nonce: fill(88, 16))
    return (
      AuthorityVector(
        Entries: entries, Approval: approval.approval, ApprovalSignature: approval.signature,
        RecoveryCode: try RecoveryCode.encode(seed: fill(21), genesis: S.stateHash(g))), states
    )
  }
  @Test func fixedBuildersMatchGoBytesAndGoVerifiesSwiftSignatures() throws {
    let (actual, states) = try generate(authority())
    let expected = try JSONDecoder().decode(
      AuthorityVector.self,
      from: Data(contentsOf: repoRoot.appendingPathComponent("ios/testdata/authority-v3.json")))
    #expect(actual.Entries.count == expected.Entries.count)
    for (a, e) in zip(actual.Entries, expected.Entries) {
      var state = try S.parseState(a.State)
      let ref = try S.parseState(e.State)
      #expect(try S.signBytes(state) == S.signBytes(ref))
      state.signature = ref.signature
      #expect(try S.stateBytes(state) == e.State)
      if let raw = a.Proof, let refRaw = e.Proof {
        var proof = try S.RecoveryStatement.parse(raw)
        let reference = try S.RecoveryStatement.parse(refRaw)
        #expect(try proof.signBytes() == reference.signBytes())
        proof.signature = reference.signature
        #expect(try proof.bytes() == refRaw)
      } else {
        #expect(a.Proof == nil && e.Proof == nil)
      }
      var lease = try S.HeadLease.parse(a.Lease)
      let reference = try S.HeadLease.parse(e.Lease)
      #expect(try lease.signBytes() == reference.signBytes())
      lease.signature = reference.signature
      #expect(try lease.bytes() == e.Lease)
    }
    #expect(actual.Approval == expected.Approval)
    #expect(actual.RecoveryCode == expected.RecoveryCode)
    try goVerify(actual)
    // Explicitly verify history rules independently of the byte oracle.
    #expect(states[5].core.contentEpoch == states[4].core.contentEpoch)
    #expect(states[5].prevSecret.isEmpty)
    #expect(try S.openPrevSecret(newSecret: fill(45), blob: states[6].prevSecret) == fill(44))
    #expect(states[7].prevSecret.isEmpty)
    let removed = try [member(1).id, member(2).id, member(3, .machine).id]
    #expect(Set(states[7].core.removed) == Set(removed))
  }
  private func goVerify(_ vector: AuthorityVector) throws {
    guard let go = GoServer.go else { throw TestFailure("Go toolchain missing") }
    let process = Process()
    process.executableURL = URL(fileURLWithPath: go)
    process.currentDirectoryURL = repoRoot
    process.arguments = ["run", "./ios/testdata/authority-oracle", "verify"]
    let input = Pipe()
    let output = Pipe()
    process.standardInput = input
    process.standardOutput = output
    process.standardError = output
    try process.run()
    try input.fileHandleForWriting.write(contentsOf: JSONEncoder().encode(vector))
    try input.fileHandleForWriting.close()
    let log = output.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()
    #expect(process.terminationStatus == 0, "\(String(decoding: log, as: UTF8.self))")
  }
  @Test func productionHPKESenderOpensInCryptoKitAndRejectsTamperedContext() throws {
    let (_, states) = try generate(Authority())
    let pin = try S.pin(for: states[0])
    for (i, state) in states.enumerated() {
      for m in state.core.members {
        let b: UInt8 =
          m.id == (try member(1).id)
          ? 1 : m.id == (try member(2).id) ? 2 : m.id == (try member(3, .machine).id) ? 3 : 4
        let x = try Curve25519.KeyAgreement.PrivateKey(
          rawRepresentation: fill(i == 3 && b == 3 ? 70 : b + 10))
        #expect(try S.openGrant(pin, state, recipient: m.id, x: x).count == 32)
      }
    }
    var tampered = states[0]
    tampered.core.issuedAt += 1
    #expect(throws: (any Error).self) {
      try S.openGrant(
        pin, tampered, recipient: tampered.core.authorityID,
        x: Curve25519.KeyAgreement.PrivateKey(rawRepresentation: fill(11)))
    }
  }
  @Test func machineAndUnauthorizedSignersCannotBecomeAuthority() throws {
    let a = authority()
    let (_, states) = try generate(authority())
    #expect(throws: (any Error).self) {
      try a.genesis(
        account: states[0].core.account, domain: fill(50), member: member(3, .machine),
        recoveryEd: key(21).publicKey, secret: fill(40), signer: key(3), now: now)
    }
    #expect(throws: (any Error).self) {
      try a.handoff(
        parent: states[2], incoming: member(3, .machine).id,
        recoveryEd: key(22).publicKey, oldSecret: fill(42), newSecret: fill(43), signer: key(1),
        now: now)
    }
    #expect(throws: (any Error).self) {
      try a.recovery(
        parent: states[2], newMember: member(5, .machine),
        members: [member(2), member(3, .machine), member(5, .machine)], secret: fill(43),
        newSigner: key(5),
        oldRecoverySeed: fill(21), newRecoveryEd: key(22).publicKey, now: now)
    }
    #expect(throws: (any Error).self) {
      try a.veto(
        parent: states[2], newRecoveryEd: key(22).publicKey, secret: fill(42), signer: key(3),
        now: now)
    }
    #expect(throws: (any Error).self) { try S.HeadLease.sign(states[6], signer: key(1), now: now) }
    #expect(throws: (any Error).self) {
      try a.remove(
        parent: states[2], targets: [member(1).id], oldSecret: fill(42), newSecret: fill(43),
        signer: key(1), now: now)
    }
    #expect(throws: (any Error).self) {
      try a.pair(
        parent: states[4], candidate: member(3, .machine), oldSecret: fill(44), newSecret: fill(45),
        signer: key(1), now: now)
    }
    #expect(throws: (any Error).self) {
      try a.veto(
        parent: states[2], newRecoveryEd: key(21).publicKey, secret: fill(42), signer: key(1),
        now: now)
    }
    #expect(throws: (any Error).self) {
      try S.HeadLease.sign(states[0], signer: key(1), now: now, floor: UInt64.max)
    }
  }
  @Test func recoveryCodesPaddingAliasesAndRenewalBoundaries() throws {
    let (_, states) = try generate(authority())
    let code = try RecoveryCode.encode(seed: fill(21), genesis: S.stateHash(states[0]))
    let decoded = try #require(
      RecoveryCode.decode(code.lowercased().replacingOccurrences(of: "-", with: " \n")))
    let prefix = try S.stateHash(states[0]).prefix(10)
    #expect(decoded.seed == fill(21) && decoded.genesis == prefix)
    let zero = try RecoveryCode.encode(seed: fill(0), genesis: fill(0))
    #expect(RecoveryCode.decode(zero.replacingOccurrences(of: "0", with: "O"))?.seed == fill(0))
    let ones = try RecoveryCode.encode(seed: fill(8), genesis: fill(8))
    #expect(RecoveryCode.decode(ones.replacingOccurrences(of: "1", with: "l"))?.seed == fill(8))
    #expect(RecoveryCode.decode(String(code.dropLast()) + "1") == nil)
    #expect(RecoveryCode.decode(code + "0") == nil)
    #expect(RecoveryCode.decode(code + "!") == nil)
    #expect(RecoveryCode.decode("") == nil)
    let lease = try S.HeadLease.sign(states[0], signer: key(1), now: now)
    #expect(Authority.needsRenewal(nil, now: now))
    #expect(!Authority.needsRenewal(lease, now: now - 1))
    #expect(!Authority.needsRenewal(lease, now: now + 48 * 60 * 60 * 1000))
    #expect(Authority.needsRenewal(lease, now: now + 48 * 60 * 60 * 1000 + 1))
    #expect(Authority.needsRenewal(lease, now: Int64(lease.notAfter)))
    var short = lease
    short.notAfter = short.issuedAt + 13 * 60 * 60 * 1000
    #expect(!Authority.needsRenewal(short, now: now + 12 * 60 * 60 * 1000 - 1))
    #expect(Authority.needsRenewal(short, now: now + 12 * 60 * 60 * 1000))
  }
}

@Suite struct AccountPublicProfileTests {
  typealias S = DeviceSecurity
  @Test func creationAndRecoveryValidateAccountAndGenesisWithoutChangingExistingRecords() throws {
    let root = Data(repeating: 31, count: 32)
    let publicKey = try Keys.derive(root: root).account.publicKey
    let signer = try SigningKey(seed: Data(repeating: 1, count: 32))
    let x = Data(repeating: 11, count: 32)
    let secret = Data(repeating: 40, count: 32)
    let member = try Authority.newMember(signer: signer, xPrivate: x, kind: .client)
    let genesis = try Authority().genesis(
      account: deviceId(publicKey: publicKey), domain: Data(repeating: 50, count: 32),
      member: member, recoveryEd: signer.publicKey, secret: secret, signer: signer, now: 1000)
    let profile = try AccountPublicProfile.created(root: root, genesis: genesis)
    #expect(try AccountPublicProfile.parse(profile.bytes()) == profile)
    let record = try S.Record(
      pin: profile.pin, states: [1: genesis], memberID: member.id,
      xPrivate: x, member: true, secret: secret)
    let rawRecord = try record.bytes()
    let recovery = try AccountPublicProfile.recovery(
      publicKey: publicKey, states: [genesis], proofs: [nil],
      genesisPrefix: profile.pin.genesis.prefix(10), local: record)
    #expect(recovery == profile)
    #expect(try record.bytes() == rawRecord)
    #expect(throws: (any Error).self) {
      try AccountPublicProfile.created(root: Data(repeating: 32, count: 32), genesis: genesis)
    }
    #expect(throws: (any Error).self) {
      try AccountPublicProfile.recovery(
        publicKey: signer.publicKey, states: [genesis], proofs: [nil],
        genesisPrefix: profile.pin.genesis.prefix(10))
    }
    #expect(throws: (any Error).self) {
      try AccountPublicProfile.recovery(
        publicKey: publicKey, states: [genesis], proofs: [nil],
        genesisPrefix: Data(repeating: 0, count: 10))
    }
    var fork = genesis
    fork.core.domain = Data(repeating: 51, count: 32)
    // Even another valid genesis cannot replace an accepted local pin.
    let other = try Authority().genesis(
      account: genesis.core.account, domain: fork.core.domain, member: member,
      recoveryEd: signer.publicKey, secret: secret, signer: signer, now: 1000)
    #expect(throws: (any Error).self) {
      try AccountPublicProfile.recovery(
        publicKey: publicKey, states: [other], proofs: [nil],
        genesisPrefix: S.stateHash(other).prefix(10), local: record)
    }
    let next = try Authority().veto(
      parent: genesis, newRecoveryEd: SigningKey(seed: Data(repeating: 21, count: 32)).publicKey,
      secret: secret, signer: signer, now: 1001)
    let accepted = try S.Record(
      pin: profile.pin, states: [1: genesis, 2: next], memberID: member.id,
      xPrivate: x, member: true, secret: secret)
    #expect(throws: (any Error).self) {
      try AccountPublicProfile.recovery(
        publicKey: publicKey, states: [genesis], proofs: [nil],
        genesisPrefix: profile.pin.genesis.prefix(10), local: accepted)
    }
    #expect(
      try AccountPublicProfile.recovery(
        publicKey: publicKey, states: [genesis, next], proofs: [nil, nil],
        genesisPrefix: profile.pin.genesis.prefix(10), local: record) == profile)
  }
}
