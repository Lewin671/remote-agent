import Foundation
import Testing
@testable import RACore
import RAProto
import SwiftProtobuf
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
 @Test func commandEvidence() throws {
  let fixture=try loadJSON("testdata/transport/outcomes.json")
  for v in fixture["commands"] as! [[String:Any]] {
   let kind=v.s("kind")
   let body:[String:Any] = kind == "schedule" ? ["operation":v.s("operation")] : [:]
   let object:[String:Any] = kind.isEmpty ? [:] : [kind:body]
   let cmd=try PBCommand(jsonUTF8Data:JSONSerialization.data(withJSONObject:object))
   let readOnly=cmd.isReadOnlyCommand
   #expect(readOnly == v["readOnly"] as! Bool)
   for code in ["canceled","deadline_exceeded","aborted"] {
    #expect(CommandError(code,"fixture",fromDaemon:true,readOnly:readOnly).unknown == !readOnly)
   }
  }
 }

}
