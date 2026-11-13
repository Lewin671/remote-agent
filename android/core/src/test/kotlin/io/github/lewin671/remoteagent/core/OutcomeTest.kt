package io.github.lewin671.remoteagent.core
import java.io.File
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Command
import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Test
class OutcomeTest {
 @Test fun sharedVectors() {
  val fixture = Json.parseToJsonElement(File(System.getProperty("outcomeVectors")).readText()).jsonObject
  val vectors = fixture.getValue("vectors").jsonArray
  assertEquals(14, fixture.getValue("protocol").jsonPrimitive.int)
  assertEquals(504, vectors.size)
  for (row in vectors) {
   val v = row.jsonObject
   fun s(k: String) = v.getValue(k).jsonPrimitive.content
   fun b(k: String) = v.getValue(k).jsonPrimitive.boolean
   assertEquals(row.toString(), s("outcome"), classifyOutcome(s("source"), s("code"), b("signed"), b("prior"), b("readOnly")).name.lowercase())
   if (s("source") == "local" || (s("source") == "daemon" && b("signed"))) {
    val error = CommandException(s("code"), "fixture", fromDaemon = s("source") == "daemon", readOnly = b("readOnly"))
    val attempt = Attempt()
    attempt.next { PreparedCommand("machine", "", "fixture-request", 1000, Envelope.getDefaultInstance()) }
    if (b("prior")) attempt.settle(CommandException(CommandException.UNKNOWN, "fixture"))
    attempt.settle(error)
    assertEquals(row.toString(), s("outcome") == "unknown", attempt.frozen)
   }
  }
 }
 @Test fun commandEvidence() {
  val fixture = Json.parseToJsonElement(File(System.getProperty("outcomeVectors")).readText()).jsonObject
  val seen = mutableSetOf<String>()
  for (row in fixture.getValue("commands").jsonArray) {
   val v=row.jsonObject;val kind=v.getValue("kind").jsonPrimitive.content
   val builder=Command.newBuilder()
   if (kind.isNotEmpty()) {
    seen.add(kind.replace(Regex("[A-Z]")) { "_"+it.value }.uppercase())
    val method=builder.javaClass.methods.first { m -> m.name=="set"+kind.replaceFirstChar {it.uppercase()} && m.parameterCount==1 && m.parameterTypes[0].methods.any {it.name=="getDefaultInstance"} }
    var request=method.parameterTypes[0].getMethod("getDefaultInstance").invoke(null)
    if (kind=="schedule") {
     val rb=request.javaClass.getMethod("toBuilder").invoke(request)
     rb.javaClass.getMethod("setOperation",String::class.java).invoke(rb,v.getValue("operation").jsonPrimitive.content)
     request=rb.javaClass.getMethod("build").invoke(rb)
    }
    method.invoke(builder,request)
   }
   val readOnly=builder.build().isReadOnlyCommand()
   assertEquals(row.toString(),v.getValue("readOnly").jsonPrimitive.boolean,readOnly)
   for (code in listOf("canceled","deadline_exceeded","aborted"))
    assertEquals(row.toString(),!readOnly,CommandException(code,"fixture",fromDaemon=true,readOnly=readOnly).unknown)
  }
  assertEquals(Command.KindCase.values().map {it.name}.filter {it!="KIND_NOT_SET"}.toSet(),seen)
 }

}
