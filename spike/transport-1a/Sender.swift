import Foundation
final class Sender {
 struct Pending {var raw,hash:Wire.Bytes;var first,notBefore:UInt64;var attempts:Int}
 let deadline:UInt64;var pending:[String:Pending]=[:],timedOut=false,frames=0,bytes=0,failures=0;var retryAt:UInt64=0,networkGeneration:UInt64=0
 init(_ deadline:UInt64){self.deadline=deadline}
 func enqueue(_ raw:Wire.Bytes,_ now:UInt64)->Bool{if timedOut || now>=deadline || pending.count>=32{return false};let hash=Wire.hash(raw),key=Wire.hex(hash);if pending[key] != nil{return true};pending[key]=Pending(raw:raw,hash:hash,first:now,notBefore:0,attempts:0);return true}
 func tick(_ now:UInt64)->[Wire.Bytes]{if now>=deadline{pending=[:];timedOut=true;return []};var out:[Wire.Bytes]=[];for key in pending.keys.sorted(){var p=pending[key]!;if p.attempts==3{continue};let due=max(p.first+[UInt64(0),500,1500][p.attempts],p.notBefore);if now>=due{precondition(Wire.hash(p.raw)==p.hash);out.append(p.raw);p.attempts+=1;pending[key]=p;frames+=1;bytes+=p.raw.count}};return out}
 func ack(_ hash:Wire.Bytes)->Bool{pending.removeValue(forKey:Wire.hex(hash)) != nil}
 func limited(_ hash:Wire.Bytes,_ now:UInt64,_ delay:UInt64){let key=Wire.hex(hash);if var p=pending[key]{p.notBefore=max(p.notBefore,(now>=deadline || delay>deadline-now) ? deadline:now+delay);pending[key]=p}}
 func backoff(_ now:UInt64,_ fraction:Double)->UInt64{let cap=failures<5 ? UInt64(1000)<<failures:30000;failures+=1;retryAt=now+UInt64(Double(min(30000,cap))*fraction);return retryAt}
 func networkChanged(_ g:UInt64){networkGeneration=max(networkGeneration,g)}
}

final class CandidateBatcher {
 let generation:UInt64;var due:UInt64=0;var pending:[Wire.Bytes]=[],count=0,batches=0,ended=false
 init(_ g:UInt64){generation=g}
 func add(_ raw:Wire.Bytes,_ g:UInt64,_ now:UInt64)->Bool{if ended || g != generation || count>=64 || raw.count>1024{return false};if pending.isEmpty{due=now+50};pending.append(raw);count+=1;return true}
 func flush(_ now:UInt64,_ end:Bool=false)->[[Wire.Bytes]]{if end{ended=true};if !end && now<due{return []};var out:[[Wire.Bytes]]=[];while pending.count>=16 || (end && !pending.isEmpty){let n=min(16,pending.count);out.append(Array(pending.prefix(n)));pending.removeFirst(n);batches+=1;precondition(batches<=4)};due=pending.isEmpty ? 0:now+50;return out}
}
