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
            cutoff(ZoneId.of(zone))
        }.getOrNull()
        val local = runCatching { instant?.atZone(ZoneId.of(next))?.toLocalDate()?.toString() }.getOrNull()
        return copy(zone = next, until = local ?: until, deadline = if (deadline.isNotEmpty()) deadline else instant?.toString().orEmpty())
    }
    fun trigger(now: Instant = Instant.now()): JsonObject {
        val tz = ZoneId.of(zone)
        val clock = LocalTime.parse(time)
        // A one-shot trigger carries no limits, so hidden ending controls are not validated.
        val max = if (kind != 0 && end == 1) count.toLong().also { requireMessage(it > 0) { UiMessages.scheduleAndroidRunCountMustBeGreaterThan0().messageText() } } else 0
        val deadline = if (kind != 0 && end == 2) (if (deadline.isNotEmpty()) Instant.parse(deadline) else cutoff(tz)).also { requireMessage(it > now) { UiMessages.scheduleAndroidTheEndDateIsInThePast().messageText() } } else null
        return buildJsonObject {
            when (kind) {
                0 -> {
                    val timestamp = absolute(LocalDate.parse(date).atTime(clock), tz)
                    requireMessage(timestamp > now) { UiMessages.scheduleSelectAFutureTime().messageText() }; put("type", "at"); put("at", timestamp.toString())
                }
                1, 2 -> {
                    put("type", "calendar"); put("time", clock.toString()); put("timezone", tz.id)
                    val days = if (kind == 1) { if (workdays) (1..5).toSet() else emptySet() } else weekdays.also { requireMessage(it.isNotEmpty()) { UiMessages.scheduleAndroidSelectAtLeastOneDay().messageText() } }
                    if (days.isNotEmpty()) put("weekdays", JsonArray(days.sorted().map(::JsonPrimitive)))
                }
                3 -> {
                    val seconds = Math.multiplyExact(interval.toLong(), listOf(60L, 3600L, 86400L, 1L)[unit])
                    requireMessage(seconds > 0) { UiMessages.scheduleAndroidIntervalMustBeGreaterThan0().messageText() }
                    put("type", "interval"); put("interval_seconds", seconds)
                    if (original?.text("type") == "interval" && original.long("interval_seconds") == seconds) {
                        original["at"]?.let { put("at", it) }; original["delay_seconds"]?.let { put("delay_seconds", it) }
                    } else put("delay_seconds", seconds)
                }
                else -> messageError(UiMessages.scheduleAndroidChooseATimeType().messageText())
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
    private fun cutoff(tz: ZoneId): Instant = absolute(LocalDate.parse(until).atTime(23, 59, 59), tz)

    private fun absolute(local: LocalDateTime, tz: ZoneId): Instant {
        val offsets = tz.rules.getValidOffsets(local)
        requireMessage(offsets.isNotEmpty()) { UiMessages.scheduleDaylightSavingTimeSkipsThisTimeInTheSelected().messageText() }
        // ZoneRules lists the earlier occurrence first when the clock repeats.
        return local.toInstant(offsets.first())
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
