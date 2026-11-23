package io.github.lewin671.remoteagent.core

import java.time.Instant
import java.time.ZoneId

fun listProjectName(key: String): MessageText = key.split("/").lastOrNull { it.isNotEmpty() }?.messageText()
    ?: UiMessages.projectsUnknown().messageText()

fun listMachineName(m: MachineView?, id: String = ""): MessageText =
    (m?.meta?.hostname?.removeSuffix(".local")?.takeIf { it.isNotEmpty() } ?: m?.id?.take(8) ?: id.take(8).takeIf { it.isNotEmpty() })?.messageText()
        ?: UiMessages.commonUnknownComputer().messageText()

fun listWhen(ms: Long, now: Long, zone: ZoneId = ZoneId.systemDefault()): MessageText {
    if (ms <= 0) return "".messageText()
    val elapsed = now - ms
    if (elapsed < 60_000) return UiMessages.timeJustNow().messageText()
    if (elapsed < 3_600_000) return UiMessages.timeMinutesAgo((elapsed / 60_000).toInt()).messageText()
    val then = Instant.ofEpochMilli(ms).atZone(zone).toLocalDate()
    val today = Instant.ofEpochMilli(now).atZone(zone).toLocalDate()
    if (then == today) return hhmm(ms, zone).messageText()
    if (then == today.minusDays(1)) return UiMessages.timeYesterday().messageText()
    return UiMessages.timeMonthDay(then.dayOfMonth, then.monthValue).messageText()
}
