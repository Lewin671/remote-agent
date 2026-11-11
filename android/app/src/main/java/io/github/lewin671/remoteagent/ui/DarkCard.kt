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
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.ApprovalCardText
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.approvalBody
import io.github.lewin671.remoteagent.core.approvalDisplay
import io.github.lewin671.remoteagent.core.approvalTruncated
import io.github.lewin671.remoteagent.core.approvalTruncatedMessage
import io.github.lewin671.remoteagent.core.capabilities
import io.github.lewin671.remoteagent.core.isSkeleton
import io.github.lewin671.remoteagent.core.skeletonMessage
import io.github.lewin671.remoteagent.proto.ApprovalRequested

/*
 * The dark card (design-system §6 终端卡): what waits for the user, dark in
 * both themes. The same shell carries an approval on the home page, and,
 * once the session is redrawn, every item docked above the composer. The
 * pieces here are that shell, its head, the black command block, its
 * buttons and field, and the one line a submitted card shrinks to;
 * [ApprovalCard] puts them together for the home page.
 */

/** Whole lines of a command the home's card shows before 看完整命令. */
const val CARD_LINES = 5

/** The lines it keeps while a reason for 不允许 is written under it. */
private const val REASON_LINES = 2

/** The shell: tc, radius 30, the thin edge in the dark theme; [float] when it floats over the page (docked). */
@Composable
fun DarkCard(modifier: Modifier = Modifier, float: Boolean = false, content: @Composable ColumnScope.() -> Unit) {
    val p = pal
    Column(
        modifier.fillMaxWidth().floating(p, Radii.R30, float).clip(Radii.R30).background(p.tc).border(0.5.dp, p.tcLine, Radii.R30).padding(18.dp),
        content = content,
    )
}

/** The card's head: the orange dot, what kind of thing waits (13 / 600), and at the right [right], orange when [hot]. */
@Composable
fun DarkCardHead(kind: String, right: String = "", hot: Boolean = false, trailing: (@Composable () -> Unit)? = null) {
    val kindRow: @Composable (Modifier) -> Unit = { m ->
        Row(m, verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(8.dp).clip(CircleShape).background(CardColors.orange))
            Spacer(Modifier.width(7.dp))
            Text(kind, style = T.s13b, color = CardColors.orange, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
    val rightText: @Composable () -> Unit = {
        if (right.isNotEmpty()) Text(right, style = T.s13, color = if (hot) CardColors.orange else CardColors.text2, textAlign = TextAlign.End)
    }
    // At a large font size the two do not fit side by side: the time goes under the kind, at the right.
    if (LocalDensity.current.fontScale >= 1.5f) Column(Modifier.fillMaxWidth()) {
        kindRow(Modifier)
        Row(Modifier.align(Alignment.End), verticalAlignment = Alignment.CenterVertically) {
            rightText()
            trailing?.invoke()
        }
    } else Row(verticalAlignment = Alignment.CenterVertically) {
        kindRow(Modifier.weight(1f).padding(end = 8.dp))
        rightText()
        trailing?.invoke()
    }
}

/**
 * The command, black in both themes, mono 13 after an orange ❯. It wraps
 * anywhere and never scrolls on its own. With [maxLines] it shows that many
 * whole lines; [onLines] hears how many it takes at full length, as laid out
 * at this width.
 */
@Composable
fun CommandBlock(text: String, modifier: Modifier = Modifier, maxLines: Int = Int.MAX_VALUE, onLines: (Int) -> Unit = {}) {
    val shown: AnnotatedString = remember(text) {
        buildAnnotatedString {
            withStyle(SpanStyle(color = CardColors.orange)) { append("❯ ") }
            append(text)
        }
    }
    val measurer = rememberTextMeasurer()
    var width by remember { mutableIntStateOf(0) }
    if (width > 0) {
        val lines = remember(shown, width) { measurer.measure(shown, T.mono13, constraints = Constraints(maxWidth = width)).lineCount }
        onLines(lines)
    }
    Box(
        modifier.fillMaxWidth().clip(Radii.R16).background(CardColors.cmd).padding(horizontal = 14.dp, vertical = 12.dp),
    ) {
        Text(
            shown, style = T.mono13, color = CardColors.cmdText, maxLines = maxLines, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.fillMaxWidth().onSizeChanged { width = it.width }.testTag("command"),
        )
    }
}

/** A button on the dark card: white with black text for the one thing to do, half-clear white otherwise; 48, 17 / 600. */
@Composable
fun CardButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, primary: Boolean = false, enabled: Boolean = true) {
    Box(
        modifier.heightIn(min = 48.dp).disabledAlpha(enabled).clip(Radii.Pill).background(if (primary) CardColors.primary else CardColors.secondary)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick).padding(horizontal = 16.dp, vertical = 6.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, style = T.s17b, color = if (primary) CardColors.onPrimary else CardColors.text, textAlign = TextAlign.Center, maxLines = 2)
    }
}

