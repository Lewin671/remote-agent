package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.minutesLeft
import io.github.lewin671.remoteagent.core.optionLabel
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.proto.AnswerPlanRequest
import io.github.lewin671.remoteagent.proto.AnswerQuestionRequest
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.PlanChoice
import io.github.lewin671.remoteagent.proto.QuestionAsked
import io.github.lewin671.remoteagent.proto.ResolveApprovalRequest

/** What the dock holds: the highest item of the priority list. */
sealed interface DockItem {
    val id: String

    data class Approval(val a: ApprovalRequested) : DockItem {
        override val id get() = "a:" + a.approvalId
    }

    data class Question(val q: QuestionAsked) : DockItem {
        override val id get() = "q:" + q.questionId
    }

    data class Plan(val text: String) : DockItem {
        override val id get() = "plan"
    }

    data class Popup(val title: String) : DockItem {
        override val id get() = "popup:$title"
    }
}

/** The dock's one line: type, a summary, 处理 (client-v7). */
fun dockSummary(item: DockItem, agent: String): Pair<String, String> = when (item) {
    is DockItem.Approval -> (if (item.a.command.isNotEmpty()) "请求执行" else "请求权限") to approvalDisplay(item.a.command.ifEmpty { item.a.toolName }).lines().first()
    is DockItem.Question -> "$agent 在问你" to item.q.questionsList.firstOrNull()?.text?.lines()?.firstOrNull().orEmpty()
    is DockItem.Plan -> "计划待批准" to ""
    is DockItem.Popup -> "终端里有弹窗" to item.title
}

/**
 * What waits for the user, docked above the message box. It is one line
 * until opened; open, its body scrolls within 240 dp while the actions stay
 * put, and the chat keyboard goes away. [more] others wait behind it.
 */
@Composable
fun PendingDock(item: DockItem, agent: String, more: Int, body: @Composable ColumnScope.(close: () -> Unit) -> Unit, actions: @Composable ColumnScope.() -> Unit) {
    val p = pal
    var open by rememberSaveable(item.id) { mutableStateOf(false) }
    val keyboard = LocalSoftwareKeyboardController.current
    val focus = LocalFocusManager.current
    val (label, brief) = dockSummary(item, agent)
    Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp).topLine(p.line)) {
        Row(
            Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(role = Role.Button, onClickLabel = if (open) "收起" else "处理") {
                if (!open) { keyboard?.hide(); focus.clearFocus() }
                open = !open
            },
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            Dot(p.orangeInk, 7)
            Text(label, style = T.sm, color = p.fg2, maxLines = 1)
            Text(
                brief + if (more > 0) " · 还有 $more 项" else "", style = T.sm, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
            ArrowText(if (open) "收起" else "处理", p.blue, T.sm, down = !open, up = open)
        }
        if (open) {
            Column(Modifier.heightIn(max = 240.dp)) {
                Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState()).padding(top = 4.dp, bottom = 6.dp)) { body { open = false } }
                Column(Modifier.padding(top = 4.dp, bottom = 10.dp)) { actions() }
            }
        }
    }
}

/** The first line of an open dock item: its type and, at the right, a note or a link. */
@Composable
fun DockHead(title: String, right: String = "", rightLink: (() -> Unit)? = null, extra: (@Composable () -> Unit)? = null) {
    val p = pal
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(title, style = T.smB, color = p.fg2, modifier = Modifier.weight(1f))
        extra?.invoke()
        if (right.isNotEmpty()) {
            if (rightLink != null) Box(Modifier.clickable(onClick = rightLink)) { ArrowText(right, p.blue, T.smB) }
            else Text(right, style = T.sm, color = p.muted)
        }
    }
}

