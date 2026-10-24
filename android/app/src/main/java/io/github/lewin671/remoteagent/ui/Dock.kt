package io.github.lewin671.remoteagent.ui

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
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.agent
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
 * What waits for the user, docked above the message box. It opens by itself, unless it comes while
 * the box holds a draft ([DockFolds]); open, its one head line carries the type and the meta, and
 * the body scrolls within [LocalDockMax] while the actions stay put. Folded by the user, it stays
 * one line until the next item. [more] others wait behind it.
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
    val p = pal
    val open = DockFolds.isOpen(sid, item.id) { hasDraft(sid) }
    val keyboard = LocalSoftwareKeyboardController.current
    val focus = LocalFocusManager.current
    val (label, rest) = dockHeadLine(item, agent, open, more, meta, left, openLabel)
    Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp).topLine(p.line)) {
        Row(
            Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(
                role = Role.Button,
                onClickLabel = if (open) "收起" else "处理",
            ) {
                if (!open) {
                    keyboard?.hide()
                    focus.clearFocus()
                }
                DockFolds.set(sid, item.id, !open)
            },
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            Dot(p.orangeInk, 7)
            // One text, so a long type keeps its place first and the rest gives way.
            Text(
                buildAnnotatedString {
                    withStyle(SpanStyle(color = p.fg2)) { append(label) }
                    if (rest.isNotEmpty())
                        withStyle(SpanStyle(color = p.muted)) { append("  $rest") }
                },
                style = T.sm,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
            if (open) headExtra?.invoke()
            ArrowText(if (open) "收起" else "处理", p.blue, T.sm, down = !open, up = open)
        }
        if (open) {
            Column(Modifier.heightIn(max = LocalDockMax.current)) {
                Column(
                    Modifier.weight(1f, fill = false)
                        .verticalScroll(rememberScrollState())
                        .padding(bottom = 6.dp),
                ) {
                    body { DockFolds.set(sid, item.id, false) }
                }
                Column(Modifier.padding(top = 4.dp, bottom = 10.dp)) { actions() }
            }
        }
    }
}

/**
 * The grey line under a dock's buttons: a question at the left, one link at the right, with an
 * [arrow] when it goes somewhere else.
 */
