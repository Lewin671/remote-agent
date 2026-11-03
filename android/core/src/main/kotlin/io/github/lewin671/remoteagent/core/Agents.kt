package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentDefaults
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.MachineMeta

interface AgentEntry {
    val agent: Agent
    val id: String
    val name: String
    val quotaWindows: List<String>
    fun planName(raw: String): String
    fun modelLabel(id: String): String
    val toolLabelFromItemType: Boolean
    val planClearContext: Boolean
    val planApprovalReturnsToAsk: Boolean
    val standardContextOption: Boolean
    val trustFolderHint: String?
    val usageHint: String?
    val reconcilesUnclaimedInput: Boolean
    val sessionNeedsModelList: Boolean
    fun defaults(settings: DaemonSettings?): AgentDefaults
    fun withDefaults(
        settings: DaemonSettings,
        model: String? = null,
        effort: String? = null,
        mode: ApprovalMode? = null,
    ): DaemonSettings
    fun sheetTitle(what: String): String
}

fun normalizePlanName(plan: String): String = when (plan.lowercase()) {
    "prolite" -> "Pro Lite"
    "claude_max" -> "Max"
    "claude_pro" -> "Pro"
    "claude_team" -> "Team"
    "claude_enterprise" -> "Enterprise"
    else -> plan.replace(Regex("^claude_", RegexOption.IGNORE_CASE), "")
        .split(Regex("[_\\s]+"))
        .filter { it.isNotEmpty() }
        .joinToString(" ") { it.replaceFirstChar { c -> if (c.isLowerCase()) c.titlecase() else c.toString() } }
}

fun gptSlugLabel(id: String): String =
    id.split("-")
        .joinToString("-") { word ->
            if (word.equals("gpt", ignoreCase = true)) "GPT"
            else word.replaceFirstChar { if (it.isLowerCase()) it.titlecase() else it.toString() }
        }

private val CODEX_ENTRY: AgentEntry = object : AgentEntry {
    override val agent: Agent = Agent.AGENT_CODEX
    override val id: String = "codex"
    override val name: String = "Codex"
    override val quotaWindows: List<String> = listOf("primary", "secondary")

    override fun planName(raw: String): String = normalizePlanName(raw)

    override fun modelLabel(id: String): String = gptSlugLabel(id)

    override val toolLabelFromItemType: Boolean = false
    override val planClearContext: Boolean = true
    override val planApprovalReturnsToAsk: Boolean = false
    override val standardContextOption: Boolean = false
    override val trustFolderHint: String? = null
    override val usageHint: String? = null
    override val reconcilesUnclaimedInput: Boolean = false
    override val sessionNeedsModelList: Boolean = false

    override fun defaults(settings: DaemonSettings?): AgentDefaults =
        if (settings == null) AgentDefaults.getDefaultInstance()
        else AgentDefaults.newBuilder()
            .setModel(settings.defaultModel)
            .setEffort(settings.defaultEffort)
            .setMode(settings.defaultMode)
            .build()

    override fun withDefaults(
        settings: DaemonSettings,
        model: String?,
        effort: String?,
        mode: ApprovalMode?,
    ): DaemonSettings {
        val b = settings.toBuilder()
        if (model != null) b.setDefaultModel(model)
        if (effort != null) b.setDefaultEffort(effort)
        if (mode != null) b.setDefaultMode(mode)
        return b.build()
    }

    override fun sheetTitle(what: String): String = what
}

