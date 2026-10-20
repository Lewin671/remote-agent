package io.github.lewin671.remoteagent.core

import java.time.Instant
import java.time.ZoneId

/** Chinese calendar labels and a zero-padded 24-hour clock, regardless of system locale. */
fun securityTime(ms: Long, now: Long = System.currentTimeMillis(), timeOnly: Boolean = false, zone: ZoneId = ZoneId.systemDefault()): String {
    if (ms <= 0) return "尚未读取"
    val date = Instant.ofEpochMilli(ms).atZone(zone)
    val clock = "${date.hour.toString().padStart(2, '0')}:${date.minute.toString().padStart(2, '0')}"
    if (timeOnly) return clock
    val today = Instant.ofEpochMilli(now).atZone(zone).toLocalDate()
    val label = when (date.toLocalDate()) {
        today -> "今天"
        today.plusDays(1) -> "明天"
        else -> "${date.monthValue}月${date.dayOfMonth}日"
    }
    return "$label $clock"
}