/** A field on the dark card, radius 16, 17 text; the caret is orange. */
@Composable
fun CardField(value: String, onValue: (String) -> Unit, placeholder: String, modifier: Modifier = Modifier, label: String = placeholder) {
    BasicTextField(
        value, onValue,
        modifier.fillMaxWidth().semantics { contentDescription = label },
        textStyle = T.s17.copy(color = CardColors.text),
        cursorBrush = SolidColor(CardColors.orange),
        maxLines = 4,
        decorationBox = { inner ->
            Box(Modifier.fillMaxWidth().heightIn(min = 48.dp).clip(Radii.R16).background(CardColors.field).padding(horizontal = 14.dp, vertical = 13.dp)) {
                if (value.isEmpty()) Text(placeholder, style = T.s17, color = CardColors.text2.copy(alpha = .5f))
                inner()
            }
        },
    )
}

/**
 * A card shrunk to one line once it was answered (「已允许」「已拒绝」), or,
 * when the answer did not reach the computer, why, in red, and 重试.
 */
@Composable
fun DarkCardLine(kind: String, status: String, modifier: Modifier = Modifier, error: Boolean = false, retry: (() -> Unit)? = null) {
    val p = pal
    Row(
        modifier.fillMaxWidth().clip(Radii.R24).background(p.tc).border(0.5.dp, p.tcLine, Radii.R24).heightIn(min = 48.dp)
            .padding(start = 16.dp, end = if (retry != null) 6.dp else 16.dp, top = 6.dp, bottom = 6.dp)
            .semantics(mergeDescendants = true) {},
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(8.dp).clip(CircleShape).background(CardColors.orange))
        Spacer(Modifier.width(7.dp))
        Text(kind, style = T.s13b, color = CardColors.orange, maxLines = 1)
        Spacer(Modifier.width(8.dp))
        Text(status, style = T.s13, color = if (error) CardColors.red else CardColors.text2, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (retry != null) Box(Modifier.heightIn(min = 44.dp).clickable(role = Role.Button, onClick = retry).padding(horizontal = 10.dp), contentAlignment = Alignment.Center) {
            Text("重试", style = T.s13b, color = CardColors.link)
        }
    }
}

/**
 * A decision on an approval sent from here: which way it went, whether it is
 * on its way or its outcome unknown ([Resolves]). [submitted] once the user
 * chose; the card is then one line.
 */
class ApprovalReply internal constructor(
    val act: Action,
    val r: Resolves.Resolve,
    private val which: androidx.compose.runtime.MutableState<Boolean?>,
    private val send: (allow: Boolean, message: String, always: Boolean) -> Unit,
) {
    val submitted: Boolean get() = act.running || r.attempt.frozen || which.value != null

    fun resolve(allow: Boolean, message: String = "", always: Boolean = false) = send(allow, message, always)

    internal val said: String get() = if (which.value ?: r.allow) "已允许" else "已拒绝"
}

