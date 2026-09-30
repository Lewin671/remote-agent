package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
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
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.PrefsOp
import io.github.lewin671.remoteagent.core.Project
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.projectGroups
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.shortPath
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Changes to the project preferences in flight, per project path, for this
 * run: the row says 保存中 until the server has it, and a failure leaves
 * the list as it was with a retry.
 */
object PrefsChanges {
    sealed interface State {
        data object Saving : State
        data class Failed(val op: PrefsOp, val error: String) : State
    }

    val states = mutableStateMapOf<String, State>()

    /** The last project removed from the list here, for 撤销 while it shows. */
    var undo by mutableStateOf<String?>(null)

    fun run(remote: Remote, op: PrefsOp, onDone: () -> Unit = {}) {
        if (states[op.path] == State.Saving) return
        states[op.path] = State.Saving
        RaApp.instance.scope.launch {
            try {
                remote.changePrefs(op)
                states.remove(op.path)
                onDone()
            } catch (e: Exception) {
                states[op.path] = State.Failed(op, errorText(e))
            }
        }
    }

    fun clear() {
        states.clear()
        undo = null
    }
}

/** Why the preferences cannot be changed now, or null when they can. */
fun prefsBlocked(state: RemoteState): String? = when {
    !state.prefsSupported -> "服务器还不支持同步项目偏好，更新服务器后才能管理"
    state.conn != ConnState.ONLINE -> "当前显示缓存 · 连上后才能管理"
    state.prefs == null -> "正在读取项目偏好…"
    else -> null
}

/** 项目管理: pinned in their order, the others by activity; only the entries change, never folders on the computer. */
@Composable
fun ManageProjectsScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val g = projectGroups(state)
    val blocked = prefsBlocked(state)
    // A change from another client while this page is open is said once.
    var seenRev by remember { mutableStateOf(state.prefs?.revision) }
    var elsewhere by remember { mutableStateOf(false) }
    LaunchedEffect(state.prefs?.revision) {
        val rev = state.prefs?.revision
        if (seenRev != null && rev != null && rev != seenRev && PrefsChanges.states.isEmpty() && PrefsChanges.undo == null) elsewhere = true
        seenRev = rev
    }
    PageScaffold("项目管理") {
        LazyColumn(Modifier.weight(1f)) {
            item { Text("只管理项目入口，不移动或删除电脑上的目录。", Modifier.padding(horizontal = 16.dp), color = p.muted, style = MaterialTheme.typography.bodyMedium) }
            if (blocked != null) item { Notice(blocked) }
            if (elsewhere) item { Notice("项目列表已在另一客户端更新") { elsewhere = false } }
            if (g.pinned.isNotEmpty()) {
                item { SectionHeader("常用", g.pinned.size) }
                item { Card(Modifier.padding(horizontal = 12.dp)) { Column { g.pinned.forEachIndexed { i, pr -> ManageRow(state, pr, pinned = true, divider = i > 0, blocked = blocked != null) } } } }
            }
            if (g.others.isNotEmpty()) {
                item { SectionHeader("其他项目", g.others.size) }
                item { Card(Modifier.padding(horizontal = 12.dp)) { Column { g.others.forEachIndexed { i, pr -> ManageRow(state, pr, pinned = false, divider = i > 0, blocked = blocked != null) } } } }
            }
            item {
                Card(Modifier.padding(12.dp), onClick = { nav.hiddenProjects() }) {
                    Row44 {
                        Text(if (g.hidden.isEmpty()) "没有已移除的项目" else "已移除 ${g.hidden.size} 个项目", Modifier.weight(1f))
                        if (g.hidden.isNotEmpty()) Text("查看 ›", color = p.blue)
                    }
                }
            }
            item {
                Text(
                    "常用和已移除项目在账户内同步。断线时可查看缓存，修改暂停；不会影响会话和提醒。",
                    Modifier.padding(16.dp), color = p.muted, style = MaterialTheme.typography.bodySmall,
                )
            }
        }
        UndoBar(remote, state)
    }
}

@Composable
private fun Notice(text: String, onDismiss: (() -> Unit)? = null) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 6.dp).background(p.orangeSoft, RoundedCornerShape(12.dp)).padding(horizontal = 12.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(text, Modifier.weight(1f), color = p.orangeInk, style = MaterialTheme.typography.bodySmall)
        if (onDismiss != null) TextButton(onClick = onDismiss) { Text("知道了") }
    }
}

