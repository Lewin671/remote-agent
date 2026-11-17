package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.SessionTimelineItem
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.core.proto.SessionWindowRequest
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.first
import java.time.Instant

/** Resolve once per result: a saved plain summary must never masquerade as the formatted answer. */
@Composable
internal fun rememberScheduleResultText(run: ScheduledRun): String? {
    val remote = LocalRemote.current
    val flow = remember(remote, run.session, run.id) {
        if (run.session.isNotEmpty()) {
            val win = SessionWindowRequest.newBuilder().setLimit(80).apply {
                if (run.id.isNotEmpty()) revealRunId = run.id
            }.build()
            remote.watchSession(run.session, win)
        } else null
    }
    return key(remote, run.id, run.status, run.json.text("final_summary")) {
        val current = flow?.collectAsState(initial = SessionViewModel.getDefaultInstance())?.value
        val initial = current?.takeIf(::scheduleResultSettled)?.let { resolvedScheduleResult(run, it) }
            ?: if (flow == null) resolvedScheduleResult(run, null) else null
        val text by produceState<String?>(initial) {
            value = loadScheduleResultText(run) {
                if (remote.state.value.sessions[run.session]?.info == null) remote.fetchSession(run.session)
                remote.openSession(run.session)
                flow!!.first(::scheduleResultSettled)
            }
        }
        text
    }
}

private fun scheduleResultSettled(vm: SessionViewModel): Boolean {
    val tl = vm.timeline
    return (tl.loaded && !tl.historyPartial) || tl.hasError() || tl.hasFetchError() || tl.unavailableCount > 0 || tl.retired
}

internal suspend fun loadScheduleResultText(run: ScheduledRun, read: suspend () -> SessionViewModel?): String = try {
    kotlinx.coroutines.withTimeoutOrNull(8_000) {
        if (run.session.isEmpty()) resolvedScheduleResult(run, null)
        else resolvedScheduleResult(run, read())
    } ?: resolvedScheduleResult(run, null)
} catch (e: CancellationException) {
    throw e
} catch (_: Exception) {
    resolvedScheduleResult(run, null)
}

internal fun resolvedScheduleResult(run: ScheduledRun, vm: SessionViewModel?): String {
    val answer = vm?.let { extractScheduleFinalAnswer(run, it) }
    return answer ?: run.json.text("final_summary").ifBlank {
        run.json.text("reason").takeIf { it.isNotBlank() }?.let { scheduleReason(it, copy = ::sessionText) }.orEmpty()
    }
}

private fun extractScheduleFinalAnswer(run: ScheduledRun, vm: SessionViewModel): String? {
    if (run.id.isEmpty()) return null
    val tl = vm.timeline
    if (!tl.loaded || tl.hasError() || tl.hasFetchError() || tl.historyPartial || tl.retired) return null
    val flat = ArrayList<SessionTimelineItem>()
    for (item in tl.itemsList) {
        if (item.hasQuietScheduledGroup()) flat += item.quietScheduledGroup.itemsList
        else flat += item
    }
    val start = flat.indexOfFirst { it.hasUserMessage() && it.userMessage.scheduledRunId == run.id }
    if (start < 0) return null
    var answer: String? = null
    for (item in flat.drop(start + 1)) {
        if (item.hasUserMessage()) {
            val um = item.userMessage
            if (!um.isScheduled || (um.scheduledRunId.isNotEmpty() && um.scheduledRunId != run.id)) break
        }
        if (item.hasForkMarker()) break
        if (item.hasTurnEnd() && item.turnEnd.isSessionEnded) break
        if (item.hasAgentMessage() && !item.agentMessage.intermediate) {
            answer = item.agentMessage.markdown.takeIf { it.isNotBlank() }
        }
    }
    return answer
}

/** The card enters together with the rest of the detail, after its text is resolved. */
@Composable
internal fun ScheduleLatestResult(run: ScheduledRun, text: String, now: Instant, unreadOnEntry: Boolean, showConversation: Boolean, open: () -> Unit) {
    val copy = uiText()
    Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
        Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp)) {
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                if (unreadOnEntry) ScheduleResultDot()
                Text(scheduleWhen(run.notificationTime, now, copy = copy), Modifier.weight(1f), style = T.s12, color = pal.ink2)
                if (showConversation && run.session.isNotEmpty()) TextAction(copy(UiMessages.scheduleConversation()), open, style = T.s13, arrow = true)
            }
            val heading = run.resultTitle
            if (heading.isNotBlank()) Text(heading, style = T.s15b, color = pal.ink, modifier = Modifier.padding(bottom = 8.dp))
            if (text.isNotBlank()) ScheduleResultMarkdown(text)
        }
    }
}
