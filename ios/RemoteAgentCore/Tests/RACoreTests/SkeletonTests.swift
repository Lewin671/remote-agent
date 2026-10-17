import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct SkeletonTests {
  @Test func allTwentySkeletonKindsHaveAVisibleNonemptyRow() throws {
    let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
      .deletingLastPathComponent()
      .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    let data = try Data(
      contentsOf: root.appendingPathComponent("testdata/projection/skeletons.json"))
    let corpus = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
    #expect(corpus["message"] as? String == skeletonMessage)
    #expect(corpus["note"] as? String == skeletonNote)
    let events = try (corpus["events"] as! [[String: Any]]).map {
      try PBEvent(jsonUTF8Data: JSONSerialization.data(withJSONObject: $0))
    }
    #expect(events.count == 20)
    #expect(events.allSatisfy(isSkeleton))
    for agent in [PBAgent.codex, .claude] {
      let rows = buildChat(events, d: derive(events, agent: agent), machine: "test")
      #expect(rows.count == 20)
      #expect(
        rows.allSatisfy {
          if case .agent(let x) = $0 { return x.text == skeletonMessage }
          return false
        })
    }
    #expect(isSkeleton(events[8].approvalRequested))
    #expect(isSkeleton(events[10].questionAsked))
    #expect(isSkeleton(events[14].planProposed.text))
  }
}