@Composable
private fun ManageRow(state: RemoteState, pr: Project, pinned: Boolean, divider: Boolean, blocked: Boolean) {
    val p = pal
    val remote = LocalRemote.current
    var menu by remember { mutableStateOf(false) }
    val change = PrefsChanges.states[pr.key]
    if (divider) HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
    Row44 {
        Avatar(pr.key)
        Column(Modifier.weight(1f)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(pr.name, style = MaterialTheme.typography.titleSmall)
                if (pinned) {
                    Spacer(Modifier.width(6.dp))
                    Text("常用", style = MaterialTheme.typography.labelSmall, color = p.blue)
                }
            }
            Text(shortPath(pr.key), style = MaterialTheme.typography.bodySmall, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
            when (change) {
                PrefsChanges.State.Saving -> Text("◌ 正在同步…", style = MaterialTheme.typography.bodySmall, color = p.muted)
                is PrefsChanges.State.Failed -> Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("没能保存，列表保持原样：${change.error}", style = MaterialTheme.typography.bodySmall, color = p.red, modifier = Modifier.weight(1f))
                    TextButton(onClick = { PrefsChanges.run(remote, change.op) }, enabled = !blocked) { Text("重试") }
                }
                null -> {}
            }
        }
        if (change == PrefsChanges.State.Saving) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
        else Box {
            IconButton(onClick = { menu = true }, enabled = !blocked) { Icon(Icons.Default.MoreVert, "更多") }
            DropdownMenu(menu, { menu = false }) {
                Text("${pr.name}\n${shortPath(pr.key)}", Modifier.padding(horizontal = 16.dp, vertical = 8.dp), style = MaterialTheme.typography.bodySmall, color = p.muted)
                DropdownMenuItem({ Text(if (pinned) "取消常用" else "设为常用") }, onClick = {
                    menu = false
                    PrefsChanges.run(remote, if (pinned) PrefsOp.Unpin(pr.key) else PrefsOp.Pin(pr.key))
                })
                DropdownMenuItem({ Text("从项目列表移除") }, onClick = {
                    menu = false
                    PrefsChanges.run(remote, PrefsOp.Hide(pr.key)) { PrefsChanges.undo = pr.key }
                })
            }
        }
    }
}

/** 已移除 xxx · 撤销: a short undo after removing; 已移除的项目 stays the lasting way back. */
@Composable
fun UndoBar(remote: Remote, state: RemoteState) {
    val p = pal
    val key = PrefsChanges.undo ?: return
    LaunchedEffect(key) {
        delay(8_000)
        if (PrefsChanges.undo == key) PrefsChanges.undo = null
    }
    Column(Modifier.fillMaxWidth().padding(12.dp).background(p.fg, RoundedCornerShape(12.dp)).padding(horizontal = 14.dp, vertical = 10.dp)) {
        Text("${projectName(key)} 的会话和历史仍然保留。有新的待处理项时，主页仍会显示。", color = p.card, style = MaterialTheme.typography.bodySmall)
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("已移除 ${projectName(key)}", color = p.card, modifier = Modifier.weight(1f))
            TextButton(onClick = {
                PrefsChanges.undo = null
                PrefsChanges.run(remote, PrefsOp.Unhide(key))
            }, enabled = prefsBlocked(state) == null) { Text("撤销", color = p.blueSoft) }
        }
    }
}

/** 已移除的项目: restore brings a project back among the others; it starts or resumes nothing. */
@Composable
fun HiddenProjectsScreen() {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val p = pal
    val hidden = projectGroups(state).hidden
    val blocked = prefsBlocked(state)
    PageScaffold("已移除的项目") {
        LazyColumn(Modifier.weight(1f)) {
            item { Text("恢复只让项目重新出现在列表里，不会启动或恢复会话。", Modifier.padding(horizontal = 16.dp), color = p.muted, style = MaterialTheme.typography.bodyMedium) }
            if (blocked != null) item { Notice(blocked) }
            if (hidden.isEmpty()) item { Text("没有已移除的项目", Modifier.padding(16.dp), color = p.muted) }
            else item {
                Card(Modifier.padding(12.dp)) {
                    Column {
                        hidden.forEachIndexed { i, pr ->
                            if (i > 0) HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                            val change = PrefsChanges.states[pr.key]
                            Row44 {
                                Avatar(pr.key)
                                Column(Modifier.weight(1f)) {
                                    Text(pr.name, style = MaterialTheme.typography.titleSmall)
                                    Text(shortPath(pr.key), style = MaterialTheme.typography.bodySmall, color = p.muted)
                                    if (change is PrefsChanges.State.Failed) Text("没能保存：${change.error}", style = MaterialTheme.typography.bodySmall, color = p.red)
                                }
                                if (change == PrefsChanges.State.Saving) CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                                else TextButton(onClick = { PrefsChanges.run(remote, PrefsOp.Unhide(pr.key)) }, enabled = blocked == null) { Text("恢复") }
                            }
                        }
                    }
                }
            }
            item {
                Text(
                    "已移除的项目不出现在项目列表和新建选择器中；进行中会话、待处理卡片和通知入口仍然可访问。",
                    Modifier.padding(16.dp), color = p.muted, style = MaterialTheme.typography.bodySmall,
                )
            }
            item { Spacer(Modifier.height(16.dp)) }
        }
    }
}
