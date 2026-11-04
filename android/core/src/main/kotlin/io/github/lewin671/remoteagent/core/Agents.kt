package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentDefaults
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.DaemonSettings
import io.github.lewin671.remoteagent.proto.MachineMeta

data class QuotaWindowDescriptor(
    val label: String,
    val group: String? = null,
)

interface AgentEntry {
    val agent: Agent
    val id: String
    val name: String
    val quotaWindows: List<String>
    fun quotaWindow(name: String): QuotaWindowDescriptor
    fun planName(raw: String): String
    fun modelLabel(id: String): String
    val toolLabelFromItemType: Boolean
    val planClearContext: Boolean
    val planKeepLabel: String
    val planApproveLabel: String
    val planNote: String?
    val standardContextOption: Boolean
    val autoMayBeUnavailable: Boolean
    val trustFolderHint: String?
    val usageHint: String?
    val reconcilesUnclaimedInput: Boolean
    val sessionNeedsModelList: Boolean
    val switchesModelInSession: Boolean
    val forks: Boolean
    fun defaults(settings: DaemonSettings?): AgentDefaults
    fun withDefaults(
        settings: DaemonSettings,
        model: String? = null,
        effort: String? = null,
        mode: ApprovalMode? = null,
    ): DaemonSettings
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

private fun fixedWindowLabel(name: String): QuotaWindowDescriptor = when (name) {
    "primary", "five_hour" -> QuotaWindowDescriptor("5 小时")
    "secondary", "seven_day" -> QuotaWindowDescriptor("7 天")
    else -> QuotaWindowDescriptor(name)
}

private val ANTIGRAVITY_QUOTA_WINDOWS = mapOf(
    "gemini-5h" to QuotaWindowDescriptor("Gemini · 5 小时", "Gemini"),
    "gemini-weekly" to QuotaWindowDescriptor("Gemini · 7 天", "Gemini"),
    "gemini-argon-wtu-weekly" to QuotaWindowDescriptor("Gemini 4 Argon · 7 天", "Gemini"),
    "3p-5h" to QuotaWindowDescriptor("第三方模型 · 5 小时", "第三方模型（Claude、GPT-OSS）"),
    "3p-weekly" to QuotaWindowDescriptor("第三方模型 · 7 天", "第三方模型（Claude、GPT-OSS）"),
)

private val CODEX_ENTRY: AgentEntry = object : AgentEntry {
    override val agent: Agent = Agent.AGENT_CODEX
    override val id: String = "codex"
    override val name: String = "Codex"
    override val quotaWindows: List<String> = listOf("primary", "secondary")

    override fun quotaWindow(name: String): QuotaWindowDescriptor = fixedWindowLabel(name)

    override fun planName(raw: String): String = normalizePlanName(raw)

    override fun modelLabel(id: String): String = gptSlugLabel(id)

    override val toolLabelFromItemType: Boolean = false
    override val planClearContext: Boolean = true
    override val planKeepLabel = "继续规划…"
    override val planApproveLabel = "批准执行"
    override val planNote: String? = null
    override val standardContextOption: Boolean = false
    override val autoMayBeUnavailable: Boolean = false
    override val trustFolderHint: String? = null
    override val usageHint: String? = null
    override val reconcilesUnclaimedInput: Boolean = false
    override val sessionNeedsModelList: Boolean = false
    override val switchesModelInSession: Boolean = true
    override val forks: Boolean = true

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
}

private val CLAUDE_ENTRY: AgentEntry = object : AgentEntry {
    override val agent: Agent = Agent.AGENT_CLAUDE
    override val id: String = "claude"
    override val name: String = "Claude Code"
    override val quotaWindows: List<String> = listOf("five_hour", "seven_day")

    override fun quotaWindow(name: String): QuotaWindowDescriptor = fixedWindowLabel(name)

    override fun planName(raw: String): String = normalizePlanName(raw)

    override fun modelLabel(id: String): String = id

    override val toolLabelFromItemType: Boolean = true
    override val planClearContext: Boolean = false
    override val planKeepLabel = "修改计划…"
    override val planApproveLabel = "批准"
    override val planNote: String? = "批准后使用询问模式"
    override val standardContextOption: Boolean = true
    override val autoMayBeUnavailable: Boolean = true
    override val trustFolderHint: String? = "默认选中的是 No, exit。按 ↓ 选中 Yes, I trust this folder 再按 ⏎"
    override val usageHint: String? = " 可在本机 Claude Code 中查看 /usage。"
    override val reconcilesUnclaimedInput: Boolean = true
    override val sessionNeedsModelList: Boolean = true
    override val switchesModelInSession: Boolean = true
    override val forks: Boolean = true

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
}

private val ANTIGRAVITY_ENTRY: AgentEntry = object : AgentEntry {
    override val agent: Agent = Agent.AGENT_ANTIGRAVITY
    override val id: String = "agy"
    override val name: String = "Antigravity"
    override val quotaWindows: List<String> = emptyList()

    override fun quotaWindow(name: String): QuotaWindowDescriptor =
        ANTIGRAVITY_QUOTA_WINDOWS[name] ?: QuotaWindowDescriptor(name, name)

    override fun planName(raw: String): String = raw

    override fun modelLabel(id: String): String = id

    override val toolLabelFromItemType: Boolean = false
    override val planClearContext: Boolean = false
    override val planKeepLabel = "修改计划…"
    override val planApproveLabel = "批准"
    override val planNote: String? = "批准后回到默认模式（询问）"
    override val standardContextOption: Boolean = false
    override val autoMayBeUnavailable: Boolean = true
    override val trustFolderHint: String? = null
    override val usageHint: String? = null
    override val reconcilesUnclaimedInput: Boolean = false
    override val sessionNeedsModelList: Boolean = true
    override val switchesModelInSession: Boolean = false
    override val forks: Boolean = false

    override fun defaults(settings: DaemonSettings?): AgentDefaults =
        if (settings != null && settings.hasAntigravity()) settings.antigravity
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
        return settings.toBuilder().setAntigravity(cb.build()).build()
    }
}

