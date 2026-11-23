package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.SendMode

val DEFAULT_AGENT: Agent = Agent.AGENT_CODEX

fun agentName(a: Agent? = null, copy: MessageResolver): String = when (a) {
    null, Agent.AGENT_UNSPECIFIED, Agent.AGENT_CODEX -> "Codex"
    Agent.AGENT_CLAUDE -> "Claude Code"
    Agent.AGENT_ANTIGRAVITY -> "Antigravity"
    else -> copy(UiMessages.modelsUnknownCli())
}

fun sendModeFor(agent: Agent, requested: SendMode = SendMode.SEND_MODE_STEER, running: Boolean): SendMode =
    if (running && agent != Agent.AGENT_UNSPECIFIED && agent != Agent.AGENT_CODEX && agent != Agent.AGENT_CLAUDE) SendMode.SEND_MODE_QUEUE else requested
