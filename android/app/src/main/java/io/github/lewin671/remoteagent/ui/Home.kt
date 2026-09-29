package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.clickable
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
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Project
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.derive
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
    val all = state.sessions.values.sortedByDescending { it.updatedMs }
    val attention = all.filter { statusIn(state, it).tone == io.github.lewin671.remoteagent.core.Tone.WAIT }
    val running = all.filter { !exited(it) && it !in attention && statusIn(state, it) != Status.OFFLINE } +
        all.filter { statusIn(state, it) == Status.OFFLINE }
    val projects = projects(state)
    var showAll by remember { mutableStateOf(false) }

    LazyColumn(Modifier.fillMaxSize()) {
        item {
            Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 8.dp, top = 12.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text("会话", style = MaterialTheme.typography.displaySmall, modifier = Modifier.weight(1f))
                FilledIconButton(
                    onClick = { nav.pickProject() },
                    modifier = Modifier.size(48.dp),
                    colors = IconButtonDefaults.filledIconButtonColors(containerColor = p.blueBg),
                ) { Icon(Icons.Default.Add, "新会话", tint = p.card) }
            }
        }
        if (state.conn != ConnState.ONLINE) item { ConnBanner(state) }
        if (!state.loaded && state.sessions.isEmpty()) {
            item { Text("读取中…", Modifier.padding(16.dp), color = p.muted) }
        } else if (state.sessions.isEmpty()) {
            item { EmptyAccount(state) }
        }
        if (attention.isNotEmpty()) {
            item { SectionHeader("需要处理", attention.size) }
            val shown = if (showAll) attention else attention.take(3)
            items(shown, key = { "a" + it.id }) { s -> AttentionCard(state, s, t) }
            if (!showAll && attention.size > 3) item {
                TextButton(onClick = { showAll = true }, Modifier.padding(horizontal = 8.dp)) { Text("还有 ${attention.size - 3} 项待处理 ›") }
            }
        }
        if (running.isNotEmpty()) {
            item { SectionHeader("进行中", running.size) }
            item {
                Card(Modifier.padding(horizontal = 12.dp)) {
                    Column { running.forEachIndexed { i, s -> SessionRow(state, s, t, divider = i > 0) } }
                }
            }
        }
        if (projects.isNotEmpty()) {
            item { SectionHeader("项目") }
            item {
                Card(Modifier.padding(horizontal = 12.dp)) {
                    Column { projects.forEach { ProjectRow(state, it, t) } }
                }
            }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun EmptyAccount(state: RemoteState) {
    val p = pal
    val nav = LocalNav.current
    val online = state.machines.values.filter { it.online }
    Card(Modifier.padding(12.dp)) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("还没有会话", style = MaterialTheme.typography.titleSmall)
            when {
                state.machines.isEmpty() -> {
                    Text("这个账户还没有电脑。在电脑上运行 remote-agent account join 加入。", color = p.muted)
                    TextButton(onClick = { nav.pair() }) { Text("添加设备") }
                }
                online.isEmpty() -> Text("没有在线的电脑。在电脑上运行 remote-agent daemon。", color = p.muted)
                else -> {
                    Text("${machineLabel(state, online.first().id)} 在线，可以直接开始", color = p.muted)
                    Button(onClick = { nav.newProject() }) { Text("开始第一个会话") }
                }
            }
        }
    }
}

/** One session in a list: title, time, preview, status · project · machine. */
@Composable
fun SessionRow(state: RemoteState, s: SessionView, t: Long, divider: Boolean, showProject: Boolean = true) {
    val p = pal
    val nav = LocalNav.current
    val st = statusIn(state, s)
    if (divider) androidx.compose.material3.HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
    Column(Modifier.fillMaxWidth().clickableRow { nav.session(s.id) }.padding(horizontal = 16.dp, vertical = 12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(titleOf(s), style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            Text(whenText(s.updatedMs, t), style = MaterialTheme.typography.bodySmall, color = p.faint)
        }
        val preview = previewOf(s, st)
        if (preview.isNotEmpty()) Text(preview, style = MaterialTheme.typography.bodyMedium, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(top = 3.dp)) {
            StatusLabel(st)
            val parts = buildList {
                if (showProject) add(projectName(projectKey(s)))
                add(machineLabel(state, s.machineId))
                if (s.agent() != io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX) add(agentName(s.agent()))
            }
            Text(" · " + parts.joinToString(" · "), style = MaterialTheme.typography.bodySmall, color = p.muted, maxLines = 1)
        }
    }
}

@Composable
private fun ProjectRow(state: RemoteState, pr: Project, t: Long) {
    val p = pal
    val nav = LocalNav.current
    val active = pr.sessions.count { !exited(it) }
    Row44(onClick = { nav.project(pr.key) }) {
        Avatar(pr.key)
        Column(Modifier.weight(1f)) {
            Text(pr.name, style = MaterialTheme.typography.titleSmall)
            Text(
                shortPath(pr.key) + " · " + if (active > 0) "$active 个进行中" else whenText(pr.updatedMs, t),
                style = MaterialTheme.typography.bodySmall, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
        }
        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = p.faint)
    }
}

/** A session that waits for the user. Approvals can be decided right here. */
@Composable
private fun AttentionCard(state: RemoteState, s: SessionView, t: Long) {
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val st = statusIn(state, s)
    val events by remote.events(s.id).collectAsState()
    val d = remember(events.events) { derive(events.events) }
    Card(Modifier.padding(horizontal = 12.dp, vertical = 5.dp)) {
        Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Dot(p.orange)
                Spacer(Modifier.width(6.dp))
                val a = d.approvals.values.firstOrNull()
                Text(
                    when (st) {
                        Status.APPROVAL -> if (a?.command.isNullOrEmpty()) "请求权限" else "请求执行"
                        Status.QUESTION -> "${agentName(s.agent())} 在问你"
                        else -> st.text
                    },
                    style = MaterialTheme.typography.titleSmall, color = p.orangeInk, modifier = Modifier.weight(1f),
                )
                if (st == Status.APPROVAL && a != null && a.hasDeadline()) {
                    Text("剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟，超时拒绝", style = MaterialTheme.typography.bodySmall, color = p.muted)
                }
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(titleOf(s), style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis)
                TextButton(onClick = { nav.session(s.id) }) { Text(if (st == Status.APPROVAL) "查看会话 ›" else "去处理 ›") }
            }
            val q = d.questions.values.firstOrNull()?.questionsList?.firstOrNull()?.text
            Text(
                listOfNotNull(projectName(projectKey(s)), machineLabel(state, s.machineId), q ?: d.approvals.values.firstOrNull()?.description?.takeIf { it.isNotEmpty() })
                    .joinToString(" · "),
                style = MaterialTheme.typography.bodySmall, color = p.muted, maxLines = 2, overflow = TextOverflow.Ellipsis,
            )
            val a = d.approvals.values.firstOrNull()
            if (st == Status.APPROVAL && a != null) {
                if (a.command.isNotEmpty()) CodeBlock(a.command, maxLines = 4)
                ApprovalButtons(s, a, compact = true)
            }
        }
    }
}

fun Modifier.clickableRow(onClick: () -> Unit): Modifier = this.clickable(onClick = onClick)
