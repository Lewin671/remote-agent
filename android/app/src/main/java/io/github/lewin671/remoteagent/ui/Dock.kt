package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.minutesLeft
import io.github.lewin671.remoteagent.core.optionLabel
import io.github.lewin671.remoteagent.proto.AnswerPlanRequest
import io.github.lewin671.remoteagent.proto.AnswerQuestionRequest
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.PlanChoice
import io.github.lewin671.remoteagent.proto.QuestionAsked
import io.github.lewin671.remoteagent.proto.ResolveApprovalRequest

/** What the dock holds: the highest item of the priority list. */
sealed interface DockItem {
    data class Approval(val a: ApprovalRequested) : DockItem
    data class Question(val q: QuestionAsked) : DockItem
    data class Plan(val text: String) : DockItem
    data class Popup(val title: String) : DockItem
}

@Composable
fun DockFrame(more: Int, content: @Composable () -> Unit) {
    val p = pal
    Surface(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 6.dp),
        shape = RoundedCornerShape(16.dp),
        color = p.card,
        border = androidx.compose.foundation.BorderStroke(1.dp, p.orange.copy(alpha = 0.5f)),
        shadowElevation = 2.dp,
    ) {
        Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            content()
            if (more > 0) Text("还有 $more 项", style = MaterialTheme.typography.bodySmall, color = p.muted)
        }
    }
}

@Composable
private fun DockTitle(title: String, right: String = "") {
    val p = pal
    Row(verticalAlignment = Alignment.CenterVertically) {
        Dot(p.orange)
        Spacer(Modifier.width(6.dp))
        Text(title, style = MaterialTheme.typography.titleSmall, color = p.orangeInk, modifier = Modifier.weight(1f))
        if (right.isNotEmpty()) Text(right, style = MaterialTheme.typography.bodySmall, color = p.muted)
    }
}

/** The dock collapsed to one line while typing: type, summary and 展开. */
@Composable
fun DockCollapsed(item: DockItem, onExpand: () -> Unit) {
    val p = pal
    val (type, text) = when (item) {
        is DockItem.Approval -> (if (item.a.command.isNotEmpty()) "请求执行" else "请求权限") to item.a.command.ifEmpty { item.a.toolName }
        is DockItem.Question -> "在问你" to item.q.questionsList.firstOrNull()?.text.orEmpty()
        is DockItem.Plan -> "计划待批准" to item.text.lines().firstOrNull().orEmpty()
        is DockItem.Popup -> "终端里有弹窗" to item.title
    }
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp).background(p.orangeSoft, RoundedCornerShape(12.dp))
            .clickable(onClick = onExpand).padding(horizontal = 12.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(type, style = MaterialTheme.typography.labelMedium, color = p.orangeInk)
        Spacer(Modifier.width(8.dp))
        Text(text, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        Text("展开", color = p.blue, style = MaterialTheme.typography.labelMedium)
    }
}

@Composable
fun ApprovalDock(s: SessionView, a: ApprovalRequested, more: Int, onDetail: () -> Unit) {
    val p = pal
    val t = now(15_000)
    DockFrame(more) {
        DockTitle(
            if (a.command.isNotEmpty()) "请求执行" else "请求权限" + (if (a.toolName.isNotEmpty()) " · ${a.toolName}" else ""),
            if (a.hasDeadline()) "剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟，超时拒绝" else "",
        )
        if (a.description.isNotEmpty()) Text(a.description, style = MaterialTheme.typography.bodyMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
        val body = a.command.ifEmpty { a.toolInputJson }
        if (body.isNotEmpty()) {
            CodeBlock(body, maxLines = 4)
            if (body.lines().size > 4 || body.length > 200) Text("展开全文 ›", color = p.blue, modifier = Modifier.clickable(onClick = onDetail))
        }
        ApprovalButtons(s, a, compact = false)
    }
}

/**
 * 拒绝 / 允许, and 附理由拒绝. Both lock after a tap until the daemon
 * answers; a failure leaves the card to try again.
 */
@Composable
fun ApprovalButtons(s: SessionView, a: ApprovalRequested, compact: Boolean) {
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val act = rememberAction()
    var which by remember { mutableStateOf<Boolean?>(null) }
    var reasonOpen by rememberSaveable(a.approvalId) { mutableStateOf(false) }
    var reason by Drafts.of("deny:${a.approvalId}")
    fun resolve(allow: Boolean, message: String = "") {
        which = allow
        act.run({
            remote.command(s.id, remote.cmd {
                resolveApproval = ResolveApprovalRequest.newBuilder().setSessionId(s.id).setApprovalId(a.approvalId).setAllow(allow).setMessage(message).build()
            })
        })
    }
    if (reasonOpen) {
        OutlinedTextField(reason, { reason = it }, Modifier.fillMaxWidth(), placeholder = { Text("告诉 ${agentName(s.agent())} 为什么不行") }, minLines = 2)
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            TextButton(onClick = { reasonOpen = false }) { Text("取消") }
            Button(
                onClick = { resolve(false, reason.trim()) }, enabled = !act.running && reason.isNotBlank(),
                colors = ButtonDefaults.buttonColors(containerColor = p.redBg), modifier = Modifier.weight(1f).heightIn(min = 44.dp),
            ) { Text(if (act.running) "拒绝中…" else "发送理由并拒绝") }
        }
    } else {
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(onClick = { resolve(false) }, enabled = !act.running, modifier = Modifier.weight(1f).heightIn(min = 44.dp)) {
                Text(if (act.running && which == false) "拒绝中…" else "拒绝")
            }
            Button(onClick = { resolve(true) }, enabled = !act.running, modifier = Modifier.weight(1f).heightIn(min = 44.dp)) {
                Text(if (act.running && which == true) "允许中…" else "允许")
            }
        }
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("想告诉 ${agentName(s.agent())} 为什么？", style = MaterialTheme.typography.bodySmall, color = p.muted, modifier = Modifier.weight(1f))
            TextButton(onClick = { if (compact) nav.approval(s.id, a.approvalId) else reasonOpen = true }) { Text("附理由拒绝 ›") }
        }
    }
    if (act.error.isNotEmpty()) {
        Text("没能提交：${act.error}" + if (act.unknown) "。可以重试，重复提交不会执行两次" else "", color = p.red, style = MaterialTheme.typography.bodySmall)
    }
}

