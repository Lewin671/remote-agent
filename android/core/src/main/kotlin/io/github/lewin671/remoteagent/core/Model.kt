// Remaining shell-side display helpers not yet retired by clientcore.
package io.github.lewin671.remoteagent.core

import java.time.Instant
import java.time.ZoneId
import java.time.ZonedDateTime

fun machineName(m: MachineView?, id: String = "", copy: MessageResolver): String =
    m?.meta?.hostname?.removeSuffix(".local")?.takeIf { it.isNotEmpty() }
        ?: m?.id?.take(8)
        ?: id.take(8).ifEmpty { copy(UiMessages.commonUnknownComputer()) }

/** Table rows in list previews keep their words, without pipes or alignment rows. */
private fun previewTableText(text: String): String {
    val lines = text.split("\n")
    val pipe = Regex("(?<!\\\\)\\|")
    fun cells(line: String) = line.trim().removePrefix("|")
        .replace(Regex("(?<!\\\\)\\|$"), "").split(pipe).map { it.trim() }
    fun separator(line: String) = '|' in line && cells(line).all { it.matches(Regex(":?-{3,}:?")) }
    var inTable = false
    return lines.mapIndexed { i, line ->
        if (separator(line)) {
            inTable = true
            ""
        } else if ('|' in line && (line.trimStart().startsWith("|") ||
                Regex("(?<!\\\\)\\|\\s*$").containsMatchIn(line) || inTable || separator(lines.getOrNull(i + 1).orEmpty()))) {
            cells(line).filter { it.isNotEmpty() }.joinToString("、") { it.replace("\\|", "|") }
        } else {
            inTable = false
            line
        }
    }.joinToString("\n")
}

/** Text for one-line previews: Markdown marks and the CLIs' image placeholders removed. */
fun plain(text: String): String =
    previewTableText(text)
        .replace(Regex("\\[Image #\\d+]\\s*"), "")
        .replace(Regex("```[a-zA-Z0-9]*"), "")
        .replace(Regex("`([^`]*)`"), "$1")
        .replace(Regex("\\*\\*([^*]+)\\*\\*"), "$1")
        .replace(Regex("\\[([^\\]]+)\\]\\([^)]*\\)"), "$1")
        .replace(Regex("(?m)^\\s{0,3}(#{1,6}\\s+|[-*+]\\s+|>\\s?)"), "")
        .replace(Regex("\\s+"), " ")
        .trim()

fun hhmm(ms: Long, zone: ZoneId = ZoneId.systemDefault()): String {
    val t = ZonedDateTime.ofInstant(Instant.ofEpochMilli(ms), zone)
    return "${t.hour}:${"%02d".format(t.minute)}"
}

/** 2:12, 1:02:03: a running timer. */
fun clock(ms: Long): String {
    val s = ms / 1000
    return if (s >= 3600) "%d:%02d:%02d".format(s / 3600, s % 3600 / 60, s % 60)
    else "%d:%02d".format(s / 60, s % 60)
}

fun shortPath(p: String): String =
    p.replace(Regex("^/Users/[^/]+"), "~").replace(Regex("^/home/[^/]+"), "~")

// --- devices and where actions came from ---

/** The account name, or a readable platform name disambiguated across all clients. */
fun clientName(d: DeviceView?, id: String, clients: Collection<DeviceView> = emptyList(), copy: MessageResolver): String =
    d?.meta?.name.orEmpty().trim().ifEmpty { defaultClientName(d, id, clients, copy = copy) }

private sealed interface ClientBase {
    data class Raw(val value: String) : ClientBase
    data object Browser : ClientBase
    data object Client : ClientBase

    fun display(copy: MessageResolver): String = when (this) {
        is Raw -> value
        Browser -> copy(UiMessages.securityBrowser())
        Client -> copy(UiMessages.securityClient())
    }
}

private fun clientBase(d: DeviceView?): ClientBase {
    val meta = d?.meta
    val description = meta?.description.orEmpty().trim()
    val model = description.substringAfter(" · ", "").trim()
    return when (meta?.platform) {
        "web" -> description.replace(" on ", " · ").takeIf { it.isNotEmpty() }?.let(ClientBase::Raw) ?: ClientBase.Browser
        "android" -> ClientBase.Raw("Android" + if (model.isNotEmpty()) " · $model" else "")
        "ios" -> ClientBase.Raw(model.ifEmpty { "iOS" })
        else -> ClientBase.Client
    }
}

/** Compare raw metadata / fallback identities, including clients with a user-supplied name. */
fun defaultClientName(d: DeviceView?, id: String, clients: Collection<DeviceView> = emptyList(), copy: MessageResolver): String {
    val base = clientBase(d)
    val duplicate = clients.any { it.kind == io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT && it.id != id && clientBase(it) == base }
    return base.display(copy) + if (duplicate) " · ${shortId(id)}" else ""
}

/** The last four hex digits of a device ID, as shown next to a name. */
private fun shortId(id: String): String = id.takeLast(4)

/** What the machine says it offers; null until its metadata is read. */
fun capabilities(m: MachineView?): io.github.lewin671.remoteagent.proto.MachineCapabilities? =
    m?.pathState?.capabilities

/**
 * Why no command goes to [m]: it speaks another protocol version than this app (PEER_PROTOCOL_VERSION),
 * said by which side to update; null when they match or the machine's metadata is not read yet.
 */
fun protocolMismatch(m: MachineView?): MessageText? {
    val theirs = m?.pathState?.protocolVersion ?: return null
    return when {
        theirs < PEER_PROTOCOL_VERSION -> localized { copy -> copy(UiMessages.errorsRemoteAgentOnIsOutOfDateUpdateIt(listMachineName(m).resolve(copy))) }
        theirs > PEER_PROTOCOL_VERSION -> UiMessages.commonAndroidThisAppVersionIsOldUpdateAndRetry().messageText()
        else -> null
    }
}
