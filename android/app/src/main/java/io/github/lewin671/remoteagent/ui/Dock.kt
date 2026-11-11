package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.approvalDisplay
import io.github.lewin671.remoteagent.core.approvalHead
import io.github.lewin671.remoteagent.core.approvalBody
import io.github.lewin671.remoteagent.core.approvalTruncated
import io.github.lewin671.remoteagent.core.approvalTruncatedMessage

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.SizeTransform
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentInfo
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.isSkeleton
import io.github.lewin671.remoteagent.core.minutesLeft
import io.github.lewin671.remoteagent.core.optionLabel
import io.github.lewin671.remoteagent.core.skeletonMessage
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
        override val id
            get() = "a:" + a.approvalId
    }

    data class Question(val q: QuestionAsked) : DockItem {
        override val id
            get() = "q:" + q.questionId
    }

    data class Plan(val text: String) : DockItem {
        // A new plan is a new item, which opens again.
        override val id
            get() = "plan:" + text.hashCode()
    }

    data class Popup(val title: String) : DockItem {
        override val id
            get() = "popup:$title"
    }
}

/** The dock's one line when folded: type, a summary, 处理 (client-v7). */
fun dockSummary(item: DockItem, agent: String): Pair<String, String> =
    when (item) {
        is DockItem.Approval ->
            if (isSkeleton(item.a)) skeletonMessage to ""
            else
                (if (item.a.command.isNotEmpty()) "请求执行" else "请求权限") to
                    approvalDisplay(item.a.command.ifEmpty { item.a.toolName }).lines().first()
        is DockItem.Question ->
            if (isSkeleton(item.q)) skeletonMessage to ""
            else
                "$agent 在问你" to
                    item.q.questionsList.firstOrNull()?.text?.lines()?.firstOrNull().orEmpty()
        is DockItem.Plan -> if (isSkeleton(item.text)) skeletonMessage to "" else "计划待批准" to ""
        is DockItem.Popup -> "终端里有弹窗" to item.title
    }

/**
 * The dock's one line, folded or open: the type, then the summary while folded, or while open the
 * item's own [meta], how many more wait and the time left ([left]). The body never repeats it.
 */
fun dockHeadLine(
    item: DockItem,
    agent: String,
    open: Boolean,
    more: Int,
    meta: String = "",
    left: String = "",
    openLabel: String? = null,
): Pair<String, String> {
    val (label, brief) = dockSummary(item, agent)
    val rest =
        if (open) listOf(meta, if (more > 0) "还有 $more 项" else "", left)
        else listOf(brief, if (more > 0) "还有 $more 项" else "")
    return (if (open) openLabel ?: label else label) to
        rest.filter { it.isNotEmpty() }.joinToString(" · ")
}

/**
 * Which waiting items the user folded or opened, by session and item, for as long as the app runs:
 * an item opens by itself, stays folded once folded, and the next item opens again. One that comes
 * while the message box holds a draft (text or images not sent) starts folded, so as not to push the
 * message aside; the box merely having focus does not count. [drafting] is asked once, when the
 * item is first shown.
 */
object DockFolds {
    private val chosen = mutableStateMapOf<String, Boolean>()
    // Whether an item came while a draft was in the box, decided when it was first shown.
    private val cameWhileDrafting = HashMap<String, Boolean>()

    fun isOpen(sid: String, itemId: String, drafting: () -> Boolean = { false }): Boolean {
        val key = "$sid/$itemId"
        chosen[key]?.let { return it }
        return !cameWhileDrafting.getOrPut(key, drafting)
    }

    fun set(sid: String, itemId: String, open: Boolean) {
        chosen["$sid/$itemId"] = open
    }

    fun clear() {
        chosen.clear()
        cameWhileDrafting.clear()
    }
}

/** Whether a session's message box holds a draft: text or images not sent. */
fun hasDraft(sid: String): Boolean =
    Outbox.draft(sid).value.isNotBlank() || ImageDrafts.forSession(sid).images.isNotEmpty()

/**
 * How tall an open dock's body and actions may grow: about 60% of the session's visible content
 * area, never less than 240 dp. The session provides it.
 */
