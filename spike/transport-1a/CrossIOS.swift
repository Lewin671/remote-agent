import Foundation
import WebRTC

final class CrossObserver: NSObject, RTCPeerConnectionDelegate, RTCDataChannelDelegate {
    let lock=NSLock();var candidates:[RTCIceCandidate]=[];var gathered=DispatchSemaphore(value:0)
    let opened=DispatchSemaphore(value:0),extra=DispatchSemaphore(value:0),rogue=DispatchSemaphore(value:0)
    var waiters:[String:DispatchSemaphore]=[:];var unexpected=0,received=0;var closed=false
    func peerConnection(_ p:RTCPeerConnection,didChange stateChanged:RTCSignalingState){}
    func peerConnection(_ p:RTCPeerConnection,didAdd stream:RTCMediaStream){}
    func peerConnection(_ p:RTCPeerConnection,didRemove stream:RTCMediaStream){}
    func peerConnectionShouldNegotiate(_ p:RTCPeerConnection){}
    func peerConnection(_ p:RTCPeerConnection,didChange newState:RTCIceConnectionState){}
    func peerConnection(_ p:RTCPeerConnection,didChange newState:RTCIceGatheringState){if newState == .complete {lock.lock();gathered.signal();lock.unlock()}}
    func peerConnection(_ p:RTCPeerConnection,didGenerate candidate:RTCIceCandidate){lock.lock();candidates.append(candidate);lock.unlock()}
    func peerConnection(_ p:RTCPeerConnection,didRemove candidates:[RTCIceCandidate]){}
    func peerConnection(_ p:RTCPeerConnection,didOpen channel:RTCDataChannel){lock.lock();unexpected+=1;closed=true;lock.unlock();channel.close();DispatchQueue.global().async{p.close();self.extra.signal()}}
    func dataChannelDidChangeState(_ c:RTCDataChannel){if c.readyState == .open {opened.signal()}}
    func dataChannel(_ c:RTCDataChannel,didReceiveMessageWith b:RTCDataBuffer){guard let text=String(data:b.data,encoding:.utf8)else{return};lock.lock();if c.channelId==0{received+=1};let waiter=waiters[text];lock.unlock();if text=="forbidden-business"{rogue.signal()};waiter?.signal()}
    func beginGather(){lock.lock();candidates=[];gathered=DispatchSemaphore(value:0);lock.unlock()}
    func copyCandidates()->[RTCIceCandidate]{lock.lock();defer{lock.unlock()};return candidates}
    func echo(_ channel:RTCDataChannel,_ text:String)throws{let done=DispatchSemaphore(value:0);lock.lock();waiters[text]=done;lock.unlock();guard channel.sendData(RTCDataBuffer(data:Data(text.utf8),isBinary:false))else{throw LabError.sdk};try wait(done);lock.lock();waiters.removeValue(forKey:text);lock.unlock()}
    func probe(_ channel:RTCDataChannel,_ generation:Int)->Bool{
        let text="probe-gen:\(generation):"+UUID().uuidString,done=DispatchSemaphore(value:0)
        lock.lock();waiters[text]=done;lock.unlock()
        let queue=DispatchQueue(label:"lab-probe"),timer=DispatchSource.makeTimerSource(queue:queue)
        timer.schedule(deadline:.now(),repeating:.milliseconds(50));timer.setEventHandler{if channel.readyState == .open {_ = channel.sendData(RTCDataBuffer(data:Data(text.utf8),isBinary:false))}};timer.resume()
        let result=done.wait(timeout:.now()+2.5);timer.cancel();queue.sync{};timer.setEventHandler{}
        lock.lock();waiters.removeValue(forKey:text);lock.unlock();return result == .success
    }
}
final class SDKPeer {
    let url:URL,token:String,id:String;var last="initial",status=0
    init(_ config:[String:Any],_ id:String){url=URL(string:config["url"] as! String)!;token=config["token"] as! String;self.id=id}
    func call(_ operation:String,_ fields:[String:Any]=[:],reject:Bool=false)throws->[String:Any]{
        last=operation;var body=fields;body["id"]=id;body["operation"]=operation
        var request=URLRequest(url:url);request.httpMethod="POST";request.timeoutInterval=10;request.setValue("Bearer "+token,forHTTPHeaderField:"Authorization");request.setValue("application/json",forHTTPHeaderField:"Content-Type");request.httpBody=try JSONSerialization.data(withJSONObject:body)
        let done=DispatchSemaphore(value:0);var data:Data?;var code=0;var failed=false
        let task=URLSession.shared.dataTask(with:request){d,r,e in data=d;code=(r as? HTTPURLResponse)?.statusCode ?? 0;failed=e != nil;done.signal()};task.resume()
        do{try wait(done)}catch{task.cancel();throw error};status=code;if failed{throw LabError.sdk};if reject{guard code==400 else{throw LabError.sdk};return ["rejected":true]}
        guard code==200,let data else{throw LabError.sdk};return (try JSONSerialization.jsonObject(with:data,options:.fragmentsAllowed)) as? [String:Any] ?? [:]
    }
}
func crossIOS(_ config:[String:Any],_ out:URL)throws {
    RTCInitializeSSL();RTCSetMinDebugLogLevel(.none);defer{RTCCleanupSSL()};let factory=RTCPeerConnectionFactory();var results:[[String:Any]]=[]
    for mode in ["client-offer","peer-offer","dcep","undeclared"]{
        let start=ProcessInfo.processInfo.systemUptime,observer=CrossObserver(),remote=SDKPeer(config,"ios-"+mode)
        let cfg=RTCConfiguration();cfg.iceServers=[];cfg.sdpSemantics = .unifiedPlan
        guard let pc=factory.peerConnection(with:cfg,constraints:RTCMediaConstraints(mandatoryConstraints:nil,optionalConstraints:nil),delegate:observer)else{throw LabError.sdk}
        var channels:[RTCDataChannel]=[]
        for id in 0...1{let c=RTCDataChannelConfiguration();c.channelId=Int32(id);c.isNegotiated=true;c.isOrdered=id==0;if id==1{c.maxRetransmits=0};guard let dc=pc.dataChannel(forLabel:"lab-\(id)",configuration:c)else{throw LabError.sdk};dc.delegate=observer;channels.append(dc)}
        func local(_ answer:Bool)throws->RTCSessionDescription{let d=try description(pc,answer:answer);try SDPCheck.validate(Data(d.sdp.utf8));observer.beginGather();try install(pc,d,local:true);try wait(observer.gathered);return d}
        func ufrag(_ s:String)->String{s.components(separatedBy:"\r\n").first{$0.hasPrefix("a=ice-ufrag:")}!.dropFirst(12).description}
        func candidates()->[[String:Any]]{observer.copyCandidates().map{["candidate":$0.sdp,"sdpMid":$0.sdpMid ?? "0","sdpMLineIndex":$0.sdpMLineIndex]}}
        func add(_ list:[[String:Any]])throws{for item in list{let c=RTCIceCandidate(sdp:item["candidate"] as! String,sdpMLineIndex:Int32(item["sdpMLineIndex"] as? Int ?? 0),sdpMid:item["sdpMid"] as? String);let done=DispatchSemaphore(value:0);var bad=false;pc.add(c){e in bad=e != nil;done.signal()};try wait(done);if bad{throw LabError.sdk}}}
        do{
            var gen=1;var answer:[String:Any];var ours:RTCSessionDescription
            if mode=="client-offer"{ours=try local(false);answer=try remote.call("answer-offer",["sdp":Data(ours.sdp.utf8).base64EncodedString()]);let raw=Data(base64Encoded:answer["sdp"] as! String)!;try SDPCheck.validate(raw);try install(pc,RTCSessionDescription(type:.answer,sdp:String(data:raw,encoding:.utf8)!),local:false)}
            else{answer=try remote.call("offer");let raw=Data(base64Encoded:answer["sdp"] as! String)!;try SDPCheck.validate(raw);try install(pc,RTCSessionDescription(type:.offer,sdp:String(data:raw,encoding:.utf8)!),local:false);ours=try local(true);_ = try remote.call("install-answer",["sdp":Data(ours.sdp.utf8).base64EncodedString(),"generation":gen])}
            let oldUfrag=ufrag(ours.sdp),before=try remote.call("stats")
            _ = try remote.call("candidates",["generation":2,"ufrag":oldUfrag,"candidates":candidates()],reject:true)
            _ = try remote.call("candidates",["generation":1,"ufrag":"wrong","candidates":candidates()],reject:true)
            let after=try remote.call("stats");guard (before["add_calls"] as! Int)==(after["add_calls"] as! Int)else{throw LabError.sdk}
            _ = try remote.call("candidates",["generation":gen,"ufrag":oldUfrag,"candidates":candidates()]);try add(answer["candidates"] as! [[String:Any]])
            try wait(observer.opened);try wait(observer.opened);let initialMs=(ProcessInfo.processInfo.systemUptime-start)*1000
            try observer.echo(channels[0],"ios-cross-"+mode);try observer.echo(channels[1],"probe:ios-"+mode)
            let stats0=try remote.call("stats");_ = channels[1].sendData(RTCDataBuffer(data:Data("business-on-probe".utf8),isBinary:false));try observer.echo(channels[0],"barrier-"+mode)
            _ = observer.rogue.wait(timeout:.now()+0.1);let stats1=try remote.call("stats");guard (stats1["probe_business_rejected"] as! Int)==(stats0["probe_business_rejected"] as! Int)+1 else{throw LabError.sdk}
            var restartMs:Double?=nil,probeCompleted:Bool?=nil
            if mode=="peer-offer"{let restartStart=ProcessInfo.processInfo.systemUptime;answer=try remote.call("restart-offer");gen=answer["generation"] as! Int;let raw=Data(base64Encoded:answer["sdp"] as! String)!;try install(pc,RTCSessionDescription(type:.offer,sdp:String(data:raw,encoding:.utf8)!),local:false);ours=try local(true);let newUfrag=ufrag(ours.sdp);guard newUfrag != oldUfrag else{throw LabError.sdk};_ = try remote.call("install-answer",["sdp":Data(ours.sdp.utf8).base64EncodedString(),"generation":gen]);_ = try remote.call("candidates",["generation":gen,"ufrag":newUfrag,"candidates":candidates()]);try add(answer["candidates"] as! [[String:Any]]);remote.last="generation-probe";probeCompleted=observer.probe(channels[1],gen);remote.last="restart-echo";try observer.echo(channels[0],"restart-ios");restartMs=(ProcessInfo.processInfo.systemUptime-restartStart)*1000}
            if mode=="dcep"{_ = try remote.call("rogue-dcep");try wait(observer.extra);guard observer.unexpected==1&&observer.closed else{throw LabError.sdk}}
            if mode=="undeclared"{_ = try remote.call("rogue-negotiated");try observer.echo(channels[0],"undeclared-barrier");guard observer.rogue.wait(timeout:.now()+0.15) == .timedOut && observer.unexpected==0 else{throw LabError.sdk}}
            var result:[String:Any]=["mode":mode,"elapsed_ms":(ProcessInfo.processInfo.systemUptime-start)*1000,"initial_ready_ms":initialMs,"extra_dcep_rejected":observer.unexpected,"candidate_negative_before_sdk":true,"probe_business_rejected":true,"original_passed_to_sdk":true]
            if let restartMs{result["restart_ready_ms"]=restartMs};if let probeCompleted{result["fresh_probe_within_2500ms"]=probeCompleted};results.append(result)
        }catch{let e=error as NSError;let detail:[String:Any]=["mode":mode,"error_domain":e.domain,"error_code":e.code,"last_operation":remote.last,"http_status":remote.status];try? JSONSerialization.data(withJSONObject:detail,options:.sortedKeys).write(to:out.appendingPathComponent("cross-stage.json"));channels.forEach{$0.delegate=nil;$0.close()};pc.close();_ = try? remote.call("close");throw error}
        channels.forEach{$0.delegate=nil;$0.close()};pc.close();_ = try remote.call("close")
        try JSONSerialization.data(withJSONObject:results,options:[.prettyPrinted,.sortedKeys]).write(to:out.appendingPathComponent("cross-ios.json"))
    }
    try JSONSerialization.data(withJSONObject:results,options:[.prettyPrinted,.sortedKeys]).write(to:out.appendingPathComponent("cross-ios.json"))
}
