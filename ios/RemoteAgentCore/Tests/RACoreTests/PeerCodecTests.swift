import Foundation
import CryptoKit
import Testing
@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct PeerCodecTests {
 typealias W = PeerWire
 let now: UInt64 = 1700000000000
 func identity(_ name: String, _ role: UInt64) throws -> W.Identity {
  let sign=try Curve25519.Signing.PrivateKey(rawRepresentation:W.hash(W.utf8("lab/sign/"+name))),box=try Curve25519.KeyAgreement.PrivateKey(rawRepresentation:W.hash(W.utf8("lab/box/"+name)))
  return W.Identity(peer:W.Public(id:W.utf8(W.hex(Array(W.hash(Array(sign.publicKey.rawRepresentation)).prefix(16)))),instance:Array(W.hash(W.utf8("lab/instance/"+name)).prefix(16)),boot:W.hash(W.utf8("lab/boot/"+name)),role:role,sign:sign.publicKey,box:box.publicKey),sign:sign,box:box)
 }
 var scope: W.Scope { W.Scope(account:W.utf8("0123456789abcdef0123456789abcdef"),accountDomain:W.hash(W.utf8("lab/account-domain")),head:W.hash(W.utf8("lab/member-head/1")),epoch:1) }
 @Test func frozenCodecAndFullSnapshot() throws {
  let corpus=try loadJSON("testdata/transport/wire/vectors.json"),a=try identity(corpus.s("sender"),1),b=try identity(corpus.s("receiver"),2)
  for row in corpus["rows"] as! [[String:Any]] {
   let result=try? W.open(scope,W.unhex(row.s("packet_hex")),a.peer,b,now)
   #expect((result != nil)==(row["accept"] as! Bool),"\(row.s("name"))")
   if let result { #expect(W.hex(result.payload)==row.s("payload_hex")) }
  }
  let rows=try JSONSerialization.jsonObject(with:Data(contentsOf:repoRoot.appendingPathComponent("testdata/transport/wire/capability-snapshot.json"))) as! [[String:Any]],machine=try identity("machine",2),client=try identity("client",1)
  for row in rows {
   var accepted=false
   do {let o=try W.open(scope,W.unhex(row.s("packet_hex")),machine.peer,client,now);_ = try PeerSchema.known(2,14,o.payload);try PeerResponder.capabilityExpiry(o,now);accepted=true}catch{}
   #expect(accepted==(row["accept"] as! Bool),"\(row.s("name"))")
  }
 }
 @Test func authenticatedUnsupportedSurvivesReplayAndRejectsOldInstance() async throws {
  typealias S=DeviceSecurity
  let fixture=try DeviceSecurityTests(),genesis=try fixture.state("genesis"),state=try fixture.state("added"),pin=try fixture.pin(),time=state.core.issuedAt
  var lease=S.HeadLease(domain:state.core.domain,head:try S.stateHash(state),issuedAt:time,notAfter:time+S.lease,signature:Data());lease.signature=try SigningKey(seed:fixture.hex("phone_seed")).sign(lease.signBytes())
  let record=try S.Record(pin:pin,states:[1:genesis,2:state],memberID:genesis.core.authorityID,xPrivate:fixture.hex("phone_x"),member:true,secret:fixture.hex("content_secret_2"),lease:lease)
  let sign=try Curve25519.Signing.PrivateKey(rawRepresentation:fixture.hex("machine_seed")),box=try fixture.machineX(),member=state.core.members.first{$0.kind==1}!,client=state.core.member(record.memberID)!
  let machine=W.Identity(peer:W.Public(id:W.utf8(member.id),instance:W.random(16),boot:W.random(32),role:2,sign:sign.publicKey,box:box.publicKey),sign:sign,box:box)
  var receiver=W.Public(id:W.utf8(client.id),instance:[],boot:[],role:1,sign:try .init(rawRepresentation:client.ed),box:try .init(rawRepresentation:client.x))
  let scope=W.Scope(account:W.utf8(pin.account),accountDomain:Array(pin.domain),head:Array(try S.stateHash(state)),epoch:state.core.epoch),conn=W.random(16),responder=PeerResponder()
  var context=W.Context(machine.peer,receiver,conn,0,0,1,time,1,true);context.recipientUnknown=1
  let probe=try W.seal(scope,machine,receiver,context,1,W.pb([W.b(1,conn),W.b(2,machine.instance),W.b(3,machine.boot)]))
  func delivery(_ data:Data,_ from:W.Public)throws->W.Bytes {let outer=try W.parse(Array(data),1),f=try W.parse(outer[3]!.data,4);return W.pb([W.b(1,from.id),W.b(2,machine.id),W.u(3,f[2]!.value),W.b(4,f[3]!.data),W.b(5,f[4]!.data)])}
  let response=try #require(await responder.receive(Data(probe),record:record,seed:fixture.hex("phone_seed"),now:time))
  let challenge=try W.open(scope,delivery(response,receiver),receiver,machine,time)
  receiver.instance=challenge.ctx.senderInstance;receiver.boot=challenge.ctx.senderBoot
  let unknown=try W.seal(scope,machine,receiver,W.Context(machine.peer,receiver,conn,1,1,1,time),31337,[1])
  let reply=try #require(await responder.receive(Data(unknown),record:record,seed:fixture.hex("phone_seed"),now:time))
  #expect(try await responder.receive(Data(unknown),record:record,seed:fixture.hex("phone_seed"),now:time)==reply)
  let opened=try W.open(scope,delivery(reply,receiver),receiver,machine,time)
  #expect(opened.type==10);#expect(try PeerSchema.fields(opened.payload,"Unsupported")[4]!.data==W.hash(unknown))
  #expect(try await PeerResponder().receive(Data(unknown),record:record,seed:fixture.hex("phone_seed"),now:time)==nil)
  var tampered=unknown;tampered[tampered.count-1] ^= 1
  #expect(try await responder.receive(Data(tampered),record:record,seed:fixture.hex("phone_seed"),now:time)==nil)
 }
}
