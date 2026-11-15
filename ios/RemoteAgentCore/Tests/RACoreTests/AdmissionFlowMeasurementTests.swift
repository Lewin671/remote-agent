import Foundation
import Testing
import RAProto
@testable import RACore

struct AdmissionFlowMeasurementTests {
 @Test(.enabled(if: ProcessInfo.processInfo.environment["RA_1B_FLOW_ROOT"] != nil))
 func fullSnapshotApprovalUnderDaemonSaturation() async throws {
  let path=try #require(ProcessInfo.processInfo.environment["RA_1B_FLOW_ROOT"])
  let root=URL(fileURLWithPath:path).resolvingSymlinksInPath()
  #expect(root.path.hasPrefix("/private/tmp/") || root.path.hasPrefix("/tmp/"))
  let config=try #require(JSONSerialization.jsonObject(with:Data(contentsOf:root.appendingPathComponent("bootstrap.json"))) as? [String:Any])
  let server=try #require(config["server"] as? String)
  #expect(URL(string:server)?.host=="127.0.0.1")
  let record=try TestSecurity.record(JSONSerialization.data(withJSONObject:config["record"]!))
  let seed=try #require(Data(base64Encoded:config["seed"] as! String))
  let oldRoot=try #require(Data(base64Encoded:config["root"] as! String))
  let account=Account(server:server,root:oldRoot,deviceSeed:seed,deviceId:record.memberID,accountId:record.pin.account)
  let remote=try Remote(account:account,http:HTTP(),securityStorage:MemorySecurityStore(record))
  let controlURL=try #require(URL(string:config["control"] as! String));let token=config["controlToken"] as! String
  func control(_ action:String)async throws->[String:Any]{
   var request=URLRequest(url:controlURL);request.httpMethod="POST";request.setValue("application/json",forHTTPHeaderField:"Content-Type");request.setValue(token,forHTTPHeaderField:"X-1b-Measure")
   request.httpBody=try JSONSerialization.data(withJSONObject:["action":action,"client":"ios"])
   let (data,response)=try await URLSession.shared.data(for:request)
   #expect((response as? HTTPURLResponse)?.statusCode==200)
   return try #require(JSONSerialization.jsonObject(with:data) as? [String:Any])
  }
  let sid="flow-ios"
  func approval(_ i:Int)->PBCommand {var command=PBCommand();command.resolveApproval.sessionID=sid;command.resolveApproval.approvalID=String(format:"%@-approval-%03d",sid,i);command.resolveApproval.allow=true;return command}
  var loaded=false
  var follow:Task<Void,Never>?
  do {
   _=try await control("reset")
   remote.start()
   _=try await withTimeout(10000,sleep:realSleep){await remote.state.first{ $0.conn == .online && $0.sessions[sid] != nil }}
   let feed=remote.events(sid)
   let stream=feed.updates();follow=Task{for await _ in stream{if Task.isCancelled{break}}}
   _=try await withTimeout(10000,sleep:realSleep){await feed.first{$0.live?.session.title.hasPrefix("owner-read-")==true}}
   for i in 0..<20{try await remote.command(sid,approval(i))}
   let load=try await control("start");loaded=true
   #expect(load["normal"] as? Int==4);#expect(load["bulk"] as? Int==2)
   var timings:[Double]=[];var completed=0;var failed=0
   for i in 20..<120{
    let start=ContinuousClock.now;let command=approval(i)
    do{try await withTimeout(1500,sleep:realSleep){try await remote.command(sid,command)};completed+=1}catch{failed+=1}
    let duration=start.duration(to:.now);timings.append(Double(duration.components.seconds)*1000+Double(duration.components.attoseconds)/1e15)
    if failed>0{break}
   }
   let counters=try await control("stop");loaded=false;timings.sort()
   func q(_ p:Double)->Double{timings[max(0,Int(ceil(Double(timings.count)*p))-1)]}
   var result=counters;result["client"]="ios";result["attempts"]=timings.count;result["warmup"]=20;result["completed"]=completed;result["failed"]=failed;result["completed_fraction"]=Double(completed)/Double(timings.count);result["full_flow_ms"]=["p50":q(0.5),"p95":q(0.95),"p99":q(0.99)]
   try JSONSerialization.data(withJSONObject:result,options:.prettyPrinted).write(to:root.appendingPathComponent("ios-flow.json"))
   #expect(completed==100);#expect(failed==0);#expect(counters["normal"] as? Int==4);#expect(counters["bulk"] as? Int==2)
   #expect(counters["input"] as? Int==1);#expect(counters["input_overdue"] as? Bool==true);#expect(counters["flood_workers_before_release"] as? Int==4);#expect((counters["flood_calls"] as? Int ?? 0)>0)
   #expect((counters["snapshot_calls"] as? Int ?? 0)>=100);#expect(counters["approval_calls"] as? Int==100);#expect(q(0.99)<=100)
  }catch{if loaded{_=try? await control("stop")};follow?.cancel();remote.close();throw error}
  follow?.cancel();remote.close()
 }
}
