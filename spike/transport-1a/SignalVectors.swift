import Foundation
@main struct SignalVectors {
 static func main()throws {
  precondition(ProcessInfo.processInfo.environment["REMOTE_AGENT_HOME"]?.hasPrefix("/tmp/")==true)
  var gates:[Signal]=[],cases=0,assertions=0;var batch:CandidateBatcher?;var sender:Sender?;var queuedRaw:Wire.Bytes=[]
  for row in try String(contentsOfFile:CommandLine.arguments[1],encoding:.utf8).split(separator:"\n") {
   let p=row.split(separator:"\t",omittingEmptySubsequences:false).map(String.init)
   switch p[0]{
   case "CASE":let a=try Wire.Identity(p[2],UInt64(p[3])!),b=try Wire.Identity(p[4],UInt64(p[5])!),conn=Array(Wire.hash(Wire.utf8("signal/"+p[1])).prefix(16));gates=[Signal(a,b,conn,UInt64(p[6])!,1700000000000),Signal(b,a,conn,UInt64(p[6])!,1700000000000)];cases+=1
   case "BATCH_CASE":batch=CandidateBatcher(UInt64(p[2])!,UInt64(p[3])!,UInt64(p[4])!);cases+=1
   case "BATCH_ADD":precondition(batch!.add(Wire.unhex(p[3]),UInt64(p[2])!,UInt64(p[1])!,1)==(p[4]=="1"));assertions+=1
   case "BATCH_FLUSH":let groups=batch!.flush(UInt64(p[1])!,p[2]=="1",2,1);precondition(groups.map{String($0.count)}.joined(separator:",")==p[3] && Wire.hex(groups.flatMap{$0}.flatMap{$0})==p[4]);assertions+=1
   case "DISPATCH":precondition(gates[Int(p[1])!].dispatch(Wire.unhex(p[4]),1700000000000+UInt64(p[2])!)==(p[3]=="1"));assertions+=1
   case "FORGET":gates[Int(p[1])!].forgetPeer()
   case "QUEUE":queuedRaw=Wire.unhex(p[2]);_ = try Wire.open(queuedRaw,gates[Int(p[1])!].peer,gates[Int(p[1])!].selfID,1700000000000)
   case "DELIVER":var ok=true;do{_ = try gates[Int(p[1])!].receive(queuedRaw,1700000000000+UInt64(p[2])!)}catch{ok=false};precondition(ok==(p[3]=="1"));assertions+=1
   case "SENDER_CASE":sender=Sender(UInt64(p[2])!);cases+=1
   case "SEND_ADD":precondition(sender!.enqueue(Wire.unhex(p[4]),UInt64(p[1])!,UInt64(p[2])!,UInt64(p[3])!)==(p[5]=="1"));assertions+=1
   case "SEND_TICK":precondition(sender!.tick(UInt64(p[1])!).count==Int(p[2])!);assertions+=1
   case "SEND_REBIND":sender!.rebind(UInt64(p[1])!)
   case "SEND_LIMIT":sender!.limited(Wire.hash(Wire.unhex(p[3])),UInt64(p[1])!,UInt64(p[2])!)
   case "SEND_ADMIT":precondition(sender!.admit(Wire.unhex(p[2]),UInt64(p[1])!)==(p[3]=="1"));assertions+=1
   case "SEND_PENDING":precondition(sender!.pending.count==Int(p[1])!);assertions+=1
   case "BATCH_BOUND_FLUSH":precondition(batch!.flush(UInt64(p[1])!,p[2]=="1",UInt64(p[3])!,UInt64(p[4])!).isEmpty && batch!.pending.isEmpty);assertions+=1
   case "POLICY":let request=try Wire.open(Wire.unhex(p[3]),gates[0].selfID,gates[1].selfID,1700000000000),reply=try Wire.open(Wire.unhex(p[4]),gates[1].selfID,gates[0].selfID,1700000000000);var ok=true;do{try Wire.validatePolicyReply(request,reply,1700000000000+UInt64(p[5])!)}catch{ok=false};precondition(ok==(p[2]=="1"),p[1]);assertions+=1
   case "RECORD":let who=Int(p[2])!;_ = gates[who].tick(1700000000000+UInt64(p[3])!);let record=try Wire.open(Wire.unhex(p[4]),gates[who].selfID,gates[1-who].selfID,1700000000000);var ok=true;do{if p[1]=="probe"{try gates[who].rememberProbe(record)}else{try gates[who].rememberChallenge(record)}}catch{ok=false};precondition(ok==(p[5]=="1"));assertions+=1
   case "PROBE","CHALLENGE":let which=Int(p[1])!;let sent=try Wire.open(Wire.unhex(p[2]),gates[which].selfID,gates[1-which].selfID,1700000000000);if p[0]=="PROBE"{try gates[which].rememberProbe(sent)}else{try gates[which].rememberChallenge(sent)}
   case "RX":var ok=false;do{_ = try gates[Int(p[1])!].receive(Wire.unhex(p[4]),1700000000000+UInt64(p[2])!);ok=true}catch{};precondition(ok==(p[3]=="1"),"RX \(cases)");assertions+=1
   case "LOCAL":var ok=false;do{try gates[Int(p[1])!].setLocal(Wire.unhex(p[5]),p[3]=="1",UInt64(p[2])!);ok=true}catch{};precondition(ok==(p[4]=="1"),"LOCAL \(cases)");assertions+=1
   case "DTLS":gates[Int(p[1])!].dtls=true
   case "NEXT":try gates[Int(p[1])!].nextGeneration(UInt64(p[2])!)
   case "TICK":_ = gates[Int(p[1])!].tick(1700000000000+UInt64(p[2])!)
   case "EXPECT":let s=gates[Int(p[1])!];let values:[String:UInt64]=["dispatches":UInt64(s.dispatches),"peer_known":s.peer.boot.count==32 ? 1:0,"stale":UInt64(s.staleChallenges),"boot":s.bootReady ? 1:0,"gap_ms":s.gapDeadline==0 ? 0:s.gapDeadline-1700000000000,"sdk":UInt64(s.sdkActions),"duplicates":UInt64(s.duplicates),"conflicts":UInt64(s.conflicts),"unsupported":UInt64(s.unsupported),"pending":UInt64(s.pending.count),"ready":s.ready ? 1:0,"closed":s.closed ? 1:0,"generation":s.generation,"next":s.next];precondition(values[p[2]]==UInt64(p[3])!,"\(p[2]) \(cases)");assertions+=1
   default:preconditionFailure(p[0])
   }
  };print("Swift signal scenarios: \(cases), assertions: \(assertions)")
 }
}