/**
 * Answers to questions, per question and index, for this run. One whose
 * outcome is unknown is sent again unchanged, under its request ID, so the
 * daemon answers from its log (architecture §10): a new request would be
 * refused once the CLI had moved on to the next question.
 */
object Answers {
    class Answer {
        val attempt = io.github.lewin671.remoteagent.core.Attempt()

        /** The options of the answer being sent, 0-based. */
        var options: List<Int> = emptyList()
    }

    private val byQuestion = HashMap<String, Answer>()

    fun of(questionId: String, index: Int): Answer = byQuestion.getOrPut("$questionId#$index") { Answer() }

    fun clear() = byQuestion.clear()

    /**
     * Sends [options], or the earlier answer while its outcome is unknown,
     * and returns the options that were answered.
     */
    suspend fun send(
        a: Answer,
        options: List<Int>,
        prepare: (List<Int>) -> io.github.lewin671.remoteagent.core.PreparedCommand,
        forward: suspend (io.github.lewin671.remoteagent.core.PreparedCommand, Boolean) -> Unit,
    ): List<Int> {
        try {
            val retry = a.attempt.frozen
            val prepared = a.attempt.next {
                a.options = options
                prepare(options)
            }
            forward(prepared, retry)
            a.attempt.settle()
            return a.options
        } catch (e: Exception) {
            throw a.attempt.settle(e) ?: e
        }
    }
}

/** Answers the current question by option; several questions go one by one. */
@Composable
fun QuestionDock(s: SessionView, q: QuestionAsked, more: Int, onTerminal: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var index by rememberSaveable(q.questionId) { mutableIntStateOf(0) }
    val picked = remember(q.questionId) { mutableStateMapOf<Int, Int>() }
    val act = rememberAction()
    val multi = remember(q.questionId) { mutableStateMapOf<Int, Set<Int>>() }
    val qs = q.questionsList
    val cur = qs.getOrNull(index) ?: qs.lastOrNull() ?: return
    val answer = Answers.of(q.questionId, index)
    fun submit(options: List<Int>) {
        val qi = index
        val multiSelect = cur.multiSelect
        act.run({
            val sent = Answers.send(answer, options, { opts ->
                remote.prepare(s.machineId, s.id, remote.cmd {
                    answerQuestion = AnswerQuestionRequest.newBuilder().setSessionId(s.id).setQuestionId(q.questionId).also { b ->
                        if (multiSelect) b.addAllOptions(opts.map { it + 1 }) else b.setOption(opts.first() + 1)
                        if (qs.size > 1) b.setQuestionIndex(qi)
                    }.build()
                })
            }) { p, retry -> remote.forward(p, retryOfUnknown = retry) }
            // Show what was answered, which after an unknown outcome is the earlier answer.
            if (multiSelect) multi[qi] = sent.toSet() else picked[qi] = sent.first()
            null
        }) { if (qi + 1 < qs.size) index = qi + 1 }
    }
    DockFrame(more) {
        DockTitle("${agentName(s.agent())} 在问你", if (qs.size > 1) "第 ${index + 1} 题，共 ${qs.size} 题" else "")
        Text(cur.text, style = MaterialTheme.typography.titleSmall)
        Column(Modifier.heightIn(max = 280.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            cur.optionsList.forEachIndexed { i, o ->
                val (label, rec) = optionLabel(o.label)
                val sel = if (cur.multiSelect) i in (multi[index] ?: emptySet()) else picked[index] == i
                Column(
                    Modifier.fillMaxWidth().background(if (sel) p.blueSoft else p.soft, RoundedCornerShape(12.dp))
                        .border(1.dp, if (sel) p.blue else p.line, RoundedCornerShape(12.dp))
                        .clickable(enabled = !act.running) {
                            if (cur.multiSelect) {
                                val set = (multi[index] ?: emptySet()).toMutableSet()
                                if (!set.add(i)) set.remove(i)
                                multi[index] = set
                            } else {
                                picked[index] = i
                                submit(listOf(i))
                            }
                        }
                        .padding(12.dp),
                ) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        if (sel) Text("✓ ", color = p.blue)
                        Text(label, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                        if (rec) Chip("推荐", color = p.blue, bg = p.blueSoft)
                    }
                    if (o.description.isNotEmpty()) Text(o.description, style = MaterialTheme.typography.bodyMedium, color = p.muted)
                    if (sel && act.running && !cur.multiSelect) Text("提交中…", style = MaterialTheme.typography.bodySmall, color = p.muted)
                }
            }
        }
        if (cur.multiSelect) {
            Button(
                onClick = { submit((multi[index] ?: emptySet()).sorted()) },
                enabled = !act.running && !multi[index].isNullOrEmpty(), modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp),
            ) { Text(if (act.running) "提交中…" else "提交（可多选）") }
        }
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("都不合适？", style = MaterialTheme.typography.bodySmall, color = p.muted, modifier = Modifier.weight(1f))
            TextButton(onClick = onTerminal) { Text("其他回答：去终端 ›") }
        }
        if (!act.running && answer.attempt.frozen) {
            val shown = answer.options.joinToString("、") { i -> cur.optionsList.getOrNull(i)?.let { optionLabel(it.label).first } ?: "${i + 1}" }
            Text(
                if (answer.attempt.expired) "不确定「$shown」有没有提交，它已过期，不会再重发。请去终端看看"
                else "上次的回答「$shown」结果不确定。再点任一选项会重发同一个回答，电脑收到过就不会再提交一次",
                color = p.orangeInk, style = MaterialTheme.typography.bodySmall,
            )
            var gaveUp by remember { mutableStateOf(0) }
            if (answer.attempt.expired) TextButton(onClick = { answer.attempt.abandon(); act.error = ""; gaveUp++ }) { Text("放弃这次回答") }
        } else if (act.error.isNotEmpty()) {
            Text("没能提交，终端里的问题可能已经变了：${act.error}", color = p.red, style = MaterialTheme.typography.bodySmall)
        }
    }
}