/** The grey line under a dock's buttons: a question at the left, one link at the right, with an [arrow] when it goes somewhere else. */
@Composable
fun DockFoot(left: String, link: String, danger: Boolean = false, arrow: Boolean = false, onLink: () -> Unit) {
    val p = pal
    // With nothing at the left the link starts the line.
    Row(Modifier.fillMaxWidth().padding(top = 7.dp), verticalAlignment = Alignment.CenterVertically) {
        if (left.isNotEmpty()) Text(left, style = T.meta, color = p.muted, modifier = Modifier.weight(1f))
        Box(Modifier.heightIn(min = 44.dp).clickable(onClick = onLink), contentAlignment = Alignment.CenterStart) {
            if (arrow) ArrowText(link, if (danger) p.red else p.blue, T.metaB) else Text(link, style = T.metaB, color = if (danger) p.red else p.blue)
        }
    }
}

@Composable
fun ApprovalDock(s: SessionView, a: ApprovalRequested, more: Int, onDetail: () -> Unit) {
    val p = pal
    val t = now(15_000)
    val agent = agentName(s.agent())
    var reasonOpen by rememberSaveable(a.approvalId) { mutableStateOf(false) }
    PendingDock(DockItem.Approval(a), agent, more, body = {
        DockHead(
            if (a.command.isNotEmpty()) "请求执行" else "请求权限" + (if (a.toolName.isNotEmpty()) " · ${approvalDisplay(a.toolName)}" else ""),
            if (a.hasDeadline()) "剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟，超时拒绝" else "",
        )
        if (a.description.isNotEmpty()) Text(approvalDisplay(a.description), style = T.sm, color = p.fg2, modifier = Modifier.padding(top = 4.dp))
        val body = approvalDisplay(a.command.ifEmpty { a.toolInputJson })
        if (body.isNotEmpty()) {
            val cut = body.lines().size > 4 || body.length > 200
            Box(Modifier.padding(vertical = 8.dp)) {
                Code(body, maxLines = 4)
                if (cut) Text(
                    "展开全文", style = T.metaB, color = p.blue,
                    modifier = Modifier.align(Alignment.BottomEnd).background(p.code).padding(start = 12.dp, end = 6.dp, bottom = 4.dp).clickable(onClick = onDetail),
                )
            }
        }
    }) {
        ApprovalButtons(s, a, compact = false, reasonOpen = reasonOpen, onReason = { reasonOpen = it })
    }
}

/**
 * Decisions on approvals sent from here, for this run. One whose outcome
 * is unknown is sent again unchanged, the same request (architecture §10):
 * the daemon answers it from its log, and the opposite decision is never
 * offered in its place.
 */
object Resolves {
    class Resolve {
        val attempt = io.github.lewin671.remoteagent.core.Attempt()
        var allow = false
        var message = ""
        var always = false
    }

    private val byApproval = HashMap<String, Resolve>()

    fun of(approvalId: String): Resolve = byApproval.getOrPut(approvalId) { Resolve() }

    fun clear() = byApproval.clear()
}

/** Sends [allow], or the earlier decision while its outcome is unknown; returns the decision that went. */
suspend fun resolveApproval(remote: io.github.lewin671.remoteagent.core.Remote, s: SessionView, a: ApprovalRequested, allow: Boolean, message: String, always: Boolean = false): Boolean {
    val r = Resolves.of(a.approvalId)
    try {
        val retry = r.attempt.frozen
        val prepared = r.attempt.next {
            r.allow = allow
            r.message = message
            r.always = always
            remote.prepare(s.machineId, s.id, remote.cmd {
                resolveApproval = ResolveApprovalRequest.newBuilder().setSessionId(s.id).setApprovalId(a.approvalId).setAllow(allow).setMessage(message).setAlways(always).build()
            })
        }
        remote.forward(prepared, retryOfUnknown = retry)
        r.attempt.settle()
        return r.allow
    } catch (e: Exception) {
        throw r.attempt.settle(e) ?: e
    }
}

/**
 * 拒绝 / 允许, and 附理由拒绝. A tap locks both until the computer
 * confirms; a definite failure lets the user choose again, while an unknown
 * outcome only offers to retry the same decision. [onCard]: on a grey card,
 * where the buttons are white.
 */
