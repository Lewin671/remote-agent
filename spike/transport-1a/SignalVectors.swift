import Foundation
@main struct SignalVectors {
 static func main()throws {
  precondition(ProcessInfo.processInfo.environment["REMOTE_AGENT_HOME"]?.hasPrefix("/tmp/")==true)
  var gates:[Signal]=[],cases=0,assertions=0;var batch:CandidateBatcher?
  for row in try String(contentsOfFile:CommandLine.arguments[1],encoding:.utf8).split(separator:"\n") {
   let p=row.split(separator:"\t",omittingEmptySubsequences:false).map(String.init)
   switch p[0]{
   case "CASE":let a=try Wire.Identity(p[2],UInt64(p[3])!),b=try Wire.Identity(p[4],UInt64(p[5])!),conn=Array(Wire.hash(Wire.utf8("signal/"+p[1])).prefix(16));gates=[Signal(a,b,conn,1),Signal(b,a,conn,1)];cases+=1
   case "BATCH_CASE":batch=CandidateBatcher(UInt64(p[2])!);cases+=1
   case "BATCH_ADD":precondition(batch!.add(Wire.unhex(p[3]),UInt64(p[2])!,UInt64(p[1])!)==(p[4]=="1"));assertions+=1
   case "BATCH_FLUSH":let groups=batch!.flush(UInt64(p[1])!,p[2]=="1");precondition(groups.map{String($0.count)}.joined(separator:",")==p[3] && Wire.hex(groups.flatMap{$0}.flatMap{$0})==p[4]);assertions+=1
   case "POLICY":let request=try Wire.open(Wire.unhex(p[3]),gates[0].selfID,gates[1].selfID,1700000000000),reply=try Wire.open(Wire.unhex(p[4]),gates[1].selfID,gates[0].selfID,1700000000000);var ok=true;do{try Wire.validatePolicyReply(request,reply)}catch{ok=false};precondition(ok==(p[2]=="1"),p[1]);assertions+=1
   case "CHALLENGE":gates[Int(p[1])!].challengeHash=Wire.unhex(p[2])
   case "RX":var ok=false;do{_ = try gates[Int(p[1])!].receive(Wire.unhex(p[4]),1700000000000+UInt64(p[2])!);ok=true}catch{};precondition(ok==(p[3]=="1"),"RX \(cases)");assertions+=1
   case "LOCAL":var ok=false;do{try gates[Int(p[1])!].setLocal(Wire.unhex(p[5]),p[3]=="1",UInt64(p[2])!);ok=true}catch{};precondition(ok==(p[4]=="1"),"LOCAL \(cases)");assertions+=1
   case "DTLS":gates[Int(p[1])!].dtls=true
   case "NEXT":try gates[Int(p[1])!].nextGeneration(UInt64(p[2])!)
   case "TICK":_ = gates[Int(p[1])!].tick(1700000000000+UInt64(p[2])!)
   case "EXPECT":let s=gates[Int(p[1])!];let values:[String:UInt64]=["stale":UInt64(s.staleChallenges),"boot":s.bootReady ? 1:0,"gap_ms":s.gapDeadline==0 ? 0:s.gapDeadline-1700000000000,"sdk":UInt64(s.sdkActions),"duplicates":UInt64(s.duplicates),"conflicts":UInt64(s.conflicts),"unsupported":UInt64(s.unsupported),"pending":UInt64(s.pending.count),"ready":s.ready ? 1:0,"closed":s.closed ? 1:0,"generation":s.generation,"next":s.next];precondition(values[p[2]]==UInt64(p[3])!,"\(p[2]) \(cases)");assertions+=1
   default:preconditionFailure(p[0])
   }
  };print("Swift signal scenarios: \(cases), assertions: \(assertions)")
 }
}
