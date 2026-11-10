package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.PendingRowText
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.RowMeta
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.TaskGroup
import io.github.lewin671.remoteagent.core.endedRowText
import io.github.lewin671.remoteagent.core.exited
import io.github.lewin671.remoteagent.core.scheduleFrequency
import io.github.lewin671.remoteagent.core.scheduleSupported
import io.github.lewin671.remoteagent.core.sessionRowText
import io.github.lewin671.remoteagent.core.taskGroup
import io.github.lewin671.remoteagent.core.text

/*
 * The rows of client-v58's lists (design-system §6 会话行, 等你的事): a
 * session that has not ended, one that has, and a row of the grey list of
 * what waits. 24 in from the sides; the line under a row starts where its
 * text does.
 */

/** The 26 wide first column of a session row, which holds its status graphic. */
private val Gutter = 26.dp

/**
 * A session that has not ended: its status graphic, the title (17 / 600) and
 * time, 「状态 · 预览」 (15 ink2, at most two lines; one when [oneLine]), and a
 * third line of only what differs in this list ([meta]) and its scheduled
 * tasks. The status word is not coloured: the graphic says it. An ended
 * session is an [EndedRow].
 */
@Composable
fun SessionRow(
    state: RemoteState,
    s: SessionView,
    t: Long,
    meta: RowMeta = RowMeta(),
    last: Boolean = false,
    selected: Boolean = false,
) {
    if (exited(s)) return EndedRow(s, t, project = meta.project, selected = selected, last = last)
    val p = pal
    val nav = LocalNav.current
    val row = sessionRowText(state, s, meta, t)
    val schedules by LocalRemote.current.schedules.state.collectAsState()
    val timed = schedules.values.flatMap { it.tasks }.filter {
        scheduleSupported(state.machines[it.machine]) && it.target.text("type") == "resume_session" &&
            it.target.text("session") == s.id && taskGroup(it) != TaskGroup.ENDED
    }
    val third = listOf(
        row.meta,
        when {
            timed.size > 1 -> "定时 ${timed.size} 个任务"
            timed.size == 1 -> "定时 " + scheduleFrequency(timed.single().trigger)
            else -> ""
        },
    ).filter { it.isNotEmpty() }.joinToString(" · ")
    Row(
        Modifier.fillMaxWidth().background(if (selected) p.fill else Color.Transparent)
            .clickable(role = Role.Button) { nav.session(s.id) }
            .semantics(mergeDescendants = true) { contentDescription = listOf(row.spoken, third.takeIf { it != row.meta }.orEmpty()).filter { it.isNotEmpty() }.joinToString("，") }
            .padding(start = 24.dp),
    ) {
        Box(Modifier.width(Gutter).padding(top = 17.dp)) { markOf(row.status)?.let { StatusMark(it) } }
        Column(
            Modifier.weight(1f).then(if (last) Modifier else Modifier.bottomLine(p.sep)).padding(top = 13.dp, bottom = 13.dp, end = 24.dp),
        ) {
            TitleAndTime(row.title, row.time, T.s17b)
            Text(
                row.status.text + if (row.preview.isNotEmpty()) " · ${row.preview}" else "",
                style = T.s15, color = p.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 3.dp),
            )
            if (third.isNotEmpty()) Text(third, style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 4.dp))
        }
    }
}

/**
 * An ended session on one line: the title (17 / 400) and at the right when
 * it ended, after the project where the list spans projects. No graphic:
 * its date group says it has ended.
 */
@Composable
fun EndedRow(s: SessionView, t: Long, project: Boolean = false, selected: Boolean = false, last: Boolean = false) {
    val p = pal
    val nav = LocalNav.current
    val row = endedRowText(s, project, t)
    Row(
        Modifier.fillMaxWidth().background(if (selected) p.fill else Color.Transparent)
            .clickable(role = Role.Button) { nav.session(s.id) }
            .semantics(mergeDescendants = true) { contentDescription = row.spoken }
            .padding(start = 24.dp),
    ) {
        Box(Modifier.weight(1f).then(if (last) Modifier else Modifier.bottomLine(p.sep)).heightIn(min = 48.dp).padding(top = 12.dp, bottom = 12.dp, end = 24.dp)) {
            TitleAndTime(row.title, row.right, T.s17)
        }
    }
}

