package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import java.io.File
import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Test


class ProjectionVectorsTest {
    private fun normalize(events: List<Event>, d: Derived): JsonElement {
        fun seqs(xs: Iterable<Long>) = xs.sorted().map { it.toString() }
        val decisions = approvalCallIDs(events, d)
        return Json.parseToJsonElement(buildJsonObject {
            put("running", d.running)
            put("approvals", JsonArray(d.approvals.keys.map(::JsonPrimitive)))
            put("resolved", buildJsonObject { for ((id, r) in d.resolved) put(id, r.first.number) })
            put("questions", JsonArray(d.questions.keys.map(::JsonPrimitive)))
            put("answered", buildJsonObject { for ((id, a) in d.answered) put(id, JsonArray(a.map(::JsonPrimitive))) })
            put("inputs", buildJsonArray { for ((id, status) in d.inputs) add(buildJsonObject {
                val st = d.inputDetails.getValue(id)
                put("id", id); put("status", status.number); put("stage", d.accepted[id]?.stage?.number ?: st.stageValue)
                put("mode", st.modeValue); put("paused", st.paused)
            }) })
            put("accepted", buildJsonObject { for ((id, a) in d.accepted) put(id, buildJsonObject {
                put("text", a.text); put("images", JsonArray(a.imageIds.map(::JsonPrimitive))); put("origin", a.origin?.kindValue ?: 0)
            }) })
            fun strings(key: String, xs: Iterable<String>) { put(key, JsonArray(xs.map(::JsonPrimitive))) }
            put("failures", buildJsonObject { for ((id, reason) in d.failures) put(id, reason) })
            strings("queue", pendingInputs(d).map { it.id }); put("plan", d.plan?.text?.let(::JsonPrimitive) ?: JsonNull)
            strings("unsent", d.unsent.keys.sorted()); strings("local", seqs(d.typedLocally))
            strings("runningTools", seqs(d.tools.map { it.seq })); strings("unknownTools", seqs(d.unfinished))
            strings("interruptedTools", seqs(d.unfinished - d.noResult))
            put("completed", buildJsonObject { for ((start, end) in d.completedAt) put(start.toString(), end.toString()) })
            strings("background", seqs(d.background.map { it.tool.seq }))
            put("compactions", buildJsonObject { for ((seq, c) in d.compactions) put(seq.toString(), buildJsonArray {
                add(c.before?.let(::JsonPrimitive) ?: JsonNull); add(c.after?.let(::JsonPrimitive) ?: JsonNull)
            }) })
            strings("terminalChanges", seqs(d.terminalChanges.keys)); strings("merged", decisions.values.sorted()); strings("callDecisions", seqs(decisions.keys))
        }.toString())
    }

    @Test fun sharedProjectionVectors() {
        val corpus = Json.parseToJsonElement(File(System.getProperty("projectionVectors")).readText()).jsonObject
        assertEquals(1, corpus.getValue("projectionVersion").jsonPrimitive.int)
        for (item in corpus.getValue("vectors").jsonArray) {
            val v = item.jsonObject
            val name = v.getValue("name").jsonPrimitive.content
            val agent = Agent.forNumber(v.getValue("agent").jsonPrimitive.int)
            val events = v.getValue("events").jsonArray.map { protoJson(Event.getDefaultInstance(), it.jsonObject) as Event }
            for (item in v.getValue("observations").jsonArray) {
                val o = item.jsonObject
                val prefix = events.take(o.getValue("through").jsonPrimitive.content.toInt())
                val expected = o.getValue("state")
                assertEquals("$name full", expected, normalize(prefix, derive(prefix, agent)))
                for (c in v.getValue("cuts").jsonArray.map { it.jsonPrimitive.int }.filter { it <= prefix.size }) {
                    val r = EventReducer(agent)
                    r.append(prefix.take(c))
                    val snapshot = r.snapshot()
                    val held = normalize(prefix.take(c), snapshot)
                    val projection = EventProjection()
                    projection.update(prefix.take(c), agent)
                    r.append(prefix.drop(c))
                    assertEquals("$name cut $c", expected, normalize(prefix, r.snapshot()))
                    assertEquals("$name projection $c", expected, normalize(prefix, projection.update(prefix, agent)))
                    // A previously returned normalized value stays independent.
                    assertEquals(held, normalize(prefix.take(c), snapshot))
                }
            }
        }
    }
}
