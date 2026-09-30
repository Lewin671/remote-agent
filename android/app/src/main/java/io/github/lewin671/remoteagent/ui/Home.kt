package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.ui.draw.clip
import androidx.compose.ui.unit.sp
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.heightIn
import androidx.compose.runtime.saveable.rememberSaveable
import io.github.lewin671.remoteagent.core.Tone
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.Button
import androidx.compose.material3.FilledIconButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.text.font.FontWeight
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Project
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.core.liveOrder
import io.github.lewin671.remoteagent.core.attentionOrder
import io.github.lewin671.remoteagent.core.endedMs
import io.github.lewin671.remoteagent.core.exited
import io.github.lewin671.remoteagent.core.minutesLeft
import io.github.lewin671.remoteagent.core.previewOf
import io.github.lewin671.remoteagent.core.projectKey
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.projects
import io.github.lewin671.remoteagent.core.shortPath
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.core.whenText

/** 主页: what needs you first, then what is running, then projects. */
@Composable
fun HomeScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val t = now()
    val all = state.sessions.values.sortedWith(liveOrder)
    val attention = all.filter { statusIn(state, it).tone == Tone.WAIT }.sortedWith(attentionOrder(state))
    val running = all.filter { !exited(it) && it !in attention }
    val groups = io.github.lewin671.remoteagent.core.projectGroups(state)
    var showAll by remember { mutableStateOf(false) }
    var pick by rememberSaveable { mutableStateOf(false) }

    LazyColumn(Modifier.fillMaxSize().background(p.card)) {
        item {
            Row(verticalAlignment = Alignment.CenterVertically) {
                LargeTitle("会话", Modifier.weight(1f), action = { pick = true })
            }
            if (state.conn != ConnState.ONLINE) ConnBanner(state)
        }
        if (!state.loaded && state.sessions.isEmpty()) {
            item { Skeleton() }
        } else if (state.sessions.isEmpty()) {
            item { EmptyAccount(state) }
        }
        if (attention.isNotEmpty()) {
            item { Label("需要处理", attention.size) }
            val shown = if (showAll) attention else attention.take(3)
            items(shown, key = { "a" + it.id }) { s -> AttentionCard(state, s, t) }
            if (!showAll && attention.size > 3) item {
                Box(Modifier.padding(start = 12.dp, end = 12.dp, bottom = 4.dp).heightIn(min = 44.dp).clickable { showAll = true }.padding(horizontal = 4.dp), contentAlignment = Alignment.CenterStart) {
                    ArrowText("还有 ${attention.size - 3} 项待处理", p.blue, T.smB)
                }
            }
        }
        if (running.isNotEmpty()) {
            item { Label("进行中", running.size) }
            items(running, key = { "r" + it.id }) { s -> SessionRow(state, s, t, last = s == running.last()) }
        }
        if (groups.pinned.isNotEmpty() || groups.others.isNotEmpty() || groups.hidden.isNotEmpty()) {
            item { Label(if (groups.pinned.isNotEmpty()) "常用项目" else "项目", action = "管理", onAction = { nav.manageProjects() }) }
            items(groups.pinned, key = { "p" + it.key }) { ProjectRow(state, it, t, last = it == groups.pinned.last()) }
            if (groups.pinned.isNotEmpty() && groups.others.isNotEmpty()) item { Label("其他项目") }
            items(groups.others, key = { "o" + it.key }) { ProjectRow(state, it, t, last = it == groups.others.last()) }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
    if (pick) PickProjectSheet(state) { pick = false }
}

@Composable
private fun EmptyAccount(state: RemoteState) {
    val p = pal
    val nav = LocalNav.current
    val online = state.machines.values.filter { it.online }
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Text("还没有会话", style = T.h17, color = p.fg)
        when {
            state.machines.isEmpty() -> {
                Text("这个账户还没有电脑。在电脑上运行 remote-agent account join 加入。", style = T.sm, color = p.muted, textAlign = androidx.compose.ui.text.style.TextAlign.Center, modifier = Modifier.padding(top = 4.dp, bottom = 14.dp))
                Btn("添加设备", { nav.pair() }, Modifier.fillMaxWidth(), BtnKind.PRIMARY)
            }
            online.isEmpty() -> Text("没有在线的电脑。在电脑上运行 remote-agent daemon。", style = T.sm, color = p.muted, textAlign = androidx.compose.ui.text.style.TextAlign.Center, modifier = Modifier.padding(top = 4.dp))
            else -> {
                Text("${machineLabel(state, online.first().id)} 在线，可以直接开始", style = T.sm, color = p.muted, modifier = Modifier.padding(top = 4.dp, bottom = 14.dp))
                Btn("开始第一个会话", { nav.newProject() }, Modifier.fillMaxWidth(), BtnKind.PRIMARY)
            }
        }
    }
}

/**
 * One session in a list (client-v7): a dot, the title and time, a preview,
 * then the status word in its color · CLI · project · machine. The line
 * under a row starts where its text does.
 */