/**
 * A title with a time at its right, 13 ink2. When both do not fit side by
 * side at a large font size, the time goes under the title, at the right.
 */
@Composable
private fun TitleAndTime(title: String, time: String, style: androidx.compose.ui.text.TextStyle) {
    val p = pal
    if (LocalDensity.current.fontScale >= 1.8f && time.isNotEmpty()) {
        Column(Modifier.fillMaxWidth()) {
            Text(title, style = style, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(time, style = T.s13, color = p.ink2, maxLines = 1, modifier = Modifier.align(Alignment.End))
        }
        return
    }
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        Text(title, style = style, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (time.isNotEmpty()) Text(time, style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.widthIn(max = 160.dp))
    }
}

/** The shape of a row of a grey list that is drawn a row at a time: its corners only where the list's are. */
fun groupShape(first: Boolean, last: Boolean, radius: Int = 24): RoundedCornerShape {
    val top = if (first) radius.dp else 0.dp
    val bottom = if (last) radius.dp else 0.dp
    return RoundedCornerShape(top, top, bottom, bottom)
}

/**
 * A row of the grey list of what waits (design-system §6 等你的事): the
 * orange dot, the title (17 / 600), then 「类型 · 摘要」 with the type in
 * orange-ink 600, and ›. [first] and [last] say where the list's corners are.
 */
@Composable
fun PendingRow(row: PendingRowText, first: Boolean, last: Boolean, onClick: () -> Unit) {
    val p = pal
    Row(
        Modifier.padding(horizontal = 16.dp).padding(top = if (first) 10.dp else 0.dp).fillMaxWidth().clip(groupShape(first, last)).background(p.gbg)
            .clickable(role = Role.Button, onClick = onClick).semantics(mergeDescendants = true) { contentDescription = row.spoken }
            .then(if (first) Modifier else Modifier.topLine(p.sep, start = 39.dp))
            .padding(start = 18.dp, end = 14.dp, top = 12.dp, bottom = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        StatusMark(MarkKind.WAITING)
        Column(Modifier.weight(1f)) {
            Text(row.title, style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(
                buildAnnotatedString {
                    withStyle(SpanStyle(color = p.orangeInk, fontWeight = platformWeight(FontWeight.SemiBold))) { append(row.kind) }
                    if (row.summary.isNotEmpty()) append(" · ${row.summary}")
                },
                style = T.s15, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 1.dp),
            )
        }
        Ic(Lucide.ChevronRight, 18.dp, p.ink3)
    }
}

/** The last row of a grey list: blue 「还有 N 件」, which opens the rest in place. */
@Composable
fun MoreRow(text: String, last: Boolean = true, onClick: () -> Unit) {
    val p = pal
    Row(
        Modifier.padding(horizontal = 16.dp).fillMaxWidth().clip(groupShape(false, last)).background(p.gbg).topLine(p.sep, start = 39.dp)
            .heightIn(min = 44.dp).clickable(role = Role.Button, onClick = onClick).padding(start = 39.dp, end = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) { Text(text, style = T.s15, color = p.blue) }
}

/** 「还有 N 个」 under 进行中, and the line that ends a list: 15, blue when a tap does something, else ink2. */
@Composable
fun ListMore(text: String, onClick: (() -> Unit)?, modifier: Modifier = Modifier) {
    val p = pal
    Row(
        modifier.fillMaxWidth().heightIn(min = 44.dp).then(if (onClick != null) Modifier.clickable(role = Role.Button, onClick = onClick) else Modifier)
            .padding(horizontal = 24.dp, vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(text, style = T.s15, color = if (onClick != null) p.blue else p.ink2)
    }
}
