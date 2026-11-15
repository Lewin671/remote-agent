import Foundation
import CryptoKit

// Experimental codec. Fixture keys are public test material, never accounts.
enum Wire {
    typealias Bytes = [UInt8]
    enum Invalid: Error { case packet }
    static func check(_ ok: Bool) throws { if !ok { throw Invalid.packet } }
    static func utf8(_ s: String) -> Bytes { Array(s.utf8) }
    static func hash(_ b: Bytes) -> Bytes { Array(SHA256.hash(data: Data(b))) }
    static func hex(_ b: Bytes) -> String { b.map { String(format: "%02x",$0) }.joined() }
    static func unhex(_ s: String) -> Bytes { let c=Array(s.utf8);return stride(from:0,to:c.count,by:2).map { UInt8(String(bytes:c[$0...$0+1],encoding:.utf8)!,radix:16)! } }
    static func num(_ n: UInt64) -> Bytes { (0..<8).reversed().map { UInt8(truncatingIfNeeded:n >> ($0*8)) } }
    static func lp(_ values: Bytes...) -> Bytes { values.flatMap { Array(num(UInt64($0.count)).suffix(4)) + $0 } }
    static func vint(_ value: UInt64) -> Bytes { var n=value,out=Bytes();repeat { var b=UInt8(n&127);n >>= 7;if n != 0 { b |= 128 };out.append(b) } while n != 0;return out }
    struct Field { let n:Int; var data:Bytes=[];var value:UInt64=0;var bytes:Bool=false }
    static func b(_ n:Int,_ value:Bytes)->Field { Field(n:n,data:value,bytes:true) }
    static func u(_ n:Int,_ value:UInt64)->Field { Field(n:n,value:value) }
    static func pb(_ fields:[Field])->Bytes { fields.sorted{$0.n<$1.n}.flatMap { vint(UInt64($0.n*8+($0.bytes ? 2:0))) + ($0.bytes ? vint(UInt64($0.data.count))+$0.data : vint($0.value)) } }
    static func parse(_ raw:Bytes,_ max:Int)throws->[Int:Field] {
        try check(raw.count<=65536);var at=0,last:UInt64=0,out:[Int:Field]=[:]
        func read()throws->UInt64 { let start=at;var n:UInt64=0
            for i in 0..<10 { try check(at<raw.count);let c=raw[at];at+=1;try check(i != 9 || c<=1);n |= UInt64(c&127) << (i*7)
                if c&128==0 { try check(Array(raw[start..<at])==vint(n));return n }
            };throw Invalid.packet
        }
        while at<raw.count { let tag=try read(),n=tag>>3,wire=tag&7;try check(n>last && n<=4095 && out.count<max);last=n
            if wire==0 {out[Int(n)]=u(Int(n),try read())}else{try check(wire==2);let size=try read();try check(size<=UInt64(raw.count-at));out[Int(n)]=b(Int(n),Array(raw[at..<at+Int(size)]));at+=Int(size)}
        };return out
    }
    static func need(_ fields:[Int:Field],_ schema:String)throws { let chars=Array(schema);try check(fields.count==chars.count);for i in chars.indices {guard let f=fields[i+1] else {throw Invalid.packet};try check(f.bytes==(chars[i]=="b"))} }
    static let account=utf8("0123456789abcdef0123456789abcdef"),accountDomain=hash(utf8("lab/account-domain")),head=hash(utf8("lab/member-head/1"))
    static func kdf(_ secret:Bytes,_ info:Bytes)->Bytes { let key=HKDF<SHA256>.deriveKey(inputKeyMaterial:SymmetricKey(data:secret),salt:hash(utf8("remote-agent/peer-hkdf/v1")),info:info,outputByteCount:32);return key.withUnsafeBytes { Array($0) } }
    static func gcm(_ key:Bytes,_ nonce:Bytes,_ aad:Bytes,_ body:Bytes,_ seal:Bool)throws->Bytes {
        try check(nonce.count==12);let k=SymmetricKey(data:key),n=try AES.GCM.Nonce(data:nonce)
        if seal {let result=try AES.GCM.seal(Data(body),using:k,nonce:n,authenticating:Data(aad));return Array(result.ciphertext)+Array(result.tag)}
        try check(body.count>=16);let box=try AES.GCM.SealedBox(nonce:n,ciphertext:body.dropLast(16),tag:body.suffix(16));return Array(try AES.GCM.open(box,using:k,authenticating:Data(aad)))
    }
    struct Identity {
        var id,instance,boot:Bytes;let role:UInt64;let sign:Curve25519.Signing.PrivateKey;let box:Curve25519.KeyAgreement.PrivateKey
        init(_ name:String,_ role:UInt64)throws {self.role=role;instance=Array(hash(utf8("lab/instance/"+name)).prefix(16));boot=hash(utf8("lab/boot/"+name));sign=try .init(rawRepresentation:hash(utf8("lab/sign/"+name)));box=try .init(rawRepresentation:hash(utf8("lab/box/"+name)));id=utf8(hex(Array(hash(Array(sign.publicKey.rawRepresentation)).prefix(16))))}
    }
    static func shared(_ from:Identity,_ to:Identity)throws->Bytes { try from.box.sharedSecretFromKeyAgreement(with:to.box.publicKey).withUnsafeBytes{Array($0)} }
    struct Context {
        var domain,from,to,machine,senderInstance,receiverInstance,senderBoot,receiverBoot,connection:Bytes
        var fromRole,toRole,use,generation,seq,issued,deadline,cls:UInt64;var recipientUnknown:UInt64=0
        init(_ a:Identity,_ z:Identity,_ conn:Bytes,_ use:UInt64,_ gen:UInt64,_ seq:UInt64,_ now:UInt64,_ cls:UInt64=1,_ boot:Bool=false){domain=utf8(boot ? "bootstrap":cls==1 ? "signal":"control");from=a.id;to=z.id;fromRole=a.role;toRole=z.role;machine=a.role==2 ? a.id:z.id;senderInstance=a.instance;receiverInstance=z.instance;senderBoot=a.boot;receiverBoot=z.boot;connection=conn;self.use=use;generation=gen;self.seq=seq;issued=now;deadline=now+10000;self.cls=cls}
        init(_ f:[Int:Field]) {domain=f[1]!.data;from=f[3]!.data;to=f[4]!.data;fromRole=f[5]!.value;toRole=f[6]!.value;machine=f[7]!.data;senderInstance=f[11]!.data;receiverInstance=f[12]!.data;senderBoot=f[13]!.data;receiverBoot=f[14]!.data;connection=f[15]!.data;use=f[16]!.value;generation=f[17]!.value;seq=f[18]!.value;issued=f[19]!.value;deadline=f[20]!.value;cls=f[21]!.value;recipientUnknown=f[24]!.value}
        func encode()->Bytes {pb([b(1,domain),b(2,account),b(3,from),b(4,to),u(5,fromRole),u(6,toRole),b(7,machine),u(8,16),u(9,1),b(10,head),b(11,senderInstance),b(12,receiverInstance),b(13,senderBoot),b(14,receiverBoot),b(15,connection),u(16,use),u(17,generation),u(18,seq),u(19,issued),u(20,deadline),u(21,cls),b(22,from),b(23,accountDomain),u(24,recipientUnknown)])}
    }
    static func context(_ raw:Bytes)throws->Context {let f=try parse(raw,24);try need(f,"bbbbuubuubbbbbbuuuuuubbu");try check(f[23]!.data==accountDomain && f[2]!.data==account && f[8]!.value==16 && f[9]!.value==1 && f[10]!.data==head && f[22]!.data==f[3]!.data);return Context(f)}
    static func wrapInfo(_ a:Bytes,_ z:Bytes)->Bytes {let sorted=[a,z].sorted{$0.lexicographicallyPrecedes($1)};return lp(utf8("remote-agent/peer-context/v1"),account,accountDomain,sorted[0],sorted[1],a,z)}
    static func bodyInfo(_ c:Context)->Bytes {if c.domain==utf8("bootstrap"){return lp(utf8("remote-agent/peer-bootstrap/v1"),account,accountDomain,c.from,c.to)};return lp(utf8("remote-agent/peer-body/v1"),account,accountDomain,c.from,c.to,c.senderInstance,c.receiverInstance,c.senderBoot,c.receiverBoot,c.connection,num(c.use),num(c.generation),num(c.cls))}
    static func aad(_ a:Bytes,_ z:Bytes,_ cls:UInt64,_ id:Bytes)->Bytes {lp(utf8("remote-agent/peer-aad/v1"),account,accountDomain,num(16),pb([b(1,a),b(2,z),u(3,cls),b(4,id)]))}
    static func sigInput(_ ad:Bytes,_ nc:Bytes,_ ec:Bytes,_ nb:Bytes,_ eb:Bytes)->Bytes {lp(utf8("remote-agent/peer-signature/v1"),ad,nc,ec,nb,eb)}
    static func random(_ n:Int)->Bytes {(0..<n).map{_ in UInt8.random(in:0...255)}}
    static func seal(_ a:Identity,_ z:Identity,_ c:Context,_ type:UInt64,_ payload:Bytes,_ frameID:Bytes?=nil,_ contextNonce:Bytes?=nil,_ bodyNonce:Bytes?=nil)throws->Bytes {
        let id=frameID ?? random(16),nc=contextNonce ?? random(12),nb=bodyNonce ?? random(12),dh=try shared(a,z),ad=aad(a.id,z.id,c.cls,id)
        let ec=try gcm(kdf(dh,wrapInfo(a.id,z.id)),nc,ad,c.encode(),true)
        let eb=try gcm(kdf(dh,bodyInfo(c)),nb,lp(ad,hash(ec)),pb([b(1,pb([u(1,type),b(2,payload)]))]),true)
        let sig=Array(try a.sign.signature(for:Data(sigInput(ad,nc,ec,nb,eb))))
        let packet=pb([b(1,a.id),b(2,z.id),u(3,c.cls),b(4,id),b(5,pb([b(1,nc),b(2,ec),b(3,nb),b(4,eb),b(5,sig)]))]);try check(packet.count<=(c.cls==2 ? 16384:65536));return packet
    }
    struct Opened {let ctx:Context;let type:UInt64;let payload,frameID,hash,raw:Bytes}
    static func open(_ raw:Bytes,_ a:Identity,_ z:Identity,_ now:UInt64)throws->Opened {try openShared(raw,a,z,now,shared(z,a))}
    static func openSent(_ raw:Bytes,_ a:Identity,_ z:Identity,_ now:UInt64)throws->Opened {let o=try openShared(raw,a,z,now,shared(a,z));try check(o.ctx.senderInstance==a.instance && o.ctx.senderBoot==a.boot);return o}
    static func openShared(_ raw:Bytes,_ a:Identity,_ z:Identity,_ now:UInt64,_ dh:Bytes)throws->Opened {
        let f=try parse(raw,5);try need(f,"bbubb");let cls=f[3]!.value,id=f[4]!.data
        try check((cls==1 || cls==2) && raw.count<=(cls==2 ? 16384:65536) && id.count==16 && f[1]!.data==a.id && f[2]!.data==z.id)
        let p=try parse(f[5]!.data,5);try need(p,"bbbbb");let nc=p[1]!.data,ec=p[2]!.data,nb=p[3]!.data,eb=p[4]!.data,sig=p[5]!.data,ad=aad(a.id,z.id,cls,id)
        try check(nc.count==12 && nb.count==12 && sig.count==64 && a.sign.publicKey.isValidSignature(sig,for:sigInput(ad,nc,ec,nb,eb)))
        let c=try context(gcm(kdf(dh,wrapInfo(a.id,z.id)),nc,ad,ec,false))
        try check(c.from==a.id && c.to==z.id && c.cls==cls && c.fromRole==a.role && c.toRole==z.role && a.role != z.role && c.connection.count==16 && c.seq != 0 && c.issued<c.deadline && c.deadline-c.issued<=30000 && now<c.deadline && c.issued<=now+300000)
        try check(c.machine==(a.role==2 ? a.id:z.id));let boot=c.domain==utf8("bootstrap")
        try check(c.senderInstance.count==16 && c.senderBoot.count==32 && (1...2).contains(c.fromRole) && (1...2).contains(c.toRole))
        try check(c.recipientUnknown<=1);if c.recipientUnknown==1{try check(c.receiverInstance.isEmpty && c.receiverBoot.isEmpty)}else{try check(c.receiverInstance.count==16 && c.receiverBoot.count==32 && c.receiverInstance==z.instance && c.receiverBoot==z.boot)}
        if !boot {try check(c.domain==utf8(cls==1 ? "signal":"control"));try check(c.senderInstance==a.instance && c.receiverInstance==z.instance && c.senderBoot==a.boot && c.receiverBoot==z.boot && c.generation != 0 && c.use<=2)}
        let body=try gcm(kdf(dh,bodyInfo(c)),nb,lp(ad,hash(ec)),eb,false),w=try parse(body,1);try need(w,"b");let t=try parse(w[1]!.data,2);try need(t,"ub");let type=t[1]!.value
        try check(boot || cls != 1 || c.use != 0 || type==8 || type==9)
        try check(type<=0xffffffff && (!boot || (cls==1 && c.use==0 && c.generation==0 && (type==1 || type==2))))
        try check(c.recipientUnknown != 1 || (boot && cls==1 && type==1));return Opened(ctx:c,type:type,payload:t[2]!.data,frameID:id,hash:hash(raw),raw:raw)
    }
    static func validatePolicyReply(_ request:Opened,_ reply:Opened,_ now:UInt64)throws {
        let a=request.ctx,z=reply.ctx;try check(now<a.deadline && now<z.deadline && a.generation==z.generation && a.use==z.use && a.senderInstance==z.receiverInstance && a.receiverInstance==z.senderInstance && a.senderBoot==z.receiverBoot && a.receiverBoot==z.senderBoot);try check(a.cls==2 && z.cls==2 && request.type==9 && reply.type==13 && a.from==z.to && a.to==z.from && a.connection==z.connection)
        let q=try parse(request.payload,4),r=try parse(reply.payload,7);try need(q,"buuu");try need(r,"uuuubub")
        try check(q[2]!.value != 0 && q[3]!.value<=1 && r[2]!.value<=1 && r[3]!.value<=1)
        try check((q[4]!.value==1 && q[1]!.data.isEmpty) || (q[4]!.value==2 && q[1]!.data.count==32))
        try check(r[1]!.value==q[2]!.value && r[4]!.value==a.seq && r[5]!.data.count==32 && r[5]!.data==request.hash && r[6]!.value==q[4]!.value && r[7]!.data==q[1]!.data)
        try check(r[2]!.value != 1 || r[3]!.value==q[3]!.value)
    }
    static func member(_ peer:Identity)->Identity{var p=peer;p.instance=[];p.boot=[];return p}
    static func probeContext(_ a:Identity,_ z:Identity,_ conn:Bytes,_ seq:UInt64,_ now:UInt64)->Context{var c=Context(a,member(z),conn,0,0,seq,now,1,true);c.recipientUnknown=1;return c}

}

