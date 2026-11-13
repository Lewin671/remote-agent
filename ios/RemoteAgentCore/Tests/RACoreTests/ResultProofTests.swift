import Foundation
import Testing
import RAProto
@testable import RACore
struct ResultProofTests {
 @Test func sharedSignedProofs() throws {
  let f=try loadJSON("testdata/transport/proofs.json")
  func raw(_ s:String)->Data { Data(base64Encoded:s)! }
  let state=try DeviceSecurity.parseState(raw(f.s("State")))
  let secret=f.bytes("Secret")
  func open(_ s:String)throws->(DeviceSecurity.Header,Data){
   let wire=try DeviceSecurity.fromEnvelope(PBEnvelope(serializedBytes:raw(s)))
   return try DeviceSecurity.open(wire,state:state,head:nil,live:false,now:0,secret:secret,sessionSeed:nil)
  }
  let(h,plain)=try open(f.s("Command"));let binding=DeviceSecurity.requestIdentity(h,plain)
  #expect(binding.key.hex==f.s("Key"));#expect(binding.payload.hex==f.s("Payload"))
  for v in f["Vectors"] as! [[String:Any]] {
   var result=Outcome.unknown
   do {let(_,body)=try open(v.s("Envelope"));let r=try PBCommandResult(serializedBytes:body);result=verifiedOutcome(r,key:binding.key,payload:binding.payload,outerOutcome:v.s("OuterOutcome"),outerReason:v.s("OuterReason"))} catch {}
   #expect(result.rawValue==v.s("Expected"),"\(v.s("Name"))")
  }
 }
}