@Composable
fun ApprovalButtons(s: SessionView, a: ApprovalRequested, compact: Boolean, reasonOpen: Boolean = false, onReason: (Boolean) -> Unit = {}, onCard: Boolean = false) {
    val p = pal
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val act = rememberAction()
    val r = Resolves.of(a.approvalId)
    var which by remember { mutableStateOf<Boolean?>(if (r.attempt.frozen) r.allow else null) }
    var reason by Drafts.of("deny:${a.approvalId}")
    var opened by remember(a.approvalId) { mutableStateOf(false) }
    val alwaysAvailable = io.github.lewin671.remoteagent.core.capabilities(state.machines[s.machineId])?.approvalAlways == true
    val stuck = r.attempt.frozen
    fun resolve(allow: Boolean, message: String = "", always: Boolean = false) {
        which = allow
        act.run({ resolveApproval(remote, s, a, allow, message, always); null })
    }
    if (act.running || stuck || which != null) {
        if (act.running || (act.error.isEmpty() && !stuck))
            Text(if (act.running) "正在送到电脑…" else "已提交，等待电脑确认", style = T.meta, color = p.muted)
    } else if (opened || reasonOpen) {
        ReasonField(reason, { reason = it }, "拒绝理由（可选）")
        Btns {
            Btn("取消", { opened = false; onReason(false) }, Modifier.weight(1f), BtnKind.SECONDARY)
            Btn("不允许", { resolve(false, reason.trim()) }, Modifier.weight(1f), BtnKind.NEGATIVE)
        }
    } else {
        Btns {
            Btn("不允许", { opened = true; onReason(true) }, Modifier.weight(1f), BtnKind.NEGATIVE, bg = if (onCard) p.card else null)
            Btn("允许", { resolve(true) }, Modifier.weight(1f), BtnKind.PRIMARY)
        }
        if (alwaysAvailable) TextAction("允许，这类请求以后不再问", { resolve(true, always = true) }, modifier = Modifier.fillMaxWidth(), style = T.smB, alignment = Alignment.Center)
    }
    if (!act.running && stuck) {
        val what = if (r.allow) "允许" else "拒绝"
        Banner(
            if (r.attempt.expired) "不确定「$what」有没有送到，它已过期，不会再重发。审批超时后会自动拒绝"
            else "没送到电脑：${act.error.ifEmpty { "结果未知" }}",
            Modifier.padding(top = 8.dp), Tone2.ERROR,
            action = if (!r.attempt.expired) "重试" else null, onAction = { resolve(r.allow, r.message, r.always) },
        )
    } else if (act.error.isNotEmpty()) {
        Banner("没送到电脑：${act.error}", Modifier.padding(top = 8.dp), Tone2.ERROR, action = "重试", onAction = { resolve(r.allow, r.message, r.always) })
    }
}

