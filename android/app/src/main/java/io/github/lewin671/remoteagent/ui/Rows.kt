package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.*
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.proto.Agent

/*
 * The rows of client-v58's lists (design-system §6 会话行, 等你的事): a
 * session that has not ended, one that has, and a row of the grey list of
 * what waits. 24 in from the sides; the line under a row starts where its
 * text does.
 */

private val Gutter = 26.dp

private fun markKindFor(status: SessionDisplayStatus): MarkKind? = when (status) {
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_APPROVAL,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_QUESTION,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_PLAN,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_POPUP -> MarkKind.WAITING
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_STARTING,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_RUNNING -> MarkKind.RUNNING
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_IDLE,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_OFFLINE,
    SessionDisplayStatus.SESSION_DISPLAY_STATUS_UNLINKED -> MarkKind.IDLE
    else -> null
}

/**
 * A live session row rendered from Core's [SessionRowView]: status graphic, title & time,
 * status/preview line, and optional third metadata line.
 */
@Composable
fun SessionRow(
    r: SessionRowView,
    last: Boolean = false,
    onClick: () -> Unit,
) {
    val p = pal
    val copy = uiText()
    val title = uiText(r.title)
    val time = uiText(r.time)
    val secondText = uiText(r.secondLine)
    val hasAgent = r.agent != Agent.AGENT_UNSPECIFIED && r.agent != Agent.UNRECOGNIZED
    val metaText = uiText(r.metaLine)
    val spoken = listOf(
        title,
        time,
        secondText,
        if (hasAgent) agentName(r.agent, copy = copy) else "",
        metaText,
    ).filter { it.isNotEmpty() }.joinToString("，")

    Row(
        Modifier.fillMaxWidth().background(if (r.selected || LocalSelectedSession.current == r.sessionId) p.fill else Color.Transparent)
            .clickable(role = Role.Button, onClick = onClick)
            .semantics(mergeDescendants = true) { contentDescription = spoken }
            .padding(start = 24.dp),
    ) {
        Box(Modifier.width(Gutter).padding(top = 17.dp)) {
            androidx.compose.animation.Crossfade(
                markKindFor(r.status),
                animationSpec = Motion.fadeInSpec(Motion.FAST),
                label = "row-mark",
            ) { kind -> kind?.let { StatusMark(it) } }
        }
        Column(
            Modifier.weight(1f)
                .then(if (last) Modifier else Modifier.bottomLine(p.sep))
                .padding(top = 13.dp, bottom = 13.dp, end = 24.dp),
        ) {
            TitleAndTime(title, time, T.s17b)
            if (secondText.isNotEmpty()) {
                Text(
                    secondText,
                    style = T.s15,
                    color = p.ink2,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(top = 3.dp),
                )
            }
            if (metaText.isNotEmpty() || hasAgent) Row(
                Modifier.padding(top = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                if (hasAgent) AgentMarkLead(r.agent, T.s13, p.ink2, scale = 1.15f)
                val words = metaText.ifEmpty { if (hasAgent) agentName(r.agent, copy = copy) else "" }
                if (words.isNotEmpty()) Text(
                    words,
                    style = T.s13,
                    color = p.ink2,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false),
                )
            }
        }
    }
}

/**
 * An ended session on one line rendered from Core's [EndedRowView]: title and right timestamp/meta.
 */
@Composable
fun EndedRow(
    r: EndedRowView,
    last: Boolean = false,
    onClick: () -> Unit,
) {
    val p = pal
    val title = uiText(r.title)
    val right = uiText(r.right)
    val spoken = listOf(title, right).filter { it.isNotEmpty() }.joinToString("，")
    Row(
        Modifier.fillMaxWidth().background(if (r.selected || LocalSelectedSession.current == r.sessionId) p.fill else Color.Transparent)
            .clickable(role = Role.Button, onClick = onClick)
            .semantics(mergeDescendants = true) { contentDescription = spoken }
            .padding(start = 24.dp),
    ) {
        Box(
            Modifier.weight(1f)
                .then(if (last) Modifier else Modifier.bottomLine(p.sep))
                .heightIn(min = 48.dp)
                .padding(top = 12.dp, bottom = 12.dp, end = 24.dp),
        ) {
            TitleAndTime(title, right, T.s17)
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
fun groupShape(first: Boolean, last: Boolean, radius: Int = 18): RoundedCornerShape {
    val top = if (first) radius.dp else 0.dp
    val bottom = if (last) radius.dp else 0.dp
    return RoundedCornerShape(top, top, bottom, bottom)
}

/**
 * A row of the grey list of what waits (design-system §6 等你的事), rendered from Core's [AttentionRowView].
 */
@Composable
fun PendingRow(r: AttentionRowView, first: Boolean, last: Boolean, onClick: () -> Unit) {
    val title = uiText(r.title)
    val kind = uiText(r.kindText)
    val detail = uiText(r.detail)
    val fallbackSummary = if (kind.isEmpty() && detail.isEmpty()) uiText(r.summary) else ""
    val spoken = listOf(title, kind, detail.ifEmpty { fallbackSummary })
        .filter { it.isNotEmpty() }.joinToString("，")
    val p = pal
    Row(
        Modifier.padding(horizontal = 16.dp).padding(top = if (first) 10.dp else 0.dp)
            .fillMaxWidth().clip(groupShape(first, last)).background(p.gbg)
            .clickable(role = Role.Button, onClick = onClick)
            .semantics(mergeDescendants = true) { contentDescription = spoken }
            .then(if (first) Modifier else Modifier.topLine(p.sep, start = 39.dp))
            .padding(start = 18.dp, end = 14.dp, top = 12.dp, bottom = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        StatusMark(MarkKind.WAITING)
        Column(Modifier.weight(1f)) {
            Text(title, style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(
                buildAnnotatedString {
                    if (kind.isNotEmpty()) {
                        withStyle(SpanStyle(color = p.orangeInk, fontWeight = platformWeight(FontWeight.SemiBold))) { append(kind) }
                        if (detail.isNotEmpty()) append(" · " + detail)
                    } else {
                        append(fallbackSummary.ifEmpty { detail })
                    }
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
