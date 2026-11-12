package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import androidx.compose.runtime.mutableStateMapOf
import io.github.lewin671.remoteagent.core.Derived
import io.github.lewin671.remoteagent.core.EventProjection
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.core.eventTime
import io.github.lewin671.remoteagent.core.unchangedPrefix
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.TurnOutcome
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

object ScheduleJump { val requested = mutableStateMapOf<String, Long>() }
fun scheduledMessageCaption(e: Event, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String? = e.origin.scheduledTaskId.takeIf { it.isNotEmpty() }?.let {
    copy(UiMessages.sessionScheduledAt(time = (Instant.ofEpochMilli(eventTime(e)).atZone(ZoneId.systemDefault()).format(DateTimeFormatter.ofPattern("HH:mm"))).toString()))
}

data class QuietSpan(val task: String, val run: String, val from: Long, val through: Long, val time: Long)

/** Only completed, explicitly identified runs may be folded. Human replies, failures,
 * interruptions, notifications and incomplete runs always remain visible. [derived] is what
 * `derive(events)` gives, e.g. kept up to date by an [EventProjection]. */
fun quietScheduledSpans(events: List<Event>, derived: Derived = derive(events)): List<QuietSpan> {
    val out = mutableListOf<QuietSpan>()
    val starts = events.indices.filter { events[it].hasUserMessage() }
    starts.forEachIndexed { i, start ->
        val first = events[start]
        val task = first.origin.scheduledTaskId
        val run = first.origin.scheduledRunId
        if (task.isEmpty() || run.isEmpty()) return@forEachIndexed
        val nextUser = starts.getOrNull(i + 1)
        var end = nextUser ?: events.size
        if (nextUser != null) {
            // Native CLIs record TurnStarted before the next UserMessage. That start belongs
            // to the next turn, rather than an unfinished follow-up of this one.
            val beforeNext = events.subList(start, nextUser)
            val started = beforeNext.indexOfLast { it.hasTurnStarted() }
            val ended = beforeNext.indexOfLast { it.hasTurnEnded() }
            if (ended >= 0 && started > ended) end = start + started
        }
        val body = events.subList(start, end)
        val ended = body.filter { it.hasTurnEnded() }
        if (ended.size != 1 || ended.any { it.turnEnded.scheduledTaskId != task || it.turnEnded.scheduledRunId != run || it.turnEnded.scheduledNotified || it.turnEnded.outcome != TurnOutcome.TURN_OUTCOME_COMPLETED }) return@forEachIndexed
        if (body.any { io.github.lewin671.remoteagent.core.isSkeleton(it) || it.hasToolCall() && (!toolOk(it.toolCall) || io.github.lewin671.remoteagent.core.wentToBackground(it.toolCall)) ||
            it.hasToolStarted() && (derived.completedAt[it.seq] ?: Long.MAX_VALUE) > ended.single().seq ||
            it.hasTurnContext() && derived.terminalChanges.containsKey(it.seq) ||
            it.hasSettingsChanged() || it.hasQuestionAnswered() || it.hasImageGenerated() || it.hasSessionForked() || it.hasScheduledNotification() || it.hasApprovalRequested() || it.hasQuestionAsked() || it.hasPlanProposed() }) return@forEachIndexed
        val afterEnd = body.drop(body.indexOfLast { it.hasTurnEnded() } + 1)
        if (afterEnd.any { it.hasAgentMessage() || it.hasReasoning() || it.hasToolStarted() || it.hasToolCall() }) return@forEachIndexed
        // A follow-up has started but has not yet ended: do not hide it.
        if (body.indexOfLast { it.hasTurnStarted() } > body.indexOfLast { it.hasTurnEnded() }) return@forEachIndexed
        out += QuietSpan(task, run, first.seq, ended.single().seq, eventTime(first))
    }
    return out
}

fun foldScheduledChat(items: List<ChatItem>, events: List<Event>, revealSeq: Long = 0): List<ChatItem> =
    foldSpans(items, quietScheduledSpans(events), revealSeq)

/** A span starts at a user message that names its scheduled task and run. */
private fun scheduledStart(e: Event) =
    e.hasUserMessage() && e.origin.scheduledTaskId.isNotEmpty() && e.origin.scheduledRunId.isNotEmpty()

/**
 * [foldScheduledChat] for a chat that keeps growing, with the same results. The state is derived
 * incrementally while the events only grow, nothing is derived while fewer than two runs could
 * fold, and the same input gives the same list without being worked out again. Not thread-safe:
 * one caller at a time, off the main thread.
 */
class ScheduledFold(private val derive: (List<Event>) -> Derived = EventProjection()::update) {
    private var counted: List<Event> = emptyList()
    private var starts = 0
    private var spansOf: List<Event>? = null
    private var spans: List<QuietSpan> = emptyList()
    private var lastItems: List<ChatItem>? = null
    private var lastReveal = 0L
    private var last: List<ChatItem> = emptyList()

    fun fold(items: List<ChatItem>, events: List<Event>, revealSeq: Long = 0): List<ChatItem> {
        if (items === lastItems && events === spansOf && revealSeq == lastReveal) return last
        if (events !== spansOf) {
            if (!unchangedPrefix(events, counted)) {
                counted = emptyList()
                starts = 0
            }
            for (i in counted.size until events.size) if (scheduledStart(events[i])) starts++
            counted = events
            // Each span starts at its own scheduled message: fewer than two cannot fold.
            spans = if (starts < 2) emptyList() else quietScheduledSpans(events, derive(events))
            spansOf = events
        }
        last = foldSpans(items, spans, revealSeq)
        lastItems = items
        lastReveal = revealSeq
        return last
    }
}

private fun foldSpans(items: List<ChatItem>, spans: List<QuietSpan>, revealSeq: Long): List<ChatItem> {
    if (spans.size < 2) return items
    val out = mutableListOf<ChatItem>()
    var at = 0
    while (at < items.size) {
        val item = items[at]
        val seq = item.key.removePrefix("e").toLongOrNull()
        val span = spans.firstOrNull { it.from == seq }
        if (span == null) { out += item; at++; continue }
        val covered = mutableListOf<ChatItem>()
        val times = mutableListOf<Long>()
        var end = span.through
        var next = at
        var current: QuietSpan = span
        while (true) {
            times += current.time
            while (next < items.size) {
                val n = items[next].key.removePrefix("e").toLongOrNull()
                if (n == null || n > current.through) break
                covered += items[next++]
            }
            end = current.through
            val following = items.getOrNull(next)?.key?.removePrefix("e")?.toLongOrNull()
            val another = spans.firstOrNull { it.task == span.task && it.from == following && Instant.ofEpochMilli(it.time).atZone(ZoneId.systemDefault()).toLocalDate() == Instant.ofEpochMilli(span.time).atZone(ZoneId.systemDefault()).toLocalDate() }
            if (another == null) break
            current = another
        }
        val before = items.getOrNull(at - 1)?.key?.removePrefix("e")?.toLongOrNull() ?: 0L
        if (times.size > 1 && !(revealSeq > before && revealSeq <= end)) out += ChatItem.QuietScheduled("quiet-${span.run}", covered, times, end)
        else out += covered
        at = if (next > at) next else at + 1
    }
    return out
}

/** Shared copy; the view keeps long clock lists to one ellipsized line. */
fun quietScheduledLabel(times: List<Long>, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault(), copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String {
    val dates = times.map { Instant.ofEpochMilli(it).atZone(zone).toLocalDate() }.distinct()
    fun time(ms: Long) = if (dates.size == 1) Instant.ofEpochMilli(ms).atZone(zone).format(DateTimeFormatter.ofPattern("HH:mm"))
        else io.github.lewin671.remoteagent.core.scheduleWhen(Instant.ofEpochMilli(ms), now, zone)
    val whenText = times.joinToString(copy(UiMessages.commonListSeparator()), transform = ::time)
    return copy(UiMessages.sessionQuietChecks(times = whenText))
}