@Composable
fun DockFoot(
    left: String,
    link: String,
    danger: Boolean = false,
    arrow: Boolean = false,
    onLink: () -> Unit,
) {
    val p = pal
    // With nothing at the left the link starts the line.
    Row(
        Modifier.fillMaxWidth().padding(top = 7.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (left.isNotEmpty())
            Text(left, style = T.meta, color = p.muted, modifier = Modifier.weight(1f))
        Box(
            Modifier.heightIn(min = 44.dp).clickable(onClick = onLink),
            contentAlignment = Alignment.CenterStart,
        ) {
            if (arrow) ArrowText(link, if (danger) p.red else p.blue, T.metaB)
            else Text(link, style = T.metaB, color = if (danger) p.red else p.blue)
        }
    }
}

@Composable
fun ApprovalDock(s: SessionView, a: ApprovalRequested, more: Int) {
    if (isSkeleton(a)) {
        Text(skeletonMessage, style = T.sm, color = pal.muted)
        return
    }
    val p = pal
    val t = now(15_000)
    val agent = agentName(s.agent())
    var reasonOpen by rememberSaveable(a.approvalId) { mutableStateOf(false) }
    PendingDock(
        s.id,
        DockItem.Approval(a),
        agent,
        more,
        left = if (a.hasDeadline()) "剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟" else "",
        openLabel = approvalHead(a),
        body = {
            // What would run comes first, whole, scrolling with the dock; then why.
            val body = approvalBody(a)
            if (body.isNotEmpty()) Code(body, Modifier.padding(bottom = 8.dp))
            if (a.description.isNotEmpty())
                Text(
                    approvalDisplay(a.description),
                    style = T.sm,
                    color = p.fg2,
                )
        },
    ) {
        ApprovalButtons(
            s,
            a,
            compact = false,
            reasonOpen = reasonOpen,
            onReason = { reasonOpen = it },
        )
    }
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
 * 拒绝 / 允许, and 附理由拒绝. A tap locks both until the computer confirms; a definite failure lets the
 * user choose again, while an unknown outcome only offers to retry the same decision. [onCard]: on
 * a grey card, where the buttons are white.
 */
@Composable
fun ApprovalButtons(
    s: SessionView,
    a: ApprovalRequested,
    compact: Boolean,
    reasonOpen: Boolean = false,
    onReason: (Boolean) -> Unit = {},
    onCard: Boolean = false,
) {
    if (isSkeleton(a)) {
        Text(skeletonMessage, style = T.sm, color = pal.muted)
        return
    }
    val p = pal
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val act = rememberAction(a.approvalId)
    val r = Resolves.of(a.approvalId)
    var which by
        remember(a.approvalId) { mutableStateOf<Boolean?>(if (r.attempt.frozen) r.allow else null) }
    var reason by Drafts.of("deny:${a.approvalId}")
    var opened by remember(a.approvalId) { mutableStateOf(false) }
    val alwaysAvailable =
        io.github.lewin671.remoteagent.core
            .capabilities(state.machines[s.machineId])
            ?.approvalAlways == true
    val stuck = r.attempt.frozen
    val truncated = approvalTruncated(a)
    if (truncated) Text(approvalTruncatedMessage, style = T.sm, color = p.red)
    fun resolve(allow: Boolean, message: String = "", always: Boolean = false) {
        which = allow
        act.run(
            {
                resolveApproval(remote, s, a, allow, message, always)
                null
            },
        )
    }
    if (act.running || stuck || which != null) {
        if (act.running || (act.error.isEmpty() && !stuck))
            Text(if (act.running) "正在送到电脑…" else "已提交，等待电脑确认", style = T.meta, color = p.muted)
    } else if (opened || reasonOpen) {
        ReasonField(reason, { reason = it }, "拒绝理由（可选）", Modifier.widthIn(max = ActionRowMax))
        Btns(Modifier.widthIn(max = ActionRowMax)) {
            Btn(
                "取消",
                {
                    opened = false
                    onReason(false)
                },
                Modifier.weight(1f),
                BtnKind.SECONDARY,
            )
            Btn("不允许", { resolve(false, reason.trim()) }, Modifier.weight(1f), BtnKind.NEGATIVE)
        }
    } else {
        Btns(Modifier.widthIn(max = ActionRowMax)) {
            Btn(
                "不允许",
                {
                    opened = true
                    onReason(true)
                },
                Modifier.weight(1f),
                BtnKind.NEGATIVE,
                bg = if (onCard) p.card else null,
            )
            Btn("允许", { resolve(true) }, Modifier.weight(1f), BtnKind.PRIMARY, enabled = !truncated)
        }
        if (alwaysAvailable && !truncated)
            TextAction(
                "允许，这类请求以后不再问",
                { resolve(true, always = true) },
                modifier = Modifier.widthIn(max = ActionRowMax).fillMaxWidth(),
                style = T.smB,
                alignment = Alignment.Center,
            )
    }
    if (!act.running && stuck) {
        val what = if (r.allow) "允许" else "拒绝"
        Banner(
            if (r.attempt.expired) "不确定「$what」有没有送到，它已过期，不会再重发。审批超时后会自动拒绝"
            else "没送到电脑：${act.error.ifEmpty { "结果未知" }}",
            Modifier.padding(top = 8.dp),
            Tone2.ERROR,
            action = if (!r.attempt.expired) "重试" else null,
            onAction = { resolve(r.allow, r.message, r.always) },
        )
    } else if (act.error.isNotEmpty()) {
        Banner(
            "没送到电脑：${act.error}",
            Modifier.padding(top = 8.dp),
            Tone2.ERROR,
            action = "重试",
            onAction = { resolve(r.allow, r.message, r.always) },
        )
    }
}

/**
 * How wide the buttons of an approval or a plan grow: on a tablet they keep to the left instead of
 * stretching across the screen; a phone is narrower anyway.
 */
val ActionRowMax = 480.dp

/** A text box on a soft field, for a reason or another short answer. */
@Composable
fun ReasonField(
    value: String,
    onValue: (String) -> Unit,
    placeholder: String,
    modifier: Modifier = Modifier,
    singleLine: Boolean = false,
) {
    val p = pal
    BasicTextField(
        value,
        onValue,
        modifier.fillMaxWidth().padding(bottom = 8.dp),
        textStyle = T.body.copy(color = p.fg),
        cursorBrush = SolidColor(p.blue),
        maxLines = if (singleLine) 1 else 4,
        singleLine = singleLine,
        decorationBox = { inner ->
            FieldBox {
                if (value.isEmpty()) Text(placeholder, style = T.body, color = p.muted)
                inner()
            }
        },
    )
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
 * Answers the current question by option; several questions go one by one. One choice answers at a
 * tap; a multiple choice ticks, then 提交 N 项. The dock shows it in [body]; the page ([full]) as a
 * whole.
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
        if (part != 2) Text(skeletonMessage, style = T.sm, color = pal.muted)
        return
    }
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
        val progress = questionProgress(q)
        // In the dock the head line carries the progress.
        if (full)
            Text(
                progress,
                style = T.meta,
                color = p.muted,
                modifier = Modifier.padding(top = 12.dp),
            )
        Text(
            cur.text,
            style = if (full) T.h17 else T.bodyB,
            color = p.fg,
            maxLines = if (full) Int.MAX_VALUE else 4,
            overflow = TextOverflow.Ellipsis,
            modifier =
                Modifier.padding(
                    top = if (full) 8.dp else 0.dp,
                    bottom = if (full) 12.dp else 6.dp,
                ),
        )
        if (!full && longQuestion(cur))
            Box(Modifier.clickable(onClick = onLong).padding(bottom = 4.dp)) {
                ArrowText("展开全文", p.blue, T.smB)
            }
        cur.optionsList.forEachIndexed { i, o ->
            val (label, rec) = optionLabel(o.label)
            val sel =
                if (cur.multiSelect) i in (QuestionPicks.multi[key] ?: emptySet())
                else QuestionPicks.single[key] == i
            Row(
                Modifier.fillMaxWidth()
                    .padding(vertical = 3.dp)
                    .heightIn(min = 44.dp)
                    .clip(Radii.SM)
                    .background(
                        if (sel) p.blueSoft else androidx.compose.ui.graphics.Color.Transparent,
                    )
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
                verticalAlignment = Alignment.Top,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                if (cur.multiSelect)
                    Box(
                        Modifier.padding(top = 1.dp)
                            .size(18.dp)
                            .clip(RoundedCornerShape(4.dp))
                            .background(
                                if (sel) p.blueBg
                                else androidx.compose.ui.graphics.Color.Transparent,
                            )
                            .border(1.dp, if (sel) p.blueBg else p.muted, RoundedCornerShape(4.dp)),
                        contentAlignment = Alignment.Center,
                    ) {
                        if (sel) Mark(Lucide.Check, androidx.compose.ui.graphics.Color.White, 12.dp)
                    }
                else if (sel)
                    Box(Modifier.padding(top = 3.dp)) { Mark(Lucide.Check, p.blue, 13.dp) }
                Column(Modifier.weight(1f)) {
                    Text(label, style = T.sm, color = p.fg)
                    if (o.description.isNotEmpty())
                        Text(o.description, style = T.meta, color = p.muted)
                    if (sel && act.running && !cur.multiSelect)
                        Text("提交中…", style = T.meta, color = p.muted)
                }
                if (rec) Text("推荐", style = T.metaB, color = p.blue)
            }
        }
        if (!act.running && answer.attempt.frozen) {
            val shown =
                answer.options.joinToString("、") { i ->
                    cur.optionsList.getOrNull(i)?.let { optionLabel(it.label).first } ?: "${i + 1}"
                }
            Banner(
                if (answer.attempt.expired) "不确定「$shown」有没有提交，它已过期，不会再重发。请去终端看看"
                else "上次的回答「$shown」没能确认，终端里的问题可能已经变了。再点任一选项会重发同一个回答",
                Modifier.padding(top = 6.dp),
                Tone2.ERROR,
                action = if (answer.attempt.expired) "放弃" else null,
                onAction = {
                    answer.attempt.abandon()
                    act.error = ""
                },
            )
        } else if (act.error.isNotEmpty()) {
            Banner("没能提交，终端里的问题可能已经变了：${act.error}", Modifier.padding(top = 6.dp), Tone2.ERROR)
        }
    }
    if (part != 1) {
        if (cur.multiSelect) {
            val n = QuestionPicks.multi[key]?.size ?: 0
            Btn(
                if (act.running) "提交中…" else if (n == 0) "选好后提交" else "提交 $n 项",
                { submit((QuestionPicks.multi[key] ?: emptySet()).sorted()) },
                Modifier.fillMaxWidth().padding(top = 6.dp),
                BtnKind.PRIMARY,
                enabled = !act.running && n > 0,
            )
            DockFoot(if (n > 0) "已选 $n 项" else "", "其他回答：去终端", arrow = true, onLink = onTerminal)
        } else DockFoot("都不合适？", "其他回答：去终端", arrow = true, onLink = onTerminal)
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

/** 第 1 题 of several, as short bars. */
@Composable
fun Steps(n: Int, at: Int) {
    val p = pal
    Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        repeat(n) { i ->
            Box(
                Modifier.width(16.dp)
                    .height(4.dp)
                    .clip(RoundedCornerShape(2.dp))
                    .background(if (i <= at) p.blue else p.track),
            )
        }
    }
}

@Composable
fun QuestionDock(s: SessionView, q: QuestionAsked, more: Int, onTerminal: () -> Unit) {
    val nav = LocalNav.current
    // One submission for both halves, which the dock lays out apart.
    val act = rememberAction()
    val qs = q.questionsList
    PendingDock(
        s.id,
        DockItem.Question(q),
        agentName(s.agent()),
        more,
        meta = if (isSkeleton(q)) "" else questionProgress(q),
        headExtra =
            if (qs.size > 1 && !isSkeleton(q))
                ({ Steps(qs.size, minOf(QuestionPicks.index[q.questionId] ?: 0, qs.size - 1)) })
            else null,
        body = {
            QuestionBody(
                s,
                q,
                onTerminal,
                full = false,
                onLong = { nav.question(s.id, q.questionId) },
                part = 1,
                act = act,
            )
        },
    ) {
        QuestionBody(s, q, onTerminal, full = false, part = 2, act = act)
    }
}

/** 回答提问: a long question on a page of its own; the answer button stays at hand. */
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
            s?.agent() ?: io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX,
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
        Column(
            Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 14.dp),
        ) {
            QuestionBody(
                s,
                q,
                toTerminal,
                full = true,
                onAnswered = { nav.back() },
                part = 1,
                act = act,
            )
        }
        Column(
            Modifier.padding(horizontal = 14.dp)
                .topLine(pal.line)
                .padding(top = 8.dp, bottom = 16.dp),
        ) {
            QuestionBody(
                s,
                q,
                toTerminal,
                full = true,
                onAnswered = { nav.back() },
                part = 2,
                act = act,
            )
        }
    }
}