/** A text box on a soft field, for a reason or another short answer. */
@Composable
fun ReasonField(value: String, onValue: (String) -> Unit, placeholder: String, modifier: Modifier = Modifier) {
    val p = pal
    BasicTextField(
        value, onValue, modifier.fillMaxWidth().padding(bottom = 8.dp),
        textStyle = T.body.copy(color = p.fg), cursorBrush = SolidColor(p.blue), maxLines = 4,
        decorationBox = { inner ->
            FieldBox {
                if (value.isEmpty()) Text(placeholder, style = T.body, color = p.muted)
                inner()
            }
        },
    )
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

/**
 * Where the user is in a question, shared by the dock and the full page:
 * which of several questions, and what is ticked in a multiple choice, so
 * going between the two keeps both.
 */
object QuestionPicks {
    val index = mutableStateMapOf<String, Int>()
    val single = mutableStateMapOf<String, Int>()
    val multi = mutableStateMapOf<String, Set<Int>>()

    fun clear() {
        index.clear()
        single.clear()
        multi.clear()
    }
}

/** A question is long when the dock would cut it: then it has a page of its own. */
fun longQuestion(q: io.github.lewin671.remoteagent.proto.Question): Boolean =
    q.text.length > 120 || q.optionsCount > 4 || q.optionsList.sumOf { it.label.length + it.description.length } > 400

/**
 * Answers the current question by option; several questions go one by one.
 * One choice answers at a tap; a multiple choice ticks, then 提交 N 项.
 * The dock shows it in [body]; the page ([full]) as a whole.
 */
@Composable
fun QuestionBody(s: SessionView, q: QuestionAsked, onTerminal: () -> Unit, full: Boolean, onLong: () -> Unit = {}, onAnswered: () -> Unit = {}, part: Int = 0, act: Action = rememberAction()) {
    val p = pal
    val remote = LocalRemote.current
    val qid = q.questionId
    val index = QuestionPicks.index[qid] ?: 0
    val qs = q.questionsList
    val cur = qs.getOrNull(index) ?: qs.lastOrNull() ?: return
    val answer = Answers.of(qid, index)
    val key = "$qid#$index"
    fun submit(options: List<Int>) {
        val qi = index
        val multiSelect = cur.multiSelect
        act.run({
            val sent = Answers.send(answer, options, { opts ->
                remote.prepare(s.machineId, s.id, remote.cmd {
                    answerQuestion = AnswerQuestionRequest.newBuilder().setSessionId(s.id).setQuestionId(qid).also { b ->
                        if (multiSelect) b.addAllOptions(opts.map { it + 1 }) else b.setOption(opts.first() + 1)
                        if (qs.size > 1) b.setQuestionIndex(qi)
                    }.build()
                })
            }) { pc, retry -> remote.forward(pc, retryOfUnknown = retry) }
            // Show what was answered, which after an unknown outcome is the earlier answer.
            if (multiSelect) QuestionPicks.multi["$qid#$qi"] = sent.toSet() else QuestionPicks.single["$qid#$qi"] = sent.first()
            null
        }) {
            if (qi + 1 < qs.size) QuestionPicks.index[qid] = qi + 1 else onAnswered()
        }
    }
    // part 0: everything; 1: the scrolling body; 2: the fixed actions.
    if (part != 2) {
        val progress = listOfNotNull(if (qs.size > 1) "第 ${index + 1} 题，共 ${qs.size} 题" else null, if (cur.multiSelect) "可多选" else null).joinToString(" · ")
        if (full) Text(progress, style = T.meta, color = p.muted, modifier = Modifier.padding(top = 12.dp))
        else DockHead("${agentName(s.agent())} 在问你", progress, extra = if (qs.size > 1) ({ Steps(qs.size, index) }) else null)
        Text(cur.text, style = if (full) T.h17 else T.bodyB, color = p.fg, maxLines = if (full) Int.MAX_VALUE else 4, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = if (full) 8.dp else 6.dp, bottom = if (full) 12.dp else 6.dp))
        if (!full && longQuestion(cur)) Box(Modifier.clickable(onClick = onLong).padding(bottom = 4.dp)) { ArrowText("展开全文", p.blue, T.smB) }
        cur.optionsList.forEachIndexed { i, o ->
            val (label, rec) = optionLabel(o.label)
            val sel = if (cur.multiSelect) i in (QuestionPicks.multi[key] ?: emptySet()) else QuestionPicks.single[key] == i
            Row(
                Modifier.fillMaxWidth().padding(vertical = 3.dp).heightIn(min = 44.dp).clip(Radii.SM).background(if (sel) p.blueSoft else androidx.compose.ui.graphics.Color.Transparent)
                    .border(1.dp, if (sel) p.blue else p.line, Radii.SM)
                    .clickable(enabled = !act.running) {
                        if (cur.multiSelect) {
                            val set = (QuestionPicks.multi[key] ?: emptySet()).toMutableSet()
                            if (!set.add(i)) set.remove(i)
                            QuestionPicks.multi[key] = set
                        } else {
                            QuestionPicks.single[key] = i
                            submit(listOf(i))
                        }
                    }
                    .padding(horizontal = 11.dp, vertical = 8.dp),
                verticalAlignment = Alignment.Top, horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                if (cur.multiSelect) Box(
                    Modifier.padding(top = 1.dp).size(18.dp).clip(RoundedCornerShape(4.dp)).background(if (sel) p.blueBg else androidx.compose.ui.graphics.Color.Transparent)
                        .border(1.dp, if (sel) p.blueBg else p.muted, RoundedCornerShape(4.dp)),
                    contentAlignment = Alignment.Center,
                ) { if (sel) Mark(Lucide.Check, androidx.compose.ui.graphics.Color.White, 12.dp) }
                else if (sel) Box(Modifier.padding(top = 3.dp)) { Mark(Lucide.Check, p.blue, 13.dp) }
                Column(Modifier.weight(1f)) {
                    Text(label, style = T.sm, color = p.fg)
                    if (o.description.isNotEmpty()) Text(o.description, style = T.meta, color = p.muted)
                    if (sel && act.running && !cur.multiSelect) Text("提交中…", style = T.meta, color = p.muted)
                }
                if (rec) Text("推荐", style = T.metaB, color = p.blue)
            }
        }
        if (!act.running && answer.attempt.frozen) {
            val shown = answer.options.joinToString("、") { i -> cur.optionsList.getOrNull(i)?.let { optionLabel(it.label).first } ?: "${i + 1}" }
            Banner(
                if (answer.attempt.expired) "不确定「$shown」有没有提交，它已过期，不会再重发。请去终端看看"
                else "上次的回答「$shown」没能确认，终端里的问题可能已经变了。再点任一选项会重发同一个回答",
                Modifier.padding(top = 6.dp), Tone2.ERROR,
                action = if (answer.attempt.expired) "放弃" else null, onAction = { answer.attempt.abandon(); act.error = "" },
            )
        } else if (act.error.isNotEmpty()) {
            Banner("没能提交，终端里的问题可能已经变了：${act.error}", Modifier.padding(top = 6.dp), Tone2.ERROR)
        }
    }
    if (part != 1) {
        if (cur.multiSelect) {
            val n = QuestionPicks.multi[key]?.size ?: 0
            Btn(
                if (act.running) "提交中…" else if (n == 0) "选好后提交" else "提交 $n 项", { submit((QuestionPicks.multi[key] ?: emptySet()).sorted()) },
                Modifier.fillMaxWidth().padding(top = 6.dp), BtnKind.PRIMARY, enabled = !act.running && n > 0,
            )
            DockFoot(if (n > 0) "已选 $n 项" else "", "其他回答：去终端", arrow = true, onLink = onTerminal)
        } else DockFoot("都不合适？", "其他回答：去终端", arrow = true, onLink = onTerminal)
    }
}