@Composable
fun PlanDock(s: SessionView, text: String, more: Int, onFull: () -> Unit) {
    val p = pal
    DockFrame(more) {
        DockTitle("计划待批准")
        Text(
            text.lines().map { io.github.lewin671.remoteagent.core.plain(it) }.filter { it.isNotBlank() }.take(4).joinToString("\n"),
            style = MaterialTheme.typography.bodyMedium, maxLines = 4, overflow = TextOverflow.Ellipsis,
        )
        Text("展开全文 ›", color = p.blue, modifier = Modifier.clickable(onClick = onFull))
        PlanButtons(s, full = false)
    }
}

@Composable
fun PlanButtons(s: SessionView, full: Boolean, onDone: () -> Unit = {}) {
    val p = pal
    val remote = LocalRemote.current
    val act = rememberAction()
    fun answer(c: PlanChoice) = act.run({
        remote.command(s.id, remote.cmd { answerPlan = AnswerPlanRequest.newBuilder().setSessionId(s.id).setChoice(c).build() })
    }) { onDone() }
    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        OutlinedButton(onClick = { answer(PlanChoice.PLAN_CHOICE_KEEP_PLANNING) }, enabled = !act.running, modifier = Modifier.weight(1f).heightIn(min = 44.dp)) { Text("继续规划…") }
        Button(onClick = { answer(PlanChoice.PLAN_CHOICE_IMPLEMENT) }, enabled = !act.running, modifier = Modifier.weight(1f).heightIn(min = 44.dp)) { Text(if (act.running) "提交中…" else "批准执行") }
    }
    // Claude Code has no such choice.
    if (full && s.agent() != io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE) {
        TextButton(onClick = { answer(PlanChoice.PLAN_CHOICE_CLEAR_AND_IMPLEMENT) }, enabled = !act.running) { Text("清空上下文后执行") }
    }
    if (act.error.isNotEmpty()) Text("没能提交：${act.error}", color = p.red, style = MaterialTheme.typography.bodySmall)
}

@Composable
fun PopupDock(s: SessionView, title: String, more: Int, onTerminal: () -> Unit) {
    val p = pal
    DockFrame(more) {
        DockTitle("终端里有弹窗")
        Text("${agentName(s.agent())} 在等你", style = MaterialTheme.typography.bodyMedium, color = p.muted)
        if (title.isNotEmpty()) Text(title, style = MaterialTheme.typography.titleSmall)
        val hint = io.github.lewin671.remoteagent.core.popupHint(s.agent(), title)
        if (hint.isNotEmpty()) Text(hint, style = MaterialTheme.typography.bodyMedium, color = p.orangeInk)
        Button(onClick = onTerminal, modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp)) { Text("去终端处理") }
    }
}