@Composable
fun PlanDock(s: SessionView, text: String, more: Int, onFull: () -> Unit) {
    if (isSkeleton(text)) {
        Text(skeletonMessage, style = T.sm, color = pal.muted)
        return
    }
    val p = pal
    PendingDock(
        s.id,
        DockItem.Plan(text),
        agentName(s.agent()),
        more,
        // In the open line before 收起, as on the web (design-system §6).
        headExtra = {
            Box(
                Modifier.heightIn(min = 44.dp)
                    .clickable(role = Role.Button, onClick = onFull)
                    .padding(end = 6.dp),
                contentAlignment = Alignment.CenterEnd,
            ) {
                ArrowText("展开全文", p.blue, T.sm)
            }
        },
        body = {
            // The whole plan, scrolling with the dock; 展开全文 in its line opens it as a page.
            Text(
                text
                    .lines()
                    .map { io.github.lewin671.remoteagent.core.plain(it) }
                    .filter { it.isNotBlank() }
                    .joinToString("\n"),
                style = ts(13.sp, lineHeight = 1.6f),
                color = p.fg,
                modifier =
                    Modifier.padding(bottom = 4.dp)
                        .fillMaxWidth()
                        .clip(Radii.XS)
                        .background(p.soft)
                        .padding(horizontal = 10.dp, vertical = 7.dp),
            )
        },
    ) {
        PlanButtons(s, full = false, terminalOnly = isSkeleton(text))
        // Claude Code leaves plan mode for manual approval of edits.
        if (!isSkeleton(text) && s.agent() == io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE)
            Text(
                "批准后使用询问模式",
                style = T.meta,
                color = p.muted,
                modifier = Modifier.padding(top = 7.dp),
            )
    }
}