/** 第 1 题 of several, as short bars. */
@Composable
fun Steps(n: Int, at: Int) {
    val p = pal
    Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        repeat(n) { i -> Box(Modifier.width(16.dp).height(4.dp).clip(RoundedCornerShape(2.dp)).background(if (i <= at) p.blue else p.track)) }
    }
}

@Composable
fun QuestionDock(s: SessionView, q: QuestionAsked, more: Int, onTerminal: () -> Unit) {
    val nav = LocalNav.current
    // One submission for both halves, which the dock lays out apart.
    val act = rememberAction()
    PendingDock(DockItem.Question(q), agentName(s.agent()), more, body = {
        QuestionBody(s, q, onTerminal, full = false, onLong = { nav.question(s.id, q.questionId) }, part = 1, act = act)
    }) { QuestionBody(s, q, onTerminal, full = false, part = 2, act = act) }
}

/** 回答提问: a long question on a page of its own; the answer button stays at hand. */
@Composable
fun QuestionPage(sid: String, qid: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val events by remote.events(sid).collectAsState()
    val d = rememberDerived(events.events, s?.agent() ?: io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX)
    val q = d.questions[qid]
    PageScaffold("回答提问", subtitle = s?.let { titleOf(it) + " · " + agentName(it.agent()) }) {
        if (s == null || q == null) {
            PageNote(if (d.answered.containsKey(qid)) "这个问题已经回答了。" else "读取中…")
            return@PageScaffold
        }
        val act = rememberAction()
        val toTerminal = { nav.back(); nav.sessionTerminal(sid) }
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 14.dp)) {
            QuestionBody(s, q, toTerminal, full = true, onAnswered = { nav.back() }, part = 1, act = act)
        }
        Column(Modifier.padding(horizontal = 14.dp).topLine(pal.line).padding(top = 8.dp, bottom = 16.dp)) {
            QuestionBody(s, q, toTerminal, full = true, onAnswered = { nav.back() }, part = 2, act = act)
        }
    }
}

