package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.mutableStateMapOf
import io.github.lewin671.remoteagent.core.eventTime
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.TurnOutcome
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

object ScheduleJump { val requested = mutableStateMapOf<String, Long>() }
fun scheduledMessageCaption(e: Event): String? = e.origin.scheduledTaskId.takeIf { it.isNotEmpty() }?.let {
    "定时任务 · " + Instant.ofEpochMilli(eventTime(e)).atZone(ZoneId.systemDefault()).format(DateTimeFormatter.ofPattern("HH:mm"))
}

data class QuietSpan(val task: String, val run: String, val from: Long, val through: Long, val time: Long)

/** Only completed, explicitly identified runs may be folded. Human replies, failures,
 * interruptions, notifications and incomplete runs always remain visible. */
fun quietScheduledSpans(events: List<Event>): List<QuietSpan> {
    val out = mutableListOf<QuietSpan>()
    val derived = io.github.lewin671.remoteagent.core.derive(events)
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

fun foldScheduledChat(items: List<ChatItem>, events: List<Event>, revealSeq: Long = 0): List<ChatItem> {
    val spans = quietScheduledSpans(events)
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
fun quietScheduledLabel(times: List<Long>, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val dates = times.map { Instant.ofEpochMilli(it).atZone(zone).toLocalDate() }.distinct()
    fun time(ms: Long) = if (dates.size == 1) Instant.ofEpochMilli(ms).atZone(zone).format(DateTimeFormatter.ofPattern("HH:mm"))
        else io.github.lewin671.remoteagent.core.scheduleWhen(Instant.ofEpochMilli(ms), now, zone)
    val whenText = times.joinToString("、", transform = ::time)
    return "$whenText 定时检查 · 没有新回复"
}