private val CLAUDE_ENTRY: AgentEntry = object : AgentEntry {
    override val agent: Agent = Agent.AGENT_CLAUDE
    override val id: String = "claude"
    override val name: String = "Claude Code"
    override val quotaWindows: List<String> = listOf("five_hour", "seven_day")

    override fun planName(raw: String): String = normalizePlanName(raw)

    override fun modelLabel(id: String): String = id

    override val toolLabelFromItemType: Boolean = true
    override val planClearContext: Boolean = false
    override val planApprovalReturnsToAsk: Boolean = true
    override val standardContextOption: Boolean = true
    override val trustFolderHint: String? = "默认选中的是 No, exit。按 ↓ 选中 Yes, I trust this folder 再按 ⏎"
    override val usageHint: String? = " 可在本机 Claude Code 中查看 /usage。"
    override val reconcilesUnclaimedInput: Boolean = true
    override val sessionNeedsModelList: Boolean = true

    override fun defaults(settings: DaemonSettings?): AgentDefaults =
        if (settings != null && settings.hasClaude()) settings.claude
        else AgentDefaults.getDefaultInstance()

    override fun withDefaults(
        settings: DaemonSettings,
        model: String?,
        effort: String?,
        mode: ApprovalMode?,
    ): DaemonSettings {
        val cur = defaults(settings)
        val cb = cur.toBuilder()
        if (model != null) cb.setModel(model)
        if (effort != null) cb.setEffort(effort)
        if (mode != null) cb.setMode(mode)
        return settings.toBuilder().setClaude(cb.build()).build()
    }

    override fun sheetTitle(what: String): String = "$name · $what"
}

private val UNKNOWN_ENTRY: AgentEntry = object : AgentEntry {
    override val agent: Agent = Agent.AGENT_UNSPECIFIED
    override val id: String = ""
    override val name: String = "未知 CLI"
    override val quotaWindows: List<String> = emptyList()

    override fun planName(raw: String): String = raw

    override fun modelLabel(id: String): String = id

    override val toolLabelFromItemType: Boolean = false
    override val planClearContext: Boolean = false
    override val planApprovalReturnsToAsk: Boolean = false
    override val standardContextOption: Boolean = false
    override val trustFolderHint: String? = null
    override val usageHint: String? = null
    override val reconcilesUnclaimedInput: Boolean = false
    override val sessionNeedsModelList: Boolean = false

    override fun defaults(settings: DaemonSettings?): AgentDefaults =
        AgentDefaults.getDefaultInstance()

    override fun withDefaults(
        settings: DaemonSettings,
        model: String?,
        effort: String?,
        mode: ApprovalMode?,
    ): DaemonSettings = settings

    override fun sheetTitle(what: String): String = what
}

val AGENTS: List<AgentEntry> = listOf(CODEX_ENTRY, CLAUDE_ENTRY)

val DEFAULT_AGENT: Agent = CODEX_ENTRY.agent

fun isKnownAgent(a: Agent?): Boolean = AGENTS.any { it.agent == a }

fun agentInfo(a: Agent? = null): AgentEntry {
    if (a == null || a == Agent.AGENT_UNSPECIFIED) {
        return CODEX_ENTRY
    }
    return AGENTS.firstOrNull { it.agent == a } ?: UNKNOWN_ENTRY
}

fun agentById(id: String?): AgentEntry {
    if (id.isNullOrEmpty()) return UNKNOWN_ENTRY
    return AGENTS.firstOrNull { it.id == id } ?: UNKNOWN_ENTRY
}

fun agentName(a: Agent? = null): String = agentInfo(a).name

fun defaultsFor(settings: DaemonSettings?, agent: Agent): AgentDefaults =
    agentInfo(agent).defaults(settings)

fun withDefaults(
    settings: DaemonSettings,
    agent: Agent,
    model: String? = null,
    effort: String? = null,
    mode: ApprovalMode? = null,
): DaemonSettings = agentInfo(agent).withDefaults(settings, model, effort, mode)

fun machineVersions(meta: MachineMeta?): List<AgentInfo> {
    if (meta == null) return emptyList()
    val primary = meta.agentsList.firstOrNull { it.agent == CODEX_ENTRY.agent }
        ?: if (meta.codexVersion.isNotEmpty() || meta.agentsList.isEmpty()) {
            AgentInfo.newBuilder()
                .setAgent(CODEX_ENTRY.agent)
                .setVersion(meta.codexVersion)
                .setVerified(meta.codexVerified)
                .build()
        } else null
    val others = meta.agentsList.filter { it.agent != CODEX_ENTRY.agent }
    return listOfNotNull(primary) + others
}
