package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Status
import org.junit.Assert.assertEquals
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class SessionSubtitleTest {
    @org.junit.Before fun sessionMessageContext() = io.github.lewin671.remoteagent.ui.installSessionMessageContext()
    @Test fun unknownModeLeavesOnlyTheStatus() {
        Status.entries.forEach { assertEquals(it.text, sessionSubtitle(it, null)) }
        assertEquals(Status.RUNNING.text + " · 询问", sessionSubtitle(Status.RUNNING, "询问"))
        assertEquals(Status.EXITED.text, sessionSubtitle(Status.EXITED, "询问"))
    }

    @Test fun theMenuNamesTheMachineOnlyWhenThereIsMoreThanOne() {
        fun machine(id: String) = io.github.lewin671.remoteagent.core.MachineView(id, true, 0, io.github.lewin671.remoteagent.proto.MachineMeta.newBuilder().setHostname("MacBook-$id").build())
        val session = io.github.lewin671.remoteagent.core.SessionView(
            "s", "a", io.github.lewin671.remoteagent.proto.Session.newBuilder().setCwd("/work/foo").setAgent(io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX).build(),
        )
        val one = io.github.lewin671.remoteagent.core.RemoteState(machines = mapOf("a" to machine("a")))
        val two = io.github.lewin671.remoteagent.core.RemoteState(machines = mapOf("a" to machine("a"), "b" to machine("b")))
        assertEquals("Codex · foo", sessionMenuTitle(one, session))
        assertEquals("Codex · foo · MacBook-a", sessionMenuTitle(two, session))
    }
}