@Composable
fun PlanDock(s: SessionView, text: String, more: Int, onFull: () -> Unit) {
    val p = pal
    PendingDock(DockItem.Plan(text), agentName(s.agent()), more, body = {
        DockHead("计划待批准", "展开全文", rightLink = onFull)
        Text(
            text.lines().map { io.github.lewin671.remoteagent.core.plain(it) }.filter { it.isNotBlank() }.take(4).joinToString("\n"),
            style = ts(13.sp, lineHeight = 1.6f), color = p.fg, maxLines = 4, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(vertical = 8.dp).fillMaxWidth().clip(Radii.XS).background(p.soft).padding(horizontal = 10.dp, vertical = 7.dp),
        )
        if (s.agent() == io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE) Text("批准后使用询问模式", style = T.meta, color = p.muted)
    }) { PlanButtons(s, full = false) }
}

@Composable
fun PlanButtons(s: SessionView, full: Boolean, onDone: () -> Unit = {}) {
    val remote = LocalRemote.current
    val act = rememberAction()
    fun answer(c: PlanChoice) = act.run({
        remote.command(s.id, remote.cmd { answerPlan = AnswerPlanRequest.newBuilder().setSessionId(s.id).setChoice(c).build() })
    }) { onDone() }
    Btns {
        Btn("继续规划…", { answer(PlanChoice.PLAN_CHOICE_KEEP_PLANNING) }, Modifier.weight(1f), enabled = !act.running)
        Btn(if (act.running) "提交中…" else "批准执行", { answer(PlanChoice.PLAN_CHOICE_IMPLEMENT) }, Modifier.weight(1f), BtnKind.PRIMARY, enabled = !act.running)
    }
    // Claude Code has no such choice.
    if (full && s.agent() != io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE) {
        DockFoot("", "清空上下文后执行") { answer(PlanChoice.PLAN_CHOICE_CLEAR_AND_IMPLEMENT) }
    }
    if (act.error.isNotEmpty()) Banner("没能提交：${act.error}", Modifier.padding(top = 8.dp), Tone2.ERROR)
}

@Composable
fun PopupDock(s: SessionView, title: String, more: Int, onTerminal: () -> Unit) {
    val p = pal
    PendingDock(DockItem.Popup(title), agentName(s.agent()), more, body = {
        DockHead("终端里有弹窗", "${agentName(s.agent())} 在等你")
        if (title.isNotEmpty()) Text(title, style = ts(15.sp, FontWeight.Medium), color = p.fg, modifier = Modifier.padding(vertical = 6.dp))
        val hint = io.github.lewin671.remoteagent.core.popupHint(s.agent(), title)
        if (hint.isNotEmpty()) Text(hint, style = T.sm, color = p.orangeInk)
    }) { Btn("去终端处理", onTerminal, Modifier.fillMaxWidth(), BtnKind.PRIMARY) }
}