val LocalDockMax = androidx.compose.runtime.compositionLocalOf { 240.dp }

/**
 * What waits for the user, docked above the message box (design-system §6
 * 等待事项): the dark card, floating. It opens by itself, unless it comes
 * while the box holds a draft ([DockFolds]); open, its head is one line,
 * 「● 类型　元信息　收起」, and the body scrolls within [LocalDockMax] while
 * the actions stay put. Folded, it is one dark line 「● 类型 · 摘要 · 处理」
 * until the next item. [more] others wait behind it.
 */
@Composable
fun PendingDock(
    sid: String,
    item: DockItem,
    agent: String,
    more: Int,
    meta: String = "",
    left: String = "",
    openLabel: String? = null,
    headExtra: (@Composable () -> Unit)? = null,
    body: @Composable ColumnScope.(close: () -> Unit) -> Unit,
    actions: @Composable ColumnScope.() -> Unit,
) {
    val open = DockFolds.isOpen(sid, item.id) { hasDraft(sid) }
    val keyboard = LocalSoftwareKeyboardController.current
    val focus = LocalFocusManager.current
    val (label, rest) = dockHeadLine(item, agent, open, more, meta, left, openLabel)
    val toggle = {
        if (!open) {
            keyboard?.hide()
            focus.clearFocus()
        }
        DockFolds.set(sid, item.id, !open)
    }
    // Folding and opening change the card's height and swap its content: the words cross-fade and the
    // height follows (§4 动效, 常).
    // The animated box takes the shell's room too, so that the shadow is inside it and the old content,
    // which is clipped to the size as it changes, does not draw over the message box.
    AnimatedContent(
        open,
        transitionSpec = { dockSwap() },
        label = "dock-fold",
    ) { isOpen ->
        DockShell {
            if (!isOpen) DockLine(label, rest, toggle)
            else DarkCard(float = true) {
                DarkCardHead(label, rest, trailing = {
                    headExtra?.invoke()
                    CardLink("收起", toggle, up = true)
                })
                Column(Modifier.heightIn(max = LocalDockMax.current)) {
                    Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState())) {
                        body { DockFolds.set(sid, item.id, false) }
                    }
                    Column { actions() }
                }
            }
        }
    }
}

/** One thing on the dock giving way to another (a folded line and the open card, the card and the one line it shrinks to once answered): the words cross-fade and the height follows, 200 ms. */
internal fun <S> androidx.compose.animation.AnimatedContentTransitionScope<S>.dockSwap(): androidx.compose.animation.ContentTransform =
    (fadeIn(Motion.fadeInSpec(Motion.BASE)) togetherWith fadeOut(Motion.fadeOutSpec(Motion.BASE))) using
        SizeTransform(clip = true) { _, _ -> Motion.enter(Motion.BASE) }

/** Where the dock sits: 12 in from the sides, just above the message box, and no wider than the chat ([ChatMax]). */
@Composable
fun DockShell(content: @Composable () -> Unit) {
    Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.TopCenter) {
        Box(Modifier.widthIn(max = ChatMax).fillMaxWidth().padding(start = 12.dp, end = 12.dp, bottom = 8.dp)) { content() }
    }
}

/** How wide the chat, the dock and the message box grow on a wide screen (design-system §5): 760. */
val ChatMax = 760.dp

/** The folded dock: one dark capsule, 「● 类型 · 摘要」 and 处理 with a down arrow. */
@Composable
private fun DockLine(label: String, rest: String, onOpen: () -> Unit) {
    val p = pal
    val shape = RoundedCornerShape(22.dp)
    Row(
        Modifier.fillMaxWidth().floating(p, shape).clip(shape).background(p.tc).border(0.5.dp, p.tcLine, shape)
            .heightIn(min = 48.dp).clickable(role = Role.Button, onClickLabel = "处理", onClick = onOpen)
            .padding(start = 16.dp, end = 14.dp, top = 6.dp, bottom = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(7.dp),
    ) {
        Box(Modifier.size(8.dp).clip(CircleShape).background(CardColors.orange))
        // One text, so a long type keeps its place first and the summary gives way.
        Text(
            buildAnnotatedString {
                withStyle(SpanStyle(color = CardColors.orange, fontWeight = T.s13b.fontWeight)) { append(label) }
                if (rest.isNotEmpty()) withStyle(SpanStyle(color = CardColors.text2)) { append(" · $rest") }
            },
            style = T.s13, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f),
        )
        ArrowText("处理", CardColors.link, T.s13, down = true)
    }
}

