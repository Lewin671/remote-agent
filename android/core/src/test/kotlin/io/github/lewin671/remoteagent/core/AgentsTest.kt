package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import io.github.lewin671.remoteagent.proto.Agent
import org.junit.Test

class AgentsTest {
    @Test
    fun namesKnownUnspecifiedAndUnknownAgents() {
        assertEquals(Agent.AGENT_CODEX, DEFAULT_AGENT)
        assertEquals("Codex", agentName(Agent.AGENT_CODEX, copy = ::messageTextForTest))
        assertEquals("Claude Code", agentName(Agent.AGENT_CLAUDE, copy = ::messageTextForTest))
        assertEquals("Antigravity", agentName(Agent.AGENT_ANTIGRAVITY, copy = ::messageTextForTest))
        assertEquals("Codex", agentName(Agent.AGENT_UNSPECIFIED, copy = ::messageTextForTest))
        assertEquals("Codex", agentName(null, copy = ::messageTextForTest))
        assertEquals("Codex", agentName(copy = ::messageTextForTest))
        assertEquals(messageTextForTest(UiMessages.modelsUnknownCli()), agentName(Agent.UNRECOGNIZED, copy = ::messageTextForTest))
    }
}