@Composable
fun SessionRow(state: RemoteState, s: SessionView, t: Long, last: Boolean = false, showProject: Boolean = true, preview: Boolean = true, selected: Boolean = false) {
    val p = pal
    val nav = LocalNav.current
    val st = statusIn(state, s)
    val ended = exited(s)
    Row(
        Modifier.fillMaxWidth().background(if (selected) p.blueSoft else androidx.compose.ui.graphics.Color.Transparent).clickableRow { nav.session(s.id) }.padding(start = 16.dp, end = 16.dp, top = 10.dp),
        verticalAlignment = Alignment.Top, horizontalArrangement = Arrangement.spacedBy(11.dp),
    ) {
        Box(Modifier.padding(top = 8.dp)) { Dot(p.tone(st.tone)) }
        Column(Modifier.weight(1f).then(if (!last) Modifier.bottomLine(p.line) else Modifier).padding(bottom = 10.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(titleOf(s), style = if (ended) T.body else T.bodyB, color = if (ended) p.fg2 else p.fg, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                Text(whenText(if (ended) endedMs(s) else s.updatedMs, t), style = T.meta, color = p.muted, maxLines = 1)
            }
            val pv = if (preview) previewOf(s, st) else ""
            if (pv.isNotEmpty()) Text(pv, style = T.sm, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
            val rest = buildList {
                add(agentName(s.agent()))
                if (showProject) add(projectName(projectKey(s)))
                add(machineLabel(state, s.machineId))
            }.joinToString(" · ")
            Text(
                androidx.compose.ui.text.buildAnnotatedString {
                    if (ended) append(st.text) else {
                        pushStyle(androidx.compose.ui.text.SpanStyle(color = p.toneInk(st.tone), fontWeight = FontWeight.SemiBold))
                        append(st.text)
                        pop()
                    }
                    append(" · $rest")
                },
                style = T.meta, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 1.dp),
            )
        }
    }
}

/** A project in a list: its avatar, name, path and what runs there. */
@Composable
private fun ProjectRow(state: RemoteState, pr: Project, t: Long, last: Boolean) {
    val p = pal
    val nav = LocalNav.current
    val active = pr.sessions.count { !exited(it) }
    Row(
        Modifier.fillMaxWidth().clickableRow { nav.project(pr.key) }.padding(start = 16.dp, end = 16.dp, top = 8.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Box(Modifier.padding(bottom = 8.dp)) { Avatar(pr.key) }
        Row(Modifier.weight(1f).then(if (!last) Modifier.bottomLine(p.line) else Modifier).padding(bottom = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text(pr.name, style = T.bodyB, color = p.fg, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(
                    shortPath(pr.key) + " · " + if (active > 0) "$active 个进行中" else whenText(pr.updatedMs, t),
                    style = T.sm, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
            }
            Text("›", style = ts(18.sp, lineHeight = 1f), color = p.faint)
        }
    }
}

/**
 * A session that waits for the user, on a grey card. Approvals are decided
 * right here; questions, plans and popups open the session.
 */
@Composable
private fun AttentionCard(state: RemoteState, s: SessionView, t: Long) {
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val st = statusIn(state, s)
    val events by remote.events(s.id).collectAsState()
    val d = rememberDerived(events.events, s.agent())
    val a = d.approvals.values.firstOrNull()
    Column(Modifier.padding(start = 12.dp, end = 12.dp, bottom = 8.dp).fillMaxWidth().clip(Radii.MD).background(p.soft).padding(horizontal = 13.dp, vertical = 11.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
            Dot(p.orangeInk, 8)
            Text(
                when (st) {
                    Status.APPROVAL -> if (a?.command.isNullOrEmpty()) "请求权限" else "请求执行"
                    Status.QUESTION -> "${agentName(s.agent())} 在问你"
                    else -> st.text
                },
                style = T.metaB, color = p.orangeInk, 
            )
            }
            Text(
                if (st == Status.APPROVAL && a != null && a.hasDeadline()) "剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟，超时拒绝" else whenText(s.updatedMs, t),
                style = T.meta, color = p.muted,
            )
        }
        Row(Modifier.padding(top = 4.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(titleOf(s), style = T.bodyB, color = p.fg, modifier = Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis)
            Box(Modifier.clickable { nav.session(s.id) }) { ArrowText(if (st == Status.APPROVAL) "查看会话" else if (st == Status.QUESTION) "去回答" else "去处理", p.blue, T.smB) }
        }
        val q = d.questions.values.firstOrNull()?.questionsList?.firstOrNull()?.text
        Text(listOfNotNull(projectName(projectKey(s)), machineLabel(state, s.machineId), q).joinToString(" · "), style = T.meta, color = p.muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
        if (st == Status.APPROVAL && a != null) {
            if (a.description.isNotEmpty()) Text(a.description, style = T.sm, color = p.fg2, modifier = Modifier.padding(top = 6.dp))
            if (a.command.isNotEmpty()) Text(
                a.command, style = T.mono, color = p.fg2, maxLines = 3, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.padding(vertical = 8.dp).fillMaxWidth().clip(Radii.XS).background(p.card).border(1.dp, p.line, Radii.XS).padding(horizontal = 10.dp, vertical = 7.dp),
            )
            ApprovalButtons(s, a, compact = true, onCard = true)
        }
    }
}

fun Modifier.clickableRow(onClick: () -> Unit): Modifier = this.clickable(onClick = onClick)