@Composable
fun rememberApprovalReply(s: SessionView, a: ApprovalRequested): ApprovalReply {
    val remote = LocalRemote.current
    val act = rememberAction(a.approvalId)
    val r = Resolves.of(a.approvalId)
    val which = remember(a.approvalId) { mutableStateOf(if (r.attempt.frozen) r.allow else null) }
    val view = androidx.compose.ui.platform.LocalView.current
    return ApprovalReply(act, r, which) { allow, message, always ->
        // One light tap for the answer, whichever it is (§4 触感).
        Motion.tap(view)
        which.value = allow
        act.run({
            resolveApproval(remote, s, a, allow, message, always)
            null
        })
    }
}

/**
 * The one line a submitted approval shrinks to: 已允许 / 已拒绝, or, when it
 * did not reach the computer, why in red with 重试. One whose outcome is
 * unknown is only ever sent again as it was.
 */
@Composable
fun ApprovalSubmitted(kind: String, d: ApprovalReply, modifier: Modifier = Modifier) {
    val failed = !d.act.running && (d.r.attempt.frozen || d.act.error.isNotEmpty())
    when {
        !failed -> DarkCardLine(kind, d.said, modifier)
        d.r.attempt.expired -> DarkCardLine(kind, "不确定「${if (d.r.allow) "允许" else "拒绝"}」有没有送到，它已过期，不会再重发", modifier, error = true)
        else -> DarkCardLine(kind, "没送到电脑：${d.act.error.ifEmpty { "结果未知" }}", modifier, error = true) { d.resolve(d.r.allow, d.r.message, d.r.always) }
    }
}

/**
 * An approval's buttons on the dark card, the same on the home page, in the
 * dock and on its page: 不允许 and 允许, and under them 「允许，这类请求以后
 * 不再问」 where the computer offers it. A command not shown whole ([cut])
 * offers 看完整命令 in place of 允许. 不允许 asks for an optional reason in
 * place ([denying]). [locked]: out of reach of the server, or past the
 * deadline; nothing can be decided.
 */