private class UnknownAgentEntry(override val agent: Agent) : AgentEntry {
    override val id: String = ""
    override val name: String = "未知 CLI"
    override val quotaWindows: List<String> = emptyList()

    override fun quotaWindow(name: String): QuotaWindowDescriptor = QuotaWindowDescriptor(name)

    override fun planName(raw: String): String = raw

    override fun modelLabel(id: String): String = id

    override val toolLabelFromItemType: Boolean = false
    override val planClearContext: Boolean = false
    override val planKeepLabel = "修改计划…"
    override val planApproveLabel = "批准"
    override val planNote: String? = null
    override val standardContextOption: Boolean = false
    override val autoMayBeUnavailable: Boolean = false
    override val trustFolderHint: String? = null
    override val usageHint: String? = null
    override val reconcilesUnclaimedInput: Boolean = false
    override val sessionNeedsModelList: Boolean = false
    override val switchesModelInSession: Boolean = false
    override val forks: Boolean = false

    override fun defaults(settings: DaemonSettings?): AgentDefaults =
        AgentDefaults.getDefaultInstance()

    override fun withDefaults(
        settings: DaemonSettings,
        model: String?,
        effort: String?,
        mode: ApprovalMode?,
    ): DaemonSettings = settings
}

private val UNKNOWN_ENTRY: AgentEntry = UnknownAgentEntry(Agent.AGENT_UNSPECIFIED)

val AGENTS: List<AgentEntry> = listOf(CODEX_ENTRY, CLAUDE_ENTRY, ANTIGRAVITY_ENTRY)

val DEFAULT_AGENT: Agent = CODEX_ENTRY.agent

fun isKnownAgent(a: Agent?): Boolean = AGENTS.any { it.agent == a }

fun agentInfo(a: Agent? = null): AgentEntry {
    if (a == null || a == Agent.AGENT_UNSPECIFIED) {
        return CODEX_ENTRY
    }
    return AGENTS.firstOrNull { it.agent == a } ?: UnknownAgentEntry(a)
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