#if WIRE_CLI
@main struct WireVectors {
    static func main()throws {
        precondition(ProcessInfo.processInfo.environment["REMOTE_AGENT_HOME"]?.hasPrefix("/tmp/")==true)
        let a=try Wire.Identity("c000000000000001",1),z=try Wire.Identity("m000000000000001",2)
        let source=try String(contentsOfFile:CommandLine.arguments[1],encoding:.utf8);var count=0
        for row in source.split(separator:"\n") {
            let p=row.split(separator:"\t",omittingEmptySubsequences:false).map(String.init);var valid=false;var result:Wire.Opened?
            do {result=try Wire.open(Wire.unhex(p[4]),a,z,1700000000000);valid=true}catch{}
            precondition(valid==(p[1]=="true"),p[0]);if let result {precondition(result.type==UInt64(p[2])! && Wire.hex(result.payload)==p[3])}
            if p[0]=="offer" {let c=Wire.Context(a,z,Array(Wire.hash(Wire.utf8("lab/connection")).prefix(16)),1,1,1,1700000000000)
                let encoded=try Wire.seal(a,z,c,3,Wire.unhex(p[3]),Array(Wire.hash(Wire.utf8("frame/offer")).prefix(16)),Array(Wire.hash(Wire.utf8("context/offer")).prefix(12)),Array(Wire.hash(Wire.utf8("body/offer")).prefix(12)));let expected=Wire.unhex(p[4]);let ef=try Wire.parse(expected,5),af=try Wire.parse(encoded,5);let ep=try Wire.parse(ef[5]!.data,5),ap=try Wire.parse(af[5]!.data,5);precondition((1...4).allSatisfy{ep[$0]!.data==ap[$0]!.data});_ = try Wire.open(encoded,a,z,1700000000000);try Data(encoded).write(to:URL(fileURLWithPath:CommandLine.arguments[1]).deletingLastPathComponent().appendingPathComponent("swift-sealed.bin"));print("Swift ciphertext matches: true; signature byte-identical: \(ep[5]!.data==ap[5]!.data)")
            };count+=1
        };print("Swift real-crypto vectors: \(count) passed (encode + decode)")
    }
}
#endif
