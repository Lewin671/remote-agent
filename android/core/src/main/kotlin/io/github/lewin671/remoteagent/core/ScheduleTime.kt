package io.github.lewin671.remoteagent.core

import java.time.*
import kotlinx.serialization.json.*

/** A time editor only replaces the trigger after explicit completion. Other spec fields survive. */
data class ScheduleTime(
    val kind: Int = 1,
    val time: String = "08:00",
    val date: String = LocalDate.now().plusDays(1).toString(),
    val zone: String = ZoneId.systemDefault().id,
    val weekdays: Set<Int> = setOf(1),
    val workdays: Boolean = false,
    val interval: String = "1",
    val unit: Int = 1,
    val end: Int = 0,
    val count: String = "",
    val until: String = LocalDate.now().plusMonths(1).toString(),
    val deadline: String = "",
    val original: JsonObject? = null,
) {
    /** Cutoffs are instants; only the main schedule's wall clock stays fixed across zones. */
    fun inZone(next: String): ScheduleTime {
        val instant = scheduleInstant(deadline) ?: runCatching {
            LocalDate.parse(until).plusDays(1).atStartOfDay(ZoneId.of(zone)).toInstant().minusSeconds(1)
        }.getOrNull()
        val local = runCatching { instant?.atZone(ZoneId.of(next))?.toLocalDate()?.toString() }.getOrNull()
        return copy(zone = next, until = local ?: until, deadline = if (deadline.isNotEmpty()) deadline else instant?.toString().orEmpty())
    }
    fun trigger(now: Instant = Instant.now()): JsonObject {
        val tz = ZoneId.of(zone)
        val clock = LocalTime.parse(time)
        val timestamp = LocalDate.parse(date).atTime(clock).atZone(tz).toInstant()
        val max = if (end == 1) count.toLong().also { require(it > 0) { "次数要大于 0" } } else 0
        val deadline = if (end == 2) (if (deadline.isNotEmpty()) Instant.parse(deadline) else LocalDate.parse(until).plusDays(1).atStartOfDay(tz).toInstant().minusSeconds(1)).also { require(it > now) { "结束日期已经过去" } } else null
        return buildJsonObject {
            when (kind) {
                0 -> { require(timestamp > now) { "请选择未来的时间" }; put("type", "at"); put("at", timestamp.toString()) }
                1, 2 -> {
                    put("type", "calendar"); put("time", clock.toString()); put("timezone", tz.id)
                    val days = if (kind == 1) { if (workdays) (1..5).toSet() else emptySet() } else weekdays.also { require(it.isNotEmpty()) { "至少选一天" } }
                    if (days.isNotEmpty()) put("weekdays", JsonArray(days.sorted().map(::JsonPrimitive)))
                }
                3 -> {
                    val seconds = Math.multiplyExact(interval.toLong(), listOf(60L, 3600L, 86400L, 1L)[unit])
                    require(seconds > 0) { "间隔要大于 0" }
                    put("type", "interval"); put("interval_seconds", seconds)
                    if (original?.text("type") == "interval" && original.long("interval_seconds") == seconds) {
                        original["at"]?.let { put("at", it) }; original["delay_seconds"]?.let { put("delay_seconds", it) }
                    } else put("delay_seconds", seconds)
                }
                else -> error("请选择时间类型")
            }
            if (kind != 0) {
                val before = original?.let { from(it) }
                if (before != null && end == before.end) {
                    original?.get("max_runs")?.let { put("max_runs", it) }; original?.get("until")?.let { put("until", it) }
                    if (end == 1 && count != before.count) put("max_runs", max)
                    if (end == 2 && (this@ScheduleTime.deadline != before.deadline || this@ScheduleTime.deadline.isEmpty() && until != before.until) && deadline != null) put("until", deadline.toString())
                } else { if (max > 0) put("max_runs", max); if (deadline != null) put("until", deadline.toString()) }
            }
        }
    }
    companion object {
        fun from(t: JsonObject): ScheduleTime {
            val z = scheduleZone(t)
            val at = scheduleInstant(t.text("at"))?.atZone(z)
            val days = (t["weekdays"] as? JsonArray).orEmpty().mapNotNull { it.jsonPrimitive.intOrNull }.toSet()
            val secs = t.long("interval_seconds")
            val unit = when { secs == 0L -> 1; secs > 0 && secs % 86400 == 0L -> 2; secs > 0 && secs % 3600 == 0L -> 1; secs % 60 != 0L -> 3; else -> 0 }
            return ScheduleTime(kind = when(t.text("type")) { "at" -> 0; "calendar" -> if (days.isEmpty() || days == (1..5).toSet()) 1 else 2; "interval" -> 3; else -> 1 },
                time = t.text("time").ifEmpty { at?.toLocalTime()?.toString()?.take(5) ?: "08:00" },
                date = at?.toLocalDate()?.toString() ?: LocalDate.now(z).plusDays(1).toString(), zone = z.id,
                weekdays = days.ifEmpty { setOf(1) }, workdays = days == (1..5).toSet(),
                interval = if (secs > 0) (secs / listOf(60L,3600L,86400L,1L)[unit]).coerceAtLeast(1).toString() else "1", unit = unit,
                end = if (t.long("max_runs") > 0) 1 else if (t.text("until").isNotEmpty()) 2 else 0,
                count = t.long("max_runs").takeIf { it > 0 }?.toString().orEmpty(),
                original = t, deadline = t.text("until"), until = scheduleInstant(t.text("until"))?.atZone(z)?.toLocalDate()?.toString() ?: LocalDate.now(z).plusMonths(1).toString())
        }
    }
}
