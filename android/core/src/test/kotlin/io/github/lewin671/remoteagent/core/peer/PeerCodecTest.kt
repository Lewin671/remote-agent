package io.github.lewin671.remoteagent.core.peer

import kotlinx.serialization.json.*
import org.junit.Test
import org.junit.Assert.*
import java.io.File
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters

class PeerCodecTest {
 private val now = 1700000000000L
 private fun hash(s:String)=Wire.hash(Wire.utf8(s))
 private fun identity(name:String, role:Long):Wire.Identity {
  val seed=hash("lab/sign/$name");val x=hash("lab/box/$name")
  val ed=Ed25519PrivateKeyParameters(seed,0).generatePublicKey().encoded
  return Wire.Identity(Wire.utf8(Wire.hex(Wire.hash(ed).copyOf(16))),role,hash("lab/instance/$name").copyOf(16),hash("lab/boot/$name"),ed,X25519PrivateKeyParameters(x,0).generatePublicKey().encoded,seed,x)
 }
 private fun codec()=Wire(Wire.utf8("0123456789abcdef0123456789abcdef"),hash("lab/account-domain"),hash("lab/member-head/1"),1)
 private fun vectors(name:String)=Json.parseToJsonElement(File(File(System.getProperty("outcomeVectors")).parentFile,"wire/$name").readText())
 @Test fun frozenCryptoAndSnapshotVectors() {
  val corpus=vectors("vectors.json").jsonObject
  val a=identity(corpus.getValue("sender").jsonPrimitive.content,1);val b=identity(corpus.getValue("receiver").jsonPrimitive.content,2)
  for(row in corpus.getValue("rows").jsonArray) {val r=row.jsonObject;val opened=runCatching{codec().open(Wire.unhex(r.getValue("packet_hex").jsonPrimitive.content),a,b,now)};assertEquals(r.getValue("name").jsonPrimitive.content,r.getValue("accept").jsonPrimitive.boolean,opened.isSuccess);if(opened.isSuccess)assertEquals(r.getValue("payload_hex").jsonPrimitive.content,Wire.hex(opened.getOrThrow().payload))}
  val machine=identity("machine",2);val client=identity("client",1)
  for(row in vectors("capability-snapshot.json").jsonArray) {val r=row.jsonObject;val result=runCatching{val o=codec().open(Wire.unhex(r.getValue("packet_hex").jsonPrimitive.content),machine,client,now);Schema.known(2,14,o.payload);PeerResponder.capabilityExpiry(o,now)};assertEquals(r.getValue("name").jsonPrimitive.content,r.getValue("accept").jsonPrimitive.boolean,result.isSuccess)}
 }
 private fun delivery(output:ByteArray,from:Wire.Identity,to:Wire.Identity):ByteArray {val f=Wire.parse(Wire.parse(output,1)[3]!!.b,4);return Wire.pb(Wire.b(1,from.id),Wire.b(2,to.id),Wire.u(3,f[2]!!.u),Wire.b(4,f[3]!!.b),Wire.b(5,f[4]!!.b))}
 @Test fun authenticatesBeforeUnsupportedAndRetainsOriginalReply() {
  val client=identity("client",1);val machine=identity("machine",2);val handler=PeerResponder(hash("lab/sign/client"));val conn=hash("connection").copyOf(16)
  fun receive(raw:ByteArray)=handler.receive(raw,"0123456789abcdef0123456789abcdef",hash("lab/account-domain"),hash("lab/member-head/1"),1,String(client.id),hash("lab/box/client"),String(machine.id),Ed25519PrivateKeyParameters(hash("lab/sign/machine"),0).generatePublicKey().encoded,X25519PrivateKeyParameters(hash("lab/box/machine"),0).generatePublicKey().encoded,now,now+60000)
  val pc=Wire.probeContext(machine,client,conn,1,now)
  val probe=codec().seal(machine,client,pc,1,Wire.pb(Wire.b(1,conn),Wire.b(2,machine.instance),Wire.b(3,machine.boot)),null,null,null)
  val reply=receive(probe)!!;assertArrayEquals(reply,receive(probe))
  val challenge=codec().open(delivery(reply,client,machine),client,machine,now)
  val bound=Wire.Identity(client);bound.instance=challenge.ctx.senderInstance;bound.boot=challenge.ctx.senderBoot
  val context=Wire.Context(machine,bound,conn,1,1,1,now,1,false)
  val unknown=codec().seal(machine,bound,context,31337,byteArrayOf(1),null,null,null)
  val refused=receive(unknown)!!;assertArrayEquals(refused,receive(unknown))
  val result=codec().open(delivery(refused,bound,machine),bound,machine,now)
  assertEquals(10L,result.type);assertArrayEquals(Wire.hash(unknown),Schema.fields(result.payload,"Unsupported")[4]!!.b)
  assertNull(receive(unknown.clone().also{it[it.lastIndex]=(it.last().toInt() xor 1).toByte()}))
  val restarted=PeerResponder(hash("lab/sign/client"))
  assertNull(restarted.receive(unknown,"0123456789abcdef0123456789abcdef",hash("lab/account-domain"),hash("lab/member-head/1"),1,String(client.id),hash("lab/box/client"),String(machine.id),Ed25519PrivateKeyParameters(hash("lab/sign/machine"),0).generatePublicKey().encoded,X25519PrivateKeyParameters(hash("lab/box/machine"),0).generatePublicKey().encoded,now,now+60000))
 }
}
