import Foundation

final class Signal {
    struct Action {var cls,type,generation:UInt64;var payload:Wire.Bytes}
    let selfID:Wire.Identity,connection:Wire.Bytes,use:UInt64;var peer:Wire.Identity
    var leaseUntil:UInt64=0,authorizationRevision:UInt64=1;var membersAuthorized=true
    var generation:UInt64=1,next:UInt64=1,gapDeadline:UInt64=0
    var bootReady=false,localSDP=false,remoteSDP=false,dtls=false,confirmed=false,closed=false,retired=false,ended=false
    var challenge:Wire.Opened?,probe:Wire.Opened?,receivedProbe:Wire.Opened?
    var clock:UInt64,roundDeadline:UInt64;var allowed:[String:Wire.Bytes]=[:],dispatched:[String:Wire.Bytes]=[:],dispatches=0
    var localHash:Wire.Bytes=[],remoteHash:Wire.Bytes=[],localFingerprint:Wire.Bytes=[],remoteFingerprint:Wire.Bytes=[],remoteUfrag:Wire.Bytes=[],remoteMid:Wire.Bytes=[]
    var candidates=0,batches=0,sdkActions=0,duplicates=0,conflicts=0,rejected=0,unsupported=0,staleChallenges=0
    var seen:[String:Wire.Bytes]=[:],cacheUntil:[String:UInt64]=[:],pending:[UInt64:Wire.Opened]=[:],pendingAt:[UInt64:UInt64]=[:]
    let lock=NSRecursiveLock()
    init(_ own:Wire.Identity,_ peer:Wire.Identity,_ connection:Wire.Bytes,_ use:UInt64,_ now:UInt64){selfID=own;self.peer=Wire.member(peer);clock=now;roundDeadline=now+10000;leaseUntil=roundDeadline;self.connection=connection;self.use=use}
    func rememberProbe(_ sent:Wire.Opened)throws{lock.lock();defer{lock.unlock()};try Wire.check(!closed && authorized);var sent=try Wire.openSent(sent.raw,selfID,peer,clock);try preflight(&sent);let c=sent.ctx;try Wire.check(sent.type==1 && c.cls==1 && c.domain==Wire.utf8("bootstrap") && c.generation==0 && c.use==0 && clock<c.deadline && c.from==selfID.id && c.to==peer.id && c.connection==connection && c.senderBoot==selfID.boot && c.senderInstance==selfID.instance);probe=sent}
    func rememberChallenge(_ sent:Wire.Opened)throws{lock.lock();defer{lock.unlock()};try Wire.check(!closed && authorized);var sent=try Wire.openSent(sent.raw,selfID,peer,clock);try preflight(&sent);let c=sent.ctx,f=try Schema.fields(sent.payload,"Challenge");try Wire.need(f,"bbbbub");guard let request=receivedProbe else{throw Wire.Invalid.packet};try Wire.check(clock<request.ctx.deadline && f[6]!.data==request.hash && sent.type==2 && c.recipientUnknown==0 && c.cls==1 && c.domain==Wire.utf8("bootstrap") && c.generation==0 && c.use==0 && clock<c.deadline && sent.hash.count==32 && c.from==selfID.id && c.to==peer.id && c.connection==connection && c.senderBoot==selfID.boot && c.receiverBoot==peer.boot && c.senderInstance==selfID.instance && c.receiverInstance==peer.instance);challenge=sent}
    func acceptACK(_ ack:Wire.Opened)throws{let raw=ack.payload;let f=try Schema.fields(raw,"Ack");try Wire.check(f.count==7 && f[1]!.data.isEmpty && f[2]!.value==0 && f[3]==nil);for n in [4,5,7,8]{try Wire.check(f[n] != nil && !f[n]!.bytes)};try Wire.check(f[6] != nil && f[6]!.bytes && f[6]!.data.count==32 && (1...2).contains(f[4]!.value) && f[5]!.value != 0 && f[8]!.value<=2)
        if challenge==nil{try Wire.check(f[7]!.value != 0 || (f[4]!.value==1 && f[8]!.value==0));return}
        if f[7]!.value==0 || (challenge != nil && f[6]!.data==challenge!.hash){guard let ch=challenge else{throw Wire.Invalid.packet};let c=ch.ctx,a=ack.ctx;try Wire.check(clock<c.deadline);try Wire.check(a.from==c.to && a.to==c.from && a.connection==c.connection && a.senderBoot==c.receiverBoot && a.receiverBoot==c.senderBoot && a.senderInstance==c.receiverInstance && a.receiverInstance==c.senderInstance);try Wire.check(f[4]!.value==c.cls && f[5]!.value==c.seq && f[6]!.data==ch.hash && f[7]!.value==c.generation && f[8]!.value==c.use);bootReady=true}else{try Wire.check(bootReady)}
    }
    func preflight(_ o:inout Wire.Opened)throws{o.payload=try Schema.known(o.ctx.cls,o.type,o.payload);if o.ctx.cls != 1{return}
        if o.type==1 || o.type==2{let f=try Schema.fields(o.payload,o.type==1 ? "Probe":"Challenge"),c=o.ctx;try Wire.check(f[1]!.data==c.connection && f[2]!.data==c.senderInstance && f[3]!.data==c.senderBoot && (o.type != 2 || f[4]!.data==c.receiverBoot))}
        if o.type==3 || o.type==4{let f=try Schema.fields(o.payload,"SDP");try Wire.check(Wire.hash(f[1]!.data)==f[2]!.data);_ = try Self.info(f[1]!.data)}
        if o.type==5{let raw=o.payload;var at=0,count=0;while at<raw.count && raw[at]==10{at+=1;let n=Int(try Schema.integer(raw,&at)),f=try Schema.fields(Array(raw[at..<at+n]),"Candidate");at+=n;let original=try Self.text(f[1]!.data);try Wire.check(original.hasPrefix("candidate:") && f[1]!.data.allSatisfy{$0>=32 && $0<=126});let tokens=original.split(separator:" ");var u=0;for(i,t)in tokens.enumerated() where t.lowercased()=="ufrag"{u+=1;try Wire.check(t=="ufrag" && u==1 && i+1<tokens.count && String(tokens[i+1]) == (try Self.text(f[2]!.data)))};try Wire.check((4...256).contains(f[2]!.data.count) && !f[3]!.data.isEmpty && f[4]!.value==0 && f[5]!.value==o.ctx.generation);count+=1};let f=try Wire.parse(Array(raw[at...]),1);try Wire.check(count>0 && f[2]!.value<=3)}
        if o.type==6{let f=try Schema.fields(o.payload,"EndCandidates");try Wire.check(f[1]!.data.count>=4 && f[2]!.value<=64 && f[3]!.value<=4)}
        if o.type==7{for f in try Schema.fields(o.payload,"Confirm").values{try Wire.check(f.data.count==32)}}
        if o.type==8{let t=try Schema.restart(o.payload);try Wire.check(offerer && o.ctx.use==use && o.ctx.generation==generation && t.count==1 && t[use]==generation)}
    }
    func setAuthorization(_ until:UInt64,_ members:Bool,_ revision:UInt64){lock.lock();defer{lock.unlock()};leaseUntil=until;membersAuthorized=members;authorizationRevision=revision}
    var authorized:Bool{clock<leaseUntil && membersAuthorized && authorizationRevision==1}
    func replyAllowed(_ raw:Wire.Bytes,_ now:UInt64)->Bool{lock.lock();defer{lock.unlock()};_ = tick(now);if closed || !authorized{return false};do{var o=try Wire.open(raw,peer,selfID,clock);try preflight(&o);let c=o.ctx;return c.connection==connection && seen[Self.key(c)]==o.hash && (c.generation==0 || c.generation==generation) && !(c.cls==2 && [4,10,11,12,13].contains(o.type))}catch{return false}}
    var offerer:Bool {selfID.id.lexicographicallyPrecedes(peer.id)}
    var ready:Bool {!closed && localSDP && remoteSDP && dtls && confirmed}
    static func key(_ c:Wire.Context)->String {[Wire.hex(c.from),Wire.hex(c.senderInstance),Wire.hex(c.connection),String(c.use),String(c.generation),String(c.cls),String(c.seq)].joined(separator:"/")}
    static func text(_ raw:Wire.Bytes)throws->String {guard let s=String(bytes:raw,encoding:.utf8),Array(s.utf8)==raw else{throw Wire.Invalid.packet};return s}
    static func info(_ raw:Wire.Bytes)throws->(Wire.Bytes,Wire.Bytes,Wire.Bytes){try SDPCheck.validate(Data(raw));var u:Wire.Bytes=[],m:Wire.Bytes=[],f:Wire.Bytes=[]
        for line in try text(raw).components(separatedBy:"\r\n"){if line.hasPrefix("a=ice-ufrag:"){u=Wire.utf8(String(line.dropFirst(12)))};if line.hasPrefix("a=mid:"){m=Wire.utf8(String(line.dropFirst(6)))};if line.hasPrefix("a=fingerprint:sha-256 "){f=Wire.unhex(String(line.dropFirst("a=fingerprint:sha-256 ".count)).replacingOccurrences(of:":",with:""))}};return(u,m,f)
    }
    func setLocal(_ raw:Wire.Bytes,_ offer:Bool,_ gen:UInt64)throws{lock.lock();defer{lock.unlock()};try Wire.check(authorized && clock<roundDeadline && gen==generation && !closed && bootReady && offer==offerer && (!offer || !localSDP) && (offer || remoteSDP));let m=try Self.info(raw);localSDP=true;localHash=Wire.hash(raw);localFingerprint=m.2}
    func nextGeneration(_ gen:UInt64)throws{lock.lock();defer{lock.unlock()};try Wire.check(clock<roundDeadline && generation<UInt64.max && (!closed || retired) && gen==generation+1 && gen != 0);generation=gen;next=1;closed=false;retired=false;localSDP=false;remoteSDP=false;dtls=false;confirmed=false;ended=false;pending=[:];pendingAt=[:];allowed=[:];dispatched=[:];gapDeadline=0;candidates=0;batches=0}
    func remember(_ o:Wire.Opened){let k=Self.key(o.ctx);seen[k]=o.hash;cacheUntil[k]=o.ctx.deadline+300000}
    func receive(_ raw:Wire.Bytes,_ now:UInt64)throws->[Action]{lock.lock();defer{lock.unlock()};do{_ = tick(now);return try accept(Wire.open(raw,peer,selfID,clock),clock)}catch{rejected+=1;throw error}}
    func accept(_ initial:Wire.Opened,_ now:UInt64)throws->[Action]{var o=initial;_ = tick(now);o=try Wire.open(o.raw,peer,selfID,clock);try Wire.check(authorized && clock<roundDeadline);let c=o.ctx;try Wire.check(c.connection==connection && c.from==peer.id);try preflight(&o);let key=Self.key(c)
        if let previous=seen[key]{if previous != o.hash{conflicts+=1;throw Wire.Invalid.packet};duplicates+=1;return []};try Wire.check((!closed || retired) && seen.count<2048)
        if c.cls==2{if o.type==4{try acceptACK(o)};remember(o);if o.type != 4 && o.type != 10{unsupported+=1;return [Action(cls:2,type:10,generation:c.generation,payload:Wire.pb([Wire.u(1,c.cls),Wire.u(2,o.type),Wire.u(3,c.seq),Wire.b(4,o.hash)]))]};return [Action(cls:2,type:o.type,generation:c.generation,payload:o.payload)]}
        if o.type==1 || o.type==2{try Wire.check(c.domain==Wire.utf8("bootstrap") && c.generation==0 && c.use==0);let f=try Schema.fields(o.payload,o.type==1 ? "Probe":"Challenge");try Wire.need(f,o.type==1 ? "bbb":"bbbbub");try Wire.check(f[1]!.data==c.connection && f[2]!.data==c.senderInstance && f[3]!.data==c.senderBoot);if o.type==2{guard let request=probe else{throw Wire.Invalid.packet};_ = try Wire.openSent(request.raw,selfID,peer,clock);try Wire.check(f[5]!.value<=1 && f[6]!.data.count==32 && clock<request.ctx.deadline && f[6]!.data==request.hash && f[4]!.data==selfID.boot)};if !peer.boot.isEmpty && (peer.boot != c.senderBoot || peer.instance != c.senderInstance){retire(false);throw Wire.Invalid.packet};if o.type==1{receivedProbe=o};peer.instance=c.senderInstance;peer.boot=c.senderBoot;remember(o);if o.type==2{bootReady=true;if f[5]!.value==1{staleChallenges+=1}};return [Action(cls:1,type:o.type,generation:0,payload:o.payload)]}
        if !bootReady && !closed && !offerer && o.type==3 && c.cls==1 && c.domain==Wire.utf8("signal") && c.use==use && c.generation==generation && c.seq==1 && c.receiverBoot==selfID.boot && c.senderBoot==peer.boot && c.receiverInstance==selfID.instance && c.senderInstance==peer.instance{let f=try Schema.fields(o.payload,"SDP");try Wire.need(f,"bb");try Wire.check(Wire.hash(f[1]!.data)==f[2]!.data);_ = try Self.info(f[1]!.data);bootReady=true}
        try Wire.check((!closed || (retired && c.generation==generation+1)) && bootReady && c.use==use && c.generation>=generation && generation<UInt64.max && c.generation<=generation+1)
        if c.generation==generation+1{try Wire.check(!offerer);try nextGeneration(c.generation)}
        try Wire.check(c.seq>=next);if c.seq>next{try Wire.check(pending.count<32);remember(o);pending[c.seq]=o;pendingAt[c.seq]=now;refreshGap();return []}
        var actions:[Action]=[]
        while true{o=try Wire.open(o.raw,peer,selfID,clock);try Wire.check(!closed && o.ctx.generation==generation && o.ctx.use==use && o.ctx.connection==connection);try preflight(&o);let action=try apply(o);remember(o);try Wire.check(next<UInt64.max);next+=1;if action.type != 0{actions.append(action);if action.cls==1 && (3...6).contains(action.type){allowed[Self.key(o.ctx)]=o.hash}};guard let value=pending.removeValue(forKey:next)else{break};pendingAt.removeValue(forKey:next);o=value}
        refreshGap();return actions
    }
    func apply(_ o:Wire.Opened)throws->Action{var action=Action(cls:1,type:o.type,generation:o.ctx.generation,payload:o.payload)
        switch o.type{
        case 3,4:
            try Wire.check(o.type==3 ? (!offerer && !remoteSDP):(offerer && localSDP && !remoteSDP));let f=try Schema.fields(o.payload,"SDP");try Wire.need(f,"bb");try Wire.check(Wire.hash(f[1]!.data)==f[2]!.data);let m=try Self.info(f[1]!.data);remoteSDP=true;remoteHash=f[2]!.data;remoteUfrag=m.0;remoteMid=m.1;remoteFingerprint=m.2;sdkActions+=1;action.payload=f[1]!.data
        case 5:
            try Wire.check(remoteSDP && !ended);let bytes=o.payload;var at=0,count=0
            func read()throws->UInt64{let start=at;var n:UInt64=0;for i in 0..<10{try Wire.check(at<bytes.count);let b=bytes[at];at+=1;try Wire.check(i != 9 || b<=1);n |= UInt64(b&127) << (i*7);if b&128==0{try Wire.check(Array(bytes[start..<at])==Wire.vint(n));return n}};throw Wire.Invalid.packet}
            while at<bytes.count && bytes[at]==10{at+=1;let size=try read();try Wire.check(size<=1024 && size<=UInt64(bytes.count-at) && count<16);let f=try Schema.fields(Array(bytes[at..<at+Int(size)]),"Candidate");at+=Int(size);try Wire.need(f,"bbbuu");let original=try Self.text(f[1]!.data);try Wire.check(original.hasPrefix("candidate:") && !original.contains("\0") && !original.contains("\r") && !original.contains("\n"));try Wire.check(f[1]!.data.allSatisfy{$0>=32 && $0<=126});let tokens=original.split(separator:" ");var ufrags=0;for (i,t) in tokens.enumerated() where t.lowercased()=="ufrag"{ufrags+=1;try Wire.check(t=="ufrag" && ufrags==1 && i+1<tokens.count && String(tokens[i+1]) == (try Self.text(f[2]!.data)))};try Wire.check(f[2]!.data==remoteUfrag && f[3]!.data==remoteMid && f[4]!.value==0 && f[5]!.value==generation);count+=1}
            let f=try Wire.parse(Array(bytes[at...]),1);try Wire.check(f.count==1 && f[2] != nil && !f[2]!.bytes && f[2]!.value==UInt64(batches) && count>0 && batches<4 && candidates+count<=64);candidates+=count;batches+=1;sdkActions+=count
        case 6:
            try Wire.check(remoteSDP && !ended);let f=try Schema.fields(o.payload,"EndCandidates");try Wire.need(f,"buu");try Wire.check(f[1]!.data==remoteUfrag && f[2]!.value==UInt64(candidates) && f[3]!.value==UInt64(batches));sdkActions+=1;ended=true
        case 7:try Wire.check(localSDP && remoteSDP && o.payload==confirmBody());confirmed=true
        case 8:try Wire.check(offerer)
        case 9:closed=true;pending=[:];pendingAt=[:];allowed=[:];dispatched=[:];gapDeadline=0
        default:unsupported+=1;action=Action(cls:2,type:10,generation:o.ctx.generation,payload:Wire.pb([Wire.u(1,o.ctx.cls),Wire.u(2,o.type),Wire.u(3,o.ctx.seq),Wire.b(4,o.hash)]))
        };return action
    }
    func confirmBody()->Wire.Bytes{Wire.pb([Wire.b(1,offerer ? localHash:remoteHash),Wire.b(2,offerer ? remoteHash:localHash),Wire.b(3,offerer ? localFingerprint:remoteFingerprint),Wire.b(4,offerer ? remoteFingerprint:localFingerprint)])}
    func refreshGap(){gapDeadline=pendingAt.map{min($0.value+2500,pending[$0.key]!.ctx.deadline,roundDeadline)}.min() ?? 0}
    func tick(_ now:UInt64)->Bool{lock.lock();defer{lock.unlock()};clock=max(clock,now);let now=clock;if now>=roundDeadline{retire(false);return true};for (key,until) in cacheUntil where now>until{cacheUntil.removeValue(forKey:key);seen.removeValue(forKey:key)};if gapDeadline != 0 && now>=gapDeadline{closed=true;retired=true;pending=[:];pendingAt=[:];allowed=[:];dispatched=[:];gapDeadline=0;return true};return false}
    func retire(_ retry:Bool){closed=true;retired=retry;pending=[:];pendingAt=[:];allowed=[:];dispatched=[:];gapDeadline=0}
    func forgetPeer(){lock.lock();defer{lock.unlock()};peer=Wire.member(peer);retire(false)}
    func dispatch(_ raw:Wire.Bytes,_ now:UInt64)->Bool{lock.lock();defer{lock.unlock()};_ = tick(now);if closed || !authorized{return false};do{let o=try Wire.open(raw,peer,selfID,clock),key=Self.key(o.ctx);if o.ctx.generation != generation || o.ctx.use != use || o.ctx.connection != connection || allowed[key] != o.hash || dispatched[key] != nil{return false};dispatched[key]=o.hash;dispatches+=1;return true}catch{return false}}
}
