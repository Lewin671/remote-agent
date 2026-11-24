package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.core.proto.TextArg
import io.github.lewin671.remoteagent.core.proto.TimestampArg
import io.github.lewin671.remoteagent.core.proto.TimestampStyle
import java.time.Instant
import java.time.ZoneId

const val PROJECT_PREFS = "project-prefs"
const val LOGIN_REFRESH_MARGIN_MS = 86_400_000L
const val CACHE_EVENT_PAGE = 128

data class LoginToken(val value: String, val expiresMs: Long) {
    val token: String get() = value
    override fun toString(): String = "LoginToken(expiresMs=$expiresMs)"
}

fun formatTimestampToMessageText(
    arg: TimestampArg,
    zone: ZoneId = ZoneId.systemDefault(),
): MessageText {
    if (arg.unixMs <= 0) return "".messageText()
    val dt = Instant.ofEpochMilli(arg.unixMs).atZone(zone)
    val clock = String.format("%d:%02d", dt.hour, dt.minute)
    return when (arg.style) {
        TimestampStyle.TIMESTAMP_STYLE_TIME -> clock.messageText()
        TimestampStyle.TIMESTAMP_STYLE_WEEKDAY ->
            UiMessages.timeWeekday(dt.dayOfWeek.name.take(3).lowercase()).messageText()
        TimestampStyle.TIMESTAMP_STYLE_MONTH_DAY ->
            UiMessages.timeMonthDay(dt.dayOfMonth, dt.monthValue).messageText()
        TimestampStyle.TIMESTAMP_STYLE_MONTH_DAY_TIME -> localized { copy ->
            val day = copy(UiMessages.timeMonthDay(dt.dayOfMonth, dt.monthValue))
            copy(UiMessages.timeDayClock(clock = clock, day = day))
        }
        TimestampStyle.TIMESTAMP_STYLE_DATE ->
            UiMessages.timeCalendarDateYear(dt.dayOfMonth, dt.monthValue, dt.year).messageText()
        TimestampStyle.TIMESTAMP_STYLE_DATE_TIME -> localized { copy ->
            val day = copy(UiMessages.timeCalendarDateYear(dt.dayOfMonth, dt.monthValue, dt.year))
            copy(UiMessages.timeDayClock(clock = clock, day = day))
        }
        else -> "".messageText()
    }
}

fun coreTextToMessageText(t: CoreText?): MessageText {
    if (t == null) return "".messageText()
    if (t.joinCount > 0) {
        val sep = if (t.separatorKey.isNotEmpty()) UiMessage(t.separatorKey).messageText() else t.separatorLiteral.messageText()
        return localized { copy ->
            val s = sep.resolve(copy)
            t.joinList.map { coreTextToMessageText(it).resolve(copy) }.filter { it.isNotEmpty() }.joinToString(s)
        }
    }
    if (t.hasTimestamp()) {
        return formatTimestampToMessageText(t.timestamp)
    }
    if (t.key.isNotEmpty()) {
        return localized { copy ->
            val args = HashMap<String, Any>(t.argsCount)
            for (arg in t.argsList) {
                args[arg.name] = when (arg.valueCase) {
                    TextArg.ValueCase.INT_VALUE -> arg.intValue.toInt()
                    TextArg.ValueCase.FLOAT_VALUE -> arg.floatValue.toString()
                    TextArg.ValueCase.TEXT_VALUE -> coreTextToMessageText(arg.textValue).resolve(copy)
                    TextArg.ValueCase.TIMESTAMP_VALUE -> formatTimestampToMessageText(arg.timestampValue).resolve(copy)
                    TextArg.ValueCase.STR_VALUE -> arg.strValue
                    else -> ""
                }
            }
            copy(UiMessage(t.key, args))
        }
    }
    return t.literal.messageText()
}