/**
 * 继续规划… / 批准执行. A reply whose outcome is unknown takes their place: it can only be sent again as it
 * was, by a button that names its choice, or given up ([Plans]).
 */
@Composable
fun PlanButtons(
    s: SessionView,
    full: Boolean,
    terminalOnly: Boolean = false,
    onDone: () -> Unit = {},
) {
    if (terminalOnly) {
        Text(skeletonMessage, style = T.sm, color = pal.muted)
        return
    }
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
    if (!offer.choices) {
        Banner(offer.note, Modifier.padding(bottom = 8.dp), Tone2.ERROR)
        Btns(Modifier.widthIn(max = ActionRowMax)) {
            if (offer.abandon)
                Btn(
                    "放弃",
                    {
                        Plans.abandon(reply)
                        act.error = ""
                    },
                    Modifier.weight(1f),
                    enabled = !act.running,
                )
            offer.retry?.let {
                Btn(
                    if (act.running) "提交中…" else it,
                    { retry() },
                    Modifier.weight(1f),
                    BtnKind.PRIMARY,
                    enabled = !act.running,
                )
            }
        }
        return
    }
    Btns(Modifier.widthIn(max = ActionRowMax)) {
        Btn(
            "继续规划…",
            { answer(PlanChoice.PLAN_CHOICE_KEEP_PLANNING) },
            Modifier.weight(1f),
            enabled = !act.running,
        )
        Btn(
            if (act.running) "提交中…" else "批准执行",
            { answer(PlanChoice.PLAN_CHOICE_IMPLEMENT) },
            Modifier.weight(1f),
            BtnKind.PRIMARY,
            enabled = !act.running,
        )
    }
    // Claude Code has no such choice.
    if (
        full && !act.running && s.agent() != io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE
    ) {
        DockFoot("", "清空上下文后执行") { answer(PlanChoice.PLAN_CHOICE_CLEAR_AND_IMPLEMENT) }
    }
    if (act.error.isNotEmpty())
        Banner("没能提交：${act.error}", Modifier.padding(top = 8.dp), Tone2.ERROR)
}

@Composable
fun PopupDock(s: SessionView, title: String, more: Int, onTerminal: () -> Unit) {
    val p = pal
    PendingDock(
        s.id,
        DockItem.Popup(title),
        agentName(s.agent()),
        more,
        meta = "${agentName(s.agent())} 在等你",
        body = {
            if (title.isNotEmpty())
                Text(
                    title,
                    style = ts(15.sp, FontWeight.Medium),
                    color = p.fg,
                    modifier = Modifier.padding(bottom = 6.dp),
                )
            val hint = io.github.lewin671.remoteagent.core.popupHint(s.agent(), title)
            if (hint.isNotEmpty()) Text(hint, style = T.sm, color = p.orangeInk)
        },
    ) {
        Btn("去终端处理", onTerminal, Modifier.fillMaxWidth(), BtnKind.PRIMARY)
    }
}
