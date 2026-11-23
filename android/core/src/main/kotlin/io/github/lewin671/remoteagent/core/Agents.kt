package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent

val DEFAULT_AGENT: Agent = Agent.AGENT_CODEX

fun agentName(a: Agent? = null, copy: MessageResolver): String = when (a) {
    null, Agent.AGENT_UNSPECIFIED, Agent.AGENT_CODEX -> "Codex"
    Agent.AGENT_CLAUDE -> "Claude Code"
    Agent.AGENT_ANTIGRAVITY -> "Antigravity"
    else -> copy(UiMessages.modelsUnknownCli())
}
