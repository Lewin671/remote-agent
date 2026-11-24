// Remaining shell-side display helpers not yet retired by clientcore.
package io.github.lewin671.remoteagent.core

import java.time.Instant
import java.time.ZoneId
import java.time.ZonedDateTime

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