@Composable
fun ApprovalButtons(
    state: RemoteState,
    s: SessionView,
    a: ApprovalRequested,
    d: ApprovalReply,
    denying: Boolean,
    onDenying: (Boolean) -> Unit,
    locked: Boolean,
    cut: Boolean = false,
    onFull: () -> Unit = {},
) {
    var reason by Drafts.of("deny:${a.approvalId}")
    val truncated = approvalTruncated(a)
    val always = capabilities(state.machines[s.machineId])?.approvalAlways == true
    if (denying) {
        CardField(reason, { reason = it }, "说说该怎么做，可以不填", Modifier.padding(top = 12.dp), label = "不允许的理由")
        Row(Modifier.fillMaxWidth().padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            CardButton("取消", { onDenying(false) }, Modifier.weight(1f))
            CardButton("不允许", { d.resolve(false, reason.trim()) }, Modifier.weight(1f), primary = true, enabled = !locked)
        }
        return
    }
    Row(Modifier.fillMaxWidth().padding(top = 16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        CardButton("不允许", { onDenying(true) }, Modifier.weight(1f), enabled = !locked)
        if (cut) CardButton("看完整命令", onFull, Modifier.weight(1f), primary = true)
        else CardButton("允许", { d.resolve(true) }, Modifier.weight(1f), primary = true, enabled = !locked && !truncated)
    }
    if (always && !cut && !locked && !truncated) Box(
        Modifier.fillMaxWidth().heightIn(min = 44.dp).padding(top = 4.dp).clickable(role = Role.Button) { d.resolve(true, always = true) },
        contentAlignment = Alignment.Center,
    ) { Text("允许，这类请求以后不再问", style = T.s15, color = CardColors.link) }
}

/** Whether an approval can be decided now: not out of reach of the server, and not past its deadline. */
fun approvalLocked(state: RemoteState, a: ApprovalRequested, now: Long): Boolean {
    val deadline = if (a.hasDeadline()) a.deadline.seconds * 1000 else 0
    return state.unlinked || (deadline > 0 && now >= deadline)
}

/**
 * The home page's dark card for the most urgent approval (design-system §6
 * 终端卡): head, title (which opens the session), 「项目 · 机器」, the
 * command, why, the buttons. A command not shown whole cannot be allowed:
 * the card offers 不允许 and 看完整命令 until it is opened in place. 不允许
 * asks for an optional reason right on the card. Once answered the card is
 * one line.
 */
@Composable
fun ApprovalCard(
    state: RemoteState,
    s: SessionView,
    a: ApprovalRequested,
    text: ApprovalCardText,
    now: Long,
    onOpen: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val d = rememberApprovalReply(s, a)
    var full by rememberSaveable(a.approvalId) { mutableStateOf(false) }
    var denying by rememberSaveable(a.approvalId) { mutableStateOf(false) }
    var lines by remember(a.approvalId) { mutableIntStateOf(0) }
    if (isSkeleton(a)) return DarkCard(modifier) {
        DarkCardHead(text.kind, text.time, text.hot)
        Text(skeletonMessage, style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
    }
    // Submitted: one line of where it stands; the next item comes up when this one is gone from the session.
    if (d.submitted) return ApprovalSubmitted(text.kind, d, modifier)
    val command = approvalBody(a)
    val more = lines - CARD_LINES
    val cut = command.isNotEmpty() && more > 0 && !full
    DarkCard(modifier.semantics(mergeDescendants = false) {}) {
        DarkCardHead(text.kind, text.time, text.hot)
        Row(
            Modifier.padding(top = 12.dp).heightIn(min = 32.dp).clickable(onClickLabel = "打开会话", role = Role.Button, onClick = onOpen).semantics { heading() },
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(text.title, style = T.h20, color = CardColors.text, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            Spacer(Modifier.width(2.dp))
            Ic(Lucide.ChevronRight, 20.dp, CardColors.text.copy(alpha = .5f))
        }
        Text(text.place, style = T.s13, color = CardColors.text2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 2.dp))
        if (command.isNotEmpty()) CommandBlock(
            command, Modifier.padding(top = 14.dp),
            maxLines = if (denying) REASON_LINES else if (cut) CARD_LINES else Int.MAX_VALUE,
            onLines = { lines = it },
        )
        if (!denying) {
            if (cut) Text("还有 $more 行没显示", style = T.s13, color = CardColors.text2, modifier = Modifier.padding(top = 8.dp))
            else if (full && more > 0) Row(verticalAlignment = Alignment.CenterVertically) {
                Text("共 $lines 行", style = T.s13, color = CardColors.text2)
                Box(Modifier.heightIn(min = 36.dp).clickable(role = Role.Button) { full = false }.padding(horizontal = 6.dp), contentAlignment = Alignment.Center) {
                    Text("收起", style = T.s13, color = CardColors.link)
                }
            }
        }
        if (approvalTruncated(a)) Text(approvalTruncatedMessage, style = T.s13, color = CardColors.red, modifier = Modifier.padding(top = 8.dp))
        if (!denying && a.description.isNotEmpty())
            Text(approvalDisplay(a.description), style = T.s15, color = CardColors.text2, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 12.dp))
        ApprovalButtons(state, s, a, d, denying, { denying = it }, approvalLocked(state, a, now), cut = cut) { full = true }
    }
}

/** The approval of a session that waits on the home page, read from its events; null until they are. */
@Composable
fun rememberApproval(s: SessionView): ApprovalRequested? {
    val remote = LocalRemote.current
    val events by remote.events(s.id).collectAsState()
    val d = rememberDerived(events, s.agent())
    return d.approvals.values.firstOrNull()
}
