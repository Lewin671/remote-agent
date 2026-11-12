package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.flow.first
import io.github.lewin671.remoteagent.core.*
import java.time.Instant
import kotlinx.coroutines.CancellationException

/** Resolve once per result: a saved plain summary must never masquerade as the formatted answer. */
@Composable
internal fun rememberScheduleResultText(run: ScheduledRun): String? {
    val remote = LocalRemote.current
    val events = remember(remote, run.session) { run.session.takeIf { it.isNotEmpty() }?.let(remote::events) }
    return key(remote, run.id, run.status, run.json.text("final_summary")) {
        val initial = events?.value?.takeIf(::scheduleResultSettled)?.let { resolvedScheduleResult(run, it) }
            ?: if (events == null) resolvedScheduleResult(run, SessionEvents()) else null
        val text by produceState<String?>(initial) {
            value = loadScheduleResultText(run) {
                if (remote.state.value.sessions[run.session]?.info == null) remote.fetchSession(run.session)
                events!!.first(::scheduleResultSettled)
            }
        }
        text
    }
}

private fun scheduleResultSettled(events: SessionEvents) =
    events.loaded && !events.historyPartial || events.error != null || events.fetchError != null || events.unavailableCount > 0

internal suspend fun loadScheduleResultText(run: ScheduledRun, read: suspend () -> SessionEvents): String = try {
    kotlinx.coroutines.withTimeoutOrNull(8_000) {
        if (run.session.isEmpty()) resolvedScheduleResult(run, SessionEvents())
        else resolvedScheduleResult(run, read())
    } ?: resolvedScheduleResult(run, SessionEvents())
} catch (e: CancellationException) { throw e }
catch (_: Exception) { resolvedScheduleResult(run, SessionEvents()) }

internal fun resolvedScheduleResult(run: ScheduledRun, events: SessionEvents): String {
    val answer = if (events.loaded && events.error == null && events.fetchError == null && !events.historyPartial)
        scheduleFinalAnswer(run, events.verifiedEvents) else null
    return answer ?: run.json.text("final_summary").ifBlank {
        run.json.text("reason").takeIf { it.isNotBlank() }?.let(::scheduleReason).orEmpty()
    }
}

/** The card enters together with the rest of the detail, after its text is resolved. */
@Composable
internal fun ScheduleLatestResult(run: ScheduledRun, text: String, now: Instant, unreadOnEntry: Boolean, showConversation: Boolean, open: () -> Unit) {
    Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
        Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp)) {
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                if (unreadOnEntry) ScheduleResultDot()
                Text(scheduleWhen(run.notificationTime, now), Modifier.weight(1f), style = T.s12, color = pal.ink2)
                if (showConversation && run.session.isNotEmpty()) TextAction("对话", open, style = T.s13, arrow = true)
            }
            val heading = run.resultTitle
            if (heading.isNotBlank()) Text(heading, style = T.s15b, color = pal.ink, modifier = Modifier.padding(bottom = 8.dp))
            if (text.isNotBlank()) ScheduleResultMarkdown(text)
        }
    }
}
