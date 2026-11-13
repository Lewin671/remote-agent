import Foundation
import Testing
@testable import RACore
import RAProto
import SwiftProtobuf
struct OutcomeTests {
 @Test func resultNoticesNeverAutomaticallyReplayUploads() {
  for error in [CommandError("disk_full","fixture",fromDaemon:true,outcome:.executed,resultAvailable:false),CommandError("storage_exhausted","fixture",fromDaemon:true,outcome:.conflict,conflictKind:"identity",resultAvailable:false)] {
   #expect(error.message == "fixture");#expect(!uploadRetryAllowed(error))
  }
  for error in [CommandError("network",L10nMessage.errorsExecutedResultUnavailable(),fromDaemon:true,outcome:.executed,resultAvailable:false),CommandError("network",L10nMessage.errorsRequestIdentityConflict(),fromDaemon:true,outcome:.conflict,conflictKind:"identity",resultAvailable:false)] {
   #expect(error.unknown);#expect(error.needsResultRefresh);#expect(!uploadRetryAllowed(error))
  }
 }
 @Test func sharedVectors() throws {
  let fixture = try loadJSON("testdata/transport/outcomes.json")
  let vectors = fixture["vectors"] as! [[String: Any]]
  #expect(fixture["protocol"] as? Int == 15)
  #expect(vectors.count == 3432)
  for v in vectors {
   let result = classifyOutcome(source: v.s("source"), code: v.s("code"), signed: v["signed"] as! Bool, prior: v["prior"] as! Bool, readOnly: v["readOnly"] as! Bool, declared:v.s("declared"),reason:v.s("reason"),conflictKind:v.s("conflictKind"),dispatched:v["dispatched"] as! Bool,notForwarded:v["notForwarded"] as! Bool,attemptBound:v["attemptBound"] as! Bool,admitted:v["admitted"] as! Bool)
   #expect(result.rawValue == v.s("outcome"))
   let error=CommandError(v.s("code"),"fixture",fromDaemon:v.s("source")=="daemon" && v["signed"] as! Bool,outcome:result,conflictKind:result == .conflict ? v.s("conflictKind") : "",resultAvailable:v["resultAvailable"] as! Bool)
   let frozen=result == .unknown || result == .conflict && v.s("conflictKind")=="identity" || result == .executed && !(v["resultAvailable"] as! Bool)
   #expect(uncertain(error)==frozen)
   let attempt=Attempt()
   let first=try attempt.next {PreparedCommand(machineId:"machine",sessionId:"",requestId:"fixture-request",deadlineMs:1000,command:PBCommand())}
   if v["prior"] as! Bool {attempt.settle(CommandError(CommandError.unknownOutcome,"fixture",outcome:.unknown))}
   attempt.settle(error);#expect(attempt.frozen==frozen)
   if attempt.frozen && !attempt.expired {#expect(try attempt.next {throw TestFailure("unknown retry rebuilt")} === first)}

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
    #expect(CommandError(code,"fixture",fromDaemon:true,readOnly:readOnly,outcome:.executed).unknown == false)
   }
  }
 }

}
