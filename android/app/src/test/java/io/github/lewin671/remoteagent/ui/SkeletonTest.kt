package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.Event
import java.io.File
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class SkeletonTest {
    @org.junit.Before fun sessionMessageContext() = io.github.lewin671.remoteagent.ui.installSessionMessageContext()
    @Test fun allTwentySkeletonKindsHaveAVisibleNonemptyRow() {
        val path = File(requireNotNull(System.getProperty("projectionVectors"))).parentFile.resolve("skeletons.json")
        val corpus = Json.parseToJsonElement(path.readText()).jsonObject
        assertEquals(corpus.getValue("message").jsonPrimitive.content, messageTextForTest(UiMessages.sessionSkeleton()))
        assertEquals(corpus.getValue("note").jsonPrimitive.content, skeletonNote)
        val events = corpus.getValue("events").jsonArray.map { protoJson(Event.getDefaultInstance(), it.jsonObject) as Event }
        assertEquals(20, events.size)
        assertTrue(events.all { isSkeleton(it) })
        for (agent in listOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE)) {
            val rows = buildChat(events, derive(events, agent), "test")
            assertEquals(20, rows.size)
            assertTrue(rows.all { it is ChatItem.Agent && it.text == messageTextForTest(UiMessages.sessionSkeleton()) })
        }
        assertTrue(isSkeleton(events[8].approvalRequested))
        assertTrue(isSkeleton(events[10].questionAsked))
        assertTrue(isSkeleton(events[14].planProposed.text))
    }
}
