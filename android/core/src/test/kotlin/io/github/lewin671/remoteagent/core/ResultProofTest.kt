package io.github.lewin671.remoteagent.core
import java.io.File
import java.util.Base64
import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Test
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.CommandResult
class ResultProofTest {
 @Test fun sharedSignedProofs() {
  val f=Json.parseToJsonElement(File(File(System.getProperty("outcomeVectors")).parentFile,"proofs.json").readText()).jsonObject
  fun s(k:String)=f.getValue(k).jsonPrimitive.content
  fun raw(s:String)=Base64.getDecoder().decode(s)
  val state=DeviceSecurity.parseState(raw(s("State")))
  fun open(s:String)=DeviceSecurity.open(state,state,false,0uL,DeviceSecurity.fromEnvelope(Envelope.parseFrom(raw(s))),s("Secret").unhex(),null)
  val(h,plain)=open(s("Command"));val(key,payload)=DeviceSecurity.requestIdentity(h,plain)
  assertEquals(s("Key"),key.hex());assertEquals(s("Payload"),payload.hex())
  for(row in f.getValue("Vectors").jsonArray){
   val v=row.jsonObject;fun value(k:String)=v.getValue(k).jsonPrimitive.content
   val result=runCatching{val(_,body)=open(value("Envelope"));verifiedOutcome(CommandResult.parseFrom(body),key,payload,value("OuterOutcome"),value("OuterReason"))}.getOrDefault(Outcome.UNKNOWN)
   assertEquals(value("Name"),value("Expected"),result.name.lowercase())
  }
 }
}
