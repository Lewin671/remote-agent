import Foundation
import Testing
@testable import RACore
import RAProto
struct OutcomeTests {
 @Test func sharedVectors() throws {
  let fixture = try loadJSON("testdata/transport/outcomes.json")
  let vectors = fixture["vectors"] as! [[String: Any]]
  #expect(fixture["protocol"] as? Int == 14)
  #expect(vectors.count == 504)
  for v in vectors {
   let result = classifyOutcome(source: v.s("source"), code: v.s("code"), signed: v["signed"] as! Bool, prior: v["prior"] as! Bool, readOnly: v["readOnly"] as! Bool)
   #expect(result.rawValue == v.s("outcome"))
   if v.s("source") == "local" || (v.s("source") == "daemon" && v["signed"] as! Bool) {
    let error = CommandError(v.s("code"), "fixture", fromDaemon: v.s("source") == "daemon", readOnly: v["readOnly"] as! Bool)
    let attempt = Attempt()
    _ = try attempt.next { PreparedCommand(machineId: "machine", sessionId: "", requestId: "fixture-request", deadlineMs: 1000, command: PBCommand()) }
    if v["prior"] as! Bool { attempt.settle(CommandError(CommandError.unknownOutcome, "fixture")) }
    attempt.settle(error)
    #expect(attempt.frozen == (v.s("outcome") == "unknown"))
   }
  }
 }
}
