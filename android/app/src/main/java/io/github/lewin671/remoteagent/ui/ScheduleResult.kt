package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.github.lewin671.remoteagent.core.*
import java.time.Instant
import kotlinx.coroutines.CancellationException

/** Uses the same authenticated event/cache loader and Markdown renderer as the conversation. */
@Composable
internal fun ScheduleLatestResult(run: ScheduledRun, now: Instant, unreadOnEntry: Boolean, showConversation: Boolean, open: () -> Unit) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    LaunchedEffect(run.session, state.conn) {
        if (run.session.isNotEmpty() && state.sessions[run.session]?.info == null) {
            try { remote.fetchSession(run.session) }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { /* Keep the saved summary without an error banner. */ }
        }
    }
    val events = if (run.session.isNotEmpty() && state.sessions[run.session]?.info != null) {
        val content by remember(remote, run.session) { remote.events(run.session) }.collectAsStateWithLifecycle()
        content
    } else SessionEvents()
    val answer = remember(run.id, events) {
        if (events.loaded && events.error == null && events.fetchError == null && !events.historyPartial)
            scheduleFinalAnswer(run, events.verifiedEvents) else null
    }
    val fallback = run.json.text("final_summary").ifBlank {
        run.json.text("reason").takeIf { it.isNotBlank() }?.let(::scheduleReason).orEmpty()
    }
    Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
        Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp)) {
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                if (unreadOnEntry) ScheduleResultDot()
                Text(scheduleWhen(run.notificationTime, now), Modifier.weight(1f), style = T.meta, color = pal.muted)
                if (showConversation && run.session.isNotEmpty()) TextAction("对话", open, style = T.sm, arrow = true)
            }
            val heading = run.resultTitle
            if (heading.isNotBlank()) Text(heading, style = T.bodyB, color = pal.fg, modifier = Modifier.padding(bottom = 8.dp))
            val body = answer ?: fallback
            if (body.isNotBlank()) ScheduleResultMarkdown(body)
        }
    }
}
