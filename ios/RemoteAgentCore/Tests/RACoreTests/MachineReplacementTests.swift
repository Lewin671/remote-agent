import CryptoKit
import Foundation
import RAProto
import Testing

@testable import RACore

@Test func sharedMachineReplacementLineage() async throws {
  let v = try loadJSON("internal/e2e/testdata/machine-replacement-vectors.json")
  let states = try (v["states"] as! [String]).map {
    try DeviceSecurity.parseState(Data(base64Encoded: $0)!)
  }
  let pin = try DeviceSecurity.pin(for: states[0])
  let target = v.s("target")
  let old = v.s("old")
  let x = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: Data(base64Encoded: v.s("x"))!)
  let secret = try DeviceSecurity.openGrant(pin, states.last!, recipient: target, x: x)
  let record = try DeviceSecurity.Record(
    pin: pin, states: Dictionary(uniqueKeysWithValues: states.map { ($0.core.epoch, $0) }),
    memberID: target, xPrivate: Data(base64Encoded: v.s("x"))!, member: true, secret: secret)
  let client = DeviceSecurityClient(
    store: MemorySecurityStore(record), memberID: target,
    signingKey: try SigningKey(seed: Data(repeating: 82, count: 32)),
    fetchStates: { _ in throw DeviceSecurity.Failure.untrusted })
  func proofList(_ key: String) -> [PBMachineReplacementProof] {
    (v[key] as! [[String: Any]]).map { p in
      var out = PBMachineReplacementProof()
      out.approval = Data(base64Encoded: p["approval"] as! String)!
      out.signature = Data(base64Encoded: p["signature"] as! String)!
      return out
    }
  }
  let proofs = proofList("proofs")
  let skewOrigins = try await client.replacementOrigins(
    target: target, proofs: proofList("clock_skew_proofs"))
  #expect(skewOrigins.contains(old))
  await #expect(throws: (any Error).self) {
    try await client.replacementOrigins(target: target, proofs: proofList("expired_proofs"))
  }
  let origins = try await client.replacementOrigins(target: target, proofs: proofs)
  #expect(origins.contains(old))
  let missing = try await client.replacementOrigins(target: target, proofs: [])
  #expect(!missing.contains(old))
  var bad = proofs
  bad[0].signature[0] ^= 1
  await #expect(throws: (any Error).self) {
    try await client.replacementOrigins(target: target, proofs: bad)
  }
  await #expect(throws: (any Error).self) {
    try await client.replacementOrigins(target: old, proofs: proofs)
  }
  await client.close()
}
