import Foundation
import Testing

@testable import RACore

struct HeadLeaseTests {
  @Test func sharedIndependentLeaseCertificates() throws {
    let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
      .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
      .deletingLastPathComponent()
    let v =
      try JSONSerialization.jsonObject(
        with: Data(
          contentsOf: root.appendingPathComponent("internal/deviceauth/testdata/domain-v3.json")))
      as! [String: Any]
    let state = try DeviceSecurity.parseState(
      Data(base64Encoded: (v["added"] as! [String: Any])["state"] as! String)!)
    let first = try DeviceSecurity.HeadLease.parse(Data(base64Encoded: v["lease"] as! String)!)
    let renewed = try DeviceSecurity.HeadLease.parse(
      Data(base64Encoded: v["renewed_lease"] as! String)!)
    try first.verify(state)
    try renewed.verify(state)
    #expect(first.head == renewed.head && renewed.issuedAt > first.issuedAt)
    #expect(state.core.epoch == 2)
    func stateField(_ key: String) throws -> DeviceSecurity.State {
      try DeviceSecurity.parseState(
        Data(base64Encoded: (v[key] as! [String: Any])["state"] as! String)!)
    }
    let parent = try stateField("handoff_parent")
    let handoff = try stateField("handoff")
    try DeviceSecurity.verifyTransition(parent: parent, next: handoff)
    try DeviceSecurity.HeadLease.parse(Data(base64Encoded: v["handoff_lease"] as! String)!).verify(
      handoff)
    #expect(handoff.core.member(parent.core.authorityID) == nil && handoff.prevSecret.count == 60)
    #expect(throws: DeviceSecurity.Failure.lease) { try first.valid(state, now: first.notAfter) }
    var overlong = first
    overlong.notAfter = first.issuedAt + DeviceSecurity.lease + 1
    #expect(throws: DeviceSecurity.Failure.invalid) { try overlong.bytes() }
  }
}