/** A blue text action on the dark card, 44 tall, with an arrow to the right, or [up] / [down] for one that folds. */
@Composable
fun CardLink(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, up: Boolean = false, down: Boolean = false, arrow: Boolean = true, lead: androidx.compose.ui.unit.Dp = 12.dp) {
    Box(modifier.heightIn(min = 44.dp).clickable(role = Role.Button, onClick = onClick).padding(start = lead), contentAlignment = Alignment.CenterEnd) {
        if (arrow) ArrowText(text, CardColors.link, T.s13, down = down, up = up) else Text(text, style = T.s13, color = CardColors.link)
    }
}

/** A line on the dark card that something went wrong, in red, with at most one action. */
@Composable
fun CardNote(text: String, modifier: Modifier = Modifier, action: String? = null, onAction: () -> Unit = {}) {
    Row(modifier.fillMaxWidth().padding(top = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = T.s13, color = CardColors.red, modifier = Modifier.weight(1f))
        if (action != null) CardLink(action, onAction, arrow = false)
    }
}

/** A waiting item that cannot be read yet, on the same card. */
@Composable
private fun DockSkeleton() = DockShell {
    DarkCard(float = true) { Text(skeletonMessage, style = T.s15, color = CardColors.text2) }
}

@Composable
fun ApprovalDock(s: SessionView, a: ApprovalRequested, more: Int) {
    if (isSkeleton(a)) return DockSkeleton()
    val state by LocalRemote.current.state.collectAsState()
    val d = rememberApprovalReply(s, a)
    val t = now(15_000)
    var denying by rememberSaveable(a.approvalId) { mutableStateOf(false) }
    // Answered, the card shrinks to the one line saying what was answered (§4 动效, 常).
    AnimatedContent(d.submitted, transitionSpec = { dockSwap() }, label = "approval-answered") { submitted ->
        if (submitted) DockShell { ApprovalSubmitted(approvalHead(a), d) }
        else PendingDock(
            s.id,
            DockItem.Approval(a),
            agentName(s.agent()),
            more,
            left = if (a.hasDeadline()) "剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟" else "",
            openLabel = approvalHead(a),
            body = { ApprovalContent(a) },
        ) {
            ApprovalButtons(state, s, a, d, denying, { denying = it }, approvalLocked(state, a, t))
        }
    }
}

