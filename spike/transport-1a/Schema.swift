import Foundation
enum Schema {
 struct Rule{let n:Int,kind:String,repeated:Bool;init(_ n:Int,_ k:String,_ r:Bool){self.n=n;kind=k;repeated=r}}
 static let signal=["","Probe","Challenge","SDP","SDP","Candidates","EndCandidates","Confirm","Restart","Close"]
 static let control=["","Capabilities","AttachPath","InputStatus","Ack","Replay","Barrier","StreamFailed","ClosePath","DirectPolicy","Unsupported","InputStatusReply","BarrierReply","PolicyReply"]
 static func known(_ cls:UInt64,_ type:UInt64,_ raw:Wire.Bytes)throws->Wire.Bytes{let names=cls==2 ? control:signal;if type>0 && type<names.count{return try normalize(raw,names[Int(type)],0,true)};return raw}
 static func cap(_ name:String,_ n:Int)->Int{name=="Candidates" && n==1 ? 16:name=="Restart" && n==1 ? 2:name=="Capabilities" ? 64:128}
 static func limit(_ k:String)->UInt64{if k=="bool"{return 1};if k=="u32"{return 0xffffffff};if k=="i64"{return 0x7fffffffffffffff};if k.hasPrefix("e"){return UInt64(k.dropFirst())!};return UInt64.max}
 static func integer(_ raw:Wire.Bytes,_ at:inout Int)throws->UInt64{let start=at;var value:UInt64=0;for i in 0..<10{try Wire.check(at<raw.count);let b=raw[at];at+=1;try Wire.check(i != 9 || b<=1);value |= UInt64(b&127) << (i*7);if b&128==0{try Wire.check(Array(raw[start..<at])==Wire.vint(value));return value}};throw Wire.Invalid.packet}
 static func normalize(_ raw:Wire.Bytes,_ name:String,_ depth:Int=0,_ shape:Bool=false)throws->Wire.Bytes{
  guard let defs=rules[name] else{throw Wire.Invalid.packet};try Wire.check(depth<=8 && raw.count<=(name=="Candidate" ? 1024:name=="LogicalFrame" ? 1<<20:65536))
  let spec=Dictionary(uniqueKeysWithValues:defs.map{($0.n,$0)});var values:[Int:[Wire.Field]]=[:],at=0,last=0
  while at<raw.count{let tag=try integer(raw,&at),n=Int(tag>>3),wire=tag&7;guard let r=spec[n]else{throw Wire.Invalid.packet};try Wire.check(n>=last && (r.repeated || values[n]==nil));last=n;var list=values[n] ?? [];let numeric=r.kind != "b" && !r.kind.hasPrefix("m")
   if numeric && wire==0{let value=try integer(raw,&at);try Wire.check(value<=limit(r.kind));list.append(Wire.u(n,value))}
   else if wire==2{let size=try integer(raw,&at);try Wire.check(size<=UInt64(raw.count-at));let body=Array(raw[at..<at+Int(size)]);at+=Int(size)
    if numeric{try Wire.check(r.repeated);var p=0;while p<body.count{let value=try integer(body,&p);try Wire.check(value<=limit(r.kind));list.append(Wire.u(n,value))}}
    else{if r.kind.hasPrefix("m"){_ = try normalize(body,String(r.kind.dropFirst()),depth+1,shape)};list.append(Wire.b(n,body))}
   }else{throw Wire.Invalid.packet};try Wire.check(list.count<=cap(name,n));values[n]=list
  }
  try Wire.check(!shape || relations(name,values));var out:Wire.Bytes=[];for r in defs{var list=values[r.n] ?? [];if list.isEmpty && !r.repeated && !r.kind.hasPrefix("m"){list=[r.kind=="b" ? Wire.b(r.n,[]):Wire.u(r.n,0)]};for f in list{if shape,let b=bounds[name]?[r.n]{let v=f.bytes ? UInt64(f.data.count):f.value;try Wire.check(v>=b[0] && v<=b[1])};out+=Wire.pb([f])}};return out
 }
 static func fields(_ raw:Wire.Bytes,_ name:String)throws->[Int:Wire.Field]{try Wire.parse(normalize(raw,name),rules[name]!.count)}
 static func restart(_ original:Wire.Bytes)throws->[UInt64:UInt64]{let raw=try normalize(original,"Restart");var at=0,targets:[UInt64:UInt64]=[:];while at<raw.count && raw[at]==10{at+=1;let size=try integer(raw,&at);try Wire.check(size<=UInt64(raw.count-at));let f=try fields(Array(raw[at..<at+Int(size)]),"RestartPC");at+=Int(size);try Wire.check((1...2).contains(f[1]!.value) && targets[f[1]!.value]==nil);targets[f[1]!.value]=f[2]!.value};let tail=try Wire.parse(Array(raw[at...]),1);try Wire.check(!targets.isEmpty && tail.count==1 && tail[2] != nil && !tail[2]!.bytes);return targets}
}
