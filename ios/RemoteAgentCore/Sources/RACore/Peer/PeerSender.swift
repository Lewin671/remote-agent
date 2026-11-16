import Foundation
final class PeerSender {
 struct Pending {var raw,hash:PeerWire.Bytes;var first,notBefore,expiry,binding:UInt64;var attempts:Int}
 var leaseUntil:UInt64;var membersAuthorized=true;let deadline:UInt64;var pending:[String:Pending]=[:],timedOut=false,frames=0,bytes=0,failures=0;var retryAt:UInt64=0,networkGeneration:UInt64=0,binding:UInt64=1,clock:UInt64=0
 init(_ deadline:UInt64){self.deadline=deadline;leaseUntil=deadline}
 func enqueue(_ raw:PeerWire.Bytes,_ now:UInt64,_ expiry:UInt64,_ binding:UInt64)->Bool{clock=max(clock,now);let now=clock;if !authorized || timedOut || binding != self.binding || now>=expiry || now>=deadline || pending.count>=32{return false};let hash=PeerWire.hash(raw),key=PeerWire.hex(hash);if pending[key] != nil{return true};pending[key]=Pending(raw:raw,hash:hash,first:now,notBefore:0,expiry:min(expiry,deadline),binding:binding,attempts:0);return true}
 func tick(_ now:UInt64)->[PeerWire.Bytes]{clock=max(clock,now);let now=clock;if now>=deadline{pending=[:];timedOut=true;return []};var out:[PeerWire.Bytes]=[];for key in pending.keys.sorted(){var p=pending[key]!;if !authorized || now>=p.expiry || p.binding != binding{pending.removeValue(forKey:key);continue};if p.attempts==3{continue};let due=max(p.first+[UInt64(0),500,1500][p.attempts],p.notBefore);if now>=due{precondition(PeerWire.hash(p.raw)==p.hash);out.append(p.raw);p.attempts+=1;pending[key]=p;frames+=1;bytes+=p.raw.count}};return out}
 func admit(_ raw:PeerWire.Bytes,_ now:UInt64)->Bool{clock=max(clock,now);guard let p=pending[PeerWire.hex(PeerWire.hash(raw))]else{return false};return authorized && !timedOut && p.binding==binding && clock<p.expiry && clock<deadline}
 var authorized:Bool{membersAuthorized && clock<leaseUntil}
 func setAuthorization(_ until:UInt64,_ members:Bool,_ revision:UInt64){leaseUntil=until;membersAuthorized=members;if revision != binding{rebind(revision)}}
 func ack(_ hash:PeerWire.Bytes)->Bool{pending.removeValue(forKey:PeerWire.hex(hash)) != nil}
 func limited(_ hash:PeerWire.Bytes,_ now:UInt64,_ delay:UInt64){let key=PeerWire.hex(hash);if var p=pending[key]{p.notBefore=max(p.notBefore,(now>=deadline || delay>deadline-now) ? deadline:now+delay);pending[key]=p}}
 func backoff(_ now:UInt64,_ fraction:Double)->UInt64{let cap=failures<5 ? UInt64(1000)<<failures:30000;failures+=1;retryAt=now+UInt64(Double(min(30000,cap))*fraction);return retryAt}
 func rebind(_ value:UInt64){binding=value;pending=[:]}
 func networkChanged(_ g:UInt64){networkGeneration=max(networkGeneration,g)}
}

final class PeerCandidateBatcher {
 let generation,deadline,binding:UInt64;var due:UInt64=0;var pending:[PeerWire.Bytes]=[],count=0,batches=0,ended=false
 init(_ g:UInt64,_ deadline:UInt64,_ binding:UInt64){generation=g;self.deadline=deadline;self.binding=binding}
 func add(_ raw:PeerWire.Bytes,_ g:UInt64,_ now:UInt64,_ binding:UInt64)->Bool{if now>=deadline{ended=true;pending=[];due=0};if binding != self.binding || ended || g != generation || count>=64 || raw.count>1024{return false};if pending.isEmpty{due=now+50};pending.append(raw);count+=1;return true}
 func flush(_ now:UInt64,_ end:Bool,_ g:UInt64,_ binding:UInt64)->[[PeerWire.Bytes]]{if now>=deadline || g != generation || binding != self.binding{ended=true;pending=[];due=0;return []};if end{ended=true};if !end && now<due{return []};var out:[[PeerWire.Bytes]]=[];while pending.count>=16 || (end && !pending.isEmpty){let n=min(16,pending.count);out.append(Array(pending.prefix(n)));pending.removeFirst(n);batches+=1;precondition(batches<=4)};due=pending.isEmpty ? 0:now+50;return out}
}
