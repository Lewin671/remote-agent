package io.github.lewin671.remoteagent.core

import java.time.Instant
import java.time.ZoneId

/** Chinese calendar labels and a zero-padded 24-hour clock, regardless of system locale. */
fun securityTime(ms: Long, now: Long = System.currentTimeMillis(), timeOnly: Boolean = false, zone: ZoneId = ZoneId.systemDefault(), copy: MessageResolver): String {
    if (ms <= 0) return copy(UiMessages.securityAndroidNotReadYet())
    val date = Instant.ofEpochMilli(ms).atZone(zone)
    val clock = "${date.hour.toString().padStart(2, '0')}:${date.minute.toString().padStart(2, '0')}"
    if (timeOnly) return clock
    val today = Instant.ofEpochMilli(now).atZone(zone).toLocalDate()
    val label = when (date.toLocalDate()) {
        today -> copy(UiMessages.timeToday())
        today.plusDays(1) -> copy(UiMessages.timeTomorrow())
        else -> copy(UiMessages.timeMonthDay((date.dayOfMonth).toInt(), (date.monthValue).toInt()))
    }
    return copy(UiMessages.timeAndroidDayClock(clock, label))
}
