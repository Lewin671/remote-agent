package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.core.protoJson
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.Event
import java.io.File
import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** Rendering reads the same domain facts tested by core. In particular an
 * async question is a chat message even though it is not pending. */
class ProjectionRowsVectorsTest {
    @Test fun sharedHistoryFactsReachChat() {
        val corpus = Json.parseToJsonElement(File(System.getProperty("projectionVectors")).readText()).jsonObject
        for (item in corpus.getValue("vectors").jsonArray) {
            val v = item.jsonObject
            val name = v.getValue("name").jsonPrimitive.content
            val agent = Agent.forNumber(v.getValue("agent").jsonPrimitive.int)
            val events = v.getValue("events").jsonArray.map { protoJson(Event.getDefaultInstance(), it.jsonObject) as Event }
            for (item in v.getValue("observations").jsonArray) {
                val o = item.jsonObject
                val prefix = events.take(o.getValue("through").jsonPrimitive.content.toInt())
                val d = derive(prefix, agent)
                val rows = buildChat(prefix, d, "test")
                val decisions = approvalDecisions(prefix, d)
                val expected = o.getValue("state").jsonObject
                assertEquals(name, expected.getValue("merged"), JsonArray(decisions.merged.sorted().map(::JsonPrimitive)))
                assertEquals(name, expected.getValue("callDecisions"), JsonArray(decisions.onCall.keys.sorted().map { JsonPrimitive(it.toString()) }))
                o["history"]?.let { history ->
                    val actual = rows.filterIsInstance<ChatItem.AsyncQuestion>().map { JsonPrimitive(it.key.removePrefix("e")) }
                    assertEquals(name, history.jsonObject.getValue("asyncQuestions"), JsonArray(actual))
                }
                if (name == "background-uses-explicit-result-fact") assertTrue(rows.filterIsInstance<ChatItem.Tool>().any { !it.background })
            }
        }
    }
}
