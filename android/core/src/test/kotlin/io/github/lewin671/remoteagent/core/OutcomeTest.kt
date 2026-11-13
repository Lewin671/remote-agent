package io.github.lewin671.remoteagent.core
import java.io.File
import io.github.lewin671.remoteagent.proto.Envelope
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
}
