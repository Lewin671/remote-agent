package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CancelOutcome
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.cancelOutcome
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.queueWhy
import io.github.lewin671.remoteagent.core.queued
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.CancelInputRequest
import kotlinx.coroutines.launch

/**
 * Cancels asked from here, per queued message, for this run. The daemon's
 * InputState says how each ended; this only holds what is in flight and why
 * a try failed, so the page can say so.
 */
object Cancels {
    sealed interface State {
        data object Canceling : State
        data class Failed(val error: String) : State
    }

    val states = mutableStateMapOf<String, State>()
    private val asked = HashSet<String>()

    /** The last message that left the queue while this device tried to cancel it, or others canceled: said once on the page. */
    val notes = mutableStateMapOf<String, String>()

    fun canceledHere(requestId: String) = requestId in asked

    fun cancel(remote: Remote, sid: String, requestId: String) {
        if (states[requestId] == State.Canceling) return
        states[requestId] = State.Canceling
        asked += requestId
        RaApp.instance.scope.launch {
            try {
                remote.command(sid, remote.cmd { cancelInput = CancelInputRequest.newBuilder().setSessionId(sid).setRequestId(requestId).build() })
                states.remove(requestId)
            } catch (e: Exception) {
                // Already being written or gone from the queue: the daemon's
                // InputState says which; the page shows it as not cancelable.
                val code = (e as? CommandException)?.code
                if (code == "failed_precondition" || code == "not_found") {
                    states.remove(requestId)
                    notes[sid] = "上一条已送入终端，不能取消"
                } else {
                    states[requestId] = State.Failed(errorText(e))
                }
            }
        }
    }

    fun clear() {
        states.clear()
        asked.clear()
        notes.clear()
    }
}

/** 消息队列: every accepted message in order, its full text where known, and cancel with its outcome. */
@Composable
fun QueuePage(sid: String) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val events by remote.events(sid).collectAsState()
    val agent = s?.agent() ?: Agent.AGENT_CODEX
    val d = rememberDerived(events.events, agent)
    val p = pal
    val ids = queued(d)
    // Say where a message went when it left the queue while shown here.
    val shown = remember { mutableStateOf(ids.toSet()) }
    LaunchedEffect(ids) {
        for (id in shown.value - ids.toSet()) {
            when (cancelOutcome(d.inputs[id], Cancels.canceledHere(id))) {
                CancelOutcome.CANCELED_ELSEWHERE -> Cancels.notes[sid] = "上一条已在另一客户端取消"
                CancelOutcome.WRITTEN -> if (Cancels.canceledHere(id)) Cancels.notes[sid] = "上一条已送入终端，不能取消"
                else -> {}
            }
        }
        shown.value = ids.toSet()
    }
    PageScaffold("消息队列") {
        if (s == null) {
            Text("读取中…", Modifier.padding(16.dp), color = p.muted)
            return@PageScaffold
        }
        Text("${titleOf(s)} · ${agentName(agent)}", Modifier.padding(horizontal = 16.dp), style = MaterialTheme.typography.bodySmall, color = p.muted)
        val status = statusIn(state, s)
        Text(
            "本轮结束后按顺序发送。取消只对尚未送入终端的消息生效。这是电脑上的同一条队列，所有客户端看到同样的已接受消息。",
            Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = p.muted,
        )
        val online = state.conn == ConnState.ONLINE
        LazyColumn(Modifier.weight(1f)) {
            itemsIndexed(ids, key = { _, id -> id }) { i, id ->
                val c = Cancels.states[id]
                Column(
                    Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp).background(p.card, RoundedCornerShape(12.dp)).padding(12.dp),
                    verticalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        val writing = when (d.accepted[id]?.stage) {
                            io.github.lewin671.remoteagent.proto.InputStage.INPUT_STAGE_PASTING_IMAGES -> "正在粘贴图片"
                            io.github.lewin671.remoteagent.proto.InputStage.INPUT_STAGE_IMAGES_RECOGNIZED, io.github.lewin671.remoteagent.proto.InputStage.INPUT_STAGE_SUBMITTING -> "正在送入终端"
                            else -> null
                        }
                        val where = writing ?: if (i == 0) queueWhy(s, status).long else "等待发送"
                        Text(
                            "${i + 1} · " + if (c == Cancels.State.Canceling) "正在取消…" else where,
                            style = MaterialTheme.typography.labelMedium, color = p.muted, modifier = Modifier.weight(1f),
                        )
                        if (c == Cancels.State.Canceling) CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp)
                        else if (writing != null) Text("不能取消", style = MaterialTheme.typography.bodySmall, color = p.faint)
                        else TextButton(onClick = { Cancels.cancel(remote, sid, id) }, enabled = online) { Text("取消") }
                    }
                    val acc = d.accepted[id]
                    val from = io.github.lewin671.remoteagent.core.originLabel(acc?.origin, state, remote.account.deviceId, machineLabel(state, s.machineId))
                    if (from != null) Text(from, style = MaterialTheme.typography.labelSmall, color = p.faint)
                    Text(
                        acc?.text?.takeIf { it.isNotEmpty() || acc.imageIds.isNotEmpty() }?.let { t -> t + if (acc.imageIds.isNotEmpty()) "（${acc.imageIds.size} 张图）" else "" }
                            ?: Outbox.text(sid, id) ?: Outbox.prompt(sid)?.takeIf { ids.size == 1 } ?: "（另一客户端发送的消息，送入终端后才能看到内容）",
                        style = MaterialTheme.typography.bodyLarge,
                        color = if (c == Cancels.State.Canceling) p.faint else p.fg,
                    )
                    if (c is Cancels.State.Failed) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text("取消未成功，消息仍在队列中：${c.error}", style = MaterialTheme.typography.bodySmall, color = p.red, modifier = Modifier.weight(1f))
                            TextButton(onClick = { Cancels.cancel(remote, sid, id) }, enabled = online) { Text("重试") }
                        }
                    }
                }
            }
            if (ids.isEmpty()) item { Text("队列是空的", Modifier.padding(16.dp), color = p.muted) }
            Cancels.notes[sid]?.let { note ->
                item {
                    Row(Modifier.padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(note, style = MaterialTheme.typography.bodySmall, color = p.orangeInk, modifier = Modifier.weight(1f))
                        TextButton(onClick = { Cancels.notes.remove(sid) }) { Text("知道了") }
                    }
                }
            }
            item { Spacer(Modifier.width(1.dp).padding(bottom = 16.dp)) }
        }
    }
}
