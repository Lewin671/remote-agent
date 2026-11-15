import Foundation

final class Signal {
    struct Action {var cls,type,generation:UInt64;var payload:Wire.Bytes}
    let selfID:Wire.Identity,connection:Wire.Bytes,use:UInt64;var peer:Wire.Identity
    var generation:UInt64=1,next:UInt64=1,gapDeadline:UInt64=0
    var bootReady=false,localSDP=false,remoteSDP=false,dtls=false,confirmed=false,closed=false,retired=false,ended=false
    var challengeHash:Wire.Bytes=[],localHash:Wire.Bytes=[],remoteHash:Wire.Bytes=[],localFingerprint:Wire.Bytes=[],remoteFingerprint:Wire.Bytes=[],remoteUfrag:Wire.Bytes=[],remoteMid:Wire.Bytes=[]
    var candidates=0,batches=0,sdkActions=0,duplicates=0,conflicts=0,rejected=0,unsupported=0,staleChallenges=0
    var seen:[String:Wire.Bytes]=[:],cacheUntil:[String:UInt64]=[:],pending:[UInt64:Wire.Opened]=[:],pendingAt:[UInt64:UInt64]=[:]
    let lock=NSRecursiveLock()
    init(_ own:Wire.Identity,_ peer:Wire.Identity,_ connection:Wire.Bytes,_ use:UInt64){selfID=own;self.peer=peer;self.connection=connection;self.use=use}
    var offerer:Bool {selfID.id.lexicographicallyPrecedes(peer.id)}
    var ready:Bool {!closed && localSDP && remoteSDP && dtls && confirmed}
    static func key(_ c:Wire.Context)->String {[Wire.hex(c.from),Wire.hex(c.senderInstance),Wire.hex(c.connection),String(c.use),String(c.generation),String(c.cls),String(c.seq)].joined(separator:"/")}
    static func text(_ raw:Wire.Bytes)throws->String {guard let s=String(bytes:raw,encoding:.utf8),Array(s.utf8)==raw else{throw Wire.Invalid.packet};return s}
    static func info(_ raw:Wire.Bytes)throws->(Wire.Bytes,Wire.Bytes,Wire.Bytes){try SDPCheck.validate(Data(raw));var u:Wire.Bytes=[],m:Wire.Bytes=[],f:Wire.Bytes=[]
        for line in try text(raw).components(separatedBy:"\r\n"){if line.hasPrefix("a=ice-ufrag:"){u=Wire.utf8(String(line.dropFirst(12)))};if line.hasPrefix("a=mid:"){m=Wire.utf8(String(line.dropFirst(6)))};if line.hasPrefix("a=fingerprint:sha-256 "){f=Wire.unhex(String(line.dropFirst("a=fingerprint:sha-256 ".count)).replacingOccurrences(of:":",with:""))}};return(u,m,f)
    }
    func setLocal(_ raw:Wire.Bytes,_ offer:Bool,_ gen:UInt64)throws{lock.lock();defer{lock.unlock()};try Wire.check(gen==generation && !closed && bootReady && offer==offerer && (!offer || !localSDP) && (offer || remoteSDP));let m=try Self.info(raw);localSDP=true;localHash=Wire.hash(raw);localFingerprint=m.2}
    func nextGeneration(_ gen:UInt64)throws{try Wire.check(generation<UInt64.max && (!closed || retired) && gen==generation+1 && gen != 0);generation=gen;next=1;closed=false;retired=false;localSDP=false;remoteSDP=false;dtls=false;confirmed=false;ended=false;pending=[:];pendingAt=[:];gapDeadline=0;candidates=0;batches=0}
    func remember(_ o:Wire.Opened){let k=Self.key(o.ctx);seen[k]=o.hash;cacheUntil[k]=o.ctx.deadline+300000}
    func receive(_ raw:Wire.Bytes,_ now:UInt64)throws->[Action]{lock.lock();defer{lock.unlock()};do{return try accept(Wire.open(raw,peer,selfID,now),now)}catch{rejected+=1;throw error}}
    func accept(_ initial:Wire.Opened,_ now:UInt64)throws->[Action]{var o=initial;let c=o.ctx;try Wire.check(c.connection==connection && c.from==peer.id);let key=Self.key(c)
        if let previous=seen[key]{if previous != o.hash{conflicts+=1;throw Wire.Invalid.packet};duplicates+=1;return []};try Wire.check(seen.count<2048)
        if c.cls==2{if !bootReady && o.type==4 && !challengeHash.isEmpty{let f=try Wire.parse(o.payload,5);try Wire.check(f.count==5 && f[4]?.value==1 && f[5]?.value==1 && f[6]?.data==challengeHash && f[7]?.value==0 && f[8]?.value==use);bootReady=true};remember(o);if o.type != 4 && o.type != 10{unsupported+=1;return [Action(cls:2,type:10,generation:c.generation,payload:Wire.pb([Wire.u(1,c.cls),Wire.u(2,o.type),Wire.u(3,c.seq),Wire.b(4,o.hash)]))]};return [Action(cls:2,type:o.type,generation:c.generation,payload:o.payload)]}
        if o.type==1 || o.type==2{try Wire.check(c.domain==Wire.utf8("bootstrap"));let f=try Wire.parse(o.payload,5);try Wire.need(f,o.type==1 ? "bbb":f.count==5 ? "bbbbu":"bbbb");try Wire.check(o.type != 2 || f.count != 5 || f[5]!.value<=1);try Wire.check(f[1]!.data==c.connection && f[2]!.data==c.senderInstance && f[3]!.data==c.senderBoot && (o.type != 2 || f[4]!.data==selfID.boot));if !localSDP && !remoteSDP{peer.instance=c.senderInstance;peer.boot=c.senderBoot};remember(o);if o.type==2{bootReady=true;if f[5]?.value==1{staleChallenges+=1}};return [Action(cls:1,type:o.type,generation:0,payload:o.payload)]}
        try Wire.check((!closed || (retired && c.generation==generation+1)) && bootReady && c.use==use && c.generation>=generation && generation<UInt64.max && c.generation<=generation+1)
        if c.generation==generation+1{try Wire.check(!offerer);try nextGeneration(c.generation)}
        try Wire.check(c.seq>=next);if c.seq>next{try Wire.check(pending.count<32);remember(o);pending[c.seq]=o;pendingAt[c.seq]=now;refreshGap();return []}
        var actions:[Action]=[]
        while true{remember(o);let action=try apply(o);try Wire.check(next<UInt64.max);next+=1;if action.type != 0{actions.append(action)};guard let value=pending.removeValue(forKey:next)else{break};pendingAt.removeValue(forKey:next);o=value}
        refreshGap();return actions
    }
    func apply(_ o:Wire.Opened)throws->Action{var action=Action(cls:1,type:o.type,generation:o.ctx.generation,payload:o.payload)
        switch o.type{
        case 3,4:
            try Wire.check(o.type==3 ? (!offerer && !remoteSDP):(offerer && localSDP && !remoteSDP));let f=try Wire.parse(o.payload,2);try Wire.need(f,"bb");try Wire.check(Wire.hash(f[1]!.data)==f[2]!.data);let m=try Self.info(f[1]!.data);remoteSDP=true;remoteHash=f[2]!.data;remoteUfrag=m.0;remoteMid=m.1;remoteFingerprint=m.2;sdkActions+=1;action.payload=f[1]!.data
        case 5:
            try Wire.check(remoteSDP && !ended);let bytes=o.payload;var at=0,count=0
            func read()throws->UInt64{let start=at;var n:UInt64=0;for i in 0..<10{try Wire.check(at<bytes.count);let b=bytes[at];at+=1;try Wire.check(i != 9 || b<=1);n |= UInt64(b&127) << (i*7);if b&128==0{try Wire.check(Array(bytes[start..<at])==Wire.vint(n));return n}};throw Wire.Invalid.packet}
            while at<bytes.count && bytes[at]==10{at+=1;let size=try read();try Wire.check(size<=1024 && size<=UInt64(bytes.count-at) && count<16);let f=try Wire.parse(Array(bytes[at..<at+Int(size)]),5);at+=Int(size);try Wire.need(f,"bbbuu");let original=try Self.text(f[1]!.data);try Wire.check(original.hasPrefix("candidate:") && !original.contains("\0") && !original.contains("\r") && !original.contains("\n"));let tokens=original.split(whereSeparator:{$0.isWhitespace});for (i,t) in tokens.enumerated() where t=="ufrag"{try Wire.check(i+1<tokens.count && String(tokens[i+1]) == (try Self.text(f[2]!.data)))};try Wire.check(f[2]!.data==remoteUfrag && f[3]!.data==remoteMid && f[4]!.value==0 && f[5]!.value==generation);count+=1}
            let f=try Wire.parse(Array(bytes[at...]),1);try Wire.check(f.count==1 && f[2] != nil && !f[2]!.bytes && f[2]!.value==UInt64(batches) && count>0 && batches<4 && candidates+count<=64);candidates+=count;batches+=1;sdkActions+=count
        case 6:
            try Wire.check(remoteSDP && !ended);let f=try Wire.parse(o.payload,3);try Wire.need(f,"buu");try Wire.check(f[1]!.data==remoteUfrag && f[2]!.value==UInt64(candidates) && f[3]!.value==UInt64(batches));sdkActions+=1;ended=true
        case 7:try Wire.check(localSDP && remoteSDP && o.payload==confirmBody());confirmed=true
        case 8:try Wire.check(offerer)
        case 9:closed=true;pending=[:];pendingAt=[:];gapDeadline=0
        default:unsupported+=1;action=Action(cls:2,type:10,generation:o.ctx.generation,payload:Wire.pb([Wire.u(1,o.ctx.cls),Wire.u(2,o.type),Wire.u(3,o.ctx.seq),Wire.b(4,o.hash)]))
        };return action
    }
    func confirmBody()->Wire.Bytes{Wire.pb([Wire.b(1,offerer ? localHash:remoteHash),Wire.b(2,offerer ? remoteHash:localHash),Wire.b(3,offerer ? localFingerprint:remoteFingerprint),Wire.b(4,offerer ? remoteFingerprint:localFingerprint)])}
    func refreshGap(){gapDeadline=pendingAt.values.min().map{$0+2500} ?? 0}
    func tick(_ now:UInt64)->Bool{lock.lock();defer{lock.unlock()};for (key,until) in cacheUntil where now>until{cacheUntil.removeValue(forKey:key);seen.removeValue(forKey:key)};if gapDeadline != 0 && now>=gapDeadline{closed=true;retired=true;pending=[:];pendingAt=[:];gapDeadline=0;return true};return false}
}