/** What an approval would run, whole, then why: the dock's and the page's body. */
@Composable
fun ApprovalContent(a: ApprovalRequested) {
    val body = approvalBody(a)
    if (body.isNotEmpty()) CommandBlock(body, Modifier.padding(top = 14.dp))
    if (approvalTruncated(a)) Text(approvalTruncatedMessage, style = T.s13, color = CardColors.red, modifier = Modifier.padding(top = 8.dp))
    if (a.description.isNotEmpty())
        Text(approvalDisplay(a.description), style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
}

/**
 * Decisions on approvals sent from here, for this run. One whose outcome is unknown is sent again
 * unchanged, the same request (architecture §10): the daemon answers it from its log, and the
 * opposite decision is never offered in its place.
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

/**
 * Sends [allow], or the earlier decision while its outcome is unknown; returns the decision that
 * went.
 */
suspend fun resolveApproval(
    remote: io.github.lewin671.remoteagent.core.Remote,
    s: SessionView,
    a: ApprovalRequested,
    allow: Boolean,
    message: String,
    always: Boolean = false,
): Boolean {
    require(!isSkeleton(a)) { skeletonMessage }
    val r = Resolves.of(a.approvalId)
    require(!approvalTruncated(a) || !(allow || (r.attempt.frozen && r.allow))) {
        approvalTruncatedMessage
    }
    try {
        val retry = r.attempt.frozen
        val prepared =
            r.attempt.next {
                r.allow = allow
                r.message = message
                r.always = always
                remote.prepare(
                    s.machineId,
                    s.id,
                    remote.cmd {
                        resolveApproval =
                            ResolveApprovalRequest.newBuilder()
                                .setSessionId(s.id)
                                .setApprovalId(a.approvalId)
                                .setAllow(allow)
                                .setMessage(message)
                                .setAlways(always)
                                .build()
                    },
                )
            }
        remote.forward(prepared, retryOfUnknown = retry)
        r.attempt.settle()
        return r.allow
    } catch (e: Exception) {
        throw r.attempt.settle(e) ?: e
    }
}

/**
 * Answers to questions, per question and index, for this run. One whose outcome is unknown is sent
 * again unchanged, under its request ID, so the daemon answers from its log (architecture §10): a
 * new request would be refused once the CLI had moved on to the next question.
 */
object Answers {
    class Answer {
        val attempt = io.github.lewin671.remoteagent.core.Attempt()

        /** The options of the answer being sent, 0-based. */
        var options: List<Int> = emptyList()
    }

    private val byQuestion = HashMap<String, Answer>()

    fun of(questionId: String, index: Int): Answer =
        byQuestion.getOrPut("$questionId#$index") { Answer() }

    fun clear() = byQuestion.clear()

    /**
     * Sends [options], or the earlier answer while its outcome is unknown, and returns the options
     * that were answered.
     */
    suspend fun send(
        a: Answer,
        options: List<Int>,
        prepare: (List<Int>) -> io.github.lewin671.remoteagent.core.PreparedCommand,
        forward: suspend (io.github.lewin671.remoteagent.core.PreparedCommand, Boolean) -> Unit,
    ): List<Int> {
        try {
            val retry = a.attempt.frozen
            val prepared =
                a.attempt.next {
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
 * Where the user is in a question, shared by the dock and the full page: which of several
 * questions, and what is ticked in a multiple choice, so going between the two keeps both.
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
    q.text.length > 120 ||
        q.optionsCount > 4 ||
        q.optionsList.sumOf { it.label.length + it.description.length } > 400

/**
 * Answers the current question by option, on the dark card; several
 * questions go one by one. One choice answers at a tap; a multiple choice
 * ticks, then 提交 N 项. The dock shows it in [body] ([full] false, the
 * question cut at four lines with 展开全文 to its page); the page as a whole.
 */
@Composable
fun QuestionBody(
    s: SessionView,
    q: QuestionAsked,
    onTerminal: () -> Unit,
    full: Boolean,
    onLong: () -> Unit = {},
    onAnswered: () -> Unit = {},
    part: Int = 0,
    act: Action = rememberAction(),
) {
    if (isSkeleton(q)) {
        if (part != 2) Text(skeletonMessage, style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
        return
    }
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
        act.run(
            {
                val sent =
                    Answers.send(
                        answer,
                        options,
                        { opts ->
                            remote.prepare(
                                s.machineId,
                                s.id,
                                remote.cmd {
                                    answerQuestion =
                                        AnswerQuestionRequest.newBuilder()
                                            .setSessionId(s.id)
                                            .setQuestionId(qid)
                                            .also { b ->
                                                if (multiSelect)
                                                    b.addAllOptions(opts.map { it + 1 })
                                                else b.setOption(opts.first() + 1)
                                                if (qs.size > 1) b.setQuestionIndex(qi)
                                            }
                                            .build()
                                },
                            )
                        },
                    ) { pc, retry ->
                        remote.forward(pc, retryOfUnknown = retry)
                    }
                // Show what was answered, which after an unknown outcome is the earlier answer.
                if (multiSelect) QuestionPicks.multi["$qid#$qi"] = sent.toSet()
                else QuestionPicks.single["$qid#$qi"] = sent.first()
                null
            },
        ) {
            if (qi + 1 < qs.size) QuestionPicks.index[qid] = qi + 1 else onAnswered()
        }
    }
    // part 0: everything; 1: the scrolling body; 2: the fixed actions.
    if (part != 2) {
        Text(
            cur.text,
            style = T.s17.copy(lineHeight = 24.6.sp),
            color = CardColors.text,
            maxLines = if (full) Int.MAX_VALUE else 4,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(top = 12.dp),
        )
        if (!full && longQuestion(cur)) CardLink("展开全文", onLong, lead = 0.dp)
        cur.optionsList.forEachIndexed { i, o ->
            val (label, rec) = optionLabel(o.label)
            val sel =
                if (cur.multiSelect) i in (QuestionPicks.multi[key] ?: emptySet())
                else QuestionPicks.single[key] == i
            val ink = if (sel) CardColors.onPrimary else CardColors.text
            val ink2 = if (sel) CardColors.onPrimary.copy(alpha = .6f) else CardColors.text2
            Row(
                Modifier.fillMaxWidth()
                    .padding(top = 8.dp)
                    .heightIn(min = 46.dp)
                    .clip(Radii.R14)
                    .background(if (sel) CardColors.primary else CardColors.field)
                    .clickable(enabled = !act.running, role = if (cur.multiSelect) Role.Checkbox else Role.Button) {
                        if (cur.multiSelect) {
                            val set = (QuestionPicks.multi[key] ?: emptySet()).toMutableSet()
                            if (!set.add(i)) set.remove(i)
                            QuestionPicks.multi[key] = set
                        } else {
                            QuestionPicks.single[key] = i
                            submit(listOf(i))
                        }
                    }
                    .padding(horizontal = 14.dp, vertical = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                if (cur.multiSelect)
                    Box(
                        Modifier.size(20.dp).clip(RoundedCornerShape(5.dp)).border(1.5.dp, ink2, RoundedCornerShape(5.dp)),
                        contentAlignment = Alignment.Center,
                    ) {
                        if (sel) Mark(Lucide.Check, ink, 14.dp)
                    }
                Column(Modifier.weight(1f)) {
                    Text(label, style = T.s17.copy(lineHeight = 23.sp), color = ink)
                    if (o.description.isNotEmpty()) Text(o.description, style = T.s13, color = ink2)
                    if (sel && act.running && !cur.multiSelect) Text("提交中…", style = T.s13, color = ink2)
                }
                if (rec) Text(
                    "推荐", style = T.s12b, color = if (sel) ink2 else CardColors.text.copy(alpha = .8f),
                    modifier = Modifier.clip(Radii.Pill).background(if (sel) Color(0x14000000) else Color(0x24FFFFFF)).padding(horizontal = 8.dp, vertical = 2.dp),
                )
            }
        }
        if (!act.running && answer.attempt.frozen) {
            val shown =
                answer.options.joinToString("、") { i ->
                    cur.optionsList.getOrNull(i)?.let { optionLabel(it.label).first } ?: "${i + 1}"
                }
            CardNote(
                if (answer.attempt.expired) "不确定「$shown」有没有提交，它已过期，不会再重发。请去终端看看"
                else "上次的回答「$shown」没能确认，终端里的问题可能已经变了。再点任一选项会重发同一个回答",
                action = if (answer.attempt.expired) "放弃" else null,
            ) {
                answer.attempt.abandon()
                act.error = ""
            }
        } else if (act.error.isNotEmpty()) {
            CardNote("没能提交，终端里的问题可能已经变了：${act.error}")
        }
    }
    if (part != 1) {
        val n = QuestionPicks.multi[key]?.size ?: 0
        if (cur.multiSelect)
            CardButton(
                if (act.running) "提交中…" else if (n == 0) "选好后提交" else "提交 $n 项",
                { submit((QuestionPicks.multi[key] ?: emptySet()).sorted()) },
                Modifier.fillMaxWidth().padding(top = 16.dp),
                primary = true,
                enabled = !act.running && n > 0,
            )
        Row(Modifier.fillMaxWidth().padding(top = if (cur.multiSelect) 4.dp else 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(if (cur.multiSelect && n > 0) "已选 $n 项" else "都不合适？", style = T.s13, color = CardColors.text2)
            CardLink("其他回答：去终端", onTerminal)
        }
    }
}

/** Where the user is in [q]: 第 2 题，共 3 题, and 可多选 when the current one is. */
fun questionProgress(q: QuestionAsked): String {
    val qs = q.questionsList
    val index = QuestionPicks.index[q.questionId] ?: 0
    val cur = qs.getOrNull(index) ?: qs.lastOrNull() ?: return ""
    return listOfNotNull(
            if (qs.size > 1) "第 ${minOf(index, qs.size - 1) + 1} 题，共 ${qs.size} 题" else null,
            if (cur.multiSelect) "可多选" else null,
        )
        .joinToString(" · ")
}

@Composable
fun QuestionDock(s: SessionView, q: QuestionAsked, more: Int, onTerminal: () -> Unit) {
    if (isSkeleton(q)) return DockSkeleton()
    val nav = LocalNav.current
    // One submission for both halves, which the dock lays out apart.
    val act = rememberAction()
    PendingDock(
        s.id,
        DockItem.Question(q),
        agentName(s.agent()),
        more,
        meta = questionProgress(q),
        body = {
            QuestionBody(s, q, onTerminal, full = false, onLong = { nav.question(s.id, q.questionId) }, part = 1, act = act)
        },
    ) {
        QuestionBody(s, q, onTerminal, full = false, part = 2, act = act)
    }
}

/** 回答提问: a long question on a page of its own, on the same card; the answer stays at hand. */
@Composable
fun QuestionPage(sid: String, qid: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val lookup = sessionLookup(sid) // Fetches it by its ID when it is not held here.
    val events by remote.events(sid).collectAsState()
    val d =
        rememberDerived(
            events,
            s?.agent() ?: DEFAULT_AGENT,
        )
    val q = d.questions[qid]
    PageScaffold("回答提问", subtitle = s?.let { titleOf(it) + " · " + agentName(it.agent()) }) {
        if (s == null || q == null) {
            PageNote(
                if (d.answered.containsKey(qid)) "这个问题已经回答了。"
                else io.github.lewin671.remoteagent.core.lookupNote(lookup) ?: "读取中…",
            )
            return@PageScaffold
        }
        val act = rememberAction()
        val toTerminal = {
            nav.back()
            nav.sessionTerminal(sid)
        }
        DarkCard(Modifier.weight(1f, fill = false).padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 16.dp)) {
            DarkCardHead("${agentName(s.agent())} 在问你", questionProgress(q))
            Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState())) {
                QuestionBody(s, q, toTerminal, full = true, onAnswered = { nav.back() }, part = 1, act = act)
            }
            QuestionBody(s, q, toTerminal, full = true, onAnswered = { nav.back() }, part = 2, act = act)
        }
    }
}

@Composable
fun PlanDock(s: SessionView, text: String, more: Int, onFull: () -> Unit) {
    if (isSkeleton(text)) return DockSkeleton()
    PendingDock(
        s.id,
        DockItem.Plan(text),
        agentName(s.agent()),
        more,
        // In the open line before 收起, as on the web (design-system §6).
        headExtra = { CardLink("展开全文", onFull) },
        body = {
            // The whole plan, scrolling with the dock; 展开全文 in its line opens it as a page.
            Text(
                text.lines().map { io.github.lewin671.remoteagent.core.plain(it) }.filter { it.isNotBlank() }.joinToString("\n"),
                style = T.s15,
                color = CardColors.text,
                modifier = Modifier.padding(top = 12.dp),
            )
        },
    ) {
        PlanButtons(s, full = false)
        agentInfo(s.agent()).planNote?.let {
            Text(it, style = T.s13, color = CardColors.text2, modifier = Modifier.padding(top = 8.dp))
        }
    }
}

/**
 * 修改计划… / 批准, on the dark card ([onCard]) or at the foot of the plan's
 * page. A reply whose outcome is unknown takes their place: it can only be
 * sent again as it was, by a button that names its choice, or given up
 * ([Plans]).
 */
@Composable
fun PlanButtons(
    s: SessionView,
    full: Boolean,
    onCard: Boolean = true,
    onDone: () -> Unit = {},
) {
    val remote = LocalRemote.current
    val act = rememberAction()
    val reply = Plans.of(s.id)
    val offer = planOffer(reply)
    fun answer(c: PlanChoice) =
        act.run(
            {
                Plans.send(
                    reply,
                    c,
                    { choice ->
                        remote.prepare(
                            s.machineId,
                            s.id,
                            remote.cmd {
                                answerPlan =
                                    AnswerPlanRequest.newBuilder()
                                        .setSessionId(s.id)
                                        .setChoice(choice)
                                        .build()
                            },
                        )
                    },
                ) { prepared, retry ->
                    remote.forward(prepared, retryOfUnknown = retry)
                }
                null
            },
        ) {
            onDone()
        }
    fun retry() =
        act.run(
            {
                Plans.retry(reply) { prepared, retry ->
                    remote.forward(prepared, retryOfUnknown = retry)
                }
                null
            },
        ) {
            onDone()
        }
    @Composable
    fun Choice(text: String, onClick: () -> Unit, modifier: Modifier, primary: Boolean = false, enabled: Boolean = true) =
        if (onCard) CardButton(text, onClick, modifier, primary, enabled)
        else PillButton(text, onClick, modifier, if (primary) ButtonKind.INK else ButtonKind.SECONDARY, enabled)
    @Composable
    fun Note(text: String) =
        if (onCard) CardNote(text) else Notice(text, Modifier.padding(top = 10.dp), NoticeTone.ERROR)
    if (!offer.choices) {
        Note(offer.note)
        Row(Modifier.fillMaxWidth().padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            if (offer.abandon)
                Choice(
                    "放弃",
                    {
                        Plans.abandon(reply)
                        act.error = ""
                    },
                    Modifier.weight(1f),
                    enabled = !act.running,
                )
            offer.retry?.let {
                Choice(if (act.running) "提交中…" else it, { retry() }, Modifier.weight(1f), primary = true, enabled = !act.running)
            }
        }
        return
    }
    Row(Modifier.fillMaxWidth().padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Choice(agentInfo(s.agent()).planKeepLabel, { answer(PlanChoice.PLAN_CHOICE_KEEP_PLANNING) }, Modifier.weight(1f), enabled = !act.running)
        Choice(
            if (act.running) "提交中…" else agentInfo(s.agent()).planApproveLabel,
            { answer(PlanChoice.PLAN_CHOICE_IMPLEMENT) },
            Modifier.weight(1f),
            primary = true,
            enabled = !act.running,
        )
    }
    // Claude Code has no such choice.
    if (full && !act.running && agentInfo(s.agent()).planClearContext) {
        val clear = { answer(PlanChoice.PLAN_CHOICE_CLEAR_AND_IMPLEMENT) }
        if (onCard) CardLink("清空上下文后执行", clear, Modifier.fillMaxWidth(), arrow = false)
        else TextLink("清空上下文后执行", clear, Modifier.fillMaxWidth())
    }
    if (act.error.isNotEmpty()) Note("没能提交：${act.error}")
}

@Composable
fun PopupDock(s: SessionView, title: String, more: Int, onTerminal: () -> Unit) {
    PendingDock(
        s.id,
        DockItem.Popup(title),
        agentName(s.agent()),
        more,
        meta = "${agentName(s.agent())} 在等你",
        body = {
            if (title.isNotEmpty())
                Text(title, style = T.mono13.copy(fontSize = 15.sp, lineHeight = 22.sp), color = CardColors.text, modifier = Modifier.padding(top = 12.dp))
            val hint = io.github.lewin671.remoteagent.core.popupHint(s.agent(), title)
            if (hint.isNotEmpty()) Text(hint, style = T.s13, color = CardColors.orange, modifier = Modifier.padding(top = 8.dp))
        },
    ) {
        CardButton("去终端处理", onTerminal, Modifier.fillMaxWidth().padding(top = 16.dp), primary = true)
    }
}
